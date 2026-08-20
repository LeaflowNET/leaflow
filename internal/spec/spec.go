// Package spec holds every API this CLI knows about.
//
// Contracts ship inside the binary, mirroring the layout of the contracts
// repository, and are read from there and nowhere else.
//
// There is deliberately no runtime refresh. Letting a local cache change which
// commands exist would make one released version behave differently on two
// machines, and "which contract do you have" becomes the first question on
// every support thread. Contracts travel with the release; a new backend
// operation arrives with the next one.
//
// Reading OpenAPI still earns its keep without that: the command tree needs no
// hand-maintained list, and arguments are checked against the contract before a
// request goes out.
package spec

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// The tree is embedded as published — leaflow/<service>/v1/openapi.yaml plus
// the shared leaflow/type/v1/error.yaml that every contract references. Keeping
// the layout means a synced file is byte-identical to upstream, so a review
// diff is the upstream diff and nothing in between.
//
//go:embed all:data/leaflow
var embedded embed.FS

const (
	embedRoot = "data/leaflow"

	// typeService holds shared schemas rather than operations, so it is not a
	// service and gets no command tree.
	typeService = "type"
)

var (
	ErrNoEmbeddedSpecs = errors.New("embedded contracts are unreadable")

	ErrSpecUnparsable = errors.New("cannot parse contract")
)

// Operation is one API operation as the CLI sees it.
//
// ID is the spec's operationId and the CLI's only anchor: the command tree
// names commands after it. It is already treated as a stable identifier — both
// the Go and TypeScript SDKs generate their method names from it — so a command
// name cannot drift without breaking those too.
type Operation struct {
	ID      string
	Service string
	Method  string
	Path    string

	Summary     string
	Description string
	Deprecated  bool

	Tags []string

	// Credential decides which token to send. It follows from the service: the
	// account face and the project face are separate services with separate
	// contracts and separate addresses.
	Credential Credential

	Parameters openapi3.Parameters

	RequestBody *openapi3.RequestBody
}

type Service struct {
	Name string

	Version string

	Doc *openapi3.T

	ops   map[string]*Operation
	order []string
}

type Set struct {
	services map[string]*Service
	names    []string
}

// Load reads every embedded contract.
func Load() (*Set, error) {
	names, err := EmbeddedNames()
	if err != nil {
		return nil, err
	}

	set := &Set{services: map[string]*Service{}}

	for _, name := range names {
		doc, err := parse(readEmbedded(name), name)
		if err != nil {
			return nil, fmt.Errorf("%w %s: %v", ErrSpecUnparsable, name, err)
		}

		set.add(newService(name, doc))
	}

	sort.Strings(set.names)

	return set, nil
}

func readEmbedded(service string) []byte {
	data, err := embedded.ReadFile(embedPath(service))
	if err != nil {
		return nil
	}

	return data
}

func embedPath(service string) string {
	return path.Join(embedRoot, service, "v1", "openapi.yaml")
}

// parse resolves the contract, including the shared error schema every service
// references as ../../type/v1/error.yaml.
//
// External references are allowed but served from the embedded tree, never from
// disk or the network: the document is loaded under its published path so the
// relative reference resolves, and the reader below refuses anything outside.
//
// doc.Validate() is deliberately not called. Contracts are generated artefacts,
// not user input, and revalidating one on every startup re-confirms what the
// producer already guarantees.
func parse(data []byte, service string) (*openapi3.T, error) {
	if len(data) == 0 {
		return nil, errors.New("empty file")
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = readEmbeddedURI

	location := &url.URL{Path: embedPath(service)}

	return loader.LoadFromDataWithPath(data, location)
}

// readEmbeddedURI serves a contract's external references from the embedded
// tree, and refuses anything outside it.
func readEmbeddedURI(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
	if location.Scheme != "" && location.Scheme != "file" {
		return nil, fmt.Errorf("%w: refusing remote reference %s", ErrSpecUnparsable, location)
	}

	clean := path.Clean(strings.TrimPrefix(location.Path, "/"))

	if !strings.HasPrefix(clean, embedRoot+"/") {
		return nil, fmt.Errorf("%w: reference outside the contract tree: %s", ErrSpecUnparsable, location.Path)
	}

	return embedded.ReadFile(clean)
}

func newService(name string, doc *openapi3.T) *Service {
	svc := &Service{
		Name:    name,
		Version: docVersion(doc),
		Doc:     doc,
		ops:     map[string]*Operation{},
	}

	if doc.Paths == nil {
		return svc
	}

	credential := CredentialFor(name)

	for _, p := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Find(p)
		if item == nil {
			continue
		}

		for method, op := range item.Operations() {
			// An operation without an operationId has no name this CLI can point
			// at. Skipping is silent because it is invisible to users, and adding
			// the id upstream makes it appear on its own.
			if op.OperationID == "" {
				continue
			}

			params := append(openapi3.Parameters{}, item.Parameters...)
			params = append(params, op.Parameters...)

			operation := &Operation{
				ID:          op.OperationID,
				Service:     name,
				Method:      method,
				Path:        p,
				Summary:     op.Summary,
				Description: op.Description,
				Deprecated:  op.Deprecated,
				Tags:        op.Tags,
				Credential:  credential,
				Parameters:  params,
			}

			if op.RequestBody != nil {
				operation.RequestBody = op.RequestBody.Value
			}

			svc.ops[op.OperationID] = operation
			svc.order = append(svc.order, op.OperationID)
		}
	}

	sort.Strings(svc.order)

	return svc
}

// docVersion reads info.version, which is where a contract states its own
// version.
func docVersion(doc *openapi3.T) string {
	if doc != nil && doc.Info != nil && doc.Info.Version != "" {
		return doc.Info.Version
	}

	return "unknown"
}

func (s *Set) add(svc *Service) {
	s.services[svc.Name] = svc
	s.names = append(s.names, svc.Name)
}

func (s *Set) Names() []string { return s.names }

func (s *Set) Service(name string) (*Service, bool) {
	svc, ok := s.services[name]

	return svc, ok
}

func (s *Set) Services() []*Service {
	out := make([]*Service, 0, len(s.names))
	for _, name := range s.names {
		out = append(out, s.services[name])
	}

	return out
}

func (s *Service) Operation(id string) (*Operation, bool) {
	op, ok := s.ops[id]

	return op, ok
}

func (s *Service) OperationIDs() []string { return s.order }

func (s *Service) Operations() []*Operation {
	out := make([]*Operation, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.ops[id])
	}

	return out
}

// EmbeddedNames lists the services that ship with a contract.
func EmbeddedNames() ([]string, error) {
	entries, err := fs.ReadDir(embedded, embedRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoEmbeddedSpecs, err)
	}

	var names []string

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == typeService {
			continue
		}

		if _, err := embedded.Open(embedPath(entry.Name())); err != nil {
			continue
		}

		names = append(names, entry.Name())
	}

	sort.Strings(names)

	return names, nil
}
