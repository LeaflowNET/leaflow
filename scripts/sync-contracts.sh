#!/usr/bin/env bash
# Mirror the authoritative public contract tree, including referenced documents.
# Usage: ./scripts/sync-contracts.sh <repository containing leaflow/>
set -euo pipefail

source_repo="${1:-}"
if [ -z "$source_repo" ] || [ ! -d "$source_repo/leaflow" ]; then
  echo "usage: $0 <contracts repository containing leaflow/>" >&2
  exit 2
fi

if [ -z "$(find "$source_repo/leaflow" -type f -name openapi.yaml -print -quit)" ]; then
  echo "source contains no OpenAPI contracts" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
destination="$root/apis/leaflow"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cp -R "$source_repo/leaflow" "$stage/leaflow"

# List exactly what the authoritative source retired before replacing the tree.
if [ -d "$destination" ]; then
  while IFS= read -r target; do
    relative="${target#"$destination"/}"
    if [ ! -f "$stage/leaflow/$relative" ]; then
      echo "remove leaflow/$relative"
    fi
  done < <(find "$destination" -type f | sort)
fi

rm -rf "$destination"
mv "$stage/leaflow" "$destination"

# Missing reference files or symbols are errors, never silently skipped.
cd "$root"
go run ./cmd/leaflow-doctor
