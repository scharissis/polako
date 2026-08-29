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
go_files=$(git ls-files '*.go')
unformatted=""
if [ -n "$go_files" ]; then
  unformatted=$(gofmt -l $go_files)
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
