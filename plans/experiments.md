# Experiments: the ledger

Status: living document · Scope: a record, not a mechanism · Behavior change:
none — nothing in the binary reads this file

polako can already compare two configurations: `-run-tag` labels a batch,
`stats -by tag` prices the batches against each other, and every record
snapshots the configuration that produced it. What the machinery cannot supply
is memory. A comparison run in one evening, concluded in a terminal and never
written down, is a comparison somebody pays for again a year later.

This is where it gets written down. One row per experiment, five columns:

- **tag** — the `-run-tag` its batch ran under. The batch is the evidence; the
  tag is the only thing that can find it again.
- **hypothesis** — what you expect to be true, stated before the batch runs.
  Stated afterwards it is a description, and it will always fit.
- **change** — what actually differed. One thing, or the verdict names nothing.
- **verdict** — what `stats -by tag` said, with the numbers.
- **decision** — kept, reverted, or still open. A verdict without a decision is
  half a row.

Rows are appended, never rewritten: a hypothesis that turned out wrong is the
most useful kind of row, and editing it away leaves the next operator to have
the same idea from scratch.

This is a document, versioned and reviewed like the rest of `plans/`. It is
not orchestration state — nothing reads it, no decision depends on it, and
deleting it changes no behaviour. Ten honest rows beat any dashboard at this
scale.

## The rule that fills it

Any change to a `SKILL.md`, to the model, or to a strategy knob — `-stall`,
`-retries`, `-poll`, the spend caps — runs its next batch under a fresh
`-run-tag`, and that batch gets a row here. See
[Improving polako](../README.md#improving-polako) for the retro this sits
inside.

## How to settle one

Run enough issues under the tag that the comparison is not one lucky night —
the batches in `run-data-capture.md` are the method, and it is batches over
time with honest labels, not A/B machinery. Then `polako stats -by tag`, and
fill in the verdict with the numbers rather than the impression. An issue
worked under two tags counts under each, and the table says so when it
happened; if that is most of the batch, the comparison is not one.

## The ledger

| tag | hypothesis | change | verdict | decision |
| --- | --- | --- | --- | --- |
| `remediation-sonnet` | Remediation runs — rebasing a conflict, fixing red checks, answering a review — are mechanical next to implementing an issue, so a smaller model finishes them at the same rate for less money. | Remediation runs dispatched on a smaller model than implementation runs. Needs the knob to exist first: `-model` is per-shift today, so this batch cannot run until something can vary the model by run reason. | *pending* — `reason` splits `remediate`, `checks` and `review` runs from `implement` ones, and `model_usage` prices each, so the baseline half is already answerable from records on disk. | *open* |
| `stall-30m` | The default `-stall` of 15m kills more healthy-but-quiet runs than it rescues hung ones, and the resume that follows pays to read the whole context again. Doubling it costs less than the resumes it avoids. | `-stall 30m` for a whole batch, against a batch at the default. | *pending* — compare the `stalled` status count and total cost per merged PR across the two tags. Watch wall clock too: a longer watchdog also means a genuinely hung run burns 30m before anyone notices. | *open* |
| `poll-floor` | The one-turn wait had no polling floor, so a run polled `/code-review`'s fan-out once a second — 13% of one shift's tool calls on `sleep` and `ListAgents`, at full late-session context each. Telling the run the Skill call already blocks, and to space any real poll a minute or two apart, cuts tool calls and cost per merged PR without losing a stall rescue. | `skills/implement-issue/SKILL.md` (#217): polling floor in "This run gets one turn", plus a line in the review gate that the `/code-review` call blocks and its subagents are not polled. | *pending* — compare `tool_use` count and cost per merged PR against the pre-#217 batch, and check the `stalled` count did not rise. | *open* |
| `review-worktree` | The review gate named the branch but not the worktree, so the review's forked agent and the finder subagents under it worked in the main checkout — reading the default-branch copy of the changed files, and once writing to it. Naming `<worktree>` on the invocation too points them at the copy that holds the change, without moving the merge rate. | `skills/implement-issue/SKILL.md` (#219): Phase 3 step 2c's `/code-review` invocation names `<worktree>` alongside the branch, with the why. | *pending* — compare the `review` and `checks` run counts and the park rate against the pre-#219 batch, and spot-check a few shift logs for a main-checkout write during the gate. | *open* |
| `review-level-scaling` | The review gate asked for `/code-review high` on every diff, so a one-line docs fix bought the same broad, full-fan-out sweep as a large refactor — the fixed part of the bill that does not shrink when the change does. Sizing the diff and dropping to `medium` under 300 changed lines roughly halves the review cost on small PRs without moving the merge rate. | `skills/implement-issue/SKILL.md` (#225): Phase 3 step 2c sizes the diff with `git diff --stat` and picks `medium` under 300 changed lines, `high` at or above, stating the level and count it chose. | *pending* — compare the `review` run token cost and subagent count per merged PR against the pre-#225 batch, split by diff size, and check the park rate and `checks` re-run count did not rise. | *open* |
| `accretion-check` | Diff-scoped review never judges the file a change lands in, so files accrete without bound one passing PR at a time. A bounded check at the review gate — each touched file's length, function length and comment density against the repo's own median or an absolute ceiling — nudges the accretion down at an audit-trail cost, without raising the park rate or the per-PR bill much. | `skills/implement-issue/SKILL.md` (#154): Phase 3 step 2e measures the files the run touched and either extracts the excess (verbatim lift only) or leaves a `## Scope` note; it never blocks. | *pending* — compare the park rate, `## Scope`-note frequency and cost per merged PR against the pre-#154 batch, and spot-check whether touched files trend smaller. | *open* |
| `plan-exists-read` | Phase 2 said "if PLAN.md doesn't exist" without naming a tool to test that with, and a live run (issue #210's shift) reached for a bare `ls` outside the allowlist and hung on the resulting permission prompt. Naming Read explicitly — already granted, a missing file is a normal Read error — should stop that specific park without changing plan behavior otherwise. | `skills/implement-issue/SKILL.md` (#275): Phase 2's opening line and the reply-handling line in "Asking a question" both say to test PLAN.md's existence by Read-ing it directly, never a Bash existence check like `ls`/`test -f`/`[ -f ]`. | *pending* — compare the park rate (specifically permission-prompt-triggered parks) against the pre-#275 batch, and spot-check shift logs for a Read call testing PLAN.md's existence in Phase 2. | *open* |
| `pr-body-question-shape` | Only `## Summary` had a length budget, so PR bodies ran as long as the run felt like, and the question path had a tone rule but no shape, so a blocked run could post several paragraphs. A house-style copy, a per-section budget, and a three-part question shape (what is blocked / what is needed / what each answer changes) should cut both without lowering the `clear-issue` PR-body score or the `ambiguous-issue` question score. | `skills/implement-issue/SKILL.md` (#272): new "## House style" section, per-section budgets on all five PR body sections in Phase 3 step 3, and a three-part shape plus one-screen cap on "Asking a question" step 1. | *pending* — compare PR body length and thread-question length against the pre-#272 batch, and check the `clear-issue`/`ambiguous-issue` graders stay green. | *open* |
| `proposal-body-budget` | `plan-backlog` and `review-health` bodies had no per-section budget and no house-style copy of their own, so a proposed issue ran as long as the run felt like and a curator had to read every one to decide. The same treatment #272 gave `implement-issue` — a house-style copy plus per-section budgets on the five-heading template — should shrink proposal bodies without lowering the `plan-vision`/`review-health` eval scores. | `skills/plan-backlog/SKILL.md` and `skills/review-health/SKILL.md` (#273): new "## House style" section in each, per-section budgets on the `## Summary`/`## Why now`/`## Acceptance criteria`/`## Pointers`/`## Out of scope` template, and a one-screen cap for a child issue (an epic body may run longer, as the design record). | *pending* — compare proposed-issue body length against the pre-#273 batch, and check the `plan-vision`/`review-health` eval-suite scores stay at parity. | *open* |

The `remediation-sonnet` and `stall-30m` rows come from
`plans/continuous-improvement.md`, pillar 4, which chose them because the
records can already answer them. `poll-floor`, `review-worktree`,
`review-level-scaling`, `accretion-check`, `plan-exists-read`,
`pr-body-question-shape` and `proposal-body-budget` came the other way — from
a shift's own log or a filed issue, on issues #217, #140, #225, #154, #210,
#272 and #273 respectively — and are here under the same rule that fills this
file: a `SKILL.md` change earns a row.
