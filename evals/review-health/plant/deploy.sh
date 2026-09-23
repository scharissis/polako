#!/usr/bin/env bash
# Copies greet.sh to the staging host. --dry-run prints the copy instead.
set -euo pipefail
if [ "${1:-}" = "--dry-run" ]; then
  echo "would copy greet.sh to staging:/srv/greet/"
  exit 0
fi
scp greet.sh staging:/srv/greet/
