package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeaflowNET/leaflow/pkg/leaflow"
)

// call makes one request and renders whatever came back.
func (s *Server) call(ctx context.Context, service, operation string, args map[string]any) *sdk.CallToolResult {
	result, err := s.client.Call(ctx, leaflow.Call{
		Service:   service,
		Operation: operation,
		Arguments: args,
	})
	if err != nil {
		return renderRefusal(err)
	}

	// A 204 carries no body, and an empty result reads as a call that did not
	// happen. Saying which status came back is what distinguishes "deleted" from
	// "returned nothing".
	if result.Value == nil {
		return renderReply(map[string]any{
			"ok":     true,
			"status": result.Status,
		}, "result")
	}

	return renderReply(result.Value, "result")
}

// succeeded renders a value as the XML a model reads.
//
// XML rather than JSON because the reader is a model: a closing tag says which
// thing ended, where a closing brace says only that something did, and that
// redundancy is what survives being quoted out of the middle of a long reply.
//
// No output schema is declared. What an operation returns is described by its
// own contract, which is fully general, and restating that per tool would
// double the size of every listing to say what the reply already shows.
func renderReply(value any, root string) *sdk.CallToolResult {
	rendered, err := (&leaflow.Result{
		Value: value,
	}).XML(root)
	if err != nil {
		return renderRefusal(err)
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{
				Text: rendered,
			},
		},
	}
}

// renderRefusal reports a refusal as a tool result rather than a protocol
// error.
//
// The protocol reserves errors for "no such tool" and the like; a request that
// was made and refused has to come back as content, or the model never sees it
// and cannot correct itself. That distinction is the whole reason IsError
// exists.
//
// What the refusal says is the library's to decide, not this layer's, so a
// caller reaching the platform without MCP reads the same text.
func renderRefusal(err error) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{
			&sdk.TextContent{
				Text: leaflow.RenderError(err, "error"),
			},
		},
	}
}

// decodeArguments decodes a call's raw arguments.
//
// Into a map rather than a Go struct because the shape is the contract's and is
// known only at run time. Numbers land as float64, which is what the contract
// validator compares against.
func decodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
	}

	if decoded == nil {
		return map[string]any{}, nil
	}

	return decoded, nil
}
