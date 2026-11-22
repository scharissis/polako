#!/usr/bin/env bash
# Delegates to the shared scaffold, passing this case's own directory so the
# stand-in gh answers from this case's issue fixture. Self-locating, because the
# runner's working directory is not something a case gets to assume.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec bash "$here/../lib/scaffold.sh" "$here"
