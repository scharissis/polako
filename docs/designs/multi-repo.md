# Multi-repo: one shift over several checkouts

Scope: `work`'s `-dir` and `-skip` flags, preflight, the drain runner, the
`ui` sink, the recorder's warn flag, the session-cost gate, `status -repo`,
`tidy -dir`, one CLAUDE.md invariant · Behavior change: `work` takes several
`-dir`s and drains them in one shift, up to one claude per repository at
once; `status -repo` and `tidy -dir` repeat; a single-`-dir` shift is
unchanged byte for byte

polako works one repository per process. An operator with several repos
runs several `polako work`s: one terminal each, one exit summary each, one
settings block each, each probing the same account's usage pool, and no one
place that says what needs them. That already works. It's just all on the
operator.

Most of the machinery is per repo already. Every child process runs in its
own `-dir`, the run data is one file per repository, the shift log is named
after the repo, the notify hook and the Remote Control session name carry
the repo, and `status -repo` reads any repo from any machine. What is not
per repo is the process: one drain loop, one terminal, one preflight, one
exit.

This design makes `-dir` repeatable. One shift preflights each checkout,
then runs today's drain loop once per repo, in parallel, and reports once.
A repo waiting on a merge or an answer holds nothing else up, because the
others keep draining. `status -repo` and `tidy -dir` repeat the same way.
No new app, no config file, no UI, nothing new leaves the machine.

## What exists today

Measured on this worktree at `005d5ff`, 2026-09-23.

- **N processes already work, and that's the ceiling of doing nothing.**
  Every gh, git and claude child gets `cmd.Dir = cfg.dir`
  (`plumbing.go:capture`, `claude.go:startClaude`); nothing calls
  `os.Chdir`. The recorder appends to `~/.polako/metrics/<owner>--<repo>.jsonl`
  with `O_APPEND` and no lock, "because concurrent supervisors interleave
  whole lines" (`metrics.go:recorder`). The shift log is
  `<owner>--<repo>--<shift>.log` (`ui.go:openShiftLog`). `-notify` sets
  `POLAKO_NOTIFY_REPO` (`notify.go`), and `-remote` names a session
  `polako <repo>#<issue>` (`claude.go:remoteName`).
- **The drain blocks on humans.** `supervisePR` (`pr.go`) polls every
  `-poll` until the PR leaves OPEN, and nothing else in the process moves
  meanwhile. `awaitAnswer` (`drain.go`) sleeps only when nothing else is
  ready. Across repos that idle time is free work: repo B can run while
  repo A's PR waits for a merge, and branches in different repositories
  cannot conflict.
- **`config` is nearly per repo.** It is passed by value with four shared
  pointers: `rec`, `queue`, `usage` and `ui` (`flags.go:config`). `cfg.ui`
  is a seam already: nil means the process-wide `sinks` (`ui.go`). Per-run
  copies are made in `issueRun` (`claude.go`), `runChoice.apply`
  (`policy.go`) and `runRemediation` (`pr.go`).
- **What is genuinely shared.** `ui.emit` writes a line's stamp and its
  text as two `Write`s, so two uis on one stderr would tear lines.
  `recorder.warned` is a plain bool behind the one shared `rec`
  (`metrics.go:warn`). `costGate` (`drain.go`) sums one `shift`'s results.
  `usageGateReason` (`budget.go`) probes fresh every pass and says why a
  cached snapshot is wrong. The shift id is one per process
  (`metrics.go:newShiftID`), with a comment saying a shift therefore
  belongs to one repo.
- **Preflight interleaves once-per-process and per-repo checks.**
  `preflightShared` (`preflight.go`) runs the binaries, the notify check,
  `claudeVersion`, the model-env warning, the effort gate, the usage probe
  and the update notice — all process-wide — between `git rev-parse`,
  `gh repo view`, the shift log, `workGates`, `pluginVersion` and the skew
  gate, which are all about one checkout. `pluginVersion` runs
  `claude plugin list` in `-dir`, and a project-scoped install can differ
  per repo.
- **Every fatal ends the process.** `handleOutcome` and `pick` return the
  error, `drain` returns it, `main` exits (`drain.go`, `main.go`). Some of
  those are about one repo — an origin that won't fetch, gh reads that
  exhausted their retries. `errAuth` and Ctrl+C are about the account or
  the operator.
- **The read verbs take one repo per call.** `status -repo` needs no
  checkout (`plumbing.go:resolveRepoConfig`, `ghArgs`). `stats` already
  spans every repository: `-repo` defaults to all of them, the text report
  prints a `repos` line when records span more than one, `-by issue` rows
  read `owner/name#N`, and `-shift last` is documented as the newest shift
  across every repository (`statsrecords.go`, `statuslastshift.go`).
  `tidy` is per `-dir` (`tidy.go`).
- **One doc already claims the wrong thing.** `docs/behaviour.md`, under
  the questions-and-PRs summary, says an unanswered question and an
  unmerged PR "both wait out of the queue rather than in it … so neither
  holds anything else up". True for questions, not for PRs.
- **Budgets that bind.** `drain.go` is 963 of the 1000-line file budget
  and `parseFlags` is near the 130-line function budget
  (`sizebudget_test.go`), so new code goes in new files. `docs/reference.md`
  is at 583 of 584 lines and `docs/behaviour.md` at 531 of 531
  (`docsbudget_test.go`), so the docs ticket cuts before it adds.
- **The test rig can already stand up two repos.** The fake gh keys on the
  `POLAKO_FAKE_GH` state file in `cfg.env`, never on cwd
  (`main_test.go`, `drain_test.go:drainConfig`); two repos are two states,
  two dirs, two envs. Drain tests call `drain` directly with `repo` set
  and never run preflight.

## The answer in one paragraph

`polako work -dir A -dir B -label ready` runs the process-wide preflight
once and the per-repo preflight once per `-dir`, refusing to start if any
repo fails or if two dirs resolve to the same repository. It builds one
`config` per repo — own `repo`, `dir`, shift log, a `ui` that prefixes
every line with `[owner/name]`, own `queueMemo`, own skip set — all under
the shift's one id, and runs today's `drain` once per config, each in its
own goroutine, each free to run its own claude whenever it has an issue
ready. Shared: the signal context, a locked terminal writer, and one
shift-wide spend counter for `-max-session-cost`. A fatal about one repo
ends that repo's drain and is named in a roll-up line; the others carry
on. `errAuth` and Ctrl+C end everything, and the exit code stays 130 only
for the operator's own signal. Every other flag applies to every repo;
`-skip` with several dirs takes `owner/repo#N`; `-once` means one issue
per repo. `status -repo a -repo b` prints one section per repo, the
account usage line once, and one combined `needs you`. `tidy -dir` loops.
One `-dir` behaves exactly as today.

## What this may read and write

- **The checkouts.** Each `-dir` is fetched, fast-forwarded `--ff-only`
  and swept exactly as today, by its own drain. Two dirs that resolve to
  the same `nameWithOwner` are refused at preflight: that would be two
  drains on one repo, the thing the invariant below forbids.
- **GitHub.** The same reads and writes as today, per repo, now from one
  process. No cross-repo write exists. *Nothing merges itself* is
  untouched.
- **`~/.polako`.** One shift id for the process. N shift logs,
  `<owner>--<repo>--<shift>.log`, one per repo as now. N record files, as
  now. No new reader: `status` and `stats` read what they read today, and
  `stats -shift <id>` simply shows records from more than one file. The
  comment saying a shift belongs to one repo goes. The write-only rule is
  untouched.
- **The terminal.** Every line carries a `[owner/name]` prefix when more
  than one `-dir` is given, and none otherwise.
- **The invariant.** *One issue in flight at a time* becomes *one issue in
  flight per repository*. Every run still branches from a default branch
  that already contains the previous merge — that guarantee is a property
  of one repository's branches, so a shift over several `-dir`s keeps it in
  each, running up to one claude per repository at once. The sentence
  saying the guarantee is "per run, not per repo" becomes "per shift and
  per repository". The PR that changes CLAUDE.md says so in its body.
- **Spend and usage.** `-max-session-cost` is checked between issues, per
  repo, so a shift can overshoot it by up to one issue per repository.
  N repos draw on the account's usage pool N times as fast. The usage gate
  and the mid-run limit wait apply per repo exactly as today; the pool is
  account-wide, so when it trips every repo waits. Both facts go in the
  docs, not behind a knob.
- **What leaves the machine.** Unchanged. `-remote` registers each run as
  it does now, once per run; `-post-summary` stays default off.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.
Independent tickets first; the drain last. Ticket 3 is the one an operator
feels first and depends on nothing, so it can land first.

### 1. Make the recorder's warn-once flag atomic

**Problem.** `recorder.warned` is a plain bool behind the one `rec`
pointer every config shares. Two drains hitting a full disk in the same
instant race on it.

**Shape.** `metrics.go`: `warned` becomes an `atomic.Bool`; `warn` uses
`CompareAndSwap` so exactly one warning prints. No callers change.

**Done when.** A test that calls `warn` from two goroutines passes under
`-race` and sees one warning; the existing warn-once test is unchanged.

Estimate: S

### 2. Give a `ui` a prefix and a shareable terminal

**Problem.** `ui.emit` writes a line's stamp and text as two `Write`s,
so two uis on one stderr tear lines. Nothing marks which repo a line is
about.

**Shape.** `ui.go`: `emit` renders stamp and text into one `Write`; a
`prefix` field applied to terminal and file lines; a `lockedWriter{mu,
w}`; `newRepoUI(base *ui, prefix string) *ui` that copies `stamp`,
`style` and `verbose` from the base and wraps `base.terminal` (and
`base.file` when set, so a test's shared buffer works). `openShiftLog`
stays per ui. `ui_test.go` covers all three.

**Done when.** With an empty prefix, output is byte-identical to today
(`ui_test.go`). Two uis writing a thousand lines each into one buffer show
no torn line under `-race`. The prefix appears on terminal and file lines.

Estimate: M

### 3. Make `status -repo` repeatable

**Problem.** An operator with several repos wants one `needs you` across
all of them, from any machine, and has to run `status` once per repo.

**Shape.** `status.go`: `-repo` becomes a repeatable `flag.Value`;
`statusConfig`, `resolveStatusScope` and `readStatus` loop per repo; the
account usage probe moves out of `readStatus` so it runs once;
`renderStatus` gets a multi-repo wrapper — header per repo, the `plan:`
line once, `needs you` parts concatenated with repo prefixes; `-json`
emits an array of documents when more than one repo is given and the
byte-identical single document otherwise. `-dir` with several `-repo`s
is an error. `status_test.go` gains two-repo cases from two
`statusConfigFor` states. `docs/reference.md`: the `-repo` row and the
JSON section.

**Done when.** `polako status -repo a -repo b` prints two sections and
one `needs you` line naming both repos; `-json` prints an array; a single
`-repo` prints what it prints today, byte for byte.

Estimate: M

### 4. Make `tidy -dir` repeatable

**Problem.** Same shape as ticket 3 for the sweep: one `tidy` per
checkout.

**Shape.** `tidy.go`: `-dir` repeatable; `tidyConfig`, `reclaim` and
`renderTidy` loop per dir (the header already prints `cfg.repo`); `-repo`
with more than one `-dir` is an error. `tidy_test.go` gains a case with
two checkouts via `tidyCfg`. `docs/reference.md`: the `-dir` row.

**Done when.** `polako tidy -dir a -dir b` prints one report per checkout;
`-apply` reclaims in both.

Estimate: S

### 5. Parse repeatable `-dir` and qualified `-skip`

**Problem.** Flags only, no behaviour change. `-dir` is one string,
`-skip` is bare issue numbers, and with two repos a bare number is
ambiguous.

**Shape.** `flags.go`: `config.dirs []string` with `dir` staying
`dirs[0]`; a `dirList` value whose entry from `POLAKO_DIR` is dropped on
the first argv `Set` (`applyEnvDefaults` and `Parse` both call `Set`);
`skipByRepo map[string]map[int]bool` parsed from `owner/repo#N` in a new
`dirs.go`, with unqualified numbers fatal when more than one dir is
given. Widen the `declaredFlags` regexp in `notify_test.go` so a
`flag.Var` still counts for `TestDocsDocumentEveryFlag`. `flags_test.go`:
env-then-argv precedence (a serial test, since it sets the environment),
qualified parsing, `-h` rendering.

**Done when.** `-dir a -dir b -skip owner/a#3` parses into two dirs and
one per-repo skip; `-dir a -dir b -skip 3` refuses with a line saying how
to qualify it; one `-dir` parses as today.

Estimate: S

### 6. Share `-max-session-cost` across configs

**Problem.** `costGate` sums one repo's results, so with two repos the
cap is per repo, which is not what the flag says.

**Shape.** `budget.go`: a nil-safe `shiftSpend{mu, total, said
sync.Once}` on config, fed beside the two `tally.add` sites
(`attempt.go`, `pr.go`) and read by `costGate` when set, with the wording
"this shift has spent … across N repositories" and one `stopped`
notification for the shift. Nil keeps today's per-shift sum.

**Done when.** Two repo drains sharing one counter both stop cleanly
between issues once the total crosses the cap, and the notify hook fires
once (`drain_test.go` or a new `budget_test.go` case).

Estimate: S

### 7. Split preflight into once-per-shift and per-repo halves

**Problem.** `preflightShared` interleaves checks about the process with
checks about one checkout, and `preflightPairs` prints them as one
block. With N dirs the process-wide rows would repeat N times and a
per-repo failure would read as a process failure.

**Shape.** `preflight.go`: `preflightGlobal` (binaries, notify check,
`claudeVersion`, model-env warning, effort gate, usage probe, update
notice) and `preflightRepo` (git dir, `gh repo view`, shift log,
`workGates`, `pluginVersion`, skew gate); `preflight` keeps its signature
by calling both, so nothing else changes. `preflightPairs` splits into
shared rows printed once and repo rows printed per repo. A new
`preflightAll(ctx, base config) ([]config, error)` builds one config per
dir — same shift id, own `new(queueMemo)`, `skipByRepo[repo]`, a
`newRepoUI` with its own shift log — refuses two dirs that resolve to one
repository, and joins every repo's error so the operator sees them all at
once. `gate_test.go`: existing cases untouched; new two-repo cases for the
refusals.

**Done when.** A single `-dir` prints the same settings block as today.
Two dirs print the shared rows once and the repo rows twice. A failing
second dir, or a duplicate, stops the shift before any drain starts, with
both repos' problems in one message.

Depends on 2, 5.

Estimate: M

### 8. Run one drain per `-dir`

**Problem.** The drain is one loop in one goroutine, and every error it
returns ends the process.

**Shape.** New `fleet.go`: `runFleet(ctx, cfgs []config) error` derives
`context.WithCancelCause`, starts one goroutine per config calling
`drain` unchanged, and applies the error policy — `errAuth` cancels the
siblings with itself as cause; any other error is that repo's and is
collected; the first fatal is returned; `context.Canceled` is returned
only when the cause is the operator's signal, so `main` still exits 130
for Ctrl+C alone. A roll-up line through the base ui names each repo's
ending. `run()` in `main.go` dispatches to it when `len(dirs) > 1`;
`-dry-run` loops sequentially. `main_test.go` gains a fake-claude
`rendezvous` mode: each fake claude drops a marker file named by a
per-test env var, then waits with a timeout for the other repo's marker
before exiting, so "both repos ran at once" is asserted rather than
timed.

Split as two PRs: **8a** the runner, the error policy and exit codes,
with the two-repo rendezvous test and a test that both backlogs report
merged; **8b** the roll-up summary, `-once` per repo, the dry-run loop,
and the two failure tests — origin unfetchable in B while A merges, and
A's `errAuth` ending B — in `fleet_test.go`.

**Done when.** `polako work -dir a -dir b` on two fake repos merges an
issue in each with overlapping run times, prints prefixed lines, and ends
with one roll-up. A per-repo fatal leaves the other repo's merge in the
summary. Ctrl+C exits 130; an auth failure exits 1.

Depends on 1, 2, 5, 6, 7.

Estimate: L

### 9. Document multi-repo work

**Problem.** Every flag is documented and a test enforces it; the new
behaviour needs its rows, and two invariants' wording changes.

**Shape.** `docs/reference.md`: `-dir` repeatable, `-skip owner/repo#N`,
`-once` per repo, `-max-session-cost` across repos with the one-issue-per-
repo overshoot, `status -repo` and `tidy -dir` rows, plus a short
"Working several repositories" subsection saying N repos draw on the
usage pool N times as fast. `docs/behaviour.md`: the PR wait blocks one
repo's drain, not the shift; the usage gate is account-wide so every repo
waits — and fix the summary line that already claims an unmerged PR holds
nothing else up. `CLAUDE.md`: the invariant wording above. `README.md`:
one clause. Both docs are at their line budget, so cut or move `docsDebt`
entries with the customary dated comment.

**Done when.** `TestDocsDocumentEveryFlag` and `TestDocsStayWithinLineBudget`
pass; the CLAUDE.md diff is quoted in the PR body.

Depends on 8.

Estimate: S

## Considered and not proposed

**A new app, or a fleet supervisor binary.** A second artefact to version
against "two halves ship from one tagged commit". It would need a repo
list — a config file, turned down three times — and the no-UI decision of
2026-09-23 already says several repos means a multi-repo `status`.

**A child process per repo under a parent verb.** Keeps N usage probes,
N summaries and N settings blocks, has to merge N stderr streams without
`ui`, and adds a layer to the kill path `shutdownSignals` exists for
(`main.go`). Every cost N terminals have, minus the terminals.

**A non-blocking scheduler rewrite of the drain.** Four blocking sleeps
across `pr.go`, `drain.go` and `attempt.go`, pinned by six thousand lines
of `drain_test.go`. Three large tickets for nothing the operator sees.

**One claude run at a time per shift.** A slot in `execClaude` would keep
spend and usage draw as predictable as a single-repo shift, at the cost
of a FIFO queue, a "waiting for the slot" line, and a fix so the wait is
not charged to `-max-issue-time`. Turned down by the operator on
2026-09-23: the no-conflict guarantee is per repository, and throttling is
what fewer `-dir`s are for.

**Priority order across repos.** Nothing to order: every repo drains
independently, and priority within a repo stays the issue number.

**Per-repo `-label` or `-model` in one invocation.** Pairing flags
positionally is un-Go-like. The label is spelled the same everywhere by
design (VISION), `POLAKO_LABEL` is already machine-wide, and a per-repo
model lives in that repo's `.claude/settings.json`. An operator who needs
different values runs two shifts, as today.

**A shared usage snapshot.** `usageGateReason` already argues a cached
reading goes stale exactly when it matters, and the probe is cheap.

**A lock or a pickup wait across shifts.** Rejected on 2026-09-22 for two
shifts on one repo; two shifts on disjoint repos have nothing to lock.

**GitHub Actions or a cloud routine per repo.** Moves the model auth, the
transcript and the `-remote` bridge off the operator's machine: a new
destination the invariants say to argue for out loud, and not needed.

## Following progress across repositories

No UI. GitHub is the UI, a UI would copy it or hold state, and the one
case that reopened the question — several repos — was answered on
2026-09-23 as a `status` that takes several repos. How N repos get
followed under this design:

- **What needs you, everywhere.** `polako status -repo a -repo b`: one
  combined `needs you` line, then a section per repo, from any machine,
  no checkout. `watch -n 60` on it is the live view; `-json` returns an
  array for a script.
- **Being told.** One `-notify` hook covers every repo; each event carries
  `POLAKO_NOTIFY_REPO`, so one script routes parked, awaiting-answer,
  stuck, cleared and epic-done per repo.
- **Watching a run live.** `-remote` registers each run in claude.ai/code
  as `polako owner/repo#N`; the session list there is the "what is running
  right now, where" view, with no liveness state kept on disk.
- **The shift.** One terminal with `[owner/repo]` prefixes, one shift log
  per repo, and `stats -shift <id>` shows the whole multi-repo shift while
  it runs.
- **GitHub itself.** An owner-wide search — `user:<owner> is:open
  label:needs-human`, or `is:pr is:open head:issue-` — is the cross-repo
  view GitHub already has. `status` is the polako-aware version of it.

If living with this shows a gap, the same rule applies: extend `status`,
`stats` or `-notify`, don't add a surface.
