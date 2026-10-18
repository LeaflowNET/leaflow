# leaflow

Command-line interface for Leaflow.

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
| `LEAFLOW_UNINSTALL` | same as `--uninstall` |
| `LEAFLOW_PURGE` | same as `--purge` |

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

## Uninstall

```sh
curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh -s -- --uninstall
```

In PowerShell:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.ps1))) -Uninstall
```

This removes the binary and the completion, and keeps `~/.config/leaflow`. Pass
`--purge`, or `-Purge` in PowerShell, to sign out and remove the configuration
as well.

## Sign in

```sh
leaflow login
leaflow project use <project>
```

To browse available services and commands:

```sh
leaflow --help
leaflow <service> --help
```

Signing in opens a browser and uses authorization code with PKCE.

The redirect goes to a loopback port on the machine running the command, so
opening the link anywhere else — over ssh, for instance — cannot complete it.
Press Enter at the prompt for a code you can approve from any device, or start
that way with `leaflow login --device`.

For CI, pipe in a refresh token, or set `LEAFLOW_TOKEN` to a scoped token:

```sh
echo "$LEAFLOW_REFRESH_TOKEN" | leaflow login --with-token
```

## Commands

Use `leaflow --help` to browse available services. Use
`leaflow <service> --help` to see a service's commands. Add `--help` to a
command path to view its usage and options.

To update Leaflow, run:

```sh
leaflow update
```

## Output

Choose `-o table` (default), `-o json`, `-o yaml`, or `-o xml`. Use
`-o custom-columns=NAME:.field` to select fields from a command's response.

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

By default, the MCP server exposes a compact set of tools for discovering
services and operations, inspecting their inputs, and making calls. To expose
each operation as its own tool, use `--tools operations`; use `--service` to
limit the available services.

| | |
| --- | --- |
| `--tools operations` | one tool per operation instead; pair with `--service` |
| `--service` | limit the available services |
| `--read-only` | drop every write before a client sees it |
| `--http` | serve over HTTP instead of stdin/stdout; binds loopback |

`--http` has no authentication of its own while holding yours, so it refuses a
non-loopback address unless `--allow-remote` says something in front of it
authenticates.

## As a library

Leaflow is also available as a Go library:

```go
import "github.com/LeaflowNET/leaflow/pkg/leaflow"

client, err := leaflow.New(leaflow.Options{})

service := "<service>"     // choose from client.Services()
operation := "<operation>" // choose from client.Operations()
arguments := map[string]any{} // provide the values this operation requires

result, err := client.Call(ctx, leaflow.Call{
    Token:     leaflow.Token{Access: userToken},  // per call, not per client
    Service:   service,
    Operation: operation,
    Arguments: arguments,
})

xml, err := result.XML("result")
```

Create one `Client` per process and share it safely across goroutines.

Credentials travel with the call because a service is handed a different token
for every request it serves.

A bare `Token` cannot be replaced once refused, which turns off the transport's
one automatic retry. Where the work outlives the credential — an access token is
good for a day, and a task waiting on a human can be paused for longer — pass
something that can mint a fresh one instead, and the retry works:

```go
result, err := client.Call(ctx, leaflow.Call{
    Credentials: yourTokenSource,   // Token(ctx, kind) + Invalidate(kind)
    Service:     service,
    Operation:   operation,
})
```

## Pointing elsewhere

Set an endpoint override for a service in another deployment, or change the
domain for the whole deployment:

```go
leaflow.New(leaflow.Options{
    Endpoints: transport.Endpoints{
        Overrides: map[string]string{
            "service-name": "https://service.example.com",
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
leaflow.Code(err)                    // the service's error code
leaflow.RenderError(err, "error")    // the same XML the MCP surface returns
```

`ErrUnauthenticated`, `ErrTokenExpired`, `ErrPermissionDenied`, `ErrNotFound`,
`ErrInvalidArgument`, `ErrConflict`, `ErrRateLimited`, `ErrUnavailable`. An
expired token satisfies both `ErrTokenExpired` and `ErrUnauthenticated`, so
either question gets a true answer.

Arguments are validated before a request is sent. `leaflow.Problems(err)`
returns each issue so it can be corrected.

`Options.ReadOnly` removes operations that change data. `Options.AccessTokenOnly`
removes operations that require an account token.

Everything an operation accepts is available before calling it:

```go
client.Count()                          // number of available operations
client.Services()                       // available services and groups
client.Operations()                     // available operations
op, _ := client.Operation(service, operation)
op.Schema()                             // accepted inputs
```

| | |
| --- | --- |
| `pkg/leaflow` | service discovery, validation, and calls |
| `pkg/mcp` | the same, over the Model Context Protocol |
| `pkg/spec` | service and operation descriptions |
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

Point a context at another deployment by changing its domain, or override one
service endpoint:

```sh
leaflow context set local --domain leaflow.test \
  --endpoint service-name=https://service.leaflow.test:18100
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
