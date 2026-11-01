#!/usr/bin/env bash
# Vet and test ctg, keeping temporary files in .scratch/tmp rather than /tmp.
#
#   scripts/test.sh [go test args...]
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR=$repo/.scratch/tmp
mkdir -p "$TMPDIR"
cd "$repo"
gofmt_out=$(gofmt -l .)
if [[ -n $gofmt_out ]]; then
  echo "gofmt needed:" >&2
  echo "$gofmt_out" >&2
  exit 1
fi
go vet ./...
go test "$@" ./...
