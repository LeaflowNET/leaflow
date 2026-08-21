// Package leaflow is the platform as a Go library: the contracts, the
// argument checking and the requests, with no opinion about who is calling.
//
// The command line is one caller and holds no privileged position. It adds a
// shell's half — flags, positional arguments, a terminal to print to — on top
// of what is here, and so does the MCP server, and so can a service. None of
// them restates what an operation is or what it accepts; that is stated once,
// by the contract, and read once, here.
//
// # Loading it once
//
// Reading two hundred operations out of seven contracts costs about forty
// milliseconds and thirty megabytes of garbage. A command line pays that once
// and exits. A service must not pay it per request — which is the whole reason
// this package exists separately from the binary — so Client holds the parsed
// contracts and is safe to share across goroutines for the life of a process.
//
// # Credentials belong to the call, not to the client
//
// A command line serves one person and can keep their token in a keychain. A
// service serves many at once and is handed one per request, so a token is a
// field on Call rather than state on Client. A Client can carry a default for
// the single-tenant case, and a call that names its own always wins.
//
// # No search
//
// Operations returns the whole list. Deciding which of two hundred operations
// belongs in a prompt is retrieval, and a caller that feeds a model already has
// its own — a second one here would only be the one that disagrees with it.
// What this package owes such a caller is the complete list and an exact schema
// for each entry, both of which come from the contracts rather than from a
// guess about relevance.
package leaflow

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// Client is the platform, loaded once.
//
// Safe for concurrent use: the contracts are read at construction and never
// written afterwards, and everything that varies per call travels in Call.
type Client struct {
	catalog     *catalog
	http        *http.Client
	addresses   transport.Addresses
	credentials transport.Credentials
}

// Options configures a Client. The zero value talks to the hosted platform
// with every contract exposed.
type Options struct {
	// Services limits the catalogue to these contracts. Empty means all of them.
	//
	// An unknown name is an error rather than an empty selection: a service that
	// exposes nothing looks identical to a working one until the first call.
	Services []string

	// ReadOnly drops every operation that is not a GET before any caller can see
	// it. It is how an assistant is handed the platform without being handed the
	// ability to change it.
	ReadOnly bool

	// AccessTokenOnly drops every operation that takes an account token, leaving
	// the ones an access token can call.
	//
	// The account face — registering, listing projects, minting project tokens —
	// only accepts a token that comes from a person's sign-in session, which a
	// service acting on someone's behalf does not have. Left in, those
	// operations are ones a model can find, call, and be refused by, with
	// nothing in the refusal explaining that they were never available.
	//
	// Naming the services to exclude would do the same thing today and would be
	// wrong the day the platform adds another one — silently, because a list of
	// names cannot notice that it is short. The contract states which token each
	// operation takes, so that is what this reads.
	AccessTokenOnly bool

	// Endpoints points at another deployment. The zero value uses the addresses
	// the contracts declare.
	Endpoints transport.Endpoints

	// Credentials is where tokens come from when a Call does not carry its own.
	//
	// A service leaves this nil and puts a Token on every Call, because every
	// call is a different person. A command line supplies its token manager here
	// instead: it serves one person, and it can renew and exchange, which a bare
	// token cannot. A Token satisfies this interface, so a single-tenant caller
	// can simply pass one.
	Credentials transport.Credentials

	// HTTP is the client requests go out on. Supply one to control timeouts,
	// proxies or tracing; nil gets a sixty-second default.
	HTTP *http.Client
}

func New(opts Options) (*Client, error) {
	specs, err := Contracts()
	if err != nil {
		return nil, err
	}

	catalog, err := newCatalog(specs, opts.Services, catalogLimits{
		readOnly:        opts.ReadOnly,
		accessTokenOnly: opts.AccessTokenOnly,
	})
	if err != nil {
		return nil, err
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}

	return &Client{
		catalog:     catalog,
		http:        httpClient,
		addresses:   opts.Endpoints,
		credentials: opts.Credentials,
	}, nil
}

// contracts caches the parsed contracts across every Client in the process.
//
// They are read from the binary and never change while it runs, so parsing
// them twice buys nothing and costs the forty milliseconds again. Shared
// rather than copied because nothing here writes to them.
var contracts = sync.OnceValues(spec.Load)

// Contracts returns the parsed contracts. Most callers want New instead; this
// is for a caller that needs the raw OpenAPI documents.
func Contracts() (*spec.Set, error) {
	return contracts()
}

// ReadOnly reports whether this client was built to refuse writes.
func (c *Client) ReadOnly() bool {
	return c.catalog.limits.readOnly
}

// AccessTokenOnly reports whether this client was built without the account
// face, leaving only what an access token can call.
func (c *Client) AccessTokenOnly() bool {
	return c.catalog.limits.accessTokenOnly
}

// transport builds the sender for one call.
//
// Per call rather than per client, because the credential is per call: a
// service handling two requests at once is acting as two different people, and
// a shared sender would have to be told which one each time anyway. The HTTP
// client underneath is shared, so this costs a struct, not a connection pool.
func (c *Client) transport(credentials transport.Credentials) *transport.Client {
	return transport.New(c.addresses, credentials, c.http)
}

var (
	// ErrNoToken means the call carried no credential and the client had no
	// default. Distinct from a rejected token: nothing was sent.
	ErrNoToken = errors.New("no token for this call")
)

// Token is one caller's credentials.
//
// Two fields because the platform has two user-facing tokens and they are not
// interchangeable. An operation says which it needs, so a caller supplying only
// one gets a clear refusal naming the other rather than a 401 it has to
// interpret.
type Token struct {
	// Access acts inside one project and is what almost every operation takes.
	// The contracts call it a project token: it names a project as well as a
	// person, which is why no request path carries a project id.
	Access string

	// Account is for the account face — registering, listing projects, minting
	// access tokens — and comes from a person's sign-in session. A service
	// acting on someone's behalf does not have one, which is what
	// AccessTokenOnly is for.
	Account string
}

// Token satisfies the transport's Credentials interface.
func (t Token) Token(_ context.Context, kind spec.Credential) (string, error) {
	if kind == spec.AccountToken {
		if t.Account == "" {
			return "", ErrNoToken
		}

		return t.Account, nil
	}

	if t.Access == "" {
		return "", ErrNoToken
	}

	return t.Access, nil
}

// Invalidate always refuses. A token handed in from outside is the only one
// this library has; dropping it would leave nothing to retry with, so the
// refusal is reported to the caller who supplied it and can get another.
func (Token) Invalidate(spec.Credential) error {
	return ErrNoToken
}

func (t Token) empty() bool {
	return t.Access == "" && t.Account == ""
}
