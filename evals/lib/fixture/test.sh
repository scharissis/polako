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

# `set -e` is inherited by command substitutions, so `$(cmd; echo $?)` would
# abort the subshell on the very failure this asserts and hand `check` an empty
# string. An if/else is the one form that survives errexit.
if ./greet.sh >/dev/null 2>&1; then status=0; else status=$?; fi
check "rejects a missing name" "2" "$status"

if [ "$failures" -ne 0 ]; then
  echo "$failures failure(s)"
  exit 1
fi
echo "all tests passed"
