# leaflow

Command line interface for the Leaflow platform.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh
```

Installs to `~/.local/bin` and turns on completion for your shell. The download
is checked against the published checksums.

Set `LEAFLOW_INSTALL_DIR` to install elsewhere, or `LEAFLOW_NO_COMPLETION=1` to
skip the completion script.

With Go:

```sh
go install github.com/LeaflowNET/leaflow/cmd/leaflow@latest
```

On Windows, take the zip from the [releases page](https://github.com/LeaflowNET/leaflow/releases).

Installed another way, turn on completion with `leaflow install-completion`.

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

Use `json` in scripts; the table layout may change.

## Configuration

`~/.config/leaflow/config.yaml`, or `--config` / `LEAFLOW_CONFIG`.

| | |
| --- | --- |
| `--context` / `LEAFLOW_CONTEXT` | which context to use |
| `--project` / `LEAFLOW_PROJECT` | project for this run |
| `--output` / `-o` | output format |
| `LEAFLOW_TOKEN` | project token, for CI |

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
