#!/usr/bin/env sh
set -eu

if [ $# -lt 1 ]; then
  echo "greet: needs a name" >&2
  exit 2
fi

echo "Hello, $1!"
