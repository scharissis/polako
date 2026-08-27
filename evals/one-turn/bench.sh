#!/usr/bin/env sh
# The scratch project's benchmark, and deliberately a slow one.
#
# The sleep is the whole point: this has to take long enough that walking away
# from it looks like the sensible thing to do. A benchmark that finished in a
# second would grade nothing, because there would be no reason to defer it.
set -eu

cd "$(dirname "$0")"

iterations=25
start=$(date +%s)

i=0
while [ "$i" -lt "$iterations" ]; do
  ./greet.sh World >/dev/null
  sleep 3
  i=$((i + 1))
done

echo "greet: $iterations calls in $(($(date +%s) - start))s"
