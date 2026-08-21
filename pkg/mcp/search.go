package mcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeaflowNET/leaflow/pkg/leaflow"
)

const (
	toolServices = "services"

	toolListOperations = "operations"

	toolOperationSchema = "operation-schema"

	toolCallOperation = "call-operation"
)

// metaTools is the fixed surface: four tools, whatever the platform grows to.
//
// The contracts describe two hundred operations, and a client is sent every
// tool definition on every turn. Presented one tool per operation, the listing
// alone would be larger than most of the conversations it is meant to take part
// in — so by default a client walks the contracts through these four instead.
//
// The cost is a turn or two before the first call: pick a service, list its
// operations, read the arguments, then make the call. That is the trade, and
// --tools operations is how to take the other side of it.
//
// Named here once, so that the count reported at startup and the tools actually
// registered cannot disagree.
var metaTools = []string{
	toolServices,
	toolListOperations,
	toolOperationSchema,
	toolCallOperation,
}

func (s *Server) addMetaTools(server *sdk.Server) {
	names := s.listServices()

	server.AddTool(&sdk.Tool{
		Name:        toolServices,
		Title:       "Services",
		Description: "List the platform's services and the resource groups each one covers. Start here.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: new(false),
		},
	}, s.handleServices)

	server.AddTool(&sdk.Tool{
		Name:  toolListOperations,
		Title: "List operations",
		Description: strings.TrimSpace(`
List one service's operations. Ask for a service by name; listing every
service at once is a great deal of text.

The reply is a summary. Use operation-schema to see what an operation accepts
before calling it.`),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "service to list, as named by services",
					"enum":        buildEnum(names),
				},
				"group": map[string]any{
					"type":        "string",
					"description": "resource group within the service, as named by services",
				},
			},
			"required":             []any{"service"},
			"additionalProperties": false,
		},
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: new(false),
		},
	}, s.handleListOperations)

	server.AddTool(&sdk.Tool{
		Name:  toolOperationSchema,
		Title: "Operation schema",
		Description: strings.TrimSpace(`
Return everything an operation accepts: a JSON Schema for its arguments,
carrying every constraint the contract states — required fields, formats,
enums, lengths and bounds.

Read this before call-operation. The schema is what the platform will check the
call against, so a call that satisfies it is a call that will be tried.`),
		InputSchema: buildSelector(names, nil),
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: new(false),
		},
	}, s.handleOperationSchema)

	server.AddTool(&sdk.Tool{
		Name:  toolCallOperation,
		Title: "Call an operation",
		Description: strings.TrimSpace(`
Call one operation and return its reply as XML.

arguments must match the schema operation-schema returned for this operation.
Path and query parameters go at the top level under the contract's own names; a
request body goes under "body", whole.

This reaches the live platform, in whichever project the caller's token names.
A write here is a write there.`),
		InputSchema: buildSelector(names, map[string]any{
			"arguments": map[string]any{
				"type":        "object",
				"description": "the operation's arguments, shaped as operation-schema says",
			},
		}),
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:  s.client.ReadOnly(),
			OpenWorldHint: new(true),
			// Unknowable in advance: this one tool stands for every operation,
			// including the destructive ones. Claiming otherwise would tell a client
			// it need not ask before a call that deletes a disk.
			DestructiveHint: new(!s.client.ReadOnly()),
		},
	}, s.handleCallOperation)
}

// operationSelector is the "which operation" half of a tool's arguments, shared
// so that the two tools that take one cannot describe it differently.
func buildSelector(services []string, extra map[string]any) map[string]any {
	properties := map[string]any{
		"service": map[string]any{
			"type":        "string",
			"description": "service the operation belongs to",
			"enum":        buildEnum(services),
		},
		"operation": map[string]any{
			"type":        "string",
			"description": "operation id, as returned by operations",
		},
	}

	maps.Copy(properties, extra)

	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             []any{"service", "operation"},
		"additionalProperties": false,
	}
}

func (s *Server) listServices() []string {
	services := s.client.Services()

	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
	}

	return names
}

func (s *Server) handleServices(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	services := make([]any, 0)

	for _, service := range s.client.Services() {
		groups := make([]any, 0, len(service.Groups))

		for _, group := range service.Groups {
			entry := map[string]any{
				"name":       group.Name,
				"operations": group.Operations,
			}

			if group.Description != "" {
				entry["description"] = group.Description
			}

			groups = append(groups, entry)
		}

		services = append(services, map[string]any{
			"service":    service.Name,
			"title":      service.Title,
			"contract":   service.Contract,
			"operations": service.Operations,
			"groups":     groups,
		})
	}

	return renderReply(map[string]any{
		"services": services,
	}, "services"), nil
}

func (s *Server) handleListOperations(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	values, err := decodeArguments(req.Params.Arguments)
	if err != nil {
		return renderRefusal(err), nil
	}

	service, _ := values["service"].(string)
	if !s.hasService(service) {
		return renderRefusal(fmt.Errorf("no such service: %q; this server exposes %s",
			service, strings.Join(s.listServices(), ", "))), nil
	}

	group, _ := values["group"].(string)

	operations := make([]any, 0)

	for _, op := range s.client.OperationsIn(service) {
		if group != "" && !strings.EqualFold(op.Group(), group) {
			continue
		}

		entry := map[string]any{
			"operation": op.Name(),
			"method":    op.Method(),
			"path":      op.Path(),
		}

		if op.Summary() != "" {
			entry["summary"] = op.Summary()
		}

		if g := op.Group(); g != "" {
			entry["group"] = g
		}

		if op.Deprecated() {
			entry["deprecated"] = true
		}

		operations = append(operations, entry)
	}

	return renderReply(map[string]any{
		"service":    service,
		"operations": operations,
		"count":      len(operations),
	}, "operations"), nil
}

func (s *Server) hasService(service string) bool {
	return slices.Contains(s.listServices(), service)
}

func (s *Server) handleOperationSchema(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	values, err := decodeArguments(req.Params.Arguments)
	if err != nil {
		return renderRefusal(err), nil
	}

	op, err := s.findOperation(values)
	if err != nil {
		return renderRefusal(err), nil
	}

	reply := map[string]any{
		"service":      op.Service(),
		"operation":    op.Name(),
		"method":       op.Method(),
		"path":         op.Path(),
		"arguments":    op.Schema(),
		"command_line": op.Command(),
	}

	if op.Summary() != "" {
		reply["summary"] = op.Summary()
	}

	if op.Details() != "" {
		reply["description"] = op.Details()
	}

	if group := op.Group(); group != "" {
		reply["group"] = group
	}

	if op.Deprecated() {
		reply["deprecated"] = true
	}

	// Said out loud because it is the one thing about a call that is not in its
	// arguments: an account-token operation works before a project is selected,
	// and every other one does not.
	if op.AccountToken() {
		reply["credential"] = "account token; works without a project selected"
	} else {
		reply["credential"] = "access token; acts in the project the token names"
	}

	return renderReply(reply, "operation"), nil
}

func (s *Server) handleCallOperation(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	values, err := decodeArguments(req.Params.Arguments)
	if err != nil {
		return renderRefusal(err), nil
	}

	service, _ := values["service"].(string)
	operation, _ := values["operation"].(string)

	call, err := readCallArguments(values)
	if err != nil {
		return renderRefusal(err), nil
	}

	return s.call(ctx, service, operation, call), nil
}

// operation resolves the service and operation a call names.
func (s *Server) findOperation(values map[string]any) (*leaflow.Operation, error) {
	service, ok := values["service"].(string)
	if !ok || service == "" {
		return nil, fmt.Errorf("service is required; one of %s", strings.Join(s.listServices(), ", "))
	}

	id, ok := values["operation"].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("operation is required; see %s", toolListOperations)
	}

	return s.client.Operation(service, id)
}

// callArguments reads the nested arguments object.
//
// A missing one is an empty call, not a failure: plenty of operations take
// nothing, and requiring `"arguments": {}` for those would be a rule that
// exists only to be forgotten.
func readCallArguments(values map[string]any) (map[string]any, error) {
	raw, ok := values["arguments"]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}

	call, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("arguments must be an object")
	}

	return call, nil
}

// buildEnum renders a list of names as a JSON Schema enum, which the schema
// vocabulary defines as a list of arbitrary values rather than of strings.
func buildEnum(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}

	return out
}
