# Models that update themselves, and knowing which one ran

Scope: `work`'s effort on a resume and its environment checks, `stats`'
default and `-by tag` reports, `status`'s last-shift line, one drain log line,
the experiments ledger and the ritual around it · Behavior change: a resumed
run keeps the effort polako chose; `work` refuses an effort flag that an
exported `CLAUDE_CODE_EFFORT_LEVEL` would override; `stats` prints model
epochs, warns on a cross-model tag comparison and counts plan and health runs;
the drain and `status` name the model that ran. Nothing changes which model or
effort a run asks for.

The question, 2026-09-23: how should polako pick models and effort? Explicit
control, good defaults, no upkeep, and maybe A/B tests that switch models on
their own.

Most of the answer already shipped (#361–#366, #395, #396). This document
covers the rest. It makes the model that ran visible, says when it changes,
and fixes the two places where the effort polako picks isn't the effort that
runs.

## What exists today

**Explicit control is done.** Six levels, most specific first: the issue's
`model:`/`effort:` label, its epic's, the `-model-by-size`/`-effort-by-size`
cell, `-remediation-*`, `-model`/`-effort`, then inherit. Inherit means
polako passes nothing and the CLI decides (`policy.go`, `choose`;
[behaviour.md](../behaviour.md#which-model-and-effort-a-run-gets)). Model
names are tier aliases, and `TestNoVersionedModelIDsInSource` refuses version
ids in the source.

**Aliases do the upkeep.** Here is what "inherit" actually ran, from the
maintainer's run data (375 runs and 176 issue outcomes, 2026-08-25 to
2026-09-23 14:52 UTC; every number below is from that snapshot):

| claude | dates | inherit ran |
| --- | --- | --- |
| 2.1.245–2.1.247 | Aug 25–28 | `claude-opus-5[1m]` |
| 2.1.250–2.1.274 | Aug 28–Sep 23 | `claude-sonnet-5` |
| 2.1.280 | Sep 23 | `claude-opus-5-5[1m]` |

Opus 5.5 arrived with no polako change. That part works.

**But the tier moved twice in four weeks, and nobody chose it.** Each
switch came with a Claude Code release. Nothing polako printed said so. `stats
-by model` shows it after the fact, if you think to ask.

**The effort that ran can't be observed.** These facts were probed by hand on
claude 2.1.280 with Opus 5.5. The probe used one reasoning prompt with tools
off, and read output tokens as the signal. Baselines: `low` 345 and 348,
`medium` 364, `high` 422, `xhigh` 510, `max` 1108, no flag 425 and 357.

- **No event names the effort.** Not the init event, the result event, or
  `--debug api`. polako can record what it asked for (`requested_effort`,
  `effort_source`), never what ran.
- **`CLAUDE_CODE_EFFORT_LEVEL` beats `--effort`.** Exported `max` with
  `--effort low` gave 1121 and 1135 tokens.
- **`--effort` doesn't survive `--resume`.** A session started at `max` and
  resumed without the flag gave 387 and 441. With `--effort max` on the
  resume it gave 1024. `buildArgs` drops `--effort` on a resume on the
  untested belief that the session keeps it. It doesn't, so a resumed run
  falls back to the default. behaviour.md promises that "a resume keeps the
  choice of the run it resumes".
- **`--model` beats `ANTHROPIC_MODEL`.** Exported `sonnet` with `--model
  opus` ran `claude-opus-5-5`. `warnClaudeModelEnv`'s comment says the
  reverse.
- **`--model opus` runs `claude-opus-5-5`, but no flag runs
  `claude-opus-5-5[1m]`.** An explicit alias and inherit aren't the same
  configuration, even on the same model.
- **`best` is an alias.** It ran `claude-fable-5-1`. So `opus` on `plan` and
  `health` is no longer "the strongest tier", as behaviour.md says it is.

**The ledger doesn't settle.** `docs/experiments.md` had 19 rows before this
document, and not one verdict. Batches are slow at this volume, and the model
moved under every one that ran.

**Where the money goes.** Implementation runs are 96% of spend ($1,604 of
$1,671). Remediation runs (rebase, red checks, review replies) are under 1%.
Plan and health runs are $43 over the month, all on `opus`.

**How big a gap a batch can see.** Cost per merged issue has a standard
deviation of 0.86 on a log scale (131 merged issues since Aug 28, median
$4.07). Here is what two batches need to tell a gap apart at the usual 95%
confidence and 80% power:

| cost gap | merged issues per batch | at ~4 merges a day |
| --- | --- | --- |
| 2× | ~25 | ~1 week |
| 1.5× | ~70 | ~2.5 weeks |
| 1.3× | ~170 | ~6 weeks |
| 1.2× | ~350 | ~3 months |

Quality is harder still. Only about 4 of 108 parks looked like a model that
wasn't strong enough (the retired tiered-orchestration assessment, #392), so
there's too little to compare. polako itself also ships almost daily (0.20.0
to 0.32.0 in three weeks), so every before/after compares skill versions too.
Only big gaps are real.

**An early read, not a verdict.** PR-opening implementation runs since Sep
10: Opus 5.5 mean $4.81 (n=6), Sonnet 5 mean $6.47 (n=68). Opus 5.5 costs
twice as much per token ($4/$20 per million in/out, against $2/$10), but it
may cost about the same per PR.

## The answer in one paragraph

**Versions float.** A tier alias moves to the newest model in its tier, and
polako takes it with no upkeep. That already works.

**Tier and effort keep inheriting.** They follow Claude Code's defaults, so
upkeep stays at zero, but polako stops being quiet about them. The drain,
`status` and `stats` name the model that ran. `stats` shows when it changed,
and warns when a comparison straddles a change. Each change of model is then
a free before/after, read with `stats -by model` once each side has enough
merged issues.

**Tagged batches stay the method for deliberate changes.** Only big levers
earn one: a tier swap, an effort step.

**Nothing switches on its own.** The binary never reads its records to choose
(the write-only rule). At this volume it would switch on noise anyway.

Two bugs get fixed on the way, so the effort polako picks is the effort that
runs.

## What to run now

| run | model | effort | note |
| --- | --- | --- | --- |
| `work`, implementation | inherit (Opus 5.5 today) | inherit | compared with the Sonnet 5 month, below |
| `work`, remediation | inherit | inherit | under 1% of spend, so its two ledger rows can wait |
| `plan`, `health` | `opus` (the default) | inherit | `plan-best` tries `-model best` once `stats` counts plan runs (ticket 4) |
| parked for want of a stronger model | a `model:best` label | — | by hand, as behaviour.md describes |

The first comparison is already running, as ledger row `opus-5-5-epoch`.
Sonnet 5 ran every inherited implementation run from Aug 28 to the morning of
Sep 23. Opus 5.5 runs them from then on.

Once Opus 5.5 has about 70 merged issues (around 2.5 weeks), `stats -by
model` can see a 1.5× gap:
- If Opus 5.5 costs 1.5× or more per merged PR and parks no less, pass
  `-model sonnet`.
- Otherwise keep inheriting.

## Keeping it right

| when | do |
| --- | --- |
| a new version ships in a tier | nothing; the alias moves |
| polako says the model changed | read `stats -by model` once the new model has ~70 merged issues |
| you set `-effort` and the model changes | re-check it: levels don't carry across models (the API default is `medium` on Opus 5.5, `high` on Opus 5) |
| a new tier ships, or a price moves 25% or more | one tagged batch |
| you run a tagged batch | keep it inside one model; `stats -by tag` warns if not (ticket 2) |

## What this may read and write

- **No new reader of the run data.** Epochs and the `-by tag` warning are
  `stats`. The model on the last-shift line rides `status`'s existing read.
  Deleting `~/.polako` mid-shift still changes no behavior.
- **The drain's model line is in-process.** It compares each run's init event
  with the last one this process saw. Nothing is read back.
- **The environment stays the operator's.** polako still sets no `cmd.Env`
  ([hardening.md](../hardening.md)). Where `CLAUDE_CODE_EFFORT_LEVEL` would
  override an effort polako was asked for, it refuses or warns. It never
  unsets the variable.
- **Nothing changes which model or effort a run asks for.** The policy seam
  is untouched. Ticket 1 changes only whether the choice reaches a resumed
  run.
- **No issue text.** Every new line is model ids, dates, versions and
  numbers.

## Drafted tickets

Ordered by dependency, the bug first. Each ticket documents its own flags and
fields under `docs/`, and each is hermetic: records fixtures for `stats`,
`fakeCLI` and `fakeClaude` for the rest.

Several docs have no room left under `docsbudget_test.go`, so a line added
there pays for itself with a line cut:

| doc | lines today | ceiling |
| --- | --- | --- |
| `run-data.md` | 500 | 500 |
| `behaviour.md` | 531 | 531 |
| `reference.md` | 583 | 584 |

### 1. The effort polako picks is the effort that runs

**Problem.** The probes above found two gaps between the effort polako
chooses and the one that runs:
- A resumed run drops `--effort`, so it falls back to the CLI's default.
- An exported `CLAUDE_CODE_EFFORT_LEVEL` beats `--effort`, and
  `warnClaudeModelEnv` says only that it "can". Its model half is wrong the
  other way round: `--model` beats `ANTHROPIC_MODEL`.

**Shape.**

- `buildArgs` passes `--effort` on a resume too. The comment that says the
  session keeps its effort goes.
- `effortFlagGate` also refuses when `-effort`, `-remediation-effort` or an
  `-effort-by-size` cell is set and `CLAUDE_CODE_EFFORT_LEVEL` is exported,
  since the flag would do nothing. The error names both fixes: unset the
  variable, or drop the flag.
- A dispatch that passes `--effort` from a label or an epic while the
  variable is exported logs one warning naming the override.
- `warnClaudeModelEnv` says what's true. `ANTHROPIC_MODEL` moves only runs
  that inherit. `CLAUDE_CODE_EFFORT_LEVEL` wins over every effort polako
  passes.

**Done when.**
- The `buildArgs` test expects `--effort` on a resume.
- A gate test refuses `-effort low` with the variable exported.
- The warning wording matches the probes.
- behaviour.md's "a resume keeps the choice" line is true again.

Estimate: S

### 2. `stats` shows model epochs and flags a cross-model comparison

**Problem.** The inherited model changed twice in four weeks, and `stats`
never said so. A `-by tag` table that compares a Sonnet batch with an Opus
batch reads as a verdict on whatever the tags were about.

**Shape.**

- The default report gains one `models` line when more than one resolved
  model is in scope. It gives each model, the dates it ran, and the
  `claude_version` at each change. For example: `models  claude-sonnet-5 Aug
  28–Sep 23 · claude-opus-5-5[1m] since Sep 23 (claude 2.1.280)`.
- `-by tag` adds one line under the table when the tags ran on different
  models: "tags a and b ran on different models — this compares the models
  too".
- `-json` gains an `epochs` array.
- `docs/run-data.md` documents both.

**Done when.**
- A fixture with two models prints the `models` line, and the `-by tag`
  warning when each tag sits on one of them.
- A fixture with one model prints neither.

Estimate: S

### 3. The drain and `status` name the model

**Problem.**
- The shift log names the model only inside the raw event stream.
- `status`'s last-shift line prices the shift but doesn't say what ran it.
- An alias that resolves to another tier goes unnoticed. That can happen
  through a gateway, a settings file or a proxy.

**Shape.**

- The drain logs the model once per shift, from the first run's init event:
  "runs are on claude-opus-5-5[1m] (inherited)". It logs again when a later
  run's model differs. This is in-process only.
- When a requested alias's tier word (`opus`, `sonnet`, `haiku`, `fable`)
  isn't in the init model, one warning names both. Other requested strings
  pass unchecked, the way `labelPolicy` treats them.
- `status`'s last-shift line adds the model or models that shift ran on.

**Done when.**
- A `fakeClaude` shift whose init model changes between runs logs two model
  lines.
- A run asked for `opus` that inits as `claude-sonnet-5` logs the warning.
- The `status` golden shows the model.

Estimate: S

### 4. `stats` counts plan and health runs

**Problem.** docs/reference.md says a `plan` or `health` `-run-tag` compares
"in `polako stats`", but `stats` skips those records. So `plan-best` can't
settle, and plan and health spend is invisible to `stats`.

**Shape.**

- `stats` reads `plan` and `health` records.
- They count toward runs and cost in the overall report, `-by tag` and `-by
  model`. They have no issue and no merge, so cost per merged PR leaves them
  out and says so.
- The overall report gains one intake line: plan and health runs, their
  cost, and the issues they created.

**Done when.**
- A fixture with one `plan` record under a tag shows its cost in that tag's
  row.
- Cost per merged PR is unchanged by it.

Estimate: S

### 5. The ritual learns about models

Depends on ticket 2.

**Problem.** `continuous-improvement.md` has no step for "the model changed".
`experiments.md` doesn't say a batch and its baseline must share a model.
behaviour.md says inherit follows the account tier, but it also follows
Claude Code's releases.

**Shape.**

- `experiments.md`, "How to settle one": a batch and its baseline run on one
  model, and `stats -by tag` says when they don't.
- `continuous-improvement.md` gains the "Keeping it right" table.
- behaviour.md, "Which model and effort a run gets":
  - Inherit moves with Claude Code's releases, with this document's table as
    the example.
  - The environment facts from ticket 1.
  - `plan`/`health`'s `opus` is the strong default, not the strongest tier.
    `best` is that, and the `plan-best` row says whether it's worth the
    price.

**Done when.** The three documents say the above.

Estimate: S

## Considered and not proposed

- **A/B arms inside the binary.** A `-challenger` flag would split one
  batch into two arms by issue number, so both arms share the same days,
  models and skill versions. Turned down on 2026-09-23: tagged batches stay
  the method, and tickets 2 and 3 make them honest.
- **Switching models on its own.** The binary would have to read its own
  records to choose, which the write-only rule forbids. Two machines with
  different histories would run different models on the same issue. And at
  ~4 merges a day it would switch on noise before the next model shipped.
- **Pinned model ids, or a model table in the source.** A pinned id is right
  for one generation and wrong for the next. `TestNoVersionedModelIDsInSource`
  stays. An operator can still pass a pinned id with `-model` for a while, if
  a new version goes wrong.
- **An explicit default tier.** `-model opus` would have held Opus through
  the Sonnet month, at a price the data can't yet justify. And `opus` isn't
  inherit anyway: inherit got the `[1m]` variant. Revisit if
  `opus-5-5-epoch` says so.
- **Unsetting `CLAUDE_CODE_EFFORT_LEVEL` for the child.** Every effort would
  stick, but polako never overrides a child's environment, and the hardening
  docs build on that. Refusing or warning keeps the promise.
- **`--fallback-model`.** It's for an overloaded model, not a weak one. One
  park in 33 was retries exhausted.
- **A triage tier or an orchestrator.** Turned down in #392 on the same
  data: there's little quality to win, and triage taxes every issue.
- **`stats -by effort`.** Every run so far inherited its effort, and none
  can report what ran. A `jq` line groups `requested_effort` the day that
  changes.
- **Remediation experiments first.** Remediation is under 1% of spend. Its
  two ledger rows stay, at lower priority.
