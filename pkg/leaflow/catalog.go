package leaflow

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"
)

var (
	ErrNoSuchService = errors.New("no such service")

	ErrNoSuchOperation = errors.New("no such operation")
)

// catalog is what this server exposes: every operation that survived the
// filters, indexed the way a caller asks for one.
type catalog struct {
	specs *spec.Set

	services []*spec.Service

	operations []*Operation

	// byService keeps the contract's own order within each service.
	byService map[string][]*Operation

	limits catalogLimits
}

// catalogLimits is what was dropped before anything was listed.
//
// Filtering here rather than at each caller means a hidden operation is hidden
// everywhere — from the listing, from the lookup, and from the tool schema a
// model is shown — so nothing can offer a call that would then be refused.
type catalogLimits struct {
	readOnly bool

	accessTokenOnly bool
}

// newCatalog selects the operations to expose.
//
// An unknown service name is an error rather than an empty selection: starting
// a server that exposes nothing looks identical to a working one until the
// first call fails, and the mistake is in a config file nobody will reread.
func newCatalog(specs *spec.Set, services []string, limits catalogLimits) (*catalog, error) {
	wanted := map[string]bool{}

	for _, name := range services {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		if _, ok := specs.Service(name); !ok {
			return nil, fmt.Errorf("%w: %q; contracts ship for %s",
				ErrNoSuchService, name, strings.Join(specs.Names(), ", "))
		}

		wanted[name] = true
	}

	c := &catalog{
		specs:     specs,
		byService: map[string][]*Operation{},
		limits:    limits,
	}

	for _, service := range specs.Services() {
		if len(wanted) > 0 && !wanted[service.Name] {
			continue
		}

		var kept []*Operation

		for _, op := range service.Operations() {
			if limits.readOnly && !isReadOnly(op) {
				continue
			}

			if limits.accessTokenOnly && op.Credential == spec.AccountToken {
				continue
			}

			kept = append(kept, newOperation(op))
		}

		if len(kept) == 0 {
			continue
		}

		c.services = append(c.services, service)
		c.byService[service.Name] = kept
		c.operations = append(c.operations, kept...)
	}

	return c, nil
}

// isReadOnly reads the method, which is where HTTP states whether a request is
// meant to change anything. It is the contract's own claim rather than a guess
// about a name: `POST /instances/{id}/actions` is a write however it is spelled.
func isReadOnly(op *spec.Operation) bool {
	switch op.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// lookup finds an operation by service and id.
//
// Both the contract's operationId and its command-line spelling are accepted,
// because those are the two names in circulation and a caller reading the CLI's
// documentation should not have to know they differ. Everything this server
// prints uses the operationId.
func (c *catalog) find(service, id string) (*Operation, error) {
	operations, ok := c.byService[service]
	if !ok {
		return nil, fmt.Errorf("%w: %q; this server exposes %s",
			ErrNoSuchService, service, strings.Join(c.listServices(), ", "))
	}

	kebab := naming.Kebab(id)

	for _, op := range operations {
		if op.spec.ID == id || naming.Kebab(op.spec.ID) == kebab {
			return op, nil
		}
	}

	// A refusal that lists the near misses is what lets a caller correct itself
	// without another round of listing.
	if near := c.suggest(operations, id); len(near) > 0 {
		return nil, fmt.Errorf("%w: %s has no %q; closest are %s",
			ErrNoSuchOperation, service, id, strings.Join(near, ", "))
	}

	if _, exposed := c.specs.Service(service); exposed {
		if c.limits.readOnly {
			return nil, fmt.Errorf("%w: %s has no read-only %q, and this client was built read-only",
				ErrNoSuchOperation, service, id)
		}

		if c.limits.accessTokenOnly {
			return nil, fmt.Errorf("%w: %s has no %q that a project token can call, "+
				"and this client was built for project tokens only",
				ErrNoSuchOperation, service, id)
		}
	}

	return nil, fmt.Errorf("%w: %s has no %q; use list-operations to see what it has",
		ErrNoSuchOperation, service, id)
}

// nearest offers the operations whose names share a word with what was asked
// for. Deliberately not an edit distance: the mistake here is almost always a
// wrong verb or a wrong noun (`get-disk` for `describe-disk`), not a typo.
func (c *catalog) suggest(operations []*Operation, id string) []string {
	words := strings.Split(naming.Kebab(id), "-")

	var near []string

	for _, op := range operations {
		name := naming.Kebab(op.spec.ID)

		for _, word := range words {
			if len(word) < 3 {
				continue
			}

			if strings.Contains(name, word) {
				near = append(near, op.spec.ID)

				break
			}
		}
	}

	sort.Strings(near)

	const most = 8
	if len(near) > most {
		near = near[:most]
	}

	return near
}

// groups lists a service's resource groups: what a caller narrows by before it
// has any vocabulary for the operations themselves.
//
// Counted from the operations this server actually exposes, not from the
// contract's tag list. Most contracts declare no top-level tags at all — the
// tag lives on the operation — so reading only the declared list reports no
// groups for a service with eight of them, and the tag filter becomes something
// no caller can discover. Under --read-only it would also offer a group whose
// operations had all been dropped.
//
// Declared tags come first, in the contract's own order, which is editorial: it
// leads with the primary resource, and alphabetising would throw that away.
func (c *catalog) groups(service *spec.Service) []any {
	counts := map[string]int{}

	var order []string

	for _, op := range c.byService[service.Name] {
		if len(op.spec.Tags) == 0 {
			continue
		}

		tag := op.spec.Tags[0]

		if counts[tag] == 0 {
			order = append(order, tag)
		}

		counts[tag]++
	}

	sort.Strings(order)

	described := map[string]string{}

	var declared []string

	if service.Doc != nil {
		for _, tag := range service.Doc.Tags {
			if tag == nil || tag.Name == "" || counts[tag.Name] == 0 {
				continue
			}

			described[tag.Name] = cutAtNewline(tag.Description)
			declared = append(declared, tag.Name)
		}
	}

	groups := make([]any, 0, len(order))
	emitted := map[string]bool{}

	for _, name := range append(declared, order...) {
		if emitted[name] {
			continue
		}

		emitted[name] = true

		group := map[string]any{
			"name":       name,
			"operations": counts[name],
		}

		if summary := described[name]; summary != "" {
			group["description"] = summary
		}

		groups = append(groups, group)
	}

	return groups
}

func (c *catalog) listServices() []string {
	names := make([]string, 0, len(c.services))
	for _, service := range c.services {
		names = append(names, service.Name)
	}

	sort.Strings(names)

	return names
}

// firstLine keeps a tag's description to a sentence. The rest is in the
// contract, and a listing is meant to be scanned.
func cutAtNewline(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index > 0 {
		text = text[:index]
	}

	return text
}
