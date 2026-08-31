#!/usr/bin/env bash
# Print the shape of cmd/polako: longest files, longest functions, comment
# density, totals. Reports nothing and fails on nothing — see scripts/health/.
#
# Not part of check.sh and not in CI: this script gates on nothing. The gate
# that does is cmd/polako/sizebudget_test.go, which measures the same way.
set -euo pipefail
cd "$(dirname "$0")/.."

exec go run ./scripts/health "$@"
