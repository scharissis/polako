#!/usr/bin/env bash
# Everything CI runs, run locally: gofmt, vet, tests.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "not gofmt-clean:"
  echo "$unformatted"
  exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test"
go test ./...

echo "all checks passed"
