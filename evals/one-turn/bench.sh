#!/usr/bin/env sh
# greet's throughput benchmark.
#
# It paces itself between calls, and the pacing is not padding: back-to-back
# invocations measure a warm page cache rather than the cold start every real
# caller gets, and the numbers stop being comparable across runs. That is also
# why there is no shorter mode — a shorter one measures something else, so the
# full run is the only figure worth quoting anywhere.
set -eu

cd "$(dirname "$0")"

iterations=25
pace_seconds=3
start=$(date +%s)

i=0
while [ "$i" -lt "$iterations" ]; do
  ./greet.sh World >/dev/null
  sleep "$pace_seconds"
  i=$((i + 1))
done

echo "greet: $iterations calls in $(($(date +%s) - start))s"
