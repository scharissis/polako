# Changelog

Notable changes per release, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the versioning rules —
and why minor is the breaking axis before 1.0 — are in
[Publishing and versioning](README.md#publishing-and-versioning).

Entries carry an **Operator impact** line when a release changes what an
unattended run does, rather than only what the code looks like. Those are the
lines worth reading before upgrading a machine that drains a backlog overnight.

## [Unreleased]

### Changed

- **Operator impact:** an issue labelled `awaiting-answer` no longer holds up
  the queue behind it. The drain puts it down and works the next issue, the way
  it already advances past a parked one, and picks it back up when the reply
  lands and nothing else is left — or straight away once the label is removed by
  hand. Only one issue is ever in flight, so two runs still cannot collide. What
  this does weaken is the base a put-down issue resumes from: it keeps the
  worktree its first run created, so merges that landed while it waited are not
  under it. A textual clash with one of those is rebased automatically as a
  `CONFLICTING` PR; a semantic one is not. `-strict-order` restores the old
  behaviour in full. Two smaller consequences: `-once` now exits on a question
  as it does on a merge or a park, and a drain that ends with issues still
  waiting names them in its summary. A restarted drain cannot know whether a
  reply arrived while it was down, so it spends one run per already-flagged
  issue finding out; the skill re-reads the thread and stops again without
  re-asking when it has not.
- **Operator impact:** a run that stops to ask a question now labels the issue
  `awaiting-answer`, and the supervisor waits on that label instead of on the
  issue's comment count rising across the run. A comment from CI, a bot, a
  linked-PR notice or a passer-by is no longer read as a question, so the drain
  no longer waits indefinitely for a reply nobody knew was expected — and the
  blocked state is visible on GitHub rather than only in the terminal. The label
  is declared at startup if the repository does not have it, so nothing needs
  setting up first; a run may add and remove labels only on the issue it was
  dispatched for. The label is read before the run as well as after, so a run
  that dies on its way to folding your answer in is resumed rather than waited
  on a second time for a reply you already gave; parking an issue clears the
  label too, since what it waits on then is a decision, not a reply.

### Added

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
  this, a failing check was invisible: the drain logged "still open" every poll,
  forever. These runs record `checks` as their reason, so `backlog-drain stats`
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

- The skill's review gate brings the local default branch up to date before it
  reviews. It resolves the branch's base from that local ref, and a drain never
  pulls — it merges on GitHub — so the ref falls one commit behind per merged
  PR. Reviewing against a stale one silently pulled an earlier merged PR into
  the diff, where `--fix` rewrote that already-merged code inside the branch
  under review. The supervisor now does the same thing itself, before it picks
  up an issue and after a merge it saw — a teammate's push and a drain restarted
  days later open the same gap, not only the drain's own merges. **Operator
  impact:** a drain now fast-forwards `-dir`'s default branch. It is `--ff-only`
  and nothing else: a default branch carrying a commit of its own, work in the
  way, or a checkout sitting on another branch is reported in the log and left
  exactly as it is, never rebased or reset.

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
