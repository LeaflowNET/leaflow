package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeaflowNET/leaflow/pkg/leaflow"
)

func client(t *testing.T, opts leaflow.Options) *leaflow.Client {
	t.Helper()

	c, err := leaflow.New(opts)
	if err != nil {
		t.Fatalf("leaflow.New: %v", err)
	}

	return c
}

func server(t *testing.T, mode Mode, opts leaflow.Options) *Server {
	t.Helper()

	s, err := New(Options{
		Client:  client(t, opts),
		Mode:    mode,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}

	return s
}

// The count reported at startup and the tools actually registered come from one
// list, so they cannot disagree.
func TestMetaModeExposesFourTools(t *testing.T) {
	if got := server(t, ModeMeta, leaflow.Options{}).Tools(); got != len(metaTools) {
		t.Errorf("tools = %d, want %d", got, len(metaTools))
	}
}

func TestMetaToolNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}

	for _, name := range metaTools {
		if seen[name] {
			t.Errorf("two tools are called %q", name)
		}

		seen[name] = true
	}
}

// End to end through the protocol library: a schema this package serves has to
// be one the SDK will actually marshal, and a tool listing that fails to do so
// fails at the client rather than here.
func TestServerServesItsToolsOverTheProtocol(t *testing.T) {
	s := server(t, ModeOperations, leaflow.Options{
		Services: []string{"tunnel"},
	})

	ctx := context.Background()

	clientTransport, serverTransport := sdk.NewInMemoryTransports()

	session, err := s.newSession().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting the server: %v", err)
	}
	defer session.Close()

	peer := sdk.NewClient(&sdk.Implementation{
		Name:    "test",
		Version: "1",
	}, nil)

	peerSession, err := peer.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	defer peerSession.Close()

	listed, err := peerSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(listed.Tools) == 0 {
		t.Fatal("no tools were served")
	}

	for _, tool := range listed.Tools {
		if !strings.HasPrefix(tool.Name, "tunnel-") {
			t.Errorf("tool %q is not from the tunnel contract", tool.Name)
		}

		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
}

// Two tools with one name is not a panic, it is a silent overwrite: one
// operation would simply stop existing, and nothing would say which.
func TestOperationToolNamesAreUniqueAndWellFormed(t *testing.T) {
	c := client(t, leaflow.Options{})

	seen := map[string]string{}

	for _, op := range c.Operations() {
		name := buildToolName(op)

		if previous, clash := seen[name]; clash {
			t.Errorf("%s and %s both become the tool %q", previous, op.Name(), name)
		}

		seen[name] = op.Name()

		// The protocol allows letters, digits, underscore and hyphen, up to 128
		// characters. The day a contract carries an operationId with a dot in it,
		// this is where it is caught.
		if len(name) > 128 {
			t.Errorf("tool name is too long: %q", name)
		}

		for _, r := range name {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				continue
			}

			t.Errorf("tool name %q has a character the protocol does not allow: %q", name, r)

			break
		}
	}
}

// A refusal comes back as a tool result, not a protocol error: a protocol error
// is invisible to the model, which then cannot correct itself.
func TestRefusalsReachTheModel(t *testing.T) {
	s := server(t, ModeMeta, leaflow.Options{})

	result, err := s.handleCallOperation(context.Background(), &sdk.CallToolRequest{
		Params: &sdk.CallToolParamsRaw{
			Name:      toolCallOperation,
			Arguments: json.RawMessage(`{"service":"compute","operation":"no-such-thing"}`),
		},
	})
	if err != nil {
		t.Fatalf("the refusal came back as a protocol error: %v", err)
	}

	if !result.IsError {
		t.Error("the refusal was not marked as an error")
	}
}

// Replies are XML because the reader is a model. A closing tag says which thing
// ended; a closing brace says only that something did.
func TestRepliesAreXML(t *testing.T) {
	s := server(t, ModeMeta, leaflow.Options{})

	result, err := s.handleServices(context.Background(), &sdk.CallToolRequest{
		Params: &sdk.CallToolParamsRaw{
			Name: toolServices,
		},
	})
	if err != nil {
		t.Fatalf("handleServices: %v", err)
	}

	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is not text: %T", result.Content[0])
	}

	for _, want := range []string{"<services>", "<service>compute</service>", "</services>"} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("reply is missing %q:\n%s", want, text.Text)
		}
	}
}

// A read-only client must not advertise a write. The filtering happens in the
// library; this checks the protocol layer does not undo it.
func TestReadOnlyServerAdvertisesNoWrites(t *testing.T) {
	s := server(t, ModeOperations, leaflow.Options{
		ReadOnly: true,
		Services: []string{"compute"},
	})

	for _, op := range s.client.Operations() {
		if !op.ReadOnly() {
			t.Errorf("%s is a %s and was exposed read-only", buildToolName(op), op.Method())
		}
	}
}
