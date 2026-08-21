package leaflow

import (
	"bytes"
	"context"
	"fmt"

	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// Call is one request to make.
type Call struct {
	// Service and Operation name the operation, as Services and Operations
	// report them. Both the contract's operationId and its command-line
	// spelling are accepted, because those are the two names in circulation.
	Service string

	Operation string

	// Arguments are shaped the way Operation.Schema says: path and query
	// parameters at the top level under the contract's own names, a request body
	// whole under "body".
	//
	// They are checked against the contract before anything is sent, so a call
	// that satisfies the schema is a call that will be tried.
	Arguments map[string]any

	// Token is this caller's credentials, overriding the client's default. In a
	// service this is where the request's own user arrives.
	//
	// It is a token and nothing more, so a refused one cannot be replaced: see
	// Credentials for the case where it can.
	Token Token

	// Credentials replaces Token for a caller whose tokens expire sooner than
	// the work outlives them.
	//
	// An access token is good for a day. A piece of work that waits on a human
	// can be paused for longer than that, and when it resumes the first request
	// it makes is the one that gets a 401. The transport already retries that
	// exactly once, after asking the credentials to drop what was refused — but
	// a Token has nothing else to offer and says so, which turns the retry off.
	//
	// Supplying something that can mint a fresh token turns it back on. The
	// alternative is that the caller's own retry has to know which errors mean
	// "expired", which is the knowledge this package exists to hold.
	Credentials transport.Credentials
}

// Result is what came back.
type Result struct {
	// Status is the HTTP status. Worth having even on success: a 204 carries no
	// body, and "deleted" is otherwise indistinguishable from "returned nothing".
	Status int

	// Value is the decoded reply — map[string]any, []any or a scalar — or nil
	// when the operation returned no body.
	Value any
}

// Call checks the arguments against the contract and makes the request.
func (c *Client) Call(ctx context.Context, call Call) (*Result, error) {
	operation, err := c.Operation(call.Service, call.Operation)
	if err != nil {
		return nil, err
	}

	// A call's own credentials win over the client's default. In a service the
	// default is nil and this is where the request's user arrives; on a command
	// line it is the other way round. Credentials beats Token because it is the
	// more capable of the two, so supplying both is not ambiguous.
	credentials := c.credentials

	switch {
	case call.Credentials != nil:
		credentials = call.Credentials
	case !call.Token.empty():
		credentials = call.Token
	}

	if credentials == nil {
		kind := "access"
		if operation.AccountToken() {
			kind = "account"
		}

		return nil, fmt.Errorf("%w: %s %s needs a %s token",
			ErrNoToken, call.Service, call.Operation, kind)
	}

	request, err := operation.Request(call.Arguments)
	if err != nil {
		return nil, err
	}

	value, status, err := c.transport(credentials).Do(ctx, request)
	if err != nil {
		return nil, err
	}

	return &Result{
		Status: status,
		Value:  value,
	}, nil
}

// XML renders the reply as tagged text, which is the shape to hand a model.
//
// See pkg/output: a closing tag says which thing ended, where a closing brace
// says only that something did — and that redundancy is what survives being
// quoted out of the middle of a long reply.
func (r *Result) XML(root string) (string, error) {
	return r.render(output.FormatXML, root)
}

// JSON renders the reply as JSON, with fields in the same order every other
// format uses.
func (r *Result) JSON() (string, error) {
	return r.render(output.FormatJSON, "")
}

func (r *Result) render(format output.Format, root string) (string, error) {
	if r == nil || r.Value == nil {
		return "", nil
	}

	var buffer bytes.Buffer

	printer := &output.Printer{
		Format: format,
		Out:    &buffer,
		Root:   root,
	}
	if err := printer.Print(r.Value); err != nil {
		return "", fmt.Errorf("cannot render the reply: %w", err)
	}

	return buffer.String(), nil
}
