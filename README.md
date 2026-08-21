# leaflow

Command line interface for the Leaflow platform.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.ps1 | iex
```

Both verify the download against the published checksums. The shell script
installs to `~/.local/bin` and turns on completion; the PowerShell one installs
to `%LOCALAPPDATA%\Programs\leaflow` and adds it to your PATH.

| | |
| --- | --- |
| `LEAFLOW_VERSION` | version to install, default the latest |
| `LEAFLOW_INSTALL_DIR` | where to put the binary |
| `LEAFLOW_NO_COMPLETION` | skip completion (install.sh) |
| `LEAFLOW_NO_PATH` | skip the PATH change (install.ps1) |

With Go:

```sh
go install github.com/LeaflowNET/leaflow/cmd/leaflow@latest
```

Installed another way, turn on completion with `leaflow install-completion`.
In PowerShell, add this to `$PROFILE`:

```powershell
leaflow completion powershell | Out-String | Invoke-Expression
```

Later, update in place:

```sh
leaflow update
```

## Sign in

```sh
leaflow login
leaflow project use <project>
leaflow compute list-instances
```

Signing in opens a browser and uses authorization code with PKCE.

The redirect goes to a loopback port on the machine running the command, so
opening the link anywhere else — over ssh, for instance — cannot complete it.
Press Enter at the prompt for a code you can approve from any device, or start
that way with `leaflow login --device`.

For CI, pipe in a refresh token, or set `LEAFLOW_TOKEN` to a project token:

```sh
echo "$LEAFLOW_REFRESH_TOKEN" | leaflow login --with-token
```

## Commands

Every command comes from a service's OpenAPI contract and is named after the
contract's own operationId:

```
leaflow <service> <operationId>
leaflow compute    create-disk
```

The same identifier names the operation in the SDKs, so there is no second
vocabulary to learn. `leaflow <service> --help` groups them by the contract's
tags.

## Output

`-o table` (default), `-o json`, `-o yaml`, or pick your own columns:

```sh
leaflow compute list-instances -o custom-columns=NAME:.name,IP:.private_ip
```

Use `json` in scripts; the table layout may change. `-o xml` is for feeding a
reply to a model: a closing tag says which thing ended, where a closing brace
says only that something did.

## MCP

The same operations, served to an assistant:

```json
{
  "mcpServers": {
    "leaflow": { "command": "leaflow", "args": ["mcp", "serve"] }
  }
}
```

It acts as whoever ran `leaflow login`, in the project that context selects.
It cannot sign in on its own.

Two hundred operations ship with the binary, and a client is sent every tool
definition on every turn — so by default four tools are exposed and the client
walks the contracts through them: `services`, `operations`, `operation-schema`,
`call-operation`. Arguments come back as JSON Schema carrying every constraint
the contract states, and replies come back as XML.

| | |
| --- | --- |
| `--tools operations` | one tool per operation instead; pair with `--service` |
| `--service` | limit to these contracts |
| `--read-only` | drop every write before a client sees it |
| `--http` | serve over HTTP instead of stdin/stdout; binds loopback |

`--http` has no authentication of its own while holding yours, so it refuses a
non-loopback address unless `--allow-remote` says something in front of it
authenticates.

## As a library

The contracts, the argument checking and the requests are a package. The
command line is one caller of it and holds no privileged position:

```go
import "github.com/LeaflowNET/leaflow/pkg/leaflow"

client, err := leaflow.New(leaflow.Options{})    // parses the contracts once

result, err := client.Call(ctx, leaflow.Call{
    Token:     leaflow.Token{Access: userToken},  // per call, not per client
    Service:   "compute",
    Operation: "create-disk",
    Arguments: map[string]any{
        "body": map[string]any{"name": "data", "size_gb": 100},
    },
})

xml, err := result.XML("disk")                   // the shape to hand a model
```

Reading the contracts costs about forty milliseconds and thirty megabytes. A
`Client` holds them and is safe to share across goroutines, so a service pays
that once per process rather than once per request.

Credentials travel with the call because a service is handed a different token
for every request it serves.

A bare `Token` cannot be replaced once refused, which turns off the transport's
one automatic retry. Where the work outlives the credential — an access token is
good for a day, and a task waiting on a human can be paused for longer — pass
something that can mint a fresh one instead, and the retry works:

```go
result, err := client.Call(ctx, leaflow.Call{
    Credentials: yourTokenSource,   // Token(ctx, kind) + Invalidate(kind)
    Service:     "compute",
    Operation:   "list-disks",
})
```

## Pointing elsewhere

Addresses come from the contracts. Override them per service — inside a cluster
that means the service name — or rewrite the domain for a whole deployment:

```go
leaflow.New(leaflow.Options{
    Endpoints: transport.Endpoints{
        Overrides: map[string]string{
            "compute": "http://compute.leaflow.svc.cluster.local:8080",
        },
    },
})

leaflow.New(leaflow.Options{
    Endpoints: transport.Endpoints{Domain: "leaflow.test"},
})
```

A model never sees an address; it names a service.

## Failures

Errors are classified, so a caller decides what to do next without matching on
prose that changes between releases:

```go
switch {
case errors.Is(err, leaflow.ErrTokenExpired):     // mint another, call again
case errors.Is(err, leaflow.ErrPermissionDenied): // no retry will help
case errors.Is(err, leaflow.ErrInvalidArgument):  // leaflow.Problems(err)
}

leaflow.CanRetry(err)                // could the same call succeed later
leaflow.Code(err)                    // the service's own code, which is contract
leaflow.RenderError(err, "error")    // the same XML the MCP surface returns
```

`ErrUnauthenticated`, `ErrTokenExpired`, `ErrPermissionDenied`, `ErrNotFound`,
`ErrInvalidArgument`, `ErrConflict`, `ErrRateLimited`, `ErrUnavailable`. An
expired token satisfies both `ErrTokenExpired` and `ErrUnauthenticated`, so
either question gets a true answer.

Arguments are checked against the contract before anything is sent, and every
problem is reported at once — `leaflow.Problems(err)` returns them individually,
because each is a separate thing to fix.

`Options.ReadOnly` drops every write before a caller can see one, and
`Options.AccessTokenOnly` drops the account face — the operations that need a
person's sign-in session, which a service acting on someone's behalf does not
have. Both read the contract rather than a list of service names, so neither
falls behind when the platform adds one.

Everything an operation accepts is available before calling it:

```go
client.Count()                          // how many operations
client.Services()                       // faces, and the groups inside them
client.Operations()                     // all of them, in contract order
op, _ := client.Operation("compute", "create-disk")
op.Schema()                             // JSON Schema, every contract constraint
```

There is no search here. Choosing which operations belong in a prompt is
retrieval, which a caller building prompts already does its own way.

| | |
| --- | --- |
| `pkg/leaflow` | contracts, validation, calls |
| `pkg/mcp` | the same, over the Model Context Protocol |
| `pkg/spec` | the parsed OpenAPI documents |
| `pkg/transport` | requests, credentials and addresses as interfaces |
| `pkg/output` | table, JSON, YAML and XML rendering |

## Configuration

`~/.config/leaflow/config.yaml`, or `--config` / `LEAFLOW_CONFIG`.

| | |
| --- | --- |
| `--context` / `LEAFLOW_CONTEXT` | which context to use |
| `--project` / `LEAFLOW_PROJECT` | project for this run |
| `--output` / `-o` | output format |
| `LEAFLOW_TOKEN` | access token, for CI |

Service addresses come from the contracts. Point a context at another
deployment by rewriting the domain, or override one service outright:

```sh
leaflow context set local --domain leaflow.test \
  --endpoint compute=https://compute.leaflow.test:18100
leaflow context use local
```

Credentials go to the system keychain, falling back to a `0600` file where
there is none. Set `credential_store` to `keychain` or `file` to decide
explicitly.

## Updating

`leaflow update` replaces the binary with the latest release, verifying it
against the published checksums first. `leaflow update --check` only reports
whether one is available.

Installed through a package manager, update through that instead — the command
refuses when it cannot write to its own directory.

## Exit codes

`0` ok, `2` usage, `3` not authenticated, `4` not permitted, `5` not found,
`6` invalid arguments, `7` service error, `130` interrupted.

Errors are written to stderr, and under `-o json` they are objects:

```json
{"error": {"kind": "validation", "problems": ["--size-gb must be at least 1 (got 0)"], "exit_code": 6}}
```

## License

MIT
