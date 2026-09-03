# Model and effort: who decides, and what the default is

Scope: the binary, its docs, one CLAUDE.md line, three rows in the experiments
ledger — no skill text changes · Behavior change: none by default; every new
knob ships empty, so a shift run with today's flags dispatches today's argv

Every run polako dispatches is one `claude -p` process, and two arguments decide
most of what it costs: which model, and how hard it thinks. Today one of those
is a per-shift flag and the other does not exist. `docs/plans/token-spend.md`
closed with "running planning or the review gate on a cheaper model is a
model-selection design question of its own, not drafted"; the
`remediation-sonnet` row in `docs/experiments.md` has been waiting since it was
written because "`-model` is per-shift today, so nothing can vary the model by
run reason". This document is that design.

It answers three things. Who gets to choose the model and effort — per
command, per ticket, per epic — and in what order when they disagree. What
the default is when nobody chose. And how that default stays right when the
model it names is two generations old, because a default that says `Sonnet 5`
is a bug six months from now.

## What exists today

- `-model` on `work` (`flags.go:260`), empty by default, meaning the flag is
  omitted and the CLI picks. `-model` on `plan` (`plan.go:106`) and `health`
  (`health.go:87`), both defaulting to the alias `opus`, for a reason
  `docs/plans/backlog-fill.md` argued out: a plan run happens once and steers
  every run downstream, so its tokens are the cheapest in the system.
- One argv builder, `buildArgs` (`claude.go:187`), which every verb and every
  remediation run goes through. `--model` joins the argv there when set.
  Nothing passes an effort.
- `POLAKO_MODEL` already works: `applyEnvDefaults` (`flags.go:376`) gives every
  flag an environment default.
- Every run record carries `model` (what actually ran, read off the init event
  in `stream.go:247`) and `requested_model` (what `-model` asked for,
  `metrics.go:261`). `stats -by model` groups on the first. The `plan` and
  `health` records carry only `model`.
- The supervisor reads no issue text. The queue listing (`listOpenIssues`,
  `backlog.go:78`) asks GitHub for `number,labels,subIssuesSummary,blockedBy`
  and nothing else. The `Estimate: S|M|L` line `plan-backlog` writes into every
  proposal is prose only the human and the implementing skill ever see.
- Nothing links a child issue to its epic. `containerInfo` (`backlog.go:137`)
  knows a container's sub-issue count, not which issues those are, and
  `processIssue` (`issue.go:186`) gets an issue number and nothing more.
- The installed CLI (2.1.252, checked with `claude --help`) takes
  `--effort <level>` with exactly `low`, `medium`, `high`, `xhigh`, `max`, and
  describes `--model` as taking "an alias for the latest model (e.g. 'fable',
  'opus', or 'sonnet')". `docs/plans/backlog-fill.md` deferred an effort knob
  until "the CLI grows an effort/reasoning flag worth passing". It has.

## The answer in one paragraph

polako has no opinion about models except where it knows something the CLI
cannot: why this run is happening — implementing an issue, rebasing a PR,
planning a backlog — and, later, how big the issue is. Everything else
inherits the CLI's own default, which the operator already set in Claude Code
and Anthropic already retunes each generation. Where polako does hold an
opinion it is spelled as a tier alias and an effort word, never a model id.
Humans override it at three altitudes, and the more specific altitude wins.

## Vocabulary

Three values, the same everywhere a model or effort can be named:

- **model** — a CLI alias or a full id. The CLI documents `opus`, `sonnet`,
  `haiku`, `fable`, `best` (the strongest tier the account can reach, Opus
  where Fable is not available) and `default` (clear any override, use the
  account's default), on its model-config page. polako checks only the shape,
  `^[A-Za-z0-9._\[\]-]+$`, and passes the value through. Whether a model exists
  is the CLI's business, and a name polako refused would be a name it had to
  keep a list of.
- **effort** — exactly `low`, `medium`, `high`, `xhigh`, `max`. A closed set,
  because the CLI's is. `ultracode`, which the CLI's docs also list, is refused
  on purpose: it is a multi-agent workflow mode, not a level, and an unattended
  run has no business fanning out a fleet. Anything else is a typo; it warns
  and falls through to the next level below.
- **inherit** — the empty value. The flag is omitted from the argv, and the CLI
  resolves the model and effort the way it would for the operator at a
  terminal: user settings, the target repository's own `.claude/settings.json`
  (`model`, `effortLevel`), the account tier. That last part is a free fourth
  altitude: a repository that wants every polako run on `sonnet` at `medium`
  says so in its own settings file, and polako never reads it.

## Who decides: five levels, most specific first

| Level | Where it lives | Who sets it | May it make a run dearer? |
| --- | --- | --- | --- |
| 1. Ticket | Labels `model:<value>` and `effort:<level>` on the issue | A maintainer, at curation | Yes |
| 2. Epic | The same labels on the parent issue, inherited by a child without its own | A maintainer | Yes |
| 3. Run reason | `-remediation-model`, `-remediation-effort` for `remediate`, `checks` and `review` runs | The operator | Yes |
| 4. Command | `-model`, `-effort` on `work`, `plan` and `health` | The operator | Yes |
| 5. Inherit | Nothing passed; the CLI's own resolution | The operator's Claude Code settings, the repo's, the account | — |

Resolution happens once per pickup — each `processIssue` call — and every run
on that issue in that leg uses it. A resume keeps the choice of the run it
resumes; changing models mid-session is not a thing an unattended supervisor
should do by accident. A remediation run resolves again with its own class, so
a `-remediation-effort medium` shift rebases at `medium` while it implements at
whatever level 4 or 5 said. Resolving per pickup rather than per shift is what
makes a label edited while the issue waited on an answer take effect on the
next leg, at no extra cost: the pickup already asks GitHub about the issue.

The rule behind the last column, which a CLAUDE.md line will carry: **labels
and flags may make a run dearer; issue text never may.** Labels need triage
rights, flags need the operator's shell. A body or a comment can be written by
anyone on a public repository, and "run this on the most expensive model at
`max`" is a cost-escalation a stranger should not be able to type.

## Per ticket: labels, not comments, not sizing

Three channels were on the table.

**Labels win.** They are already how polako reads human judgement about an
issue — `needs-human`, `proposed`, `awaiting-answer`, the `-label` gate — and
`hasLabel` (`backlog.go:334`) already matches them the way GitHub does,
case-insensitively. They cost nothing to read at pickup, they show in the
issue list where curation happens, and only a maintainer can apply one. Two
families, one value each: `model:opus`, `model:sonnet`, `model:haiku`,
`model:best`, `model:default`, `model:<full id>`; `effort:low` through
`effort:max`. Two `model:` labels on one issue is a mistake, so it warns and
falls through rather than picking one. polako does not create these labels —
eight `ensureLabel` calls at preflight would litter every repository it
touches with labels most shifts never use — and a human applying one for the
first time creates it in the same GitHub gesture.

**Comments, no.** A comment is data. polako reads the thread only to notice
that a human replied to a question, and `implement-issue` reads it as the
spec. Turning either into a control channel means parsing free text written
by anyone, forever, for a setting a label expresses in one word.

**Sizing, later, and only downward.** The `Estimate: M — likely 1–2 runs`
line is the obvious per-ticket signal: `plan-backlog` writes it, a curator
edits it, and an S issue plausibly needs less thinking than an L. But reading
it means the supervisor's first read of issue body text — exactly the step
`docs/plans/backlog-fill.md` said to argue about before building, and
`docs/hardening.md` lists bodies as attacker-editable. It is drafted here as
the last, optional ticket with two safeguards: the parse is constrained to an
enum, and the body only picks *which* operator-set cell applies
(`-effort-by-size S=medium,L=max`), so an edit from `S` to `L` can raise a run
to what the operator already agreed an L may cost, and no further. Neither
experiment below needs it; the epic body says it is the one piece still to be
argued for.

## Per epic: the parent's labels

An epic's labels apply to every child that has none of its own. The design
record for a body of work is the natural place to say "all of this is
mechanical, run it on `sonnet`" or "this one is subtle, `xhigh` throughout",
and per-child labelling of a ten-issue epic is the kind of clerical work the
backlog-fill plan exists to remove. A child that disagrees carries its own
label; a child that wants the CLI's default despite its parent carries
`model:default`, which is an explicit choice rather than an absence.

The read is one call at pickup: `gh issue view N --json labels,parent`. gh
2.98.0 serves `parent` as `{number,state,title}` on both `issue view` and
`issue list` (checked on this repository). It goes on the *view*, not the
listing, deliberately: a third extended field would join the single-flag
old-gh fallback in `listOpenIssues` (`backlog.go:86-94`), so a gh that knows
`subIssuesSummary` but not `parent` would lose container detection for the
whole shift, and the `-label` gate filters the listing so the parent may not
be in it anyway. A view per pickup ignores both problems. A second `view <parent> --json
labels` follows whenever the child settled fewer than both families itself
(and has a parent) — the two families resolve independently, so a child with
`model:sonnet` and no effort label still reads its epic for an effort. A child
that settled both keys itself costs exactly the one read. A gh that rejects
`parent` retries with `--json labels` through the existing `unknownJSONField`
check. A read that fails outright warns and falls through to the flags: this
is a preference, not orchestration state, and a run on the default model beats
no run.

## Per command: the flags

`-model` stays what it is. `-effort` joins it on all three verbs, empty by
default, passed as `--effort` when set. `-remediation-model` and
`-remediation-effort` join `work` only, empty by default, applied to the three
runs `pr.go` dispatches against an open PR. They exist because the ledger's
oldest open row cannot run without them, and because the remediation runs are
the one class polako can tell apart that the CLI cannot: a rebase, a red-check
fix and a review reply are shorter, narrower tasks than implementing an issue,
and whether they need the same model or the same depth is a measurable
question.

`POLAKO_EFFORT` and the two remediation variables come free from
`applyEnvDefaults`. A `-dry-run` prints the argv, so the flag's effect is
visible before a run spends anything. A CLI whose `--help` does not list
`--effort` fails the flag at preflight with a message naming the CLI version,
because otherwise the usage error looks like a crash, spends `-retries`
resumes, and parks the issue for nothing.

## The default, and why it is mostly "inherit"

Everything above is override. When nobody chose:

| Run | Model | Effort |
| --- | --- | --- |
| `work`, implementation runs | inherit | inherit |
| `work`, remediation runs | inherit — until a ledger row says otherwise | inherit — until a ledger row says otherwise |
| `plan`, `health` | `opus` (unchanged) | inherit |

Three arguments for that table, in order of weight.

**Inherit is the default that maintains itself.** The CLI's default model
follows the account tier (Opus for Max, Team Premium, Enterprise and API keys;
Sonnet for Pro and Team Standard, per its model-config page) and moves when a
generation ships. Its default effort is `high` today, on the same page,
described as the balance of spend and intelligence — Anthropic's own answer to
the question this document asks, retuned with every model. A number polako
hardcoded instead would be right for one generation and silently wrong for the
next, with nothing to tell anyone. The `opus` alias on `plan` and `health` is
the same bet in the other direction: the strongest tier, whatever it is called
this year.

**Cost is per merged PR, not per token.** That is why `stats` prints a
`$/merged` column and why every verdict in `docs/experiments.md` is written in
it. A cheaper model that parks one issue in five has cost a human's attention
and a re-run, and `stats -by tag` will say so; the argument for any cheaper
cell has to be made in `$/merged` across a tagged batch, not in the price
sheet. For a subscription the currency is pool percentage instead, and the
usage gates already exist for that.

**Effort before model.** Anthropic's guidance for the current generation is to
try the same model at a lower effort before building a model cascade: lower
effort on a newer model often matches a previous generation at high effort,
and one model keeps one cache. So the first experiment is not
`remediation-sonnet`, it is the same model with `-remediation-effort medium`.
Only if that holds quality does the model step-down get its turn.

That is also the answer to "what about small issues on a small model": it is
a hypothesis, the size ticket makes it testable, and nothing ships it as a
default without a row.

## Where it shows

- **One log line per dispatch**, only when something other than inherit
  resolved: `issue #42: model sonnet (label), effort medium (epic #40)`. The
  source in parentheses is the level that won.
- **`-dry-run`** prints the argv it would run, so `--effort` and the label
  resolution are visible before a run.
- **Preflight** lists `-effort` and the remediation cells in the settings block
  beside `-model`, and warns when `ANTHROPIC_MODEL` or
  `CLAUDE_CODE_EFFORT_LEVEL` is exported: the CLI's own precedence puts
  `ANTHROPIC_MODEL` above `--model`, the child inherits the operator's
  environment by design (`TestDispatchGivesTheChildTheOperatorsEnvironment`
  pins `cmd.Env` nil so the `HTTPS_PROXY` egress keeps working), so an exported
  variable silently beats every level in the table.
- **A mismatch warning** when the init event's model does not contain the
  alias that was asked for — `opus` requested, `claude-sonnet-*` ran — which
  is what the exported variable looks like from inside a run. Skipped for
  `best`, `default` and full ids, whose resolution is not a substring check.
- **Records** gain `requested_effort` on every run, `requested_model` and
  `requested_effort` on the `plan` and `health` records that lack the first
  today, and two enums, `model_source` and `effort_source`, each one of
  `label`, `epic`, `remediation`, `size`, `flag`, `inherit`. Identifiers and
  operator-chosen labels only, so the record rules hold, and `stats -by tag`
  can price a batch by what it actually asked for.
- **Not the exit summary and not `-post-summary`.** Widening the one comment
  that leaves the machine is a change to argue for on its own.

## What this does to the invariants

One line joins CLAUDE.md, under the invariants:

> **Model names are tier aliases, never ids, and defaults inherit.** The
> binary spells a model as `opus`, `sonnet`, `haiku` or passes the operator's
> string through; a versioned id in the source is a default that rots, and a
> test refuses it. Labels and flags may make a run dearer; issue text never
> may — a body or comment is anyone's to write on a public repository, and
> the most expensive model at `max` is not a thing a stranger gets to ask for.

The test is a regexp, `claude-[a-z]+-[0-9]`, over the non-test Go source in
`cmd/polako`. It passes today. Fakes and fixtures in `_test.go` files are out
of its scope, and so are the docs, which quote real ids in record examples.

Nothing else moves. State stays on GitHub — a label is already state, and the
resolution is recomputed from it every pickup, never stored. Telemetry stays
write-only: the new fields are written, and the only readers are still
`stats` and the pricing line. Nothing merges itself. The `-label` gate stays
human-only; a `model:` label is not the gate label and grants nothing.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.

### 1. `-effort` on every verb, recorded

**Problem.** The CLI takes `--effort` and polako cannot pass it, so the one
lever Anthropic says to pull first is out of reach, and no record says what
depth a run asked for.

**Shape.** `cfg.effort` beside `-model` in `flags.go:260`, validated against
the five words at parse time, `ultracode` refused with a sentence saying why.
`--effort` beside `--model` in `buildArgs` (`claude.go:187`); a test mirroring
`TestBuildArgsPassesTheRequestedModel` (`main_test.go:1525`). `-effort` on
`plan.go:106` and `health.go:87` mirroring `opt.model` — both dry runs already
go through `buildArgs`. `RequestedEffort` on `runRecord` (`metrics.go:262`)
and both `requested_*` fields on the `plan` and `health` records. In
`preflightPairs` (`main.go`, the settings block): the `-effort` row, the
`--help` check when `-effort` is set, and the exported-variable warning. The
no-versioned-id test in `repo_test.go`. The CLAUDE.md line.

Docs, under a budget: `docs/run-data.md` is at its 500-line ceiling and
`docs/reference.md` one line under it, and `docsbudget_test.go` allows no
debt. The moves that cost no lines: the `plan` and `health` `-model` rows
(`reference.md:275`, `:312`) become `-model` / `-effort` rows the way
`-tools` / `-add-tools` already share one; `run-data.md:11`'s field list
names the new fields in place; `run-data.md:466`'s `-model claude-opus-5`
example becomes `-model opus`, which it should have been anyway.

**Done when.** `polako work -effort medium -dry-run` prints `--effort medium`,
the empty default passes nothing, a record from a fake run carries
`requested_effort`, and a `plan` record carries `requested_model`.

### 2. A policy seam, and the remediation flags

**Problem.** Every run on an issue gets the same `--model`, so the ledger's
`remediation-sonnet` row has waited since it was written, and a model or
effort chosen per run has nowhere to live: `newRunRecord` reads
`cfg.model` (`metrics.go:446`), and the three remediation sites in `pr.go`
(`remediateConflicts` :191, `remediateChecks` :218, `remediateReview` :251)
pass the outer config to `recordRun`, so a remediation on another model would
record the implement model.

**Shape.** A new `policy.go`: `runPolicy` (the operator's cells), `runChoice`
(model, effort, and a source enum for each), `choose(reason)` sorting the
seven reasons (`metrics.go:47`) into the two classes, `apply(cfg)` returning
the per-run config copy the way `issueRun` (`claude.go:146`) and
`remediateReview`'s `runCfg := cfg` already do. The choice rides on
`runContext` (`metrics.go:199`, "everything the event stream cannot know"),
and `newRunRecord` reads it there. Flags `-remediation-model` and
`-remediation-effort`; resolution in `processIssue` (`issue.go:186`), because
`drain` sits at its size ceiling and `dispatchRun` is near it
(`sizebudget_test.go`). The dispatch log line. `dryrun.go:92` goes through
the same seam. `reference.md`'s `work` table gains one row for the pair; the
line it displaces is the duplicated startup quote at `:140-142`.

**Done when.** A fake drain under `-remediation-effort medium` shows the
rebase run's argv carrying `--effort medium` and the implement run's not, and
the two records carry `effort_source` of `remediation` and `inherit`.

### 3. Per-issue labels

**Problem.** A curator who knows one issue is subtle, or trivial, has no way
to say so that a run will honour.

**Shape.** `labelPolicy(labels)` in `policy.go`: `EqualFold` prefix match on
`model:` and `effort:`, the value regexp, the effort enum, warn-and-fall-through
on a typo or a duplicate, `model:default` as the explicit no-inherit.
The pickup read, `gh issue view N --json labels`, through `retryRead` like
`issueHasLabel` (`backlog.go:342`); the fake gh's view handler already
composes `labels` (`drain_test.go:288`). Extend
`TestIssueTemplatesApplyNoOrchestrationLabel` to the two prefixes: an issue
form on a public repository applies labels with no triage rights, and this
repository's templates must not hand out `effort:max`. A `docs/behaviour.md`
paragraph naming the two families.

**Done when.** A fixture issue labelled `effort:low` dispatches with
`--effort low` and records `effort_source` of `label`; the same issue with
two `model:` labels warns and runs on the flag.

### 4. The epic's labels reach its children

**Problem.** Ten children under one epic need ten labels to say one thing.

**Shape.** Widen ticket 3's read to `--json labels,parent`, with the
`unknownJSONField` retry to `labels` alone; a second `view <parent> --json
labels` whenever the child settled fewer than both families itself, each
family filled from the epic independently; `model:default` on the child stops
inheritance. `fakeIssue` gains `Parent` and the view handler composes it. A
read failure warns and falls through.

**Done when.** A child with no labels under a parent labelled `model:sonnet`
dispatches on `sonnet` and records `model_source` of `epic`; a child carrying
`model:default` under the same parent passes no `--model`.

### 5. The precedence, written down, and the ledger unblocked

**Problem.** Five levels and a rule about who may raise a price is the kind
of thing that has to be on one page or it is not true.

**Shape.** A "Which model and effort a run gets" section in
`docs/behaviour.md` after "Keeping an open PR green" (`:282`; the file has
room), carrying the table above and the log-line example. In
`docs/experiments.md`: a `remediation-effort-medium` row above
`remediation-sonnet` (`:52`), that row's "needs the knob to exist first"
clause replaced by the flag that now exists, and a `plan-best` row after
both. A pointer from `docs/plans/token-spend.md`'s "noted here, not drafted"
(`:239`) to this document.

**Done when.** `TestDocsDocumentEveryFlag` passes with every new flag named
in `docs/`, and the ledger's two remediation rows name a flag that exists.

### 6. Effort by size, from the `Estimate:` line — optional, last

**Problem.** An `S` and an `L` get the same depth, and the one signal that
says which is which sits in a body nothing reads.

**Shape.** `-effort-by-size S=medium,L=max` on `work`, empty by default,
applied to implementation-class runs only. One `gh issue view N --json body`
at pickup; a parse constrained to `^Estimate:\s*([SML])\b`, anything else
meaning no size; `size` on the terminal issue record — which is also the
per-size pricing `docs/plans/backlog-fill.md` deferred to its phase 4. A body
only selects which operator-set cell applies. The epic body names this as the
one
piece still to be argued for: the supervisor's first read of attacker-editable
text, and neither experiment above needs it. A curator may leave it
`proposed` until a row asks for it.

**Done when.** An issue whose body says `Estimate: S` dispatches at the `S`
cell's effort and records `size` of `S` and `effort_source` of `size`; an
issue with no estimate line runs on the flag; a body saying `Estimate: XL`
runs on the flag and warns nothing, since a hand-written issue owes no line.

## Experiments — rows, not tickets

These are operator ritual under `docs/continuous-improvement.md`'s rule, run
in this order once tickets 1 and 2 have shipped:

| tag | hypothesis | knob |
| --- | --- | --- |
| `remediation-effort-medium` | A rebase, a red-check fix or a review reply finishes at the same rate at `medium` as at the inherited effort, for less. | `-remediation-effort medium`, same model. |
| `remediation-sonnet` | Already in the ledger. Runs only if the effort row did not already take the saving. | `-remediation-model sonnet`. |
| `plan-best` | A plan run on the strongest tier the account reaches proposes a better-cut backlog than `opus`, at a cost a once-per-batch run can carry. | `polako plan -model best`. |

Each earns a row, a `stats -by tag` verdict in `$/merged` or pool percentage,
and a decision. A verdict that holds moves a cell in the default table above
and gets a `docs/` line; nothing moves without one.

## Considered and not proposed

- **Reading the operator's `~/.claude/settings.json`** to know what "inherit"
  will resolve to, so the log line could name it. The CLI's precedence has
  five layers and the account default is not on disk; the init event already
  reports what ran, and the record keeps it. Guessing beforehand would be
  wrong often enough to mislead.
- **`parent` in the queue listing.** Argued above: a third extended field
  joins a single-flag fallback and costs container detection on a mid-age gh.
- **A `-max-model` ceiling** — "labels may not go above `sonnet` this shift".
  Plausible, but no shift has asked for it, and a label a maintainer applied
  is a decision the operator can undo with one click.
- **`stats -by effort`.** `-by` keeps its list short on purpose;
  `requested_effort` is on disk and a jq line groups it until a row needs more.
- **`best` or `max` as the `plan` and `health` default.** Fable is priced
  above Opus and `max` above `high`; a once-per-batch run can carry that, but
  it is a tagged experiment, not a default. Listed above.
- **A cheaper model for the `/usage` probe.** Two tiny calls per issue; the
  token-spend plan already left them alone.
- **`size:` labels instead of the `Estimate:` line.** The label pass strips
  every proposal to exactly `proposed` (`labelpass.go:146-150`), so `plan` could
  not apply them, and a curator adding them by hand is the clerical work the
  line exists to avoid.

## Open questions

Four facts to check against the installed CLI with one throwaway
`claude -p … --output-format json` before ticket 1 starts, so the tickets are
built against what the CLI does rather than what its docs say:

1. Does the `system`/`init` event report the effort in force, the way it
   reports `model`? If so, log it on the `session started` line and record it
   as `effort` beside `requested_effort`.
2. Does `--effort` combine with `--resume` on a session started at another
   level, or is it rejected? A resume keeps its run's choice either way; this
   decides whether the flag is passed on a resume at all.
3. Is `--model default` accepted headless, as the alias page says? It is the
   child's escape from an epic's label, so ticket 4 depends on it.
4. Which wins for effort when both `CLAUDE_CODE_EFFORT_LEVEL` and `--effort`
   are given? The model-config page settles it for `ANTHROPIC_MODEL` over
   `--model` and is silent here. The preflight warning's wording depends on
   the answer.

## Work items

Each is one PR, one of the tickets above. Tickets 1 and 2 first, in one
release: the effort flag and the per-reason seam are what the ledger has been
waiting for, and together they change no default. Then 3 and 4, the
operator-facing half. 5 rides on whichever of those ships last. 6 waits for a
row to ask for it.

- [ ] `-effort` on every verb, recorded (ticket 1)
- [ ] A policy seam, and the remediation flags (ticket 2)
- [ ] Per-issue labels (ticket 3)
- [ ] The epic's labels reach its children (ticket 4)
- [ ] The precedence, written down, and the ledger unblocked (ticket 5)
- [ ] Effort by size, from the `Estimate:` line — optional (ticket 6)
- [ ] The `remediation-effort-medium` batch has a verdict in the ledger
- [ ] The `remediation-sonnet` batch has a verdict, or the row above made it moot
