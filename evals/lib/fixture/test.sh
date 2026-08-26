#!/usr/bin/env sh
# The scratch project's whole suite. A case that implements something is
# expected to run this, and to leave it passing.
set -eu

cd "$(dirname "$0")"
failures=0

check() {
  description=$1
  expected=$2
  actual=$3
  if [ "$expected" = "$actual" ]; then
    echo "ok   - $description"
  else
    echo "FAIL - $description: expected '$expected', got '$actual'"
    failures=$((failures + 1))
  fi
}

check "greets by name" "Hello, World!" "$(./greet.sh World)"
check "rejects a missing name" "2" "$(./greet.sh >/dev/null 2>&1; echo $?)"

if [ "$failures" -ne 0 ]; then
  echo "$failures failure(s)"
  exit 1
fi
echo "all tests passed"
