package leaflow

import (
	"fmt"
	"strings"

	"github.com/LeaflowNET/leaflow/pkg/spec"
)

// Service is one of the platform's faces.
type Service struct {
	Name string

	// Title is the contract's own, which is what to show a person or a model.
	Title string

	// Contract is the version of the document this was read from.
	Contract string

	// Operations is how many this client exposes, which under ReadOnly is fewer
	// than the contract declares.
	Operations int

	// Groups are the resource groups within the service — Disk, Instance,
	// SecurityGroup — and are what to narrow by before knowing any operation
	// names.
	Groups []Group
}

type Group struct {
	Name string

	Description string

	Operations int
}

// Services lists what this client exposes.
func (c *Client) Services() []Service {
	services := make([]Service, 0, len(c.catalog.services))

	for _, service := range c.catalog.services {
		services = append(services, Service{
			Name:       service.Name,
			Title:      readTitle(service),
			Contract:   service.Version,
			Operations: len(c.catalog.byService[service.Name]),
			Groups:     c.groups(service),
		})
	}

	return services
}

func (c *Client) groups(service *spec.Service) []Group {
	raw := c.catalog.groups(service)

	groups := make([]Group, 0, len(raw))

	for _, entry := range raw {
		group, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		name, _ := group["name"].(string)
		count, _ := group["operations"].(int)
		description, _ := group["description"].(string)

		groups = append(groups, Group{
			Name:        name,
			Description: description,
			Operations:  count,
		})
	}

	return groups
}

func readTitle(service *spec.Service) string {
	if service.Doc != nil && service.Doc.Info != nil && service.Doc.Info.Title != "" {
		return service.Doc.Info.Title
	}

	return service.Name
}

// Operations lists everything this client exposes, in the contract's order.
//
// There is deliberately no search here. A caller that feeds these to a model
// has its own retrieval — that is a property of how it builds prompts, not of
// what the platform offers — and a second, worse search living in this library
// would only be the one that disagrees with it. What this package owes such a
// caller is the whole list and an exact schema for each entry.
//
// Narrowing that is not retrieval belongs in Options: Services picks the
// contracts and ReadOnly drops the writes, both before anything is listed.
func (c *Client) Operations() []*Operation {
	operations := make([]*Operation, len(c.catalog.operations))
	copy(operations, c.catalog.operations)

	return operations
}

// OperationsIn lists one service's, for a caller that walks them a contract at
// a time rather than holding all two hundred.
func (c *Client) OperationsIn(service string) []*Operation {
	operations := make([]*Operation, len(c.catalog.byService[service]))
	copy(operations, c.catalog.byService[service])

	return operations
}

// Count is how many operations this client exposes.
func (c *Client) Count() int {
	return len(c.catalog.operations)
}

// Operation finds one by service and id.
//
// A refusal names the near misses, so a caller that guessed can correct itself
// without listing everything again.
func (c *Client) Operation(service, id string) (*Operation, error) {
	return c.catalog.find(service, id)
}

// Name is the operationId, which is the operation's one identifier: the command
// line names a command after it, the SDKs generate a method name from it, and a
// tool takes its name from it.
func (o *Operation) Name() string {
	return o.spec.ID
}

func (o *Operation) Service() string {
	return o.spec.Service
}

func (o *Operation) Method() string {
	return o.spec.Method
}

func (o *Operation) Path() string {
	return o.spec.Path
}

func (o *Operation) Summary() string {
	return o.spec.Summary
}

func (o *Operation) Details() string {
	return strings.TrimSpace(o.spec.Description)
}

func (o *Operation) Deprecated() bool {
	return o.spec.Deprecated
}

// Group is the resource group the contract files this under.
func (o *Operation) Group() string {
	if len(o.spec.Tags) == 0 {
		return ""
	}

	return o.spec.Tags[0]
}

// ReadOnly reports whether the operation changes anything, read off the method
// — which is where HTTP states it. Nothing is inferred from the name: `POST
// /instances/{id}/actions` is a write however it is spelled.
func (o *Operation) ReadOnly() bool {
	return isReadOnly(o.spec)
}

// AccountToken reports that this operation takes an account token rather than
// an access token. It is the one thing about a call that is not in its
// arguments.
func (o *Operation) AccountToken() bool {
	return o.spec.Credential == spec.AccountToken
}

// Command is the equivalent command line, which is what lets an assistant tell
// someone how to do the same thing themselves.
func (o *Operation) Command() string {
	return fmt.Sprintf("leaflow %s %s", o.spec.Service, o.spec.ID)
}
