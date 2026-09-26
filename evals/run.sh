#!/usr/bin/env bash
# Run the eval suite by hand, beside the CLI's own runner (evals/plugin-eval.sh).
#
# This is the runner a skill run calls under its fixed `Bash(evals/run.sh:*)`
# grant, and the only one that reaches visual-change and focus-change (each a
# by-hand.yaml, not a case.yaml). It does what `claude plugin eval` does for this suite: scaffold
# each case into a fresh workspace, run the case's prompt in a headless session
# with the plugin loaded, the same toolset and the same dontAsk permission mode,
# and grade what the run left behind with the same grader semantics
# (lib/grade.py's header has them). The two should agree on every verdict.
# Where this runner differs, it says so:
#
#   - The stand-in gh reaches the run by putting .eval/bin first in the launch
#     environment. A headless session ignores a workspace settings file's
#     env.PATH (issue #126), and plugin-eval.sh routes through a dispatcher for
#     the same reason. The `.eval/bin` spelling mirrors lib/scaffold.sh's
#     record layout.
#   - With no model named, each runner resolves its own default, and they can
#     differ: the CLI starts each run from a fresh config, this runs from the
#     machine's own. Pass the same --model to both when comparing verdicts.
#   - The run's shell isn't sandboxed, and the session keeps this machine's
#     own user settings (though not its MCP servers), where the CLI gives each
#     run a fresh config dir.
#
# Costs real money: roughly $0.30–$1.60 per case at 2026-08 prices, plus cents
# for judging. Needs claude, git, python3 and the network. Cases run one at a
# time on purpose: the sessions would isolate fine, but N concurrent runs race
# the account's rate limits and interleave the progress output this script is
# often watched through. Results land under a temp directory outside the
# checkout (see `results=` below — issue #459), one timestamped directory per
# invocation and one directory per case in it: the case's workspace under ws/,
# and beside it the run's stream, evidence.md, summary.json and each llm
# grader's judge prompt. The durable record of a run is the scores quoted in a
# PR body or a docs/experiments.md row, per "Improving polako" in the README.
#
# Exit: 0 green; 1 a grader failed, a case timed out, or the harness itself
# broke (the per-case line says which); 3 nothing failed but graders await a
# human (--no-judge); 4 the --max-cost cap stopped the run before every case
# executed.
#
# Usage: evals/run.sh [--no-judge] [--judge-model M] [--model M]
#                     [--plugin-dir DIR] [--max-cost N] [case ...]
#        (no case names = every directory under evals/ with a case.yaml or a
#        by-hand.yaml)
#
# --plugin-dir points the scaffolded runs at a plugin checkout other than this
# script's own. An implement-issue run drives the suite from its worktree while
# invoking the main checkout's copy of this script, so the tool grant can stay
# a fixed `Bash(evals/run.sh:*)` — see CLAUDE.md, "The suite is the
# verification". Defaults to this script's repo root.
#
# --model overrides every case's model, as the CLI's own --model does.
# Otherwise a case's execution.model is used, and with neither the session
# inherits this machine's default.
#
# --max-cost caps the run in dollars: after each case the `total_cost_usd` of
# every case run so far is summed, and once that is at or past the cap the
# remaining cases are skipped and the suite exits 4. Default 0 — off; a
# skill-PR run passes `--max-cost 5`.
set -euo pipefail

evals_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(dirname "$evals_dir")
plugin_dir=$repo_root
max_cost=0
# shellcheck source=lib/grants.sh
. "$evals_dir/lib/grants.sh"

# A case directory holds case.yaml, or by-hand.yaml for a case the CLI can't
# run (evals/README.md says which and why): the CLI looks for case.yaml only.
case_file_of() {
  local f
  for f in case.yaml by-hand.yaml; do
    if [ -f "$evals_dir/$1/$f" ]; then
      echo "$evals_dir/$1/$f"
      return 0
    fi
  done
  return 1
}

judge_model=haiku
model_override=
cases=()
while [ $# -gt 0 ]; do
  case $1 in
    --no-judge) judge_model=none ;;
    --judge-model) judge_model=${2:?--judge-model needs a value}; shift ;;
    --model) model_override=${2:?--model needs a value}; shift ;;
    --plugin-dir) plugin_dir=${2:?--plugin-dir needs a value}; shift ;;
    --max-cost) max_cost=${2:?--max-cost needs a value}; shift ;;
    -*) echo "run.sh: unknown flag $1 (see the header of this script)" >&2; exit 2 ;;
    *) cases+=("$1") ;;
  esac
  shift
done
if [ ${#cases[@]} -eq 0 ]; then
  for d in "$evals_dir"/*/; do
    if case_file_of "$(basename "$d")" >/dev/null; then cases+=("$(basename "$d")"); fi
  done
fi
# Guarded before the loop: on the macOS system bash (3.2), expanding an empty
# array under `set -u` dies with an unbound-variable error, not a diagnosis.
if [ ${#cases[@]} -eq 0 ]; then
  echo "run.sh: no case.yaml found under $evals_dir — run from a full checkout" >&2
  exit 2
fi

for tool in claude git python3; do
  command -v "$tool" >/dev/null || {
    echo "run.sh: needs $tool on PATH — install it and rerun" >&2; exit 2; }
done

plugin_dir=$(cd "$plugin_dir" 2>/dev/null && pwd) || {
  echo "run.sh: --plugin-dir is not a directory: $plugin_dir" >&2; exit 2; }
if [ ! -f "$plugin_dir/.claude-plugin/plugin.json" ]; then
  echo "run.sh: --plugin-dir $plugin_dir has no .claude-plugin/plugin.json — point it at a plugin checkout" >&2
  exit 2
fi
[ "$plugin_dir" = "$repo_root" ] || echo "plugin under test: $plugin_dir"

python3 -c 'import sys; float(sys.argv[1])' "$max_cost" 2>/dev/null || {
  echo "run.sh: --max-cost wants a number of dollars, got: $max_cost" >&2; exit 2; }

# Outside the checkout, not under evals/results/: recent claude CLI releases
# refuse writes anywhere under the tree --plugin-dir loads as "sensitive" —
# plugin-source protection, not a per-file check (issue #459) — and
# evals_dir sits inside repo_root, the default plugin_dir. mktemp -d keeps
# this true unconditionally, including under --plugin-dir <worktree>, rather
# than relying on the worktree happening to sit outside evals_dir too.
results=$(mktemp -d "${TMPDIR:-/tmp}/polako-evals.XXXXXX")/$(date +%Y%m%d-%H%M%S)-by-hand
mkdir -p "$results"
echo "results: $results"

# spent_usd prints the summed `total_cost_usd` of every result event written
# under the results tree so far. python3 is already required above; jq is not.
spent_usd() {
  python3 - "$results" <<'PY'
import glob, json, os, sys
total = 0.0
for path in glob.glob(os.path.join(sys.argv[1], "*", "run.stream.jsonl")):
    try:
        lines = open(path).read().splitlines()
    except OSError:
        continue
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("type") == "result" and isinstance(ev.get("total_cost_usd"), (int, float)):
            total += ev["total_cost_usd"]
print(f"{total:.2f}")
PY
}

failed=0
pending=0
budget_stopped=0
for c in "${cases[@]}"; do
  spent=$(spent_usd || echo 0)
  if python3 -c 'import sys; s,c=float(sys.argv[1]),float(sys.argv[2]); sys.exit(0 if c>0 and s>=c else 1)' "$spent" "$max_cost"; then
    echo
    echo "=== budget: STOPPED at \$$spent, at or past the --max-cost of \$$max_cost — $c and any case after it not run"
    budget_stopped=1
    break
  fi
  case_dir=$evals_dir/$c
  case_file=$(case_file_of "$c") || {
    echo "run.sh: no case named '$c' under evals/" >&2; exit 2; }

  prompt=; max_turns=; timeout_s=; scaffold_script=; model=; allowed=; tools=; system_extra=
  meta=$(mktemp)
  python3 "$evals_dir/lib/grade.py" meta "$case_file" "${eval_grants[@]}" > "$meta"
  while IFS= read -r -d '' rec; do
    val=${rec#*=}
    case ${rec%%=*} in
      prompt) prompt=$val ;;
      max_turns) max_turns=$val ;;
      timeout_seconds) timeout_s=$val ;;
      scaffold_script) scaffold_script=$val ;;
      model) model=$val ;;
      allowed) allowed=$val ;;
      tools) tools=$val ;;
      append_system_prompt) system_extra=$val ;;
    esac
  done < "$meta"
  rm -f "$meta"
  model=${model_override:-$model}

  case_out=$results/$c
  ws=$case_out/ws
  mkdir -p "$ws"
  echo
  echo "=== $c — scaffolding ($scaffold_script)"
  (cd "$ws" && PATH="$ws/.eval/bin:$PATH" bash "$case_dir/$scaffold_script")
  # What the run found, so grading can tell what it created: the CLI's
  # file_exists passes only for paths the run itself made.
  python3 "$evals_dir/lib/grade.py" snapshot "$ws" "$case_out/files-before.txt"

  echo "=== $c — running: $prompt (max $max_turns turns, ${timeout_s}s)"
  # The exec matters: it makes $pid the claude process itself, so the timeout
  # kill below reaches the session rather than orphaning it inside a dead
  # wrapper — an orphan keeps spending money and keeps writing the very stream
  # grading is about to read. --tools is the toolset the CLI would give the
  # case, and CLAUDE.md loading and this machine's MCP servers are off because
  # the CLI's run has neither; the case's append_system_prompt says what a
  # workspace CLAUDE.md would have. dontAsk is the CLI's mode for a case: a
  # command lib/grants.sh doesn't cover is refused outright, the way an
  # unattended run's is, whatever default mode this machine's settings name.
  (
    cd "$ws" && exec env PATH="$ws/.eval/bin:$PATH" CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 \
      claude -p "$prompt" \
      --plugin-dir "$plugin_dir" \
      --tools "$tools" \
      --allowedTools "$allowed" \
      --permission-mode dontAsk \
      --strict-mcp-config \
      ${model:+--model "$model"} \
      ${system_extra:+--append-system-prompt "$system_extra"} \
      --max-turns "$max_turns" \
      --output-format stream-json --verbose \
      > "$case_out/run.stream.jsonl" 2> "$case_out/run.err"
  ) &
  pid=$!
  timed_out=0
  start=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    if [ $((SECONDS - start)) -ge "$timeout_s" ]; then
      kill "$pid" 2>/dev/null || true
      timed_out=1
      break
    fi
    sleep 5
  done
  rc=0; wait "$pid" || rc=$?
  if [ "$timed_out" -eq 1 ]; then
    echo "=== $c — TIMED OUT after ${timeout_s}s; grading what exists for diagnosis, but the case is RED regardless"
  elif [ $rc -ne 0 ]; then
    echo "=== $c — session exited $rc (see $case_out/run.err)"
  fi

  echo "=== $c — grading (judge: $judge_model)"
  grc=0
  python3 "$evals_dir/lib/grade.py" grade "$case_file" "$case_out" "$judge_model" || grc=$?
  if [ "$timed_out" -eq 1 ]; then
    echo "=== $c — RED (timed out)"
    failed=1
  else
    case $grc in
      0) echo "=== $c — GREEN" ;;
      3) echo "=== $c — NEEDS HUMAN GRADING (see $case_out/judge/)"
         pending=1 ;;
      2) echo "=== $c — HARNESS ERROR, not a skill verdict: see the message above, fix, rerun this case"
         failed=1 ;;
      *) echo "=== $c — RED"
         failed=1 ;;
    esac
  fi
done

echo
echo "spend: \$$(spent_usd || echo 0) across $results"
if [ $failed -ne 0 ]; then
  echo "suite: RED — read the failing case's summary.json and evidence.md under $results"
  exit 1
fi
if [ "$budget_stopped" -eq 1 ]; then
  echo "suite: STOPPED at the \$$max_cost --max-cost cap — the cases that ran are graded above, the rest did not"
  exit 4
fi
if [ $pending -ne 0 ]; then
  echo "suite: graders await a human — score them from each case's judge/ prompts under $results"
  exit 3
fi
echo "suite: all graders green — quote the per-case verdicts in the PR body"
exit 0
