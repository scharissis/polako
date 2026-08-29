#!/usr/bin/env bash
# Everything CI runs, run locally: gofmt, vet, tests.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> gofmt"
# Unlike go build/vet/test, gofmt does not skip dot-directories, so "."
# would format-check every nested worktree under .claude/worktrees too —
# this checkout is expected to hold them. List tracked files instead: git
# ls-files needs no maintenance when a new nesting convention appears and
# can't be defeated by a worktree placed somewhere unanticipated.
# git ls-files -z / xargs -0, not mapfile: macOS ships bash 3.2 by default,
# which lacks mapfile, and the NUL delimiter keeps this correct even for a
# tracked path containing a space.
unformatted=""
if [ -n "$(git ls-files '*.go')" ]; then
  unformatted=$(git ls-files -z '*.go' | xargs -0 gofmt -l)
fi
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
