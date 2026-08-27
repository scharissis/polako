#!/usr/bin/env bash
# Render docs/demo.gif from docs/demo.tape.
#
# Not part of check.sh and not in CI, for the same reason smoke.sh is not: it
# needs the network, an authenticated gh, and a tool most machines do not have.
# It is a maintainer step, run when the demo goes stale.
set -euo pipefail

cd "$(dirname "$0")/.."

command -v vhs >/dev/null || {
  echo "vhs is not installed: brew install vhs (or see https://github.com/charmbracelet/vhs)" >&2
  exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# The tape runs `polako`, and it should be this tree's polako rather than
# whatever happens to be installed.
go build -o "$tmp/polako" ./cmd/polako
PATH="$tmp:$PATH" vhs docs/demo.tape

echo "wrote docs/demo.gif — check it before committing: the recording contains"
echo "whatever your backlog and your run data actually said at render time."
