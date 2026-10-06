# Keeping contracts in sync

The CLI embeds the public contracts from `leaflowapis/leaflowapis`. It does not
consume the private `apis` repository. Commands and credentials come from the
public OpenAPI documents; merging a sync changes source, and a CLI release
makes those changes available to installed users.

`sync-contracts` accepts the upstream `contracts/sync` branch, a scheduled
checkout, or a manually selected source ref. Both paths mirror the complete
`leaflow/` tree with `scripts/sync-contracts.sh`, then build, test and run
`leaflow-doctor` before committing to main. The mirror copies referenced YAML
files alongside the entry points and removes documents retired by the source.
It does not preserve old modules, rename schemas or combine public and private
contract generations.

The parser discovers `openapi.yaml` recursively and uses each document's actual
path to resolve references. Neither service nesting nor the version directory
is hardcoded. Multiple versions declaring the same service name are rejected
as ambiguous rather than silently selecting one. Missing files and schema
references fail verification.

To sync a published release locally:

```sh
# Check out the desired release in a separate source directory first.
./scripts/sync-contracts.sh /path/to/public-contracts
go build ./...
go test ./...
```

The source directory must contain the complete `leaflow/` tree. For the current
sync, the public source is `v43.0.0`, commit
`e9455d107dba87f0c49f1ca029a1d80e4b3b9165`.

To inspect command changes, capture `go run ./cmd/leaflow-doctor -commands`
before and after the sync, then pass the files to
`scripts/contract-changes.sh`. That summary names additions and removals without
requiring tests to assert a particular production command layout.
