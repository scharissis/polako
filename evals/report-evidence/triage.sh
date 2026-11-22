#!/usr/bin/env sh
# Sorts the inbox. Every message in inbox/ is kept or dropped, and the reason
# goes in triage-report.txt, one line per message.
set -eu

usage() {
  echo "usage: triage.sh [-h]"
  echo "Sort inbox/*.txt into keep and drop. Decisions go to triage-report.txt."
}

case "${1:-}" in
-h | --help)
  usage
  exit 0
  ;;
"") ;;
*)
  echo "triage: unknown argument: $1" >&2
  usage >&2
  exit 2
  ;;
esac

cd "$(dirname "$0")"
report=triage-report.txt
: >"$report"
kept=0
dropped=0
for msg in inbox/*.txt; do
  name=$(basename "$msg")
  if ! grep -q '[^[:space:]]' "$msg"; then
    echo "$name: drop (empty)" >>"$report"
    dropped=$((dropped + 1))
  else
    echo "$name: keep" >>"$report"
    kept=$((kept + 1))
  fi
done
echo "triage: $kept kept, $dropped dropped"
