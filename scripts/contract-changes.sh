#!/usr/bin/env bash
#
# Summarise what a contract sync does to the command surface.
#
#   go run ./cmd/leaflow-doctor -commands > before.txt
#   ./scripts/sync-contracts.sh ../leaflowapis
#   go run ./cmd/leaflow-doctor -commands > after.txt
#   ./scripts/contract-changes.sh before.txt after.txt
#
# The point is what a reviewer can act on. A synced contract is a few thousand
# lines of YAML in which a removed operation looks like any other hunk, and a
# removed operation is a command that disappears from people's scripts.

set -euo pipefail

before="${1:-}"
after="${2:-}"

if [ -z "$before" ] || [ -z "$after" ]; then
  echo "usage: $0 <before.txt> <after.txt>" >&2
  exit 2
fi

added=$(comm -13 <(sort "$before") <(sort "$after") || true)
removed=$(comm -23 <(sort "$before") <(sort "$after") || true)

if [ -z "$added" ] && [ -z "$removed" ]; then
  echo "No change to the command surface."
  exit 0
fi

if [ -n "$removed" ]; then
  # Removals first: they are the breaking half, and a reader stops at the top.
  echo "### Removed commands"
  echo
  echo "These disappear from this release. Anything scripted against them breaks."
  echo
  echo '```'
  echo "$removed" | cut -f3
  echo '```'
  echo
fi

if [ -n "$added" ]; then
  echo "### New commands"
  echo
  echo '```'
  echo "$added" | cut -f3
  echo '```'
  echo
fi

printf 'Added %s, removed %s.\n' \
  "$(printf '%s' "$added" | grep -c . || true)" \
  "$(printf '%s' "$removed" | grep -c . || true)"
