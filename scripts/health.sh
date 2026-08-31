#!/usr/bin/env bash
# Print the shape of cmd/polako: longest files, longest functions, comment
# density, totals. Reports nothing and fails on nothing — see scripts/health/.
#
# Not part of check.sh and not in CI: nothing gates on these numbers yet. A
# sibling change adds the budget that does.
set -euo pipefail
cd "$(dirname "$0")/.."

exec go run ./scripts/health "$@"
