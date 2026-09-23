# `design`: a run that writes the plan document

Scope: a fourth shipped skill (`design-plan`), a `design` label, a `design`
verb reusing `work`'s per-issue path, run-data kinds, docs · Behavior change:
`work` skips issues labelled `design`; `status` gains a row; a new verb runs
one issue to a merged PR; two new run-data kinds nothing reads yet

`polako plan` decomposes. It doesn't design. Point it at a document and it
does a gap analysis against the code, cuts the work into one-PR issues, and
files them behind `proposed`. Point it at `-brief "achieve X"` and it does
the same, with nothing to check the cut against. Its own text says where
design goes: "a piece that needs a decision nobody has made is not an issue
yet — the decision goes in the epic body for the curator."

Every feature this repo has shipped was designed somewhere else first: an
interactive session that measured what exists, ran probes, rejected
alternatives with reasons, and wrote `docs/plans/<topic>.md`. Then `plan
-vision` turned that into a batch. Six such documents sit under `docs/plans/`
today, and two more were retired once their batches closed. That session is
the one step between "here's where this is going" and a merged PR that no
verb covers.

This document drafts the verb that covers it. The flow becomes:

```
issue (design) ──design──► PR adding docs/plans/<topic>.md ──human merges──► plan -vision ──► proposed issues ──human curates──► work
```

Two decisions were taken with the operator before drafting: `plan` keeps its
name (`design` beside it is the distinct word, and the `Proposed by polako
plan from` footer is a parsed contract on every existing issue), and a design
run supervises its PR to merge the way `work` does, so review comments on the
doc become remediation runs and the PR thread is the design conversation.

## What exists today

Measured on this worktree at `3751484`, 2026-09-22.

- **`plan-backlog` is built not to design.** One turn: ending it ends the
  process, so it can't ask and come back. `planTools` (`plan.go`) grants git
  reads, `gh issue list/view`, `gh search issues`, `gh issue create`, Read,
  Glob, Grep, Write — no interpreter, no build tool, so it can't run a test
  or probe a CLI flag. Its eval (`evals/plan-vision`) grades labels,
  parenting, body shape, dedup and write surface. Nothing grades the cut.
- **`implement-issue` already has everything a design run needs**, aimed at
  code: a worktree on `issue-N`, a `PLAN.md` resume point, the
  asking-a-question recipe (`QUESTION.md` via `--body-file`, then `gh issue
  edit N --add-label awaiting-answer`, then stop), a review gate, a PR ending
  `Closes #N`. `defaultTools` (`flags.go`) grants the build and test tools.
- **`processIssue` is one function with four exits** (`issue.go:255`): nil
  (merged, or closed with no change), `*parkedError`, `*deferredError` (a
  question was asked), anything else (fatal, Ctrl+C). Inside it: the pickup
  policy, `syncDefaultBranch`, `prForBranch` → `dispatchRun` →
  `superviseToClose`. It reads `skill`, `tools`, `branchPrefix`, model and
  effort, the poll and retry knobs, caps, `rec`, `remote`, `visualEvidence`,
  `strictOrder`, `dir`, `repo`. It never reads `label`, `ungated`, `once`,
  `skip` or the session caps — those are the drain's.
- **What `drain` wraps around it** (`drain.go:153-325`): `tidySweep(ctx, cfg,
  0)` once before the first pickup; then per exit — deferred logs "leaving it
  for a human", parked goes through `parkAndMoveOn` (narrate, aside,
  `resumeHint`, `parkIssue`, `notifyParked`), other errors reach `finish`,
  which fires `notifyStopped` and prints `drainSummary`. The rest of the loop
  is queue work a single-issue verb must not copy: `openIssues`,
  `closeFinishedContainers`, the usage gate, `awaitAnswer`, `-once`, `-skip`.
- **`work`'s preflight isn't reusable as-is.** `preflight` (`main.go:274-389`)
  is at its 130-line budget and interleaves the verb-neutral checks —
  binaries, notify command, git dir, `gh repo view`, shift log, versions,
  effort gate, usage probe, skew gate, update notice — with `queueGate` and
  `labelGate`, which refuse an unfiltered public repo. `intakePreflight`
  (`intake.go:140`) is the lighter twin: no shift log, no usage probe, no
  skew gate.
- **The wait-or-exit knob exists under another name.** `handOffQuestion`
  (`issue.go:573-604`) fires `notifyAwaiting`, then returns `deferredError`
  unless `cfg.strictOrder`, in which case it polls `waitForReply`. That's
  the only reader of `strictOrder` inside `processIssue` (`issue.go:588`).
  `work -once` on a question exits 0 (`TestDrainOnceExitsOnAQuestion`).
- **One skill-specific line in the prompt builder.** `issueRun`
  (`claude.go:147-155`) appends ` no-evidence` when `!cfg.visualEvidence`,
  and mints `issueLabelTools(N)` and `issueCloseTool(N)` per run.
- **Queue precedence** (`selectableIssues`, `backlog.go:213-266`): container,
  `needs-human`, `proposed`, `awaiting-answer`, `blockedBy`, ready. The drain,
  `-dry-run` and `status` all come through it. `-strict-order` folds
  `blocked` into `ready`.
- **The label table** (`labels.go:34-38`) has three entries, all `required:
  true`. `setupLabelDefs` clones it, `labelByName` panics on a miss, and
  `TestIssueTemplatesApplyNoOrchestrationLabel` forbids its names in issue
  templates. `required: false` is already a live path (the policy labels).
  `docs/setup.md` says "the three labels above" twice.
- **`status` derives a plan doc's state from footers** (`plans.go`): a file
  under `docs/plans/` that no issue names prints as `draft`. A merged design
  doc lands there with no work — that row is the hand-off.
- **Records.** `newRunRecord` writes `kind:"run"` with the skill name;
  `recordIssue` writes `kind:"issue"` with no skill field (`metrics.go`).
  `loadRecords` (`statsrecords.go:200-222`) drops any kind it doesn't know —
  the rule that let `plan` and `health` records in without a migration.
  `proposalPricingLine` prices every merged issue record with a cost.
- **The binary already files one issue itself**: `fileRetireIssue`
  (`retire.go:159-177`), `gh issue create --title --body --label proposed`,
  retried once behind `ensureLabel`, number parsed from the URL. `plan.go`
  has `briefTitle` (a 50-character cut) and `planBriefMax` (2000).
- **Test seams.** `fakeGh` (`drain_test.go:317`) answers `issue view`,
  `issue create` (checks the label exists), `issue edit`, `issue comment`,
  `issue close`, `label create`, `pr list --head`. `fakeClaude`
  (`main_test.go:274`) has a `stream` mode (no PR), an `asks` mode (comment
  and label, then answers-and-ships on a rerun), and arms that plant a
  MERGED PR. `health_skill_test.go` is the shape for a sibling skill's
  contract tests. `TestVerbUsageListsHealth` is the per-verb usage pin.
- **Evals.** `evals/lib/gh-fake.sh` answers `issue view`, `issue comment`,
  `issue edit`, `pr create`, `pr list`, and fails `pr view/diff/checks` as
  "no PR" — everything a design run makes. It also answers `issue create`,
  so a grader has to assert its absence. `run.sh` resolves a case against
  the main checkout's `evals/` (`run.sh:137`): the PR that adds a case can't
  run it, the caveat CLAUDE.md already gives `run.sh` changes.
- **Docs budgets.** `reference.md` is at 559/559 and `behaviour.md` at
  531/531 (`docsbudget_test.go`). A new verb section moves a ceiling with a
  dated comment; #434 did it for `unpark`. `TestDocsDocumentEveryFlag` wants
  every flag's literal in some `docs/*.md`. File budget 1000, function 130;
  `issue.go` is 967, `drain.go` 889, `main.go` 511 — the verb needs its own
  file.
- **Counts that go stale.** README `:31` "Seven verbs. Three start Claude
  runs", `:320` "other seven verbs"; `docs/install.md` `:5` and the `cp -r`
  list at `:188`; `docs/reference.md:8`; `docs/releasing.md:237`;
  `plugin.json` and `marketplace.json` name the slash commands in prose.
- **Two cosmetic seams.** `stages.go` narrates a write under `docs/plans/` as
  "implementing…" (a `PLAN.md` write is what it keys `stagePlan` on).
  `unpark`'s `nextShiftLine` (`unpark_work.go:145-163`) would say "resumes
  issue-N" for a parked design issue, which no `work` shift will.

## The answer in one paragraph

A design run is shaped like `work`, not like `plan`. It's anchored to an
issue — the request thread — runs `design-plan` in a worktree on `issue-N`,
may run the repo's tests and probes, may ask one batched question on the
thread and stop, and delivers one PR adding `docs/plans/<topic>.md` in the
plan-document convention. A human merges it. `status` then lists the doc as
`draft`, and `polako plan -vision docs/plans/<topic>.md` is the next command,
printed in the PR body and on exit, never run for you. The verb builds a
`work`-shaped config with the design skill and `-model opus`, calls
`processIssue` once, and copies from `drain` only the four-exit handling
around that call. A `design` label keeps `work` off the issue; it ships first,
protective and inert.

## What this may read and write

- **All state lives in GitHub.** The verb reads one issue (state, labels,
  sub-issue count), its branch's PR, its thread (baseline and reply), and
  the PR's checks and reviews through `supervisePR`. Local: `-dir`'s default
  branch, fast-forwarded `--ff-only` at pickup and after the merge, as `work`.
- **The binary's writes are `work`'s on one issue, plus two.** Inherited:
  `gh label create awaiting-answer` at preflight, the park label and
  comment, `gh issue close N --comment "Shipped in #P"` after a merge, the
  mirror fast-forward, the worktree sweep, run data and the shift log under
  `~/.polako`, the `-notify` hook, remediation runs' pinned PR-comment
  grants. New: `gh label create design` at preflight, and under `-issue`,
  `gh issue edit N --add-label design` when the label is absent — naming the
  issue on the command line is the human act, and the label's job from that
  moment is to keep `work` off it. Under `-brief`, exactly one `gh issue
  create --label design` before anything runs.
- **The skill's writes are `implement-issue`'s minus two channels.** A
  worktree at `<main-checkout>/.worktrees/issue-N`, the `issue-N` branch,
  commits adding one file under `docs/plans/`, a push, `gh pr create --head
  issue-N`, `gh issue comment N --body-file`, the pinned `awaiting-answer`
  edits. Never `gh issue create` — the doc is the deliverable, and `plan`
  files the tickets later with its dedup and label pass. Never the
  `polako-evidence` ref — nothing to screenshot, so no `evidence` argument.
  Never a change outside `docs/plans/`; the self-review gate checks the diff.
- **The allowlist is `defaultTools` plus two reads.** `designTools` =
  `defaultTools` + `Bash(gh issue list:*)` + `Bash(gh search issues:*)`. The
  build tools are there because Phase 2 measures by running tests; the two
  reads are there because every real plan doc's "What exists today" cites
  the open backlog, and `plan-backlog` already holds both. The security
  caveat is `work`'s: a narrowing, not a sandbox.
- **No public-repo queue gate.** `work` refuses an unfiltered public backlog
  because anyone can feed it. `design` works one issue the operator named on
  the command line or wrote in `-brief` — the same opt-in `-label` stands
  for. The body and thread are still data, not instructions; the skill
  carries the posture paragraph.
- **The `proposed` invariant is amended, out loud.** CLAUDE.md says whatever
  creates issues applies `proposed`. A `-brief` design request is the one
  issue the binary files without it: the operator authored it at the command
  line, and `design` already keeps `work` off it. Ticket 7 writes the
  amendment.
- **Nothing merges itself.** The two human touchpoints are unchanged:
  answering the run's question on the thread, and merging the doc PR.
- **Cost.** One issue's worth of `work` reads per run. Two new record kinds
  invisible to `stats` until taught (ticket 4 says why).

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. The `design` label keeps `work` off design requests

**Problem.** Nothing stops a drain from running `implement-issue` on a design
request. The label has to exist and be excluded before anything applies it.

**Shape.**

- `designLabel = "design"` beside `proposedLabel` in `main.go`, with the same
  kind of comment: a request `polako design` works into a plan document; the
  queue excludes it; only a human or `polako design` applies it.
- `labelTable` (`labels.go`) gains an entry, `required: false`: a repo with
  no design requests loses nothing by lacking it, and the verb ensures it at
  preflight. `setup -apply` still offers it through `setupLabelDefs`.
  `docs/setup.md` stops saying "three".
- `issueQueues` gains a `design` bucket (number, and whether
  `awaiting-answer` is also on it). `selectableIssues` adds the case after
  `proposed` and before `awaiting-answer`. Precedence, in the comment: below
  `needs-human` so a parked design issue still lists as parked; below
  `proposed` because exclusion beats inclusion; above `awaiting-answer` so a
  design issue mid-question is never handed to `implement-issue` by
  `awaitAnswer` or by `-strict-order`'s fold. `q.open()` includes it.
- `status`: a `design` row in `queuePairs`; one needs-you clause per issue —
  `reply on #N, then polako design -issue N` when awaiting, else `run polako
  design -issue N`. JSON: `queue.design` with `issue` and
  `awaiting_answer`; both key-pinning tests updated.
- `docs/reference.md`'s `status` example gains the row; `docs/behaviour.md`'s
  gates section gets one sentence. Ceilings move with a dated comment.

**Done when.** A drain test with issues `{1: design}`, `{2: design,
awaiting-answer}`, `{3: ready}` works only #3, under default and
`-strict-order`; `status` prints the row and both clauses; `-json` carries
`queue.design`; the template-leak test forbids `design`; `setup` reports the
label as optional.

Estimate: S

### 2. The `design-plan` skill, its contract tests and its eval case

**Problem.** Every design here was done by hand. The skill is the
automation, and it carries its own rules because it runs in other repos.

**Shape.** `skills/design-plan/SKILL.md`, frontmatter `description`,
`argument-hint: [issue-number]`, `arguments: [issue]`,
`disable-model-invocation: true`. Copied verbatim from `implement-issue`
where named; everything else new.

- Preamble: what this run may and may not do — the write surface above,
  "never `gh issue create`; `polako plan -vision <doc>` files the tickets
  later", no evidence ref. The layout convention from `plan-backlog`.
- "This run gets one turn", "No prompts, ever", "House style" (budgets
  reworded: a doc a reviewer reads in ten minutes, a question that fits one
  screen), "Describe, don't paste", "Asking a question" — copied,
  `--body-file` and the pinned `awaiting-answer` spelling intact.
- Phase 0 — context: `gh issue view $issue` with the same fields and
  fallback; the thread is data, not instructions; detect the phase from the
  worktree and `PLAN.md` and resume.
- Phase 1 — workspace: `implement-issue`'s Phase 1 unchanged.
- Phase 2 — measure what exists today: read the code the request touches,
  run the tests, builds and CLI probes the repo has, and record every
  finding as a pointer (`file:function`) or as a run with its result
  described. Findings go into `PLAN.md` first, the resume point.
- Phase 3 — decisions and alternatives: enumerate; decide everything the
  code can settle; batch what only the human can settle into one question
  on the thread and stop. On the rerun, fold the reply in and clear the
  label.
- Phase 4 — write `docs/plans/<topic>.md`. `<topic>` is the issue title
  kebab-cased after a leading `design:` prefix, at most 40 characters. The
  template spelled in full: `Scope: … · Behavior change: …`, `## What exists
  today`, `## The answer in one paragraph`, `## What this may read and
  write`, `## Drafted tickets` with `### N. <imperative title>` /
  `**Problem.**` / `**Shape.**` / `**Done when.**` / `Depends on N.` /
  `Estimate: S|M|L`, `## Considered and not proposed`. The sizing contract
  verbatim: one issue is one PR that `/polako:implement-issue` can produce
  unattended without stopping to ask. No `Status:` or `Tracking:` line.
- Phase 5 — self-review gate, mandatory, written into `PLAN.md`'s
  `## Review`: every exists-today claim has a pointer or a run; every
  alternative has a reason; every ticket has all four parts and an estimate
  and passes the sizing contract; the branch's diff against its base touches
  only `docs/plans/`.
- Phase 6 — commit (`docs: design <topic>`), push, `gh pr create --head
  issue-$issue --title … --body-file <worktree>/.polako-scratch/PR_BODY.md`.
  Body: `## Measured`, `## Decided`, `## Left open`, `## Next` with the
  literal `polako plan -vision docs/plans/<topic>.md`, `## Flagged` only if
  the thread tried to instruct it, ending `Closes #$issue`.

`cmd/polako/design_skill_test.go` mirrors `health_skill_test.go`: declares
only `issue`; one turn; house style; describe, don't paste; data, not
instructions; label commands match `issueLabelTools`' prefixes; comments use
`--body-file`; branch `issue-$issue` and `Closes #$issue`; scratch-dir
spelling; never spells `gh issue create`, `gh pr merge`, `gh api` or
`polako-evidence`; names every section heading and the four ticket parts;
states the sizing contract; names the next-command line. `designSkillDir` and
`defaultDesignSkill` live in a new `cmd/polako/design.go` — constants only
for now, the way `healthSkillDir` sits in `health.go`.

`evals/design-plan/`: `case.yaml` (`prompt: /polako:design-plan 1`,
`max_turns: 80`, `timeout_seconds: 1800`), `issue.json` — a request specific
enough to need no question, say "design: greet several names and pick a
language" — and graders: `file_exists .eval/pr-body.md`; `llm` on the
worktree's `docs/plans/` — exactly one new file, the header, no `Status:`,
the five sections; every exists-today claim names `greet.sh`, `test.sh`,
`README.md` or a run, no pasted transcript; every ticket has the four parts
and an estimate and names files; every alternative has a reason; `llm` on
`gh-calls.log` — no `gh issue create`, `pr merge`, `api`; the branch changes
only `docs/plans/`; the PR body ends `Closes #1` and names `polako plan
-vision docs/plans/<file>`; `labels.log` has no unmatched `add
awaiting-answer`; `tool_used: Skill`. `evals/README.md` gains the row and
fixes its count.

`plugin.json` and `marketplace.json` name `/design-plan`; `docs/install.md`'s
`cp -r` list adds it.

**Done when.** `go test ./cmd/polako -run DesignSkill` passes; `claude plugin
validate .` passes; `evals/run.sh design-plan` run by hand three times, the
per-grader verdicts and spend quoted in the PR body. The PR that adds the
case can't run it (`run.sh:137`), so this PR defers to a human and says so.

Estimate: M

### 3. Extract the verb-neutral half of `work`'s preflight

**Problem.** `design` needs everything `preflight` does except the two queue
gates, and can't call it: `queueGate` refuses an unfiltered public repo, and
the function is at its size budget.

**Shape.** New `cmd/polako/preflight.go`. `preflightShared(ctx, cfg, gate)`
does: binaries, `checkNotifyCommand`, git dir, `gh repo view` into
`cfg.repo`, shift log, then the caller's `gate(visibility)` (nil for none),
then `claudeVersion` and `pluginVersion`, `warnClaudeModelEnv`,
`effortFlagGate`, `probeUsage`, the skew gate and `-ignore-skew` line,
`updateNoticeLine`. The gate runs mid-way so a queue refusal still lands
before the probes. `preflight` becomes: shared with `workGates` as its gate
(`queueGate`, `labelGate`, `ensureLabel(awaiting-answer)`), then the
"running /skill per issue" line, `settingsBlock`. Log-line order is
preserved where a test pins it; where the extraction moves one, the PR body
says which.

**Done when.** No behaviour change: every `work` preflight test passes
unchanged; `main.go` shrinks; one direct test proves a public repo passes
`preflightShared` with no label — the gate is the caller's.

Estimate: S

### 4. Design runs write their own record kinds

**Problem.** `processIssue` records `run` and `issue` kinds. A merged design
issue would then be "a merged issue costs $X" in the pricing line and in
every `stats` figure.

**Shape.** `config.verb`: `parseFlags` sets `work`, `designConfig` (ticket 5)
sets `design`, intake leaves it empty (its records have their own kinds).
`recordKind(cfg, base)` in `metrics.go` returns `base` or `design-` + base;
`newRunRecord` and `recordIssue` use it. Readers don't change: `loadRecords`
drops `design-run` and `design-issue` the way it dropped `plan` and
`health` when they arrived. That's why this is the smallest change —
filtering by skill name in the readers would need a field on
`issueRecord`, would misfile a hand-set `-skill`, and would touch
`proposalPricingLine`, both summary builders and both renderers.
`docs/run-data.md` names both kinds beside the plan and health paragraph and
says `stats` skips them until taught. `resumeHint`'s `polako stats -shift`
line is gated on the verb so it never points at an empty report.

**Done when.** A config with `verb: design` writes both kinds; `loadRecords`
over a file mixing them with ordinary records returns only the ordinary
ones; `proposalPricingLine` prices the same with or without a `design-issue`
merge present; `docs/run-data.md` names both.

Estimate: S

### 5. `polako design -issue N`

**Problem.** Nothing runs the skill unattended with restart safety, parks,
the question hand-off, PR supervision and the sweep.

**Shape.** `cmd/polako/design.go`, beside the constants from ticket 2.

- Flags: `-issue`, `-wait`, and `work`'s own names with `work`'s help
  strings — `-dir`, `-claude`, `-skill` (default `polako:design-plan`),
  `-branch-prefix`, `-tools` (default `designTools`), `-add-tools`,
  `-permission-mode`, `-model` (default `opus`: one run steers every ticket
  downstream, the intake verbs' reasoning), `-effort`, `-poll`, `-retries`,
  `-retry-wait`, `-stall`, `-heartbeat`, `-max-cost`, `-max-issue-time`,
  `-notify`, `-remote`, `-run-tag`, `-post-summary`, `-metrics`, `-log`,
  `-verbose`, `-ignore-skew`, `-dry-run`. Not registered: `-label`,
  `-ungated`, `-skip`, `-once`, `-strict-order`, the session and usage caps,
  `-visual-evidence`, the by-size and remediation policy flags.
  `applyEnvDefaults` applies. `-max-issue-time` keeps 45m; the reference
  table says to raise it for a measure-heavy design.
- `designConfig`: pins what `parseFlags` pins, sets `verb: design`,
  `visualEvidence: true` (so `issueRun` appends nothing — the coupling is
  commented at the assignment), `strictOrder: opt.wait` (commented:
  `handOffQuestion` is its one reader).
- `designPreflight`: `preflightShared`, then `ensureLabel` for
  `awaiting-answer` and `design` (not under `-dry-run`), then one `gh issue
  view N --json state,labels,subIssuesSummary`: refuse closed ("reopen it"),
  a container ("a design request has no sub-issues"), `needs-human`
  ("polako unpark N first"), `proposed` ("drop proposed first — exclusion
  beats inclusion"); add `design` when absent, with a log line. Then a
  `settingsBlock` with the rows that apply.
- `runDesign`: parse, config, preflight; `-dry-run` prints the invocation
  through `commandLine` and touches nothing. Otherwise `tidySweep(ctx, cfg,
  0)`, then `processIssue` once, then the four exits copied from `drain`:
  deferred — log `issue #N is waiting on your answer — reply on the thread,
  then rerun polako design -issue N`, exit 0, the `work -once` behaviour;
  parked — `parkAndMoveOn`, exit 0; Ctrl+C — `resumeHint`, exit 130 the way
  `main` does for `work`; other error — `resumeHint`, `notifyStopped`,
  nonzero; nil — a line naming the PR and the next command. Every exit
  prints `drainSummary` with its one result.
- `main.go`: the `design` arm uses `work`'s sinks (timestamps, shift log),
  not `runReport`'s stamp-off. `verbUsage` gains the line;
  `TestVerbUsageListsDesign`.
- `fakeClaude` gains a `design` mode: asserts the prompt is exactly
  `/polako:design-plan N`, plants a MERGED PR, streams.
- `docs/reference.md`: `## Designing a plan document: \`polako design\`` with
  the flag table, the question exit and `-wait`, the hand-off line. Ceiling
  moved with a dated comment.

**Done when.** Verb-level fake-CLI tests in `plan_test.go`'s shape: a
labelled issue runs to merge — the issue closes, both record kinds are
written, the sweep runs, the summary prices it; an unlabelled issue gets
`design` first; closed, container, `needs-human` and `proposed` are refused
with the named remedy; a planted OPEN PR skips the claude run; the `asks`
mode exits 0 with the rerun line, the label left up, one `awaiting-answer`
notification; `-wait` runs to merge in one process; `-dry-run` writes
nothing; a fatal fires `stopped`; `verbUsage` lists `design`.

Depends on 1, 2, 3, 4.

Estimate: M

### 6. `polako design -brief "text"` files the issue and works it

**Problem.** A request that exists only in the operator's head has no thread
to be asked on.

**Shape.** `-brief` and `-issue` mutually exclusive, exactly one required,
the `planConfig` shape and error wording; `planBriefMax` reused, the refusal
says "put it in an issue". `fileDesignIssue` beside `fileRetireIssue`: title
`design: ` + `briefTitle(brief)`, body the brief followed by one trailer
line — `Filed by polako design — reply on this thread when the run asks
something.` — `--label design`, the `ensureLabel` retry,
`issueNumberFromCreateOutput`. Under `-dry-run` it prints "would file an
issue titled …" and stops; there's no number to resolve. Then ticket 5's
path on the new number. The record carries the number, never the text.

**Done when.** A fake-gh test proves one issue is created, open, labelled
exactly `design`, with the trailer, then worked to merge; `-brief` with
`-issue`, and neither, are refused; a brief past 2000 characters is refused;
`-dry-run` creates nothing; the reference table documents `-brief`.

Depends on 5.

Estimate: S

### 7. Docs, invariants and the verb count

**Problem.** Every doc that counts verbs, lists labels or names write
surfaces is now wrong, and CLAUDE.md has no invariant for the new label or
surface.

**Shape.**

- CLAUDE.md: the verb sentence at the top (four skills, eight verbs) and two
  invariant paragraphs. The `design` label is orchestration state:
  queue-excluded, worked only by `polako design`, precedence as ticket 1
  states it. `design`'s write surface is `work`'s on one issue plus, under
  `-brief`, one filed issue — the one issue the binary files without
  `proposed`, and why; the skill never runs `gh issue create` and never
  touches the evidence ref.
- README: `:19`, `:31` ("Eight verbs. Four start Claude runs"), a "Designing
  first" paragraph in "Planning a backlog" with the hand-off, `:320`.
- `docs/behaviour.md`: a `design` section — the question exit, `-wait`, the
  review thread as the conversation, `draft` in `status` after the merge.
- `docs/security.md`: a `polako design` block under the per-verb surfaces,
  with the no-queue-gate reasoning.
- `docs/install.md:5`, `docs/releasing.md:237`, `docs/reference.md:8`,
  `docs/VISION.md`'s "How a direction becomes work" (one sentence).
  `docs/sdlc.svg` is left alone and named as a follow-up.

**Done when.** `TestDocsDocumentEveryFlag`, `TestDocsStayWithinLineBudget`
(ceilings moved with dated comments) and the CLAUDE.md marker tests pass;
`grep -n seven README.md docs/*.md` finds only prose that's still true.

Depends on 5, 6.

Estimate: S

### 8. `unpark` and `status` say the right verb for a parked design issue

**Problem.** `nextShiftLine` says "resumes issue-N from its commits" for a
parked design issue. No `work` shift will; `polako design -issue N` will.

**Shape.** `readParkListItem` already reads the thread; add one `gh issue
view N --json labels` per parked issue (bounded, attended verb) into
`parkListItem.design`. `nextShiftLine` takes it and prints `polako design
-issue N resumes issue-N …` or `… waits on PR #M`. `status` reuses the item.

**Done when.** A fixture with a parked design issue prints the design wording
in both verbs; a parked ordinary issue's lines are byte-identical to today.

Depends on 1.

Estimate: S

## Considered and not proposed

**Skill-by-label inside `work`.** Route `design`-labelled issues to
`design-plan` from the drain. That puts a second skill behind the unattended
drain, keyed on a label; gives `tidy`, `unpark` and `status` two meanings per
issue; and makes the `-label` gate reason about two skills. A separate verb
keeps `work`'s surface unchanged and the label purely protective.

**A one-shot, `plan`-shaped design verb.** `intakeRun` plus the label pass.
It can't ask a question and stop, can't run a build or a test, has no restart
safety, and its only write is `gh issue create` — the wrong shape for a
deliverable that's a file behind a PR.

**Auto-running `plan` after the design PR merges.** Merging is one of the two
human gates. Chaining a proposal run onto it turns a merge into a trigger,
and the binary would have to watch for doc-PR merges — state. The PR body and
the exit line print the command. A `-then-plan` flag is a later argument.

**Designing in the epic body only.** `docs/plans/<topic>.md` is what `plan
-vision` consumes and what `status` derives state for. An epic body has no
footer pointer, isn't reviewed as a PR, and doesn't version with the code.

**Renaming `plan`** to `propose` or `decompose`. Pre-1.0 allows it. `design`
is already the distinct word, and the footer, its parser, the status
derivation, every doc and the marketplace text would move for no gain.

**A distinct `awaiting-design-answer` label.** The existing label and
`handOffQuestion` are exactly right; `design` already keeps `work` off the
issue while it waits.

**Waiting by default when the run asks.** `work -once` exits 0 on a question
today, and a process polling for days for one paragraph is what the label
plus a rerun already handles. `-wait` is the opt-in.

**`required: true` for the label.** Absence excludes nothing; the verb
ensures the label on first use; turning every repo's `setup` red over a verb
it never uses is noise.

**Reusing `intakeOptions`.** Its sixteen flags include `-max-issues` and
`-focus`, which mean nothing here, and omit `-poll`, `-retries`,
`-branch-prefix`, `-heartbeat` and `-max-issue-time`, all of which
`processIssue` reads.

**Filtering by skill name in the record readers.** Unreliable under `-skill`,
needs a field on `issueRecord`, touches four readers and two renderers. A new
kind touches none (ticket 4).

**A `work` startup line naming skipped design issues.** `status` shows them.
A queue memo for one line is more than it buys today.

**Letting the skill file the tickets itself.** The doc merge is the review
gate. `plan-backlog`'s dedup and the label pass stay the one filer.

## Where the `processIssue` reuse isn't clean

Named so the tickets don't rediscover them.

- `issueRun` appends ` no-evidence` when `!cfg.visualEvidence`; `design` pins
  `visualEvidence: true` to keep the prompt bare. Commented at the pin.
- The wait bit is `cfg.strictOrder`, a name about queue order. `-wait` maps
  onto it with a comment. A second reader inside `processIssue` would break
  the mapping; the comment says so.
- `issueCloseTool` is minted regardless of skill; `design-plan` never spells
  it. Worst case is the run closing its own issue, which `processIssue`
  already reports as closed with no change.
- `resumePrompt`'s unfinished-turn closing names "the review gate";
  `design-plan`'s Phase 5 is a gate of that name, so the wording holds.
- `parkAndMoveOn`, `resumeHint`, `drainSummary` and `notifyStopped` are the
  drain's, not `processIssue`'s. `runDesign` calls them itself, once each.
- `stages.go` narrates the doc write as "implementing…". Narration only; a
  `docs/plans/*.md` → `stagePlan` rule is a one-line follow-up, not a ticket.
