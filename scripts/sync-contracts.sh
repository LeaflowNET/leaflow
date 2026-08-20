#!/usr/bin/env bash
#
# Copy OpenAPI documents into internal/spec/data.
#
# CI and a person run the same script, so "it worked on my machine" and "it
# worked in the workflow" cannot mean different things.
#
#   ./scripts/sync-contracts.sh ../leaflowapis
#
# The embedded tree mirrors the contracts repository exactly:
#
#   leaflow/<service>/v1/openapi.yaml
#   leaflow/type/v1/error.yaml        shared, referenced by every contract
#
# Keeping the layout is what lets the relative reference in each contract
# resolve, and means a synced file is byte-identical to upstream — so the review
# diff is the upstream diff, with no transformation in between.
#
# Only services already embedded are updated. Adding one brings a whole command
# tree with it, so it stays a deliberate act rather than something a nightly job
# does on its own.

set -euo pipefail

source_repo="${1:-}"

if [ -z "$source_repo" ]; then
  echo "usage: $0 <path to contracts repository>" >&2
  exit 2
fi

if [ ! -d "$source_repo/leaflow" ]; then
  echo "not a contracts repository (no leaflow/ directory): $source_repo" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
destination="$root/internal/spec/data/leaflow"

updated=0
missing=0

for existing in "$destination"/*/; do
  service="$(basename "$existing")"
  found="$source_repo/leaflow/$service/v1/openapi.yaml"

  # type/ holds the shared error schema rather than a service contract.
  if [ "$service" = "type" ]; then
    found="$source_repo/leaflow/type/v1/error.yaml"
    target="$destination/type/v1/error.yaml"
  else
    target="$destination/$service/v1/openapi.yaml"
  fi

  if [ ! -f "$found" ]; then
    echo "no contract found for $service" >&2
    missing=$((missing + 1))
    continue
  fi

  mkdir -p "$(dirname "$target")"
  cp "$found" "$target"
  echo "$service <- $found"
  updated=$((updated + 1))
done

echo
echo "updated $updated file(s)"

# A contract that cannot be found fails the run. Keeping the stale copy would
# ship a CLI whose commands do not match the platform, and nothing downstream
# would notice.
if [ "$missing" -gt 0 ]; then
  echo "$missing contract(s) could not be found" >&2
  exit 1
fi
