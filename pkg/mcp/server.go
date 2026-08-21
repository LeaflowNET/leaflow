// Package mcp serves the platform over the Model Context Protocol.
//
// It is a presentation layer and nothing else, in the same sense that the cobra
// command tree and an HTTP handler are: it translates one wire protocol into
// calls on pkg/leaflow and translates the answers back. What an operation is,
// what it accepts, whether an argument is valid, where the request goes — none
// of that is decided here, so a tool cannot drift from a command.
//
// The one thing this layer does decide is shape, and it is the reason MCP is
// worth having over the command line: a tool call is already JSON. A shell
// spends real effort flattening a request body into one flag per field; here
// the contract's schema is handed to the client as it stands, nesting intact,
// under the contract's own names.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeaflowNET/leaflow/pkg/leaflow"
)

var ErrUnknownMode = errors.New("unknown tool mode")

// Mode decides how the two hundred operations are presented.
//
// This is the one question this surface has that a command line does not. A
// shell can hold every command because nobody reads them all; a model is sent
// the whole tool list on every turn, and a list that large crowds out the
// conversation it is meant to serve.
type Mode string

const (
	// ModeMeta exposes four fixed tools and lets the client walk the contracts
	// through them. Context cost does not grow with the platform.
	ModeMeta Mode = "meta"

	// ModeOperations exposes one tool per operation, which is what clients are
	// built for — names complete, schemas visible without a call — and is usable
	// once Services narrows it to a service or two.
	ModeOperations Mode = "operations"
)

var Modes = []string{
	string(ModeMeta),
	string(ModeOperations),
}

// Options is one server's configuration.
type Options struct {
	// Client is the platform. Its credentials, services and read-only setting
	// are already decided; this server does not second-guess any of them.
	Client *leaflow.Client

	// Version is reported to the client as the server's.
	Version string

	Mode Mode
}

type Server struct {
	client  *leaflow.Client
	mode    Mode
	version string
}

func New(opts Options) (*Server, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeMeta
	}

	if mode != ModeMeta && mode != ModeOperations {
		return nil, fmt.Errorf("%w %q: use one of %s", ErrUnknownMode, mode, strings.Join(Modes, ", "))
	}

	if opts.Client == nil {
		return nil, errors.New("mcp: no client")
	}

	return &Server{
		client:  opts.Client,
		mode:    mode,
		version: opts.Version,
	}, nil
}

// Tools reports how many this server will advertise, so whoever starts it can
// say so before it goes quiet.
func (s *Server) Tools() int {
	if s.mode == ModeOperations {
		return s.client.Count()
	}

	return len(metaTools)
}

func (s *Server) newSession() *sdk.Server {
	server := sdk.NewServer(
		&sdk.Implementation{
			Name:    "leaflow",
			Title:   "Leaflow platform",
			Version: s.version,
		},
		&sdk.ServerOptions{
			Instructions: s.writeInstructions(),
		},
	)

	if s.mode == ModeOperations {
		s.addOperationTools(server)
	} else {
		s.addMetaTools(server)
	}

	return server
}

// Run serves one client over the given streams, which in practice are stdin
// and stdout.
//
// Nothing else may write to out while this runs: the stream is the protocol.
// Everything that would normally be printed goes to stderr, which the client
// shows in its logs.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	incoming := &hangup{
		Reader: in,
	}

	err := s.newSession().Run(ctx, &sdk.IOTransport{
		Reader: incoming,
		// Not closed on the way out: out is the process's stdout, which the shell
		// owns and which anything still writing an error needs afterwards.
		Writer: nopCloser{out},
	})

	// A client disconnects by closing the stream, and that is how a session that
	// went entirely to plan ends. The end of the input is observed here rather
	// than read out of the error, which arrives worded by the protocol library
	// and would go unrecognised the day that wording changes — leaving every
	// ordinary exit looking, in the client's log, like a server that crashed.
	if incoming.reachedEOF.Load() {
		return nil
	}

	return err
}

// Handler serves the same server over HTTP.
//
// One sdk.Server backs every session rather than one per request: the tools
// come from contracts that do not change while the process runs.
func (s *Server) Handler() http.Handler {
	server := s.newSession()

	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		return server
	}, nil)
}

// hangup notices the end of the client's half of the connection.
type hangup struct {
	io.Reader

	reachedEOF atomic.Bool
}

func (h *hangup) Read(p []byte) (int, error) {
	n, err := h.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		h.reachedEOF.Store(true)
	}

	return n, err
}

func (h *hangup) Close() error {
	return nil
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error {
	return nil
}

// instructions tell a client what this server is before it calls anything.
//
// Worth the words: without them a model discovers the credential model by
// failing a call, and "not logged in" is a problem the user has to fix
// elsewhere, not one the model can work around.
func (s *Server) writeInstructions() string {
	var builder strings.Builder

	builder.WriteString(`Leaflow platform, served from the same OpenAPI contracts as the leaflow CLI.

Replies come back as XML rather than JSON: a closing tag says which thing
ended, which is what survives being read out of the middle of a long reply.

`)

	if s.mode == ModeMeta {
		builder.WriteString(`Two hundred operations exist and they are not listed here. Start with
services, then operations for the one you want, then operation-schema to read
what it accepts, then call-operation. operation-schema returns the exact JSON
Schema for an operation's arguments, including every constraint the contract
states, so a call that satisfies it is a call that will be tried.

`)
	}

	if s.client.ReadOnly() {
		builder.WriteString("This server is read-only: it exposes no operation that changes anything.\n\n")
	}

	names := make([]string, 0)
	for _, service := range s.client.Services() {
		names = append(names, service.Name)
	}

	fmt.Fprintf(&builder, "Services: %s.", strings.Join(names, ", "))

	return builder.String()
}

// addOperationTools gives every operation its own tool.
func (s *Server) addOperationTools(server *sdk.Server) {
	for _, op := range s.client.Operations() {
		server.AddTool(&sdk.Tool{
			Name:        buildToolName(op),
			Title:       op.Summary(),
			Description: writeToolDescription(op),
			InputSchema: op.Schema(),
			Annotations: annotate(op),
		}, s.bindOperation(op))
	}
}

func (s *Server) bindOperation(op *leaflow.Operation) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		values, err := decodeArguments(req.Params.Arguments)
		if err != nil {
			return renderRefusal(err), nil
		}

		return s.call(ctx, op.Service(), op.Name(), values), nil
	}
}

// toolName is the operation on this surface: `compute-create-disk` for what the
// command line spells `leaflow compute create-disk`.
//
// Both are the contract's operationId, so a tool name cannot drift from a
// command name — and neither can change without changing the identifier the
// SDKs generate their method names from.
func buildToolName(op *leaflow.Operation) string {
	return op.Service() + "-" + op.Name()
}

// toolDescription is what the model reads to decide whether this is the
// operation it wants.
//
// It ends with the method, the path and the equivalent command line: the first
// two are what match backend logs and API documentation, and the third is what
// lets an assistant tell someone how to do the same thing themselves.
func writeToolDescription(op *leaflow.Operation) string {
	var builder strings.Builder

	if op.Summary() != "" {
		builder.WriteString(op.Summary())
		builder.WriteString("\n\n")
	}

	if op.Details() != "" {
		builder.WriteString(op.Details())
		builder.WriteString("\n\n")
	}

	if op.Deprecated() {
		builder.WriteString("Deprecated by the contract.\n\n")
	}

	fmt.Fprintf(&builder, "%s %s\ncommand line: %s", op.Method(), op.Path(), op.Command())

	return builder.String()
}

// annotate states what kind of call this is, from the method the contract
// declares. HTTP already defines these: a GET changes nothing, a DELETE
// destroys, a PUT repeated is a PUT. Nothing is inferred from the name.
//
// They are hints, and a client is free to ignore them — but a client that does
// use them is one that asks before deleting a disk, which is the difference
// worth having.
func annotate(op *leaflow.Operation) *sdk.ToolAnnotations {
	annotations := &sdk.ToolAnnotations{
		Title:         op.Summary(),
		OpenWorldHint: new(true),
	}

	if op.ReadOnly() {
		annotations.ReadOnlyHint = true

		return annotations
	}

	switch op.Method() {
	case http.MethodDelete, http.MethodPut:
		annotations.DestructiveHint = new(true)
		annotations.IdempotentHint = true
	default:
		// POST and PATCH are left as non-destructive: they create and they amend,
		// and marking every write destructive would make the flag mean "write",
		// which is what a client would then have to ignore.
		annotations.DestructiveHint = new(false)
	}

	return annotations
}
