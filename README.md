# leaflow

Command line interface for the Leaflow platform.

## Install

```sh
go install github.com/LeaflowNET/leaflow/cmd/leaflow@latest
```

## Sign in

```sh
leaflow login
leaflow project use <project>
leaflow compute instance list-instances
```

Signing in opens a browser and uses authorization code with PKCE. On a machine
without one — over ssh, in a container — use `leaflow login --device` and
approve the code elsewhere.

For CI, pipe in a refresh token, or set `LEAFLOW_TOKEN` to a project token:

```sh
echo "$LEAFLOW_REFRESH_TOKEN" | leaflow login --with-token
```

## Commands

Commands come from each service's OpenAPI contract, named after the contract's
own tag and operationId:

```
leaflow <service> <tag> <operationId>
leaflow compute    disk  create-disk
```

The same identifier names the operation in the SDKs, so there is no second
vocabulary to learn.

## Output

`-o table` (default), `-o json`, `-o yaml`, or pick your own columns:

```sh
leaflow compute instance list-instances -o custom-columns=NAME:.name,IP:.private_ip
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

Point a context at another deployment:

```sh
leaflow context set local --domain leaflow.test \
  --endpoint compute=https://compute.leaflow.test:18100
leaflow context use local
```

Credentials go to the system keychain, falling back to a `0600` file where
there is none. Set `credential_store` to `keychain` or `file` to decide
explicitly.

## Exit codes

`0` ok, `2` usage, `3` not authenticated, `4` not permitted, `5` not found,
`6` invalid arguments, `7` service error, `130` interrupted.

Errors are written to stderr, and under `-o json` they are objects:

```json
{"error": {"kind": "validation", "problems": ["--size-gb must be at least 1 (got 0)"], "exit_code": 6}}
```

## License

MIT
