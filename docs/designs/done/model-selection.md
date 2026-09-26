# Models that update themselves, and knowing which one ran

Scope: `work`, `plan` and `health` effort handling and environment checks, the
drain's session line, `stats`' default and `-by tag` reports, `status`'s
last-shift line, the experiments ledger and the ritual around it · Behavior
change: a resumed run keeps the effort polako chose; an effort flag that an
exported `CLAUDE_CODE_EFFORT_LEVEL` would override refuses to start; the
session line says whether the model was inherited or asked for, and warns
when an asked-for tier ran as another; `stats` lists the models inherited
runs resolved to, and notes a batch whose model moved under it; `status`
names the model the last shift ran on. Nothing changes which model or effort
a run asks for.

The question, 2026-09-23: how should polako pick models and effort? Explicit
control, good defaults, no upkeep, and maybe A/B tests that switch models on
their own.

Most of the answer already shipped (#361–#366, #395, #396). This document
covers the rest:
- Say plainly which model ran.
- Flag it when that changes.
- Fix the two places where the effort polako picks isn't the effort that
  runs.

## What exists today

**Explicit control is done.** Six levels, most specific first: the issue's
`model:`/`effort:` label, its epic's, the `-model-by-size`/`-effort-by-size`
cell, `-remediation-*`, `-model`/`-effort`, then inherit. Inherit means
polako passes nothing and the CLI decides (`policy.go`, `choose`;
[behaviour.md](../../behaviour.md#which-model-and-effort-a-run-gets)). Model
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

**But the tier moved twice in four weeks, and nobody chose it.**
- Each switch came with a Claude Code release, between two shifts.
- Every run's `session started (model …)` line named its model, but nothing
  said the model had changed.
- `stats -by model` shows it after the fact, if you think to ask.
- The retro's version-drift `jq` recipe (continuous-improvement.md) would
  have shown it too, but nobody ran it across either switch.

**The effort that ran can't be observed.** These facts were probed by hand on
claude 2.1.280 with Opus 5.5. The probe used one reasoning prompt with tools
off, and read output tokens as the signal. Baselines: `low` 345 and 348,
`medium` 364, `high` 422, `xhigh` 510, `max` 1108, no flag 425 and 357.

- **No event names the effort.** Not the init event, the result event, or
  `--debug api`. polako can record what it asked for (`requested_effort`,
  `effort_source`), never what ran. So there's no new record field.
- **`CLAUDE_CODE_EFFORT_LEVEL` beats `--effort`.** Exported `max` with
  `--effort low` gave 1121 and 1135 tokens.
- **`--effort` doesn't survive `--resume`.** A session started at `max` and
  resumed without the flag gave 387 and 441. With `--effort max` on the
  resume it gave 1024. `buildArgs` drops `--effort` on a resume on the
  untested belief that the session keeps it. It doesn't, so a resumed run
  falls back to the default. behaviour.md promises that "a resume keeps the
  choice of the run it resumes". No past data is affected: every run so far
  inherited its effort.
- **`--model` beats `ANTHROPIC_MODEL`.** Exported `sonnet` with `--model
  opus` ran `claude-opus-5-5`. `warnClaudeModelEnv`'s comment says the
  reverse.
- **`ANTHROPIC_DEFAULT_OPUS_MODEL` remaps the alias.** Exported
  `claude-sonnet-5` with `--model opus` ran `claude-sonnet-5`. polako's
  warning doesn't know this variable.
- **`--model opus` runs `claude-opus-5-5`, but no flag runs
  `claude-opus-5-5[1m]`.** An explicit alias and inherit aren't the same
  configuration, even on the same model.
- **The `[1m]` suffix isn't stable.** Every fresh Opus run on 2.1.245–2.1.247
  reported `[1m]`, but 23 of 28 resumes reported the bare id.
- **`best` is an alias.** It ran `claude-fable-5-1`. So `opus` on `plan` and
  `health` is no longer "the strongest tier", as behaviour.md says it is.
- **The init event carries `claude_code_version`.** Records take
  `claude_version` from a preflight snapshot, which a mid-shift CLI update
  outdates.

**The ledger doesn't settle.** `docs/experiments.md` had 19 rows before this
document, and not one verdict. Only one run in 375 carries a `-run-tag`.

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
upkeep stays at zero, but polako stops being quiet about them:
- Each run's session line says whether its model was inherited or asked for,
  and warns when an asked-for tier ran as another.
- `status` names the model the last shift ran on.
- `stats` lists the models inherited runs resolved to, and notes a batch
  whose model moved under it.

Each change of model is then a free before/after, read with `stats -by
model` once each side has enough merged issues.

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
| `plan`, `health` | `opus` (the default) | inherit | `plan-best` tries `-model best`, priced by ticket 6's recipe |
| parked for want of a stronger model | a `model:best` label | — | by hand, as behaviour.md describes |

The first comparison is already running, as ledger row `opus-5-5-epoch`.
Sonnet 5 ran every inherited implementation run from Aug 28 to the morning of
Sep 23. Opus 5.5 runs them from then on.

Once Opus 5.5 has about 70 merged issues (around 2.5 weeks), `stats -by
model` can see a 1.5× gap. The row's `jq` line counts parks per model.
- If Opus 5.5 costs 1.5× or more per merged PR and parks no less, pass
  `-model sonnet`.
- Otherwise keep inheriting.

## Keeping it right

| when | do |
| --- | --- |
| a new version ships in a tier | nothing; the alias moves |
| `stats` or `status` shows a new model | read `stats -by model` once it has ~70 merged issues |
| you set `-effort` and the model changes | re-check it: levels don't carry across models (the API default is `medium` on Opus 5.5, `high` on Opus 5) |
| a new tier ships, or a price moves 25% or more | one tagged batch |
| you run a tagged batch | keep it inside one model; `stats -by tag` notes it if the model moved under it (ticket 3) |

## What this may read and write

- **No new reader of the run data.** The model list and the `-by tag` note
  are `stats`. The model on the last-shift line rides `status`'s existing
  read. Deleting `~/.polako` mid-shift still changes no behavior.
- **The drain compares within one run only.** The tier check sets a run's
  init event against what that same run asked for. Nothing is remembered
  across runs, and nothing is read back from an earlier shift.
- **The environment stays the operator's.** polako still sets no `cmd.Env`
  ([hardening.md](../../hardening.md)). Where `CLAUDE_CODE_EFFORT_LEVEL` would
  override an effort polako was asked for, it refuses or warns. It never
  unsets the variable.
- **Nothing changes which model or effort a run asks for.** The policy seam
  is untouched. Ticket 1 changes only whether the choice reaches a resumed
  run.
- **This reverses one earlier call.** continuous-improvement.md says
  "Version drift is a recipe, not a flag": a `jq` line, run after an
  upgrade, until someone resents typing it. The recipe missed both model
  switches. The model is also the one drift that moves cost 2×. Ticket 2
  rewrites that paragraph and says why.
- **No issue text.** Every new line is model ids, dates, versions and
  numbers.

## Drafted tickets

Ordered by dependency, the bug first. Each ticket documents what it adds
under `docs/`, and each is hermetic: records fixtures for `stats` and
`status`, `fakeCLI` and `fakeClaude` for the rest.

Two rules bind every ticket:
- `TestNoVersionedModelIDsInSource` bans `claude-[a-z]+-[0-9]` in non-test
  Go, comments included, so no example model id goes in code.
- Several docs have no room left under `docsbudget_test.go`, so a line added
  there pays for itself with a line cut:

| doc | lines today | ceiling |
| --- | --- | --- |
| `run-data.md` | 500 | 500 |
| `behaviour.md` | 531 | 531 |
| `reference.md` | 583 | 584 |

### 1. The effort polako picks is the effort that runs

**Problem.** The probes above found gaps between the effort and model polako
chooses and the ones that run:
- A resumed run drops `--effort`, so it falls back to the CLI's default.
- An exported `CLAUDE_CODE_EFFORT_LEVEL` beats `--effort`, and
  `warnClaudeModelEnv` says only that it "can".
- The model half of that warning is wrong the other way round: `--model`
  beats `ANTHROPIC_MODEL`. But `ANTHROPIC_DEFAULT_OPUS_MODEL`, which the
  warning doesn't know, remaps `opus` itself.

**Shape.**

- `buildArgs` passes `--effort` on a resume too. The comment that says the
  session keeps its effort goes.
- **Refuse an effort flag the variable would override.** `effortFlagGate`
  refuses when `-effort`, `-remediation-effort` or an `-effort-by-size` cell
  is set, `CLAUDE_CODE_EFFORT_LEVEL` is exported with a different value, and
  so the flag would do nothing.
  - The check goes after the gate's early return and before its `--help`
    probe. `preflight` has no room left under its size budget.
  - `plan` and `health` reach the same gate, so they refuse too.
  - The error names both fixes: unset the variable, or drop the flag.
- **Read the variables through one lookup** that checks `cfg.env` before
  `os.Getenv`. Tests then need no `t.Setenv`, and test helpers blank both
  variables. Otherwise a developer's own export fails the gate tests.
- **Warn once per pickup when a label is overridden.** If an issue's or
  epic's `effort:` label resolves while the variable is exported, warn once,
  in `processIssue` right after `issuePickupPolicy`. Warning per dispatch
  would repeat on every resume.
- **`warnClaudeModelEnv` says what's true.**
  - `ANTHROPIC_MODEL` moves only runs that inherit.
  - `ANTHROPIC_DEFAULT_OPUS_MODEL` remaps `opus` wherever it's asked for.
    Its sonnet and haiku siblings likely do the same, but weren't probed.
  - `CLAUDE_CODE_EFFORT_LEVEL` wins over every effort polako passes.

**Done when.**
- The `buildArgs` test expects `--effort` on a resume.
- Gate tests refuse a differing value, pass an equal one, and cover
  `-dry-run`.
- The pickup warning is tested through `cfg.env`.
- behaviour.md's "a resume keeps the choice" line is true again.

Estimate: S

### 2. `stats` lists the models inherited runs ran on

**Problem.** The inherited model changed twice in four weeks, and `stats`
never said so unless asked with `-by model`.

**Shape.**

- **One `models` line in the default report.** It appears when more than one
  model is in scope, giving each model's first and last date and the CLI
  version it first appeared on. `-json` gains an `epochs` array, always
  present, empty when there's nothing to show. The HTML page gets the line
  through its sections list.
- **Count only fresh, inherited runs.** That means `requested_model` empty,
  and a reason other than `resume` or `unfinished`. This drops the resume
  `[1m]` flap, and runs someone asked for a model on purpose.
- **List models; don't build consecutive epochs.** Inherit can differ per
  repo through `.claude/settings.json`, so one machine draining two repos
  would flap.
- **Take `claude_version` from the init event's `claude_code_version` when
  it's there**, and keep the preflight snapshot as the fallback.
- **Make room in `run-data.md`** by moving its "Comparing configurations"
  section into `experiments.md`. That file already restates `-run-tag` and
  `-by tag`, and nothing links to the section.
- **Rewrite continuous-improvement.md's "Version drift is a recipe, not a
  flag".** Model drift becomes a line in the report, and the paragraph says
  why: the recipe missed both switches. CLI-version drift stays a recipe.

**Done when.**
- A fixture with two inherited models prints the line and the JSON array.
- A fixture where a resume reports the bare id prints one model, not two.
- A fixture with one model prints nothing.
- The `TestStatsReport` and JSON goldens are updated.

Estimate: S

### 3. `-by tag` notes a batch whose model moved under it

Depends on ticket 2, for where the docs live.

**Problem.** A batch that asked for one thing and ran on two models reads as
a verdict on whatever the tag was about. It shouldn't warn on a deliberate
tier swap, or on `plan-best` (`opus` against `best`). There, the model *is*
the change.

**Shape.**

- Note it under the table only when one `requested_model` resolved to more
  than one model within a tag. Inherit counts as one request.
- Skip `(none)`: untagged runs aren't a batch, and would note on almost every
  report.
- Skip resumes, for the `[1m]` flap.
- Show it in the text table, the HTML table's `Note`, and `-json`'s `by`
  document.
- `experiments.md`, "How to settle one": a batch and its baseline each run
  on one model, and `stats -by tag` says when one didn't.

**Done when.**
- An inherit-only tag spanning two models notes it.
- A tag with two different requests stays quiet.
- `(none)` and resumes are ignored.

Estimate: S

### 4. The session line says where its model came from

**Problem.** `[claude] session started (model …)` names the model but not
whether anyone asked for it. An asked-for tier that ran as another one goes
unnoticed. That can happen through `ANTHROPIC_DEFAULT_OPUS_MODEL`, a
settings file or a gateway.

**Shape.**

- The line adds `inherited` or `asked for <value>`. `eventLog` gets the
  requested model from `scanEvents`.
- **Warn when an asked-for tier ran as another.** Split both strings on
  non-letters, so `opus[1m]` and full ids are checked. If the requested tier
  word (`opus`, `sonnet`, `haiku`, `fable`) isn't in the init model, one
  warning names both. `opusplan`, `best`, `default` and any other string pass
  unchecked, the way `labelPolicy` treats them.
- `plan` and `health` runs get the same check.
- CLAUDE.md's alias sentence gains `fable`, since the code now lists it.

**Done when.**
- `eventLog` unit tests cover inherited, a match, a mismatch, and an
  unchecked string.
- One drain test runs `cfg.model = "sonnet"` against the unchanged fake,
  which inits as an opus id, and sees the warning. Existing drain tests
  assert on argv and records, so they still pass.

Estimate: S

### 5. `status`'s last-shift line names the model

**Problem.** The last-shift line prices a shift but doesn't say what ran it.
So a model switch between shifts shows nowhere a human looks by default.

**Shape.**

- `statuslastshift.go` only, since `status.go` sits at 999 of 1,000 lines.
- The line and `-json`'s `last_shift` gain the models the shift's fresh,
  inherited runs reported, by ticket 2's rule.
- `reference.md` changes in place only.

**Done when.**
- A new fixture shows the model.
- The existing goldens, whose fixtures carry no model, are unchanged.

Estimate: S

### 6. Plan and health runs compare by a recipe

**Problem.** `reference.md` says a `plan` or `health` `-run-tag` compares "in
`polako stats`". `stats` skips those records, so the sentence is false, and
`plan-best` has no stated way to settle.

**Shape.**

- Fix both `reference.md` rows in place, to point at the recipe.
- `experiments.md` gains the one-liner beside `plan-best`: `jq` over
  `kind == "plan"` records, grouped by `tag` and `model`, summing `cost_usd`
  and `issues_created`.
- Folding them into `stats` waits until the recipe gets used. See
  "Considered and not proposed".

**Done when.** The rows are true, and the recipe runs on the maintainer's
records.

Estimate: S

### 7. The ritual learns about models

Depends on tickets 1–4.

**Problem.** `continuous-improvement.md` has no step for "the model
changed". behaviour.md says inherit follows the account tier, but it also
follows Claude Code's releases.

**Shape.**

- `continuous-improvement.md` gains the "Keeping it right" table.
- behaviour.md, "Which model and effort a run gets" (net zero lines):
  - Inherit moves with Claude Code's releases.
  - The environment facts from ticket 1.
  - `plan`/`health`'s `opus` is the strong default, not the strongest tier.
    `best` is that, and the `plan-best` row says whether it's worth the
    price.

**Done when.** The two documents say the above and stay within budget.

Estimate: S

## Considered and not proposed

- **A/B arms inside the binary.** A `-challenger` flag would split one
  batch into two arms by issue number, so both arms share the same days,
  models and skill versions. Turned down on 2026-09-23: tagged batches stay
  the method, and tickets 2–5 make them honest.
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
- **A drain line when the model changes mid-shift.** Both switches happened
  between shifts, so it would have caught neither. It would also need a memo
  keyed by requested model, or remediation runs on another model would log a
  change every time. `stats` and `status` catch a switch where it happens.
- **Folding plan and health runs into `stats`.** It's medium-sized, not
  small. Pushed into the run rollups they make a phantom issue #0, move
  `-shift last` onto the plan's shift, and blank `status`'s last-shift line
  after every `polako plan`. Doing it right needs a slice of their own. That
  can wait until ticket 6's recipe gets used: plan and health are 2.5% of
  spend, and `plan-best` has never run.
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
