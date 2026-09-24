#!/usr/bin/env bash
# A `gh` that never leaves the machine.
#
# Reads come from the case fixture; writes are recorded rather than performed.
# Recording is the whole trick: it lets a case assert "it asked a question and
# stopped" without a GitHub repo to ask the question on, and it gives the
# graders fixed paths to read instead of having to work out where the run put
# its worktree.
#
# It runs from a copy inside the workspace, beside a copy of the case's
# fixtures, because `claude plugin eval` sandboxes the run's shell away from the
# plugin tree this file ships in (lib/scaffold.sh makes the copies).
#
# Only the subcommands a shipped skill is permitted are answered. Anything else
# exits non-zero, because a case passing on a call the real run would never be
# permitted to make is a case that proves nothing. That set is each verb's own
# grant: defaultTools in flags.go for implement-issue, planTools and healthTools
# for the two skills that only file issues.
set -euo pipefail

record=$1
shift
fixtures=$1
shift

# lib/scaffold.sh's routing check, answered before anything is logged so the
# check leaves no trace a grader could read as a call the run made.
if [ "${1-}" = --polako-eval-stand-in ]; then
  printf '%s\n' "$record"
  exit 0
fi

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
# Most cases fix the whole answer in issue.json. plan-vision and review-health
# have no single subject issue — their fixtures are issues.json/issues-closed.json,
# the backlog a dedup-minded run reads in Phase 0 — so a view of one of those
# numbers (a call its own grader permits, even though no SKILL.md instructs it)
# falls back to looking the number up there instead of crashing under set -e.
"issue view")
  if [ -f "$fixtures/issue.json" ]; then
    cat "$fixtures/issue.json"
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
' "$number" "$fixtures/issues.json" "$fixtures/issues-closed.json"); then
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
    fixture=$fixtures/issues-closed.json
  else
    fixture=$fixtures/issues.json
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
# seeded one. Each one also lands in issues-created.md, and everything a run
# puts in front of a human lands in posted.md: a CLI judge reads one file, not
# a directory.
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
  { printf '\n=== issue %s ===\n' "$number"; cat "$record/created/$number.md"; } \
    | tee -a "$record/posted.md" >> "$record/issues-created.md"
  echo "https://github.com/eval/scratch/issues/$number"
  ;;

"issue comment")
  mkdir -p "$record/comments"
  comment=$record/comments/$(ls "$record/comments" | wc -l | tr -d ' ').md
  body_of "$@" > "$comment"
  { printf '\n=== comment on #%s ===\n' "${3-1}"; cat "$comment"; } >> "$record/posted.md"
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
  # Real `gh pr create` pushes the head branch first when it has no upstream —
  # this stand-in skipped that, which made a rejected push nothing any case
  # could ever exercise. Pushed from the checkout itself, not a worktree: refs
  # are shared across worktrees of the same repo, so this reaches the branch
  # regardless of which worktree has it checked out. A rejection (the
  # push-blocked case's pre-receive hook) fails this call exactly as a real
  # `gh pr create` would, with git's own stderr — the thing the run must
  # describe rather than paste (issue #386).
  if head=$(value_of --head "$@"); then
    repo=$(dirname "$record")/repo
    if ! out=$(git -C "$repo" push origin "$head:$head" 2>&1); then
      echo "$out" >&2
      exit 1
    fi
    # What a reviewer would see on the PR: a design-plan run's whole
    # deliverable is a file on this branch.
    git -C "$repo" diff --name-status "main...$head" > "$record/pr-files.txt" 2>&1 || true
    git -C "$repo" diff "main...$head" > "$record/pr-diff.txt" 2>&1 || true
  fi
  # Same reasoning as body_of: record whatever was given, including nothing, and
  # let the graders be the ones to object.
  value_of --title "$@" > "$record/pr-title.txt" || true
  body_of "$@" > "$record/pr-body.md"
  { printf '\n=== pull request: %s ===\n' "$(cat "$record/pr-title.txt")"
    cat "$record/pr-body.md"; } >> "$record/posted.md"
  # The whole PR in one file, for a grader that has to check the body against
  # the change or against the evidence branch: origin is read at this moment,
  # after any evidence push and before the run can tidy anything away. The
  # === markers can't be mistaken for the body's own markdown headings.
  origin=$record/origin.git
  {
    printf 'title: %s\n\n=== body ===\n' "$(cat "$record/pr-title.txt")"
    cat "$record/pr-body.md"
    printf '\n=== changed files ===\n'
    cat "$record/pr-files.txt" 2>/dev/null || echo "(no --head given, so none recorded)"
    printf '\n=== origin refs ===\n'
    git --git-dir="$origin" for-each-ref --format='%(objectname) %(refname)'
    printf '\n=== polako-evidence tree ===\n'
    git --git-dir="$origin" ls-tree -r polako-evidence 2>/dev/null \
      || echo "(no polako-evidence branch on origin)"
  } > "$record/pr.md"
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
