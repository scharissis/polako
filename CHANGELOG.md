# Changelog

Notable changes per release, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the versioning rules —
and why minor is the breaking axis before 1.0 — are in
[Publishing and versioning](README.md#publishing-and-versioning).

Entries carry an **Operator impact** line when a release changes what an
unattended run does, rather than only what the code looks like. Those are the
lines worth reading before upgrading a machine that drains a backlog overnight.

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
