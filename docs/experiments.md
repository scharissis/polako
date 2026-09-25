# Experiments: the ledger

Scope: a record, not a mechanism · Behavior change:
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

This is a document, versioned and reviewed like the rest of the docs. It is
not orchestration state — nothing reads it, no decision depends on it, and
deleting it changes no behaviour. Ten honest rows beat any dashboard at this
scale.

## The rule that fills it

Any change to a `SKILL.md`, to the model, or to a strategy knob — `-stall`,
`-retries`, `-poll`, the spend caps — runs its next batch under a fresh
`-run-tag`, and that batch gets a row here. See
[continuous-improvement.md](continuous-improvement.md) for the retro this
sits inside.

## Comparing configurations

`-run-tag` labels a batch so you can price one setup against another later:

```bash
polako work -model opus -run-tag baseline
```

Change one thing — model, skill wording, `-stall` — tag the next batch
differently, and the two sets of records are comparable. The binary's
version doesn't pin the skill's text, so tag discipline is what makes
skill-wording experiments mean anything.

```bash
polako stats -by tag
```

```
by tag
  tag         issues  merged  runs   cost  $/merged  tokens
  baseline         3       2     5  $6.70     $3.35   16.9M
  terse-plan       2       1     2  $1.40     $1.40    2.2M
```

A change nobody chose — Claude Code moving the inherited model — needs no
tag: the default `stats` report's `models` line shows each model's first
and last day, and `stats -by model` splits the two sides. Those ranges can
overlap, since inherit can differ per repo, so read a switch off them only
when they don't. For anything `stats` doesn't
answer, the files are JSONL:

```bash
cat ~/.polako/metrics/*.jsonl | jq -s 'map(select(.kind=="run")) | map(.cost_usd) | add'
```

## How to settle one

Run enough issues under the tag that the comparison is not one lucky night —
batches over time with honest labels are the method, not A/B machinery. Then `polako stats -by tag`, and
fill in the verdict with the numbers rather than the impression. An issue
worked under two tags counts under each, and the table says so when it
happened; if that is most of the batch, the comparison is not one.

A batch and its baseline should each run on one model. When Claude Code
moves the inherited model mid-batch, `stats -by tag` says so under the
table — `(model moved under baseline: inherit ran on A and B)` — and the
comparison is partly a model comparison. A tag that asked for two models on
purpose, like `plan-best`, never gets the note: there the model is the
change.

## The ledger

| tag | hypothesis | change | verdict | decision |
| --- | --- | --- | --- | --- |
| `remediation-effort-medium` | A rebase, a red-check fix or a review reply finishes at the same rate at `medium` as at the inherited effort, for less. | `-remediation-effort medium` for a whole batch, same model as the implementation runs, against a batch at the default. | *pending* — compare the `checks` / `review` / `remediate` run park rate and cost per merged PR against the baseline, split on `effort_source`. | *open* |
| `remediation-sonnet` | Remediation runs — rebasing a conflict, fixing red checks, answering a review — are mechanical next to implementing an issue, so a smaller model finishes them at the same rate for less money. | `-remediation-model sonnet` for a whole batch, against a batch at the default. Runs only if the `remediation-effort-medium` row above did not already take the saving. | *pending* — `reason` splits `remediate`, `checks` and `review` runs from `implement` ones, and `model_usage` prices each, so the baseline half is already answerable from records on disk. | *open* |
| `plan-best` | A `plan` run on the strongest tier the account reaches proposes a better-cut backlog than `opus`, at a cost a once-per-batch run can carry. | `polako plan -model best`, against a batch of proposals cut by `opus`. | *pending* — judge the backlog it proposes on whether the cuts are better, and price the run against a batch's total with the `jq` line under "Plan and health runs". | *open* |
| `stall-30m` | The default `-stall` of 15m kills more healthy-but-quiet runs than it rescues hung ones, and the resume that follows pays to read the whole context again. Doubling it costs less than the resumes it avoids. | `-stall 30m` for a whole batch, against a batch at the default. | *pending* — compare the `stalled` status count and total cost per merged PR across the two tags. Watch wall clock too: a longer watchdog also means a genuinely hung run burns 30m before anyone notices. | *open* |
| `poll-floor` | The one-turn wait had no polling floor, so a run polled `/code-review`'s fan-out once a second — 13% of one shift's tool calls on `sleep` and `ListAgents`, at full late-session context each. Telling the run the Skill call already blocks, and to space any real poll a minute or two apart, cuts tool calls and cost per merged PR without losing a stall rescue. | `skills/implement-issue/SKILL.md` (#217): polling floor in "This run gets one turn", plus a line in the review gate that the `/code-review` call blocks and its subagents are not polled. | *pending* — compare `tool_use` count and cost per merged PR against the pre-#217 batch, and check the `stalled` count did not rise. | *open* |
| `review-worktree` | The review gate named the branch but not the worktree, so the review's forked agent and the finder subagents under it worked in the main checkout — reading the default-branch copy of the changed files, and once writing to it. Naming `<worktree>` on the invocation too points them at the copy that holds the change, without moving the merge rate. | `skills/implement-issue/SKILL.md` (#219): Phase 3 step 2c's `/code-review` invocation names `<worktree>` alongside the branch, with the why. | *pending* — compare the `review` and `checks` run counts and the park rate against the pre-#219 batch, and spot-check a few shift logs for a main-checkout write during the gate. | *open* |
| `review-level-scaling` | The review gate asked for `/code-review high` on every diff, so a one-line docs fix bought the same broad, full-fan-out sweep as a large refactor — the fixed part of the bill that does not shrink when the change does. Sizing the diff and dropping to `medium` under 300 changed lines roughly halves the review cost on small PRs without moving the merge rate. | `skills/implement-issue/SKILL.md` (#225): Phase 3 step 2c sizes the diff with `git diff --stat` and picks `medium` under 300 changed lines, `high` at or above, stating the level and count it chose. | *pending* — compare the `review` run token cost and subagent count per merged PR against the pre-#225 batch, split by diff size, and check the park rate and `checks` re-run count did not rise. | *open* |
| `accretion-check` | Diff-scoped review never judges the file a change lands in, so files accrete without bound one passing PR at a time. A bounded check at the review gate — each touched file's length, function length and comment density against the repo's own median or an absolute ceiling — nudges the accretion down at an audit-trail cost, without raising the park rate or the per-PR bill much. | `skills/implement-issue/SKILL.md` (#154): Phase 3 step 2e measures the files the run touched and either extracts the excess (verbatim lift only) or leaves a `## Scope` note; it never blocks. | *pending* — compare the park rate, `## Scope`-note frequency and cost per merged PR against the pre-#154 batch, and spot-check whether touched files trend smaller. | *open* |
| `plan-exists-read` | Phase 2 said "if PLAN.md doesn't exist" without naming a tool to test that with, and a live run (issue #210's shift) reached for a bare `ls` outside the allowlist and hung on the resulting permission prompt. Naming Read explicitly — already granted, a missing file is a normal Read error — should stop that specific park without changing plan behavior otherwise. | `skills/implement-issue/SKILL.md` (#275): Phase 2's opening line and the reply-handling line in "Asking a question" both say to test PLAN.md's existence by Read-ing it directly, never a Bash existence check like `ls`/`test -f`/`[ -f ]`. | *pending* — compare the park rate (specifically permission-prompt-triggered parks) against the pre-#275 batch, and spot-check shift logs for a Read call testing PLAN.md's existence in Phase 2. | *open* |
| `pr-body-question-shape` | Only `## Summary` had a length budget, so PR bodies ran as long as the run felt like, and the question path had a tone rule but no shape, so a blocked run could post several paragraphs. A house-style copy, a per-section budget, and a three-part question shape (what is blocked / what is needed / what each answer changes) should cut both without lowering the `clear-issue` PR-body score or the `ambiguous-issue` question score. | `skills/implement-issue/SKILL.md` (#272): new "## House style" section, per-section budgets on all five PR body sections in Phase 3 step 3, and a three-part shape plus one-screen cap on "Asking a question" step 1. | *pending* — compare PR body length and thread-question length against the pre-#272 batch, and check the `clear-issue`/`ambiguous-issue` graders stay green. | *open* |
| `proposal-body-budget` | `plan-backlog` and `review-health` bodies had no per-section budget and no house-style copy of their own, so a proposed issue ran as long as the run felt like and a curator had to read every one to decide. The same treatment #272 gave `implement-issue` — a house-style copy plus per-section budgets on the five-heading template — should shrink proposal bodies without lowering the `plan-vision`/`review-health` eval scores. | `skills/plan-backlog/SKILL.md` and `skills/review-health/SKILL.md` (#273): new "## House style" section in each, per-section budgets on the `## Summary`/`## Why now`/`## Acceptance criteria`/`## Pointers`/`## Out of scope` template, and a one-screen cap for a child issue (an epic body may run longer, as the design record). | *pending* — compare proposed-issue body length against the pre-#273 batch, and check the `plan-vision`/`review-health` eval-suite scores stay at parity. | *open* |
| `review-cheap-path` | #225 scaled the gate's level to the diff but `medium` still forks an agent and fans out subagents, so a genuinely trivial diff (the kind `/implement-issue` is sized to produce) pays a fan-out cost that can dwarf the change it is checking. Skipping `/code-review` altogether under a small-diff threshold, in favour of the single-pass self-review the skill already runs when the review skill is unavailable, should cut review cost on trivial diffs without lowering merged-PR quality. | `skills/implement-issue/SKILL.md` (#255): Phase 3 step 2c adds a `cheap` tier under 30 changed lines that does a self-review pass instead of invoking `/code-review`, `medium` and `high` unchanged apart from the floor moving off zero. | *pending* — compare the `review` run token cost and subagent count per merged PR against the pre-#255 batch, split by diff size, and check eval scores and the park rate did not move. | *open* |
| `model-by-size` | An `S` issue merges at the same rate on a cheaper `-model-by-size` cell as it does on the inherited model, for less per merged PR. | `-model-by-size` for a whole batch, against a batch at the default. Runs only now that the flag exists (#395). | *pending* — compare cost and park rate per merged PR against the baseline, split on `model_source`. | *open* |
| `size-backfill` | Cost and park rate differ enough by size to be worth routing on, and enough closed issues carry an `Estimate:` line to make the comparison real. | None — `gh issue list --state closed --json number,body`, an `Estimate:` regex in `jq`, joined to the metrics JSONL on issue number. | *pending* — gates a triage run for unsized issues (never filed as an issue; needs this row plus a `model-by-size` saving first). | *open* |
| `long-tail` | The ≥120-turn runs spend their turns somewhere nameable — the review gate, test loops, exploration — rather than nowhere in particular. | None — reopen those runs' transcripts (`claude --resume <session>`) and records, and read where the turns go. | *pending* — gates a single awaited subagent trial to keep the main loop small (never filed as an issue; needs this row to say where the tail's turns actually go). | *open* |
| `visual-evidence-on` | Evidence adds little to `$/merged` on a frontend repo and causes zero parks. | Default on against `-visual-evidence=false`, same repo. | *pending* — compare cost per merged PR and the park rate between the two tags. | *open* |
| `evidence-preview` | Shots from `build` plus `preview` are steadier than shots from `dev`. | Skill wording: prefer `preview` when the script exists, against today's `dev`-only wording. | *pending* — compare the shot failure/retry rate between the two tags. | *open* |
| `evidence-webserver` | Where the repo has Playwright, a scratch `webServer` config beats background-and-stop. | Skill wording for that rung, against today's background-and-stop lifecycle. | *pending* — compare the shot failure/retry rate and turns spent in the capture step between the two tags. | *open* |
| `setup-claude-md` | In a repo with no CLAUDE.md, the block that `polako setup -apply` proposes cuts median turns per `implement-issue` run and the permission-refused park rate. | The CLAUDE.md block merged vs not, same repo, consecutive batches. | *pending* — compare median turns per run and the permission-refused park rate between the two. | *open* |
| `opus-5-5-epoch` | Opus 5.5 costs less than 1.5× what Sonnet 5 did per merged PR, and parks no more, despite twice the per-token price. Early PR-opening runs: $4.81 mean (n=6) against $6.47 (n=68). | None by polako. Claude Code 2.1.280 moved the inherited model from `claude-sonnet-5` to `claude-opus-5-5[1m]` on 2026-09-23. No tag: `stats -by model` splits the two. | *pending* — `stats -by model` for cost once Opus 5.5 has ~70 merged issues, the size that sees a 1.5× gap; parks per model by the `jq` line below. polako ships almost daily, so skill versions differ too; read only a big gap. | *open* — 1.5× or more per merged PR with no fewer parks means `-model sonnet`; otherwise keep inheriting. |

The `remediation-sonnet` and `stall-30m` rows come from
`docs/continuous-improvement.md`, pillar 4, which chose them because the
records can already answer them. `poll-floor`, `review-worktree`,
`review-level-scaling`, `accretion-check`, `plan-exists-read`,
`pr-body-question-shape`, `proposal-body-budget` and `review-cheap-path` came
the other way — from a shift's own log or a filed issue, on issues #217,
#140, #225, #154, #210, #272, #273 and #255 respectively — and are here under
the same rule that fills this file: a `SKILL.md` change earns a row.

The `remediation-effort-medium` and `plan-best` rows come from the model/effort
design in [behaviour.md](behaviour.md#which-model-and-effort-a-run-gets);
`remediation-effort-medium` waited on the `-remediation-effort` knob and could
not be filed until it shipped (#365).

The `visual-evidence-on`, `evidence-preview` and `evidence-webserver` rows
come from `docs/plans/visual-evidence.md` (retired: every issue it proposed —
#399's six children, #400-#405 — closed). Its durable design already lives in
`skills/implement-issue/SKILL.md`'s Evidence ref section, `docs/security.md`
and `docs/reference.md`; these three rows are the only part of the plan that
was still open. `visual-evidence-on` was filed here once ticket 4 (#404)
shipped the capture it measures; the other two move here with the retirement,
unfiled as batches. Ticket 7, an optional remediation re-shoot, was never
filed — it waits on a ledger row asking for it, the same way `model-by-size`
gates the two tickets below.

The `model-by-size`, `size-backfill` and `long-tail` rows come from
`docs/plans/tiered-orchestration.md` (retired: every issue it proposed —
#393's three children — closed). That assessment said no to a triage tier and
an orchestrator, drafted `-model-by-size` and the escalation-by-label passage
instead (both shipped: #395, #396), and left two further tickets gated behind
these two rows rather than filing them — a per-issue triage run, and a single
awaited subagent — pending on the data these rows produce.

The `setup-claude-md` row comes from `docs/plans/setup.md` (retired: every
issue it proposed — #411's seven children — closed). Its durable design
already lives in `docs/setup.md`, `docs/behaviour.md` and CLAUDE.md's own
`setup` invariant; this row is the only part of the plan the tickets
explicitly deferred — "left out, on purpose" until the CLAUDE.md block it
measures shipped (#417), which it now has.

The `opus-5-5-epoch` row comes from `docs/designs/model-selection.md`. Nobody
chose its change: a Claude Code release moved the inherited model. It's
written down the day that happened, because a hypothesis stated afterwards
always fits. Its "tag" is the model split rather than a `-run-tag`. `-by model`
counts merges, not parks, so this lists each model's park reasons, keyed by
the model of each issue's last implementation run. Drop the parks that aren't
the model's doing, such as a refused git credential:

```bash
jq -s '(map(select(.kind == "run" and .reason == "implement")) | map({key: "\(.repo)#\(.issue)", value: .model}) | from_entries) as $m | map(select(.kind == "issue")) | group_by($m["\(.repo)#\(.issue)"]) | map({model: $m["\(.[0].repo)#\(.[0].issue)"], merged: map(select(.outcome == "merged")) | length, parked: map(select(.outcome == "needs_human") | .park_reason)})' ~/.polako/metrics/*.jsonl
```

### Plan and health runs

`stats` skips `plan` and `health` records, so `stats -by tag` can't settle
`plan-best`. This can — each tag and model's runs, spend and issues filed:

```bash
jq -s 'map(select(.kind == "plan")) | group_by([.tag, .model]) | map({tag: .[0].tag, model: .[0].model, runs: length, cost_usd: (map(.cost_usd) | add), issues_created: (map(.issues_created) | add)})' ~/.polako/metrics/*.jsonl
```

Swap `"plan"` for `"health"` to compare `health` runs the same way.
