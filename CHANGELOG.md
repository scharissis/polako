# Changelog

One section per release, newest first. Since 0.13.0 a section is the release
notes GitHub generates when the release PR opens: every merged PR as a linked
bullet with its author and the issues it closed, capped by a compare link — so
"was #166 in this release?" is answered by searching this file. The versioning
rules — and why minor is the breaking axis before 1.0 — are in
[Publishing and versioning](docs/releasing.md).

Entries carry an **Operator impact** line when a release changes what an
unattended run does, rather than only what the code looks like. That line is
still written by a person, in the release PR, because no PR list can say what
a change means for a machine working a backlog overnight — those lines are the
ones worth reading before upgrading one.

## [0.19.0]

### What's Changed
* test: a line budget for the docs, so terser stays terser by @scharissis in https://github.com/scharissis/polako/pull/297 (closes #282)
* docs: cut the README from 445 to 312 lines by @scharissis in https://github.com/scharissis/polako/pull/298 (closes #283)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.18.0...v0.19.0

## [0.18.0]

### What's Changed
* feat: refuse to start when the installed skill is behind the binary by @scharissis in https://github.com/scharissis/polako/pull/276 (closes #254)
* feat: a fourth ending — close an issue that needs no code change by @scharissis in https://github.com/scharissis/polako/pull/277 (closes #210)
* fix: name Read as the way to test PLAN.md existence in implement-issue by @scharissis in https://github.com/scharissis/polako/pull/278 (closes #275)
* docs: write the plain-English house-style rule down in CLAUDE.md by @scharissis in https://github.com/scharissis/polako/pull/279 (closes #271)
* feat: implement-issue writes a PR body and question a human can read in a minute by @scharissis in https://github.com/scharissis/polako/pull/280 (closes #272)
* feat: house style and body-length budgets for plan-backlog and review-health by @scharissis in https://github.com/scharissis/polako/pull/290 (closes #273)
* docs: rewrite CLAUDE.md invariants and conventions in house style by @scharissis in https://github.com/scharissis/polako/pull/294 (closes #274)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.17.0...v0.18.0

## [0.17.0]

### What's Changed
* feat: scripts/health.sh — measure the shape of the codebase before gating it by @scharissis in https://github.com/scharissis/polako/pull/253 (closes #148)
* test: size budgets that fail CI, with an allowlist that only shrinks by @scharissis in https://github.com/scharissis/polako/pull/259 (closes #149)
* refactor: split main.go along the seams the test files already name by @scharissis in https://github.com/scharissis/polako/pull/260 (closes #150)
* refactor: extract processIssue's phases into named helpers by @scharissis in https://github.com/scharissis/polako/pull/261 (closes #151)
* docs: comments have an expiry — the convention, and a pass over the densest regions by @scharissis in https://github.com/scharissis/polako/pull/262 (closes #152)
* feat: review-health — a skill that audits any repo and files proposed issues by @scharissis in https://github.com/scharissis/polako/pull/263 (closes #153)
* feat: accretion check at the implement-issue review gate by @scharissis in https://github.com/scharissis/polako/pull/264 (closes #154)
* feat: the drain reclaims as it goes — the sweep at shift start and after each merge by @scharissis in https://github.com/scharissis/polako/pull/265 (closes #162)
* feat: worktrees live in the checkout, at .worktrees/issue-N by @scharissis in https://github.com/scharissis/polako/pull/266 (closes #163)
* feat: polako health — the periodic remediation pass, on any repo it works by @scharissis in https://github.com/scharissis/polako/pull/267 (closes #164)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.16.0...v0.17.0

## [0.16.0]

### What's Changed
* fix: worktree cleanup resolves the path instead of guessing it by @scharissis in https://github.com/scharissis/polako/pull/243 (closes #159)
* docs: investigate suspected token overspending, draft the tickets by @scharissis in https://github.com/scharissis/polako/pull/246 (closes #239)
* feat: wait out the usage gate instead of stopping the shift by @scharissis in https://github.com/scharissis/polako/pull/247 (closes #245)
* fix: name the skill and args in a Skill tool-detail line by @scharissis in https://github.com/scharissis/polako/pull/248 (closes #213)
* feat: stage narration — a run says which phase it is in by @scharissis in https://github.com/scharissis/polako/pull/249 (closes #214)
* feat: the heartbeat — a quiet "still working" line so a long phase is not silence by @scharissis in https://github.com/scharissis/polako/pull/250 (closes #215)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.15.0...v0.16.0

## [0.15.0]

### What's Changed
* fix: the review gate names the worktree, not just the branch by @scharissis in https://github.com/scharissis/polako/pull/231 (closes #219)
* fix: narrate one session-started/finished per run, not per dequeued prompt by @scharissis in https://github.com/scharissis/polako/pull/232 (closes #224)
* chore: SHA-pin the actions, add Dependabot, and turn on the free GitHub security checks by @scharissis in https://github.com/scharissis/polako/pull/233
* fix: scale the review gate's level to the diff size by @scharissis in https://github.com/scharissis/polako/pull/234 (closes #225)
* fix: sum the per-turn fields across a run's result events by @scharissis in https://github.com/scharissis/polako/pull/235 (closes #227)
* fix: name the resumable session when a limit wait is interrupted by @scharissis in https://github.com/scharissis/polako/pull/236 (closes #218)
* feat: polako plan skeleton — dispatch, flags, preflight, probes, -dry-run by @scharissis in https://github.com/scharissis/polako/pull/237 (closes #102)
* feat: polako plan runs the skill, with the label pass and the issue cap by @scharissis in https://github.com/scharissis/polako/pull/238 (closes #103)
* feat: the plan record and the proposed notify event by @scharissis in https://github.com/scharissis/polako/pull/240 (closes #104)
* feat: the pricing line — history prices the plan report by @scharissis in https://github.com/scharissis/polako/pull/241 (closes #105)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.14.0...v0.15.0

## [0.14.0]

### What's Changed
* fix: latch permissionRefused on a refused tool_result, not just prose by @scharissis in https://github.com/scharissis/polako/pull/223 (closes #209)
* feat: give the review gate a resume point by @scharissis in https://github.com/scharissis/polako/pull/226 (closes #216)
* fix: give the one-turn wait a polling floor by @scharissis in https://github.com/scharissis/polako/pull/228 (closes #217)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.13.0...v0.14.0

## [0.13.0]

### What's Changed
* fix: plan-backlog declares the dependency order it works out by @scharissis in https://github.com/scharissis/polako/pull/206 (closes #178)
* fix: implement-issue stops on an open blocker before creating anything by @scharissis in https://github.com/scharissis/polako/pull/207 (closes #180)
* feat: the usage probe — the plan's own limits, read free and fail-soft by @scharissis in https://github.com/scharissis/polako/pull/208 (closes #138)
* feat: the usage gate — stop a shift before it eats the week by @scharissis in https://github.com/scharissis/polako/pull/211 (closes #139)
* feat: stats windows, and what an issue costs the plan by @scharissis in https://github.com/scharissis/polako/pull/220 (closes #140)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.12.3...v0.13.0

## [0.12.3]

### What's Changed
* fix: eval stand-in gh no longer crashes on 'issue view' without issue.json by @scharissis in https://github.com/scharissis/polako/pull/201 (closes #130)
* fix: check.sh gofmt-checks every nested worktree, and CI cannot see it by @scharissis in https://github.com/scharissis/polako/pull/202 (closes #160)
* feat: polako tidy — reclaim the worktrees and branches of finished issues by @scharissis in https://github.com/scharissis/polako/pull/203 (closes #161)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.12.2...v0.12.3

## [0.12.2]

### What's Changed
* fix: the exit summary names the epics this shift finished by @scharissis in https://github.com/scharissis/polako/pull/188 (closes #169)
* fix: a finished epic gets one comment on its own thread, once by @scharissis in https://github.com/scharissis/polako/pull/189 (closes #170)
* fix: notify fires when an epic's last child closes by @scharissis in https://github.com/scharissis/polako/pull/191 (closes #171)
* chore: resumed_from is the resume target, not a session chain by @scharissis in https://github.com/scharissis/polako/pull/192 (closes #172)
* test: fail when a shipping fix sits unreleased for a day by @scharissis in https://github.com/scharissis/polako/pull/193 (closes #181)
* chore(docs): updated demo by @scharissis in https://github.com/scharissis/polako/pull/194
* fix: name the tool-grant remedy when a mid-run permission ask parks an issue by @scharissis in https://github.com/scharissis/polako/pull/195 (closes #182)
* fix: name the installed plugin id in the version-skew remedy by @scharissis in https://github.com/scharissis/polako/pull/196 (closes #190)
* feat: polako closes a finished epic itself, and says so on the thread by @scharissis in https://github.com/scharissis/polako/pull/198 (closes #197)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.12.1...v0.12.2

## [0.12.1]

### What's Changed
* feat: release notes become GitHub's linked notes, annotated with closed issues by @scharissis in https://github.com/scharissis/polako/pull/184 (closes #137, #166)
* fix: queue blockedBy so an unmerged prerequisite is never invisible by @scharissis in https://github.com/scharissis/polako/pull/185 (closes #179)


**Full Changelog**: https://github.com/scharissis/polako/compare/v0.12.0...v0.12.1

## [0.12.0]

### Added

- PR bodies show the change, not just describe it

### Fixed

- code review -- add finished to the JSON rollup, correct a stale doc comment
- read the sub-issue rollup's completed count in status
- code review -- everProgressed missed the clean-exit resume arm
- progressed() required an assistant event, not evidence of work
- code review — word-boundary matching, drop dead field, dedupe park tail
- a park no longer throws away the run's own account of why it stopped
- second review pass — anchor the gate's git log, fix wording gaps
- code review — gh pr create needs --head, and other cwd assumptions
- Phase 1 never says cd, and the escape hatch reaches it
- code review — resolve Evidence's mermaid contradiction, loosen the grader, sync the PR template
- name the branch-from-origin shortcut as not a substitute for the ff-only refresh
- code review — scope the gh-surface claim to today, lock it with a test
- plan-backlog closes its gh surface, so a run can't improvise gh label list

### Changed

- cover a resume crash-loop that never does real work
- document the permission_refused park category
- cover the permission-refusal park path and the retired sentence

## [0.11.0]

### Added

- stats -json — a typed summary both renderers read
- status -json — the snapshot as one machine-readable document
- a settings block instead of a startup essay
- timestamps everywhere, worn quietly — a dim time-only TTY gutter
- run the suite by hand, without the gated plugin eval command
- declare severity at every narration call site the old table matched
- typed severity in the narration styler
- one presentation layer — a report renderer status, stats and work share

### Fixed

- code review — pointer ReviewsMedian, shared span/token-split math, dead field, HTML cards from summary
- code review — dedupe nonNilSlice, cover the undetailed-PR cap in JSON, actionable encode error
- code review — honest preflightPairs doc comment, gating regression test
- code review — stampKind enum, shared dim(), honest -log-off doc
- code review — narrate() sink parity, section rationale, index coupling
- code review — narration colour, attention markers, naming collision
- gitignore the skill's PLAN.md and PR_BODY.md scratch files

### Changed

- correct the eval invocation against the CLI's own help
- bound the fake-CLI warm-up exec
- warm the fake CLI so a timed test does not race its first exec
- name the macOS trust store and the auth hosts the allowlist needs
- pin the environment a dispatched claude child inherits
- a hardening guide for putting an egress firewall around a shift

## [0.10.1]

**Operator impact:** `-remote` never worked and now says so. No `claude` CLI
registers headless runs with Remote Control — the current one accepts
`--remote-control` under `-p`, runs a normal session and never starts the
remote bridge — so no run has ever appeared in a session list, whatever
startup claimed. polako now passes no such flag at all, and the shift-start
line says registration is unavailable instead of promising it. Nothing about a
shift leaves the machine by this path, and nothing ever did; `-post-summary`
is once again the only outward path, and it stays off by default. If you set
`-remote=false` to keep runs local, that is now the default behaviour and the
flag only silences one startup line.

### Fixed

- `-remote` no longer puts `--remote-control` or a session name on any
  `claude` invocation. The flag stays accepted, documented and on by default,
  so a later release has one place to pass it again once a supporting CLI
  ships — an upgrade you would choose, not a change that arrives on its own.
- The shift-start line states that headless runs cannot be registered and stay
  unwatched, in place of the claim that each one is watchable from
  claude.ai/code.

### Removed

- The rejection-and-redispatch machinery behind the flag, which the CLI's
  actual behaviour made unreachable: a refusal that never comes cannot be
  detected, and the fallback line it guarded never fired.

## [0.10.0]

**Operator impact:** the terminal goes quiet and a new local file appears. A
run now shows on the terminal as milestones only — session started, finished
with its cost, PR opened and merged, parks, the summary — while the full
`[claude]` event stream, the claude process's stderr and every milestone go
to one log file per shift under `~/.polako/logs`, on by default. That file is
the second write-only local artifact beside the run data, and the one that
holds transcript text; it never leaves the machine and nothing reads it back.
Anything that greps the live stderr stream for `[claude] →` lines should read
the shift log instead. Piped stderr keeps its timestamps; a TTY drops them
and gains colour.

### Added

- One log file per shift under `~/.polako/logs` (`-log <dir>` moves it,
  `-log off` disables it), holding everything the shift narrates: milestones,
  the full claude event stream, and the claude process's stderr as
  `[claude stderr]` lines — previously passed through raw and unattributed.
- `-verbose`: mirror the full event stream to the terminal as well, for
  watching a shift work rather than glancing at it.
- Colour on a capable TTY, one colour per whole line, honouring `NO_COLOR`
  and `TERM=dumb`; Windows stays plain. Pipes and redirects see the same
  plain, timestamped lines as before.
- A crashed run's last stderr lines are surfaced beside the error milestone,
  since for a crash they are often the only cause on record.

## [0.9.0]

**Operator impact:** two changes to what an unattended run does. `polako work`
now refuses to start on a public repository unless the queue is gated with
`-label` or the new `-ungated` flag consents to the open backlog — a drain
that ran there yesterday stops today, on purpose. And a run that exits cleanly
but leaves uncommitted work or an unpushed branch behind is now resumed to
finish the job, where it used to be parked for a human to unpick.

### Added

- `-ungated`: work a public repository without a `-label` gate. Without one or
  the other, `polako work` refuses to start there, because anyone who can open
  an issue can feed an unattended agent's queue — the Security section's
  standing advice, enforced on the one repository shape where the risk is
  structural rather than a judgement call. A `-dry-run` may still look.
- A clean exit that left work on disk is resumed to finish the job instead of
  parked unread, and such a resume is recorded in the run data under its own
  reason, so `stats` can tell it apart from a crash recovery.
- A park now says what the run left on disk — branch, commits, uncommitted
  files — instead of only that it produced nothing.
- `polako stats -html` writes the report as one self-contained HTML file that
  fetches nothing when opened.
- The release workflows open their PRs as you when a `RELEASE_TOKEN` secret is
  set, so those PRs run CI without the bot-PR approval click.

### Fixed

- The skill's own plan file no longer counts as work a run left behind, so a
  run that planned and then found nothing to do exits clean rather than
  looking interrupted.
- The operator's local worktree path stays off the issue thread when a run
  reports where it stopped.
- The skill is told it gets one turn, so it waits out slow work in that turn
  instead of deferring it to a next one that never comes.
- The CI smoke run clones the way `claude` itself does, and says more when it
  cannot.

### Changed

- The repository is public. Installing needs no access grant and no
  `GOPRIVATE` any more; the README's Install and Access sections say so, and
  the `-ungated` gate above is the flip side of the same coin.

## [0.8.0]

### Added

- automate the release pipeline between the two human gates
- register each run with Remote Control, on by default
- name the session in the progress log and beside a park
- list individual runs in stats with -runs

### Fixed

- catch the refusal that never exits, and bound the stderr wait

### Changed

- name Remote Control as the second thing that leaves the machine
- fix the anchor, the column claim and the park sample in the -runs section
- document -runs and the claude --resume recipe

## [0.7.0]

### Added

- `polako status`: one snapshot of where the backlog stands, derived
  from GitHub. The queue in the order `polako work` would take it, what is waiting on
  an answer and how long its thread has been quiet, what is parked, and every
  open PR on an `issue-N` branch with its mergeable, checks and review state —
  closing with a `needs you:` line naming only the things a person has to move.
  It takes `-repo`, `-dir`, `-label`, `-branch-prefix` and `-strict-order`, and
  honours the same environment defaults, so it can be scoped exactly as the
  shift it describes is — and it names on its header line whatever narrowed or
  reordered the snapshot, since a flag left in a profile is otherwise invisible.
  `-repo owner/name` means it needs no checkout at all: any machine with `gh`
  authenticated for the repository gets the same answer.

  It reports state, not liveness — it never asks whether a shift is running and
  says the same thing either way, which is what makes it useful about a shift
  running on another machine. Every call it makes is a read, using the same `gh`
  subcommands polako re-derives its state with; a test asserts the list of
  calls rather than merely checking that nothing changed. It opens no run-data
  file, so telemetry stays write-only outside `stats`, and it prints no issue,
  PR or comment text.

  **Operator impact:** none on what a shift does. The subcommand is new, purely
  additive, and mutates nothing.

- Run data says which shift produced it. Every process generates a random
  8-character id at startup, stamps it into every record it writes as `shift`,
  and prints it once beside the "recording run data in …" line with the exact
  `stats` invocation that reports on it. `polako stats` gains `-shift
  <id>` — with `last` meaning whichever shift wrote the newest record in scope,
  so it composes with `-repo` and `-since` — and `-by shift` alongside the
  existing groupings. That makes "what did the shift I left running overnight
  do?" and "what has the one running right now spent?" exact questions, which
  a `-since` window cannot answer once two shifts overlap or run back to back.
  The field is additive: older files still load, and their records group and
  filter as `(none)`. Nothing reads the id back — telemetry stays write-only.

- The skill half has coverage. `repo_test.go` now asserts the promises the
  supervisor actually depends on — that PLAN.md is written before
  implementation, that the `awaiting-answer` label command is spelled the one
  way `issueLabelTools` grants it, that the branch name matches the
  `-branch-prefix` default, and that the PR body keeps its four sections and its
  closing line. Those run free on every CI platform. Alongside them, `evals/`
  holds four `claude plugin eval` cases that drive real plan-to-PR runs against
  a scratch repo and a stand-in `gh`, and grade what they leave behind. The
  eval suite is opt-in and deliberately outside `check.sh` and CI — it is the
  one place this repo accepts a non-hermetic test — and it has not yet had a
  green run; `evals/README.md` says what to expect first time.

- `-dry-run`: resolve the next issue, print the exact `claude` invocation it
  would get, and exit. The queue is resolved the way a real shift resolves it —
  `-skip`, `needs-human`, and the preference for an issue waiting on an answer
  when nothing else is ready — and an issue whose branch already carries a PR is
  reported as the PR it would wait on rather than a run it would not make.
  Nothing is run and nothing is written: every GitHub call is a read, no label
  is declared, and run-data recording is forced off so a `POLAKO_METRICS`
  in the environment cannot leave a record of a run that never happened. The
  invocation goes to stdout and the narration to stderr, so it can be piped
  somewhere and pasted. It is an action rather than a preference, so — like
  `-version`, and unlike every other flag — it takes no default from the
  environment: a `POLAKO_DRY_RUN` left in a profile would turn every
  later shift into a successful exit that worked nothing.

- **Operator impact:** `-notify <command>` runs a command of your choosing
  whenever polako needs a human — an issue parked, an issue blocked on an
  answer, the backlog cleared, or the shift stopping early on a fatal error or a
  spent `-max-session-cost`. Off unless asked for. It exists for the states that
  are otherwise silent: polako handles a parked or blocked issue by working
  the queue behind it, so the only trace is a label on a thread nobody is
  watching until morning. Context arrives in `POLAKO_NOTIFY_EVENT`,
  `_ISSUE`, `_REPO` and `_REASON` — numbers, identifiers and this program's own
  words, never issue or PR text. The command is run directly rather than through
  a shell (put a pipeline in a script), a failing or hanging hook is logged and
  never ends a shift, and a `-notify` naming a program that is not on `PATH` is
  refused at startup rather than at the first notification hours later.

- **Operator impact:** three caps on what a shift may spend, all off by default
  so nothing changes for a shift that sets none. `-max-cost` parks an issue
  once this shift's runs on it have cost that many dollars; `-max-issue-time`
  parks it once those runs have taken that much *run time*, killing the run in
  flight when the limit lands mid-run; `-max-session-cost` ends the shift
  cleanly, between issues, once its runs have cost that much. `-max-issue-time`
  is what `-stall` cannot stand in for: that watchdog kills a run that has gone
  silent, and an agent looping productively but uselessly for three hours emits
  events the whole way through. The two per-issue caps read the tally of every
  run dispatched for the issue — the first attempt, its resumes, the re-run
  that folds an answer in, and any conflict, CI or review remediation against
  its PR. They gate work about to be dispatched and never work already done, so
  a run that overspends but opens a PR is left to be merged rather than parked.
  Caps in force are named at startup, since the environment can set any flag.
  One honest limitation: a run that crashed, stalled or was interrupted never
  reported a cost, so a cap is a ceiling on what was observed.

### Changed

- **Operator impact — the tool is now `polako`, and the CLI takes a verb.**
  The module path, binary, plugin, release-tag prefix, environment namespace
  (`POLAKO_<FLAG>`, `POLAKO_NOTIFY_*`) and run-data directory
  (`~/.polako/metrics` — move existing records with one `mv`) all take the
  new name, and the old spellings are gone rather than aliased: this tool
  currently has no users but its author, so nothing keeps a compatibility
  surface alive. A bare `polako` prints the verb table instead of starting
  an unattended agent loop — starting one now takes the word `work` — and
  `status` and `stats` are verbs of the same binary, with `polako drain`
  answered by a usage error that names the verb that replaced it. The
  `drained` notify event is now `cleared`, records carry `polako_version`
  where they carried `drain_version`, and the `issue-N` branch naming
  contract is deliberately unchanged: branches are named for issues, not
  for the tool.

- The README now documents `claude --plugin-dir` — with a `-claude` wrapper
  script, since there is no pass-through for extra `claude` arguments — as the
  way to run both halves from a working tree. It used to say a local
  marketplace developed against `main`, which cannot work: the marketplace
  entry is a `github` source pinned to a `ref`, so adding the clone still
  installs the tagged release and the tree is never read.

- **Operator impact:** a wait on `awaiting-answer` now ends when a *person*
  comments, not when the comment count goes up. Whether an issue is blocked was
  already the label's job; when the wait ends was still any new comment, so CI, a
  linked-PR notice or a stale-bot nudge each cost a full Claude run to discover
  that the thread was as blocked as before. GitHub Apps are now read and skipped,
  and the log says so — `still awaiting a reply (2 new comment(s), none of them
  from a person)` — rather than repeating "still awaiting a reply" while GitHub
  plainly shows new comments. Comments from the account polako authenticates
  as are deliberately *not* skipped: on most setups that is the operator's own
  account, so skipping them would swallow the answer and wait forever, and
  nothing polako writes can end a wait in any case. Deciding this needs an
  author type, which `gh issue view --json comments` does not carry — its author
  payload is a login and nothing else — so the thread is now read through `gh
  api`. No new permission: the same read access polako already needed.

- The exit summary now prices the shift and each issue in it, and says when
  runs that reported no cost make the total an undercount. Dollars appear only
  when this shift spent some, so a shift that only waited on a PR an earlier
  process opened prints the line it always printed.

- **Operator impact:** an issue labelled `awaiting-answer` no longer holds up
  the queue behind it. Polako puts it down and works the next issue, the way
  it already advances past a parked one, and picks it back up when the reply
  lands and nothing else is left — or straight away once the label is removed by
  hand. Only one issue is ever in flight, so two runs still cannot collide. What
  this does weaken is the base a put-down issue resumes from: it keeps the
  worktree its first run created, so merges that landed while it waited are not
  under it. A textual clash with one of those is rebased automatically as a
  `CONFLICTING` PR; a semantic one is not. `-strict-order` restores the old
  behaviour in full. Two smaller consequences: `-once` now exits on a question
  as it does on a merge or a park, and a shift that ends with issues still
  waiting names them in its summary. A restarted shift cannot know whether a
  reply arrived while it was down, so it spends one run per already-flagged
  issue finding out; the skill re-reads the thread and stops again without
  re-asking when it has not.
- **Operator impact:** a run that stops to ask a question now labels the issue
  `awaiting-answer`, and the supervisor waits on that label instead of on the
  issue's comment count rising across the run. A comment from CI, a bot, a
  linked-PR notice or a passer-by is no longer read as a question, so polako
  no longer waits indefinitely for a reply nobody knew was expected — and the
  blocked state is visible on GitHub rather than only in the terminal. The label
  is declared at startup if the repository does not have it, so nothing needs
  setting up first; a run may add and remove labels only on the issue it was
  dispatched for. The label is read before the run as well as after, so a run
  that dies on its way to folding your answer in is resumed rather than waited
  on a second time for a reply you already gave; parking an issue clears the
  label too, since what it waits on then is a decision, not a reply.

### Added

- **Operator impact:** requesting changes on an open PR now dispatches a
  remediation run, the way a merge conflict and a red build already did. The
  supervisor reads the PR's reviews each poll; on a request for changes it sends
  a run that reads the review bodies and the comments left on individual lines of
  the diff, makes the changes, gets the suite passing and pushes — then goes back
  to waiting, because the re-review is yours. It may not dismiss or resolve the
  review, and still may not merge. Before this, asking for changes was invisible:
  polako logged "still open" every poll until somebody re-ran the skill by
  hand. Whether a review has been answered is derived from GitHub rather than
  remembered, so a restarted shift agrees with the one that dispatched the run: a
  review is outstanding until the branch carries a commit newer than it. That
  makes a rebase read as an answer — including one the conflict remediation
  performs — so a conflicting PR with a review open on it comes back for a fresh
  look rather than being reworked against a diff that no longer exists. The
  reviews themselves are the authority rather than GitHub's summary
  `reviewDecision`, which is empty on any repository whose branch protection does
  not require a review, so this works on the repositories most people have. One
  run per review, bounded by `-retries`; a run that finishes without moving the
  branch parks the issue rather than looping. These runs record `review` as their
  reason, so `polako stats` tells them apart from the other two. The
  allowlist gains one entry, minted per run and pinned to the one PR being fixed
  — `Bash(gh api repos/OWNER/REPO/pulls/N/comments:*)` — because gh has no `pr`
  subcommand that prints line comments; `gh api` is not granted wholesale.
- **Operator impact:** a red CI check on an open PR is now repaired the way a
  merge conflict already was. The supervisor reads the PR's check rollup each
  poll, and on a failure dispatches a run that reads the failing job logs, fixes
  the cause, re-runs the suite locally and pushes. It waits while any check is
  still running, and treats only conclusions a code change can fix as failures:
  `NEUTRAL` and `SKIPPED` are green, and a check stopped on a person
  (`CANCELLED`, or a deployment gate at `ACTION_REQUIRED`/`WAITING`) is reported
  as `needs a human` — not dispatched at, and not counted as still running,
  which would hide a real failure beside it. One run per observed failure,
  bounded by `-retries`; a run that
  finishes without moving the branch parks the issue rather than looping. Before
  this, a failing check was invisible: polako logged "still open" every poll,
  forever. These runs record `checks` as their reason, so `polako stats`
  tells them apart from conflict remediations. The allowlist gains three
  read-only entries the diagnosis needs — `gh pr checks`, `gh run list` and
  `gh run view`; `gh run` is still not granted wholesale, since that carries
  `rerun`, `cancel` and `delete`.
- `-strict-order`, which keeps the queue in strict ascending order: an issue
  awaiting an answer blocks every issue behind it until you reply.
- `scripts/smoke.sh` (and `smoke.ps1`), run between tagging and publishing:
  checks the tags, the published release and its five binaries, the changelog
  section against the release body, `go install ...@vX.Y.Z`, and the plugin
  installing with the `ref` moved — everything CI cannot reach because it needs
  the network, `gh` and a real `claude`. It installs into a throwaway
  `CLAUDE_CONFIG_DIR`, so it never moves the machine running it onto the
  release under test.
- The release scripts' STEP 3 message names that command, and the README covers
  both halves of a release smoke test — the script, and the one real issue that
  has to be driven through the skill by hand.

### Fixed

- A stream event too large to read is reported as one, and the run is killed
  rather than waited on. The reader has a 32 MB ceiling — an event can carry a
  whole file — and a line over it ended the read silently. The CLI then blocked
  writing into a pipe nothing was draining, so the supervisor waited on a
  process that would never exit, and the run died as a `-stall` kill up to
  fifteen minutes later. **Operator impact:** that failure now surfaces in
  seconds, saying what it was; it is still treated as a crash, so the run is
  resumed on the usual `-retries` budget rather than parking the issue.
- The recorded `plugin_version` is the copy of the skill that actually drove the
  run. It was whichever entry `claude plugin list --json` happened to name
  first, and that list holds more than one when a `--plugin-dir` copy is loaded
  alongside an install of the same plugin — how a tip skill gets tested against
  a tip binary. The session copy is what runs, so every record carried the
  installed version instead, and the version-skew warning compared a pair that
  was not in play. A session-scoped copy now wins, disabled copies are not
  candidates at all, and a list that still cannot be resolved records nothing
  rather than a number nothing downstream could tell was wrong.
- The skill's review gate brings the local default branch up to date before it
  reviews. It resolves the branch's base from that local ref, and polako never
  pulls — it merges on GitHub — so the ref falls one commit behind per merged
  PR. Reviewing against a stale one silently pulled an earlier merged PR into
  the diff, where `--fix` rewrote that already-merged code inside the branch
  under review. The supervisor now does the same thing itself, before it picks
  up an issue and after a merge it saw — a teammate's push and a shift restarted
  days later open the same gap, not only polako's own merges. **Operator
  impact:** polako now fast-forwards `-dir`'s default branch. It is `--ff-only`
  and nothing else: a default branch carrying a commit of its own, work in the
  way, or a checkout sitting on another branch is reported in the log and left
  exactly as it is, never rebased or reset.
- **Operator impact:** four interruptions the supervisor did not recover from.
  `SIGTERM` and `SIGHUP` now end the run the way `SIGINT` always did, so a
  machine shutting down or a service manager stopping the unit takes the running
  `claude` down with the supervisor — previously it was orphaned, keeping
  `acceptEdits` and the whole `--allowedTools` set, free to push a branch and
  open a PR that a restarted shift would then race with a second run on the same
  issue. A crashed session that turns out to be unresumable — its transcript
  truncated by a hard kill, or aged out of the CLI's retention — is given up on
  and the next attempt starts fresh, instead of failing on it identically until
  the issue parks as "claude crashed and 3 resume attempts failed". `-retries`
  now counts consecutive *fruitless* crashes: a run cut off after real work
  resets it rather than spending it, so a laptop that sleeps four times across
  one long issue no longer parks a healthy one, with a crude ceiling on total
  resumes per issue still guaranteeing the loop ends. And the read-only GitHub
  lookups that decide what to work next are retried a few times before their
  failure is believed, the way the waiting paths always did — waking from sleep
  is exactly when a `gh` call fails for a few seconds. A `gh` that genuinely
  cannot answer is still fatal, and writes are still made once.
- A resumed run re-derives where things stand instead of taking the last step
  for done. It was told to "continue exactly where it stopped", but a resume
  only ever happens because something cut the previous attempt off mid-action —
  the CLI's own words are "the response above may be incomplete". An edit could
  have applied without the check meant to follow it, a commit could be
  half-staged, a `gh pr create` could have succeeded with only its reply lost.
  The resumed session is now asked to check `git status`, whether the branch and
  PR already exist, and the issue thread, before it acts on anything. The thread
  because a kill between posting a question and raising `awaiting-answer` leaves
  a question the supervisor cannot see — it reads that run as having produced
  nothing and parks a healthy issue over it.
- The skill's Phase 1 no longer recreates a branch that already exists. It had
  no case for `issue-N` being there already — left locally by a killed run, or
  on the remote by one that pushed and died before `gh pr create` — so the
  branch got rebuilt from the default branch and the commits on it went with
  it. The lookup now comes before the workspace cases rather than after them, so
  every one of them takes an existing `issue-N` as-is. **Operator impact:** the
  interrupted run that got furthest is the one whose work survives; the
  `issue-N` naming contract and `-branch-prefix` are unchanged.

## [0.6.1]

### Fixed

- The skill's review gate names the branch it is reviewing. It invoked
  `/code-review high --fix` with no target, and the review forks a fresh agent
  that starts in the session's working directory — which the skill's own `cd`
  into the worktree does not move. A run driven from the main checkout reviewed
  whatever was there, so on a clean default branch it reviewed the change
  someone had already merged and wrote its fixes into that checkout, while the
  branch heading for a PR went unreviewed.

## [0.6.0]

### Added

- Any flag can take its default from the environment: `BACKLOG_DRAIN_<FLAG>`,
  e.g. `BACKLOG_DRAIN_POST_SUMMARY=1` or `BACKLOG_DRAIN_MODEL=claude-opus-5`.
  Arguments still win, `-h` prints the defaults actually in force, and one
  `BACKLOG_DRAIN_METRICS` points both the drain and `stats` at one directory.
- Startup says when `-post-summary` is on, so a preference set once in a shell
  profile never becomes a mystery about where PR comments are coming from.

### Changed

- `-version` is deliberately not settable from the environment.
  `BACKLOG_DRAIN_VERSION` is what a Dockerfile or CI job pins an install with,
  and honouring it would turn every drain on that machine into a version print.
- A `BACKLOG_DRAIN_*` value a flag cannot parse stops the run and names both the
  variable and the flag, rather than being ignored.

**Operator impact:** if a `BACKLOG_DRAIN_*` variable is already set in an
environment that runs this binary, it now changes that flag's default. Check
`backlog-drain -h`, which prints the value each flag will actually use.

## [0.5.0]

### Added

- Terminal issue records carry what GitHub knows about the PR: additions,
  deletions, changed files, a count of reviews, and when it opened and merged.
  One `gh pr view` as each issue ends, and none at all under `-metrics off`.
- `-post-summary` (default off) comments one line of run numbers on each merged
  PR — runs, question rounds, tokens, dollars, wall time. Numbers only, on the
  PR they describe. Independent of `-metrics`, so `-metrics off -post-summary`
  is team visibility with no local files at all.
- `backlog-drain stats` reports a `change per issue` line from that enrichment,
  and takes GitHub's own timestamps for PR-open-to-merge, which are right even
  when the run that opened the PR falls outside the window.

**Operator impact:** `-post-summary` is the first thing this tool can send off
the machine, and it stays off unless you ask for it. The invariant in CLAUDE.md
now says so explicitly: records never leave your disk; that one flag posts the
same numbers to your own merged PR, where exactly the people who can already
see the PR can read them.

## [0.4.0]

### Added

- `-version` prints which release this binary is, so a mismatch between the
  binary and the installed skill can be diagnosed from the shell.
- The supervisor reads the installed plugin's version at startup and warns when
  it differs from its own. The two halves share one version number by design.
- Run records gained a `plugin_version` field, so recorded runs can be
  attributed to a (binary, skill, CLI) triple rather than two thirds of one.
- This changelog. The release workflow publishes the matching section as the
  GitHub release body.

### Changed

- **The marketplace entry pins to a release tag** instead of tracking the
  default branch. A version number now identifies exactly one commit, and
  moving that `ref` is the act of publishing.
- Release binaries are stamped with their tag at build time. Before this they
  reported a bare commit SHA, because `go build` leaves the module version at
  `(devel)`.
- `scripts/release.sh` and `release.ps1` check that `claude plugin tag` exists
  before they start, and require a changelog section for the version.

### Fixed

- **Refused credentials stop the drain instead of being retried.** A resume
  cannot mint a new token, so a 401 used to burn `-retries` attempts reaching
  the identical failure and then report it as a crash. The run now ends the
  drain naming the fix. Run records carry an `auth` status, so these rows no
  longer inflate the crash rate.
- The finished line reports `ERROR` rather than `ERROR: success` — `is_error`
  is the authority for a run's status, not the subtype.
- README no longer claims installs track the default branch. They do not: the
  `version` field in `plugin.json` is the update signal and the cache key, so
  commits that land without a bump never reach an installed user.

**Operator impact:** two things.

Upgrade both halves together — a binary and a skill from different releases are
not a supported combination, since the supervisor finds a PR by the branch name
the skill chooses, and the new startup warning says so when it happens.

A drain that hits refused credentials now stops rather than working through its
retries. It stops sooner and says what to do; state lives in GitHub, so starting
it again once the token works resumes exactly where it left off.

## [0.3.0]

### Added

- `backlog-drain stats` reports on recorded run data.

### Fixed

- A windowed stats report no longer counts pricing work that resolved to zero.

## [0.2.0]

### Added

- Run-data capture: a line of numbers per Claude invocation and per issue,
  appended under `~/.backlog-drain`. Write-only — the drain loop never reads it.

### Fixed

- A missing `-skill` is detected from the init event's command inventory, so a
  run that would never resolve to a skill fails early instead of looking
  successful.

## [0.1.0]

Initial release: the `implement-issue` skill and the `backlog-drain`
supervisor.
