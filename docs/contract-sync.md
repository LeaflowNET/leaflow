# Keeping contracts in sync

Commands are generated from the contracts in `apis/`, so a contract release has
to reach this repository before it can reach users.

## How it is wired

```
leaflowapis: tag pushed
      │
      │  deploy key (write, this repository only)
      ▼
LeaflowNET/leaflow: branch contracts/sync
      │
      │  sync-contracts workflow
      ▼
pull request, reviewed and merged
      │
      │  tag pushed here
      ▼
release: users get the new commands
```

Contracts are embedded at build time. Merging a sync does not change anything
for users; the next release does.

## Setting up the deploy key

A deploy key rather than a personal access token: it is scoped to this one
repository, carries no API rights, belongs to no person, and can be revoked
without touching anyone's account. Nothing breaks when someone leaves.

Generate a key pair, with no passphrase, kept out of any repository:

```sh
ssh-keygen -t ed25519 -C "leaflowapis -> LeaflowNET/leaflow" -f /tmp/leaflow-sync -N ""
```

Add the **public** half here, with write access:

```sh
gh repo deploy-key add /tmp/leaflow-sync.pub \
  --repo LeaflowNET/leaflow --title "contracts sync" --allow-write
```

Add the **private** half to the contracts repository as a secret:

```sh
gh secret set LEAFLOW_CLI_DEPLOY_KEY --repo leaflowapis/leaflowapis < /tmp/leaflow-sync
rm /tmp/leaflow-sync /tmp/leaflow-sync.pub
```

## The workflow to add to `leaflowapis`

```yaml
name: publish-contracts

on:
  push:
    tags: ["v*"]

jobs:
  push-to-cli:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: webfactory/ssh-agent@v0.9.0
        with:
          ssh-private-key: ${{ secrets.LEAFLOW_CLI_DEPLOY_KEY }}

      - name: push contracts to the CLI repository
        run: |
          git clone --depth 1 git@github.com:LeaflowNET/leaflow.git cli
          cd cli
          git checkout -B contracts/sync

          rm -rf apis/leaflow
          cp -R ../leaflow apis/leaflow

          if git diff --quiet -- apis/; then
            echo "Contracts already up to date."
            exit 0
          fi

          git config user.name "leaflow-contracts"
          git config user.email "contracts@leaflow.net"
          git add apis/
          git commit -m "chore: sync API contracts from ${GITHUB_REF_NAME}"

          # Force-pushed because this branch only ever holds the latest sync;
          # its history is the contracts repository's history.
          git push -f origin contracts/sync
```

## Why this does not depend on deploy keys triggering workflows

GitHub documents that pushes authenticated with `GITHUB_TOKEN` do not start
workflows, to stop runs recursing. It says nothing either way about deploy
keys, and this setup does not need the answer.

The sync workflow listens on both `push` to `contracts/sync` and a daily
schedule. If the push starts it, the pull request appears at once. If it does
not, the scheduled run reaches the same branch within a day and opens the same
pull request. The outcome is identical; only the latency differs.

That is also why the scheduled run is not redundant once the push works. A
cross-repository trigger goes quiet whenever a key is rotated, a workflow is
edited, or a release is cut by hand — and nothing reports that it went quiet.

## Doing it by hand

```sh
./scripts/sync-contracts.sh ../leaflowapis
go test ./... && go run ./cmd/leaflow-doctor
```

To see what a sync does to the command surface:

```sh
go run ./cmd/leaflow-doctor -commands > /tmp/before.txt
./scripts/sync-contracts.sh ../leaflowapis
go run ./cmd/leaflow-doctor -commands > /tmp/after.txt
./scripts/contract-changes.sh /tmp/before.txt /tmp/after.txt
```
