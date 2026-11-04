# Eval-suite work: handoff notes

Working notes for the effort to get `evals/` green and trustworthy. Branch
`claude/tender-johnson-36xjx2`. Newest session first.

## Session 1 — 2026-11-04

### Where it stands

- Phase 0 (read and explain, no spend) is done. Next: the operator answers
  five check questions. Then Phase 1: pin a model and a `claude` version, run
  every case once with `run.sh`, rerun the red ones twice.
- Nothing spent yet. Budget rules: say the estimate before any run; ask
  before more than $50 in one go or $200 in total.

### Facts established

- `claude` here is 2.1.318, OAuth login. There's no `~/.claude/settings.json`,
  so `run.sh`'s "user settings widen the grant" caveat doesn't apply here.
- The last baseline is PR #646 (2026-09-24, Opus 5.5, CLI runner,
  `--ablation none`): nine cases green, but case by case, with some graders
  reworded after their green run. About $27. One clean full pass is still owed.
- 11 cases, 98 assertions, 30 of them `llm` judges (haiku, three votes).
- Checked in the installed CLI bundle: `grade.py`'s judge matches the CLI's
  (same system prompt, same "PASS or FAIL" ask, same parse, three votes).
- The skills pass `--head` to `gh pr create`, which the stand-in needs to
  push and record the diff.

### Who wrote `evals/`

Mapped each commit to the PR branch that merged it (full history; the clone
was shallow at first).

- 61 commits touch `evals/`: 50 from `issue-N` branches (the implement-issue
  skill, mostly unattended shifts), 9 from `claude/*` Claude Code sessions, 2
  from other branches, both Claude co-authored.
- `git blame` of today's 4,260 lines: 79% from `issue-N` branches, 20% from
  `claude/*` sessions, under 1% from anything else.
- So none of it was written by hand. The git author is the operator's name
  only because runs commit under the operator's identity.

### Open leads to test in Phase 1 (not conclusions)

- review-gate may rarely reach `/code-review`: the skill self-reviews instead
  under 30 changed lines, and the issue's change is small. If so, three of
  its graders pass without testing anything.
- Some `llm` judges check things code could check (the `proposed` label,
  parent numbers).
- Some graders demand one exact command spelling (`diff --stat`,
  `merge --ff-only`, the pinned `npx` form): path checks, maybe brittle.
- `run.sh` results go to a `mktemp` dir and vanish: no history.
- A judge reply that says "PASS" and also contains the word "FAIL" counts as
  FAIL (both runners parse it this way).
