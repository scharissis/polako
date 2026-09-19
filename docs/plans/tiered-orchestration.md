# Tiered orchestration: an assessment, and the part worth building

Scope: the binary's policy seam, its docs, rows in the experiments ledger; one
gated skill experiment · Behavior change: none by default; every new knob
ships empty

The idea on the table: a medium-model triage agent sizes each issue, then
either does the work or spawns an orchestrator of fitting model and effort,
which plans and spawns one or more implementor agents and waits for them.

This document assesses that against 24 days of run records, says no to the
three tiers, and drafts the one piece the numbers support: the issue's size
picks the model, on the seam `docs/plans/model-and-effort.md` already built.
The rest sits behind ledger rows.

## What the records say

`polako stats` over `~/.polako/metrics`, 2026-08-25 to 2026-09-18: 269 runs,
108 terminal issues, $1,100.60, five repos.

- 91 merged (84%), 17 parked. $12.09 per merged PR.
- Median issue: 1 run, $4.11, +192/−24 across 4 files. Median implement run
  $3.04, p75 $7.12, p90 $11.98, max $29.57.
- Implement runs are 95% of spend ($1,049.90). All three remediation classes
  together: $11.30.
- By model: `sonnet` 80 merges at $9.21 each, `opus[1m]` 12 at $28.66, `opus`
  2 at $9.98. Uncontrolled — different issues, different weeks — and an
  issue that spanned two models counts under both.
- Park reasons: permission refused 4, checks remediation 4, produced nothing
  2, no skill 1, budget 1, retries exhausted 1, review remediation 1,
  unrecorded 3. At most 4 of 108 look like a model that wasn't strong enough.
- Cache reads are ~98% of tokens (2.5G of 2.5G; output 9.1M). On `sonnet`,
  clean implement runs:

  | turns | runs | cost | share | $/turn | cache read per turn |
  | --- | --- | --- | --- | --- | --- |
  | <40 | 23 | $17.58 | 3% | 0.037 | 44k |
  | 40–79 | 61 | $164.35 | 25% | 0.045 | 74k |
  | 80–119 | 26 | $116.58 | 18% | 0.045 | 100k |
  | 120–199 | 31 | $258.01 | 40% | 0.057 | 144k |
  | 200+ | 7 | $93.74 | 14% | 0.059 | 194k |

  Runs of 120 turns or more are 26% of runs and 54% of spend.
- No issue record carries `size`: `-effort-by-size` has never been armed, so
  no body was ever read.

## The assessment

**The tiers already exist, one level up.** `plan` and `health` decompose on
`opus`. A human approves behind `proposed`. `work` implements. The sizing
contract makes every issue one PR, and the median issue shows it holds: one
run, four files. There is nothing inside an issue to orchestrate, and two
implementors on one branch bring back the conflicts *one issue in flight*
exists to remove.

**There is little quality to win.** 84% merge, most of it on the medium tier
already. Parks are mostly infrastructure. A stronger orchestrator would be
bought for every issue to help at most four in a hundred.

**Fan-out is this repo's known cost driver, not its cure.** The review gate's
subagents double a run's `model_usage` (`docs/run-data.md`). Four ledger rows
exist to cut that fan-out. `-effort ultracode` is refused because "an
unattended run must not fan out a fleet". Issue #217 was a run lost to
polling its own subagents. Every tier is one more place an unattended run
hangs with nobody watching.

**Triage would tax every issue.** A separate `claude -p` reads the issue and
the code cold, shares no cache with the run that follows, and costs something
like $0.30–1 against a $3 median run — to route issues that mostly run on the
medium model anyway.

**Issue text may not make a run dearer.** That invariant is in CLAUDE.md. A
model reading a body and choosing a model is only acceptable in the shape
`-effort-by-size` already has: an enum out, picking among cells the operator
set.

**Effort before model.** `docs/plans/model-and-effort.md` argued it, and its
two remediation rows are still pending. A cascade built before those verdicts
skips the repo's own method.

Two things the records do support:

- **Cost is context × turns.** $/turn climbs 60% from short runs to long ones
  because every turn re-reads a bigger context. The lever is a shorter,
  smaller main loop, not a smarter one.
- **Triage is already paid for.** `plan-backlog` writes `Estimate: S|M|L` on
  `opus` and a human approves it. Routing on that line costs zero extra runs.

## The answer in one paragraph

polako keeps one process per run and one issue in flight. The issue's size —
written at plan time, approved by a human — may pick the model as it may
already pick the effort, among cells the operator set. An unsized issue may
later get a cheap, read-only triage run that emits the same enum and nothing
else, if a ledger row says size routing pays. Subagents get one narrow trial:
keeping the main loop's context small. No orchestrator, no parallel
implementors, no model choosing a model outside the operator's cells, no
triage state anywhere durable.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.

### 1. `-model-by-size`, the mirror of `-effort-by-size`

**Problem.** An `S` issue and an `L` issue get the same `--model`. The
`Estimate:` line already says which is which, the supervisor already knows how
to read it safely, and only effort can use it.

**Shape.** `-model-by-size S=…,M=…,L=…` on `work`, empty by default, no body
read when off. Implementation-class runs only. It sits where effort-by-size
sits: below any `model:` label — the issue's, the epic's, `model:default` —
and above `-model`. A remediation run never sees it.

- `policy.go`: `sizeModel` on `runPolicy`, copied in `newRunPolicy`; a second
  block in `choose` beside the effort one, guarded by
  `!remediation && !p.labels.modelSet`, source `sourceSize`. Fix `choose`'s
  doc comment while there: it lists size below the flag, the code puts it
  above.
- `flags.go`: `parseEffortBySize` (`:487`) becomes `parseBySize(flagName,
  spec, validate)`; model cells are shape-checked with `modelLabelValue`.
  `parseFlags` is at 128 of 130 lines (`sizebudget_test.go`), so the five
  policy flags and their validation move into helpers first.
- `backlog.go:401`: the body read arms on either flag.
- `main.go` `modelEffortLine`: the settings row shows the cells, since
  `POLAKO_MODEL_BY_SIZE` can set them silently.
- No new record field: `model_source` of `size` plus the issue record's
  `size` is enough. The dispatch line and `-dry-run` go through the seam
  already.
- Tests mirror the effort ones: `policy_test.go` (plus `model:default`, epic,
  and resume-class cases), a parse test in `main_test.go`, a drain test
  cloned from the effort-by-size one, `dryrun_test.go`. Tier aliases only.
- Docs, under budget: the flag joins the `-effort-by-size` row in
  `docs/reference.md` (506 of 509). `docs/behaviour.md` gains the size level
  its "five levels" table never got, and `size` in the source-word list.
  Both say the intended use out loud: `-model` names the dear default, the
  cells name cheaper tiers, so body text can only make a run cheaper. The
  binary can't enforce that — model strings have no order.

**Done when.** `polako work -dry-run -model opus -model-by-size S=sonnet` on
an `Estimate: S` issue prints `--model sonnet` and a `(size)` dispatch line;
a `model:` label on the issue or its epic beats it; a remediation run's argv
is unchanged; the run record says `model_source` `size` and the issue record
`size` `S`.

Estimate: M

### 2. Escalation is a label, written down

**Problem.** "Retry on a stronger model" has no trigger in the binary: a
clean exit with no work parks at once, `unfinished` is a one-turn failure,
crashes are infrastructure, and a budget park has nothing left to spend. The
manual version works today and no doc says so.

**Shape.** A short passage in `docs/behaviour.md`, beside the park section:
to re-run a parked issue on a stronger tier, add `model:opus` (or an
`effort:` label) and remove `needs-human`. Labels resolve per pickup, so the
next drain does it.

**Done when.** The passage exists, names both labels, and `behaviour.md`
stays under its budget.

Estimate: S

### 3. A triage run for issues with no `Estimate:` line

**Problem.** Size routing only reaches issues `plan` or `health` filed. A
hand-written issue has no line and runs on the flag.

**Gate.** Leave this `proposed` until the `model-by-size` row below shows a
saving *and* the backfill shows a real share of unsized issues.

**Shape.** A new `triage.go`: `(r *issueLoop) triageSize()`, called in
`processIssue` inside `if pr == nil`, just before `dispatchRun` — after
`prForBranch`, so restart safety holds for free. Not inside `dispatchRun` or
`drain`; both sit at their size ceilings.

- Runs only when `-triage-model` is set, a by-size map is armed, the family
  isn't label-settled, the body gave no size, the leg isn't a resume, and the
  issue is under budget. Once per leg; the letter is cached on `issueState`
  in memory. Nothing is written to GitHub, and a restart re-derives it.
- Dispatched like `runRemediation` (`pr.go:249`): a prose prompt, tools
  `Read,Glob,Grep,Bash(gh issue view N:*)` with `-add-tools` not passed
  through, a short time limit. The result text goes through `sizeFromBody`;
  only the letter is kept. The body still picks a cell, never a model.
- Recorded under a new `triage` reason, so `-max-cost`, `-max-issue-time` and
  the session tally cover it. `stats -by model` credits a merge to every
  model that touched the issue (`statsgroups.go`), so triage runs are
  excluded from that credit and from outcome rates.
- Any failure is a warning and no size. It never parks an issue and never
  ends a drain; an auth or limit error falls through so the implement run
  reports it properly.
- `-dry-run` says it can't show the size a triage would pick.

**Done when.** A fake drain with `-triage-model haiku -model-by-size
S=sonnet` on an unsized issue shows a triage run, then an implement run at
the cell's model; a crashing triage still ends in a merge; an issue with a
PR open, or an `Estimate:` line, gets no triage run; `stats -by model` gives
the triage model no merges.

Estimate: L — splits into the dispatch seam, then the stats exclusion.

### 4. One awaited subagent, to keep the main loop small

**Problem.** A long run pays for its whole context on every turn. Exploration
and test output are the bulk of that context, and most of it is dead weight a
turn later.

**Gate.** Leave this `proposed` until the ≥120-turn tail has been read (row
below) and says where those turns go.

**Shape.** Skill-side, in `implement-issue`: hand one bounded job —
exploring the code before the plan, or digesting a long test log — to one
foreground subagent, await it, keep its summary. One at a time, never polled
with `ListAgents` (#217), inside the one-turn rule. The subagent tool joins
`defaultTools`; `repo_test.go` pins the wording; a new eval case checks the
result was awaited; `--runs 3`; a version bump; a ledger row. This cuts
against four rows that reduced fan-out, so the row has to show a lower
$/merged, not just fewer main-loop tokens.

**Done when.** The eval case and `one-turn` pass three times; a tagged batch
has a verdict in `docs/experiments.md`; the PR body quotes both.

Estimate: L — splits into the tool grant with its tests, then the skill
wording with its eval case.

## Experiments — rows, not tickets

Operator ritual under `docs/continuous-improvement.md`. The first two need no
code.

| tag | hypothesis | knob |
| --- | --- | --- |
| `size-backfill` | Cost and park rate differ enough by size to be worth routing on, and enough issues carry an `Estimate:` line. | None: `gh issue list --state closed --json number,body`, the `estimateLine` regex in jq, joined to the JSONL on issue number. |
| `long-tail` | The ≥120-turn runs spend their turns somewhere nameable — the review gate, test loops, exploration. | None: read those runs' logs and records. |
| `remediation-effort-medium` | Already in the ledger, still pending. It goes first; effort before model. | `-remediation-effort medium`. |
| `model-by-size` | `S` issues merge at the same rate on a cheaper cell, for less per merged PR. | `-model-by-size`, after ticket 1. |

Don't arm `-effort-by-size` just to record sizes. It has no inherit cell, so
it changes what runs.

## Considered and not proposed

- **An orchestrator with parallel implementors.** Argued above. It also needs
  several `claude -p` processes on one branch, or nested subagents, and the
  one-in-flight guarantee is worth more than the wall-clock it would save —
  a PR here waits a median 5m34s for its human, not for its run.
- **A triage model that names the model directly.** That is issue text making
  a run dearer with one extra step.
- **Writing the triaged size to GitHub** as a label or a comment. The label
  pass strips proposals to `proposed`, a comment is data, and a wrong
  re-derivation costs one run on the wrong cell.
- **Escalating on failure in the binary.** No trigger exists (ticket 2), a
  resume keeps its model by design, and a fresh run on a dearer model re-reads
  everything cold.
- **`-by size` in `stats`.** `size` is on the issue record; a jq line groups
  it until a row needs more.

## Open questions

Facts to check against the installed CLI before the ticket that needs them:

1. Ticket 3: does `--permission-mode acceptEdits` approve `Edit` and `Write`
   when they are not in `--allowedTools`? If so a triage run uses another
   mode.
2. Ticket 4: is the subagent tool `Agent` or `Task` in `--allowedTools`, and
   does a subagent inherit the parent's allowlist?
3. Ticket 4: does the parent's event stream stay live while a foreground
   subagent works? If not, `-stall` kills the run.
4. Ticket 4: may a plugin ship `agents/*.md` with a per-agent model alias, and
   does `claude plugin validate .` accept it?

## Work items

Ticket 1 and 2 are independent and change no default. 3 and 4 wait for their
rows.

- [ ] `-model-by-size` (ticket 1)
- [ ] Escalation is a label, written down (ticket 2)
- [ ] The `size-backfill` and `long-tail` rows have verdicts
- [ ] The `remediation-effort-medium` row has a verdict
- [ ] The `model-by-size` row has a verdict
- [ ] A triage run for unsized issues — gated (ticket 3)
- [ ] One awaited subagent — gated (ticket 4)
