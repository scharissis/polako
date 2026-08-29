#!/usr/bin/env bash
# A `gh` that never leaves the machine.
#
# Reads come from the case fixture; writes are recorded rather than performed.
# Recording is the whole trick: it lets a case assert "it asked a question and
# stopped" without a GitHub repo to ask the question on, and it gives the
# graders fixed paths to read instead of having to work out where the run put
# its worktree.
#
# Only the subcommands a shipped skill is permitted are answered. Anything else
# exits non-zero, because a case passing on a call the real run would never be
# permitted to make is a case that proves nothing. For implement-issue that set
# is defaultTools in main.go; the plan-backlog reads and the one `issue create`
# it is allowed are not in defaultTools and deliberately so — there is no plan
# verb yet, and the allowlist that grants them ships with it.
set -euo pipefail

record=$1
shift
case_dir=$1
shift

printf '%s\n' "gh $*" >> "$record/gh-calls.log"

# value_of --flag "$@" — pulls a flag's argument out of the remaining argv.
value_of() {
  local want=$1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    "$want")
      printf '%s' "${2-}"
      return 0
      ;;
    "$want"=*)
      printf '%s' "${1#*=}"
      return 0
      ;;
    esac
    shift
  done
  return 1
}

# body_of resolves --body or --body-file, whichever the run chose. It succeeds
# either way: under `set -e` a bare "neither was given" would abort the whole
# stand-in, and a run that forgot the body should fail its graders with an empty
# recording rather than fail gh with no output at all.
body_of() {
  local text
  if text=$(value_of --body "$@"); then
    printf '%s' "$text"
  elif text=$(value_of --body-file "$@"); then
    cat "$text"
  fi
  return 0
}

subcommand="${1-} ${2-}"

case "$subcommand" in
# Most cases fix the whole answer in issue.json. plan-vision has no single
# subject issue — its fixtures are issues.json/issues-closed.json, the backlog
# a dedup-minded run reads in Phase 0 — so a view of one of those numbers (a
# call its own grader permits, even though no SKILL.md instructs it) falls
# back to looking the number up there instead of crashing under set -e.
"issue view")
  if [ -f "$case_dir/issue.json" ]; then
    cat "$case_dir/issue.json"
  else
    # Fixture priority mirrors "issue list" just below: open before closed.
    number=${3-}
    if found=$(python3 -c '
import json, sys
number, paths = sys.argv[1], sys.argv[2:]
for path in paths:
    try:
        with open(path) as f:
            issues = json.load(f)
    except FileNotFoundError:
        continue
    for issue in issues:
        if str(issue.get("number")) == number:
            json.dump(issue, sys.stdout)
            sys.exit(0)
sys.exit(1)
' "$number" "$case_dir/issues.json" "$case_dir/issues-closed.json"); then
      printf '%s\n' "$found"
    else
      echo "GraphQL: Could not resolve to an issue or pull request with the number of $number. (repository.issue)" >&2
      exit 1
    fi
  fi
  ;;

# The backlog a plan run reads before it proposes anything. Open and closed are
# separate fixtures because dedupe treats them differently — an open issue means
# "already proposed", a closed one means "already shipped" — and a case that
# served the same list for both could not tell the two apart. Either may be
# absent: a greenfield repository has no backlog, and that is a valid state.
"issue list")
  state=$(value_of --state "$@" || true)
  if [ "$state" = closed ]; then
    fixture=$case_dir/issues-closed.json
  else
    fixture=$case_dir/issues.json
  fi
  if [ -f "$fixture" ]; then cat "$fixture"; else echo "[]"; fi
  ;;
"search issues")
  echo "[]"
  ;;

# The one write a plan run is allowed. Recorded rather than performed, like the
# rest — but this one has to answer as well as record: the run files the epic
# first and passes the number it gets back as `--parent` for every child, so a
# stand-in that printed a fixed number would make the hierarchy ungradeable.
# Numbers start above any fixture's so a created issue is never confused for a
# seeded one.
"issue create")
  mkdir -p "$record/created"
  number=$((100 + $(ls "$record/created" | wc -l | tr -d ' ')))
  {
    printf 'number: %s\n' "$number"
    printf 'argv: %s\n' "$*"
    printf 'title: %s\n' "$(value_of --title "$@" || true)"
    printf 'labels: %s\n' "$(value_of --label "$@" || true)"
    printf 'parent: %s\n' "$(value_of --parent "$@" || true)"
    printf -- '---\n'
    body_of "$@"
  } > "$record/created/$number.md"
  echo "https://github.com/eval/scratch/issues/$number"
  ;;

"issue comment")
  mkdir -p "$record/comments"
  body_of "$@" > "$record/comments/$(ls "$record/comments" | wc -l | tr -d ' ').md"
  echo "https://github.com/eval/scratch/issues/${3-1}#issuecomment-1"
  ;;

"issue edit")
  if label=$(value_of --add-label "$@"); then
    printf 'add %s\n' "$label" >> "$record/labels.log"
  fi
  if label=$(value_of --remove-label "$@"); then
    printf 'remove %s\n' "$label" >> "$record/labels.log"
  fi
  echo "https://github.com/eval/scratch/issues/${3-1}"
  ;;

"pr create")
  # Same reasoning as body_of: record whatever was given, including nothing, and
  # let the graders be the ones to object.
  value_of --title "$@" > "$record/pr-title.txt" || true
  body_of "$@" > "$record/pr-body.md"
  echo "https://github.com/eval/scratch/pull/1"
  ;;

# A run orients itself before deciding what to do, and on a scratch repo the
# honest answer to all of these is "there is no PR yet". `pr list` says so with
# an empty result; the rest say so by failing, which is what the real gh does.
"pr list")
  echo "[]"
  ;;
"run list")
  echo "[]"
  ;;
"pr view" | "pr diff" | "pr checks" | "run view")
  echo "no pull requests found for this branch" >&2
  exit 1
  ;;

*)
  echo "eval stand-in gh: unsupported subcommand '$subcommand'." >&2
  echo "No shipped skill is permitted it either — see defaultTools in main.go, and the" >&2
  echo "write surface each SKILL.md names." >&2
  exit 1
  ;;
esac
