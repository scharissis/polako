# Update: one verb, one notice, shorter install docs

Scope: a new `update` verb, one best-effort read in `work` preflight and
`status`, a checksum file on releases, install docs rewritten · Behavior
change: `work` and `status` make one more bounded `gh` read and may print one
line; the version-skew remedy names `polako update`

Updating polako takes three commands, and nothing says when to run them. The
operator who wrote this has them in a shell alias. Anyone else finds out a
release exists when the skew gate refuses to start a shift.

This document drafts `polako update`, an "update available" line, and an
install page that leads with the common path.

## What exists today

- **Three commands, two tools.** `claude plugin marketplace update scharissis
  && claude plugin update polako@scharissis`, then `go install
  github.com/scharissis/polako/cmd/polako@latest`. `docs/install.md` says to
  run them "in the same breath". Nothing does.
- **`@latest` has a window where it breaks a shift.** A release pushes the
  `vX.Y.Z` tag first; the marketplace `ref` moves later, when the publish PR
  merges (`docs/releasing.md`). In between, `@latest` installs the new binary
  and `plugin update` still resolves the old skill. That is the skill behind
  the binary — the one direction `versionSkewGate` (`gate.go`) refuses to
  start on. `skewRemedy` and `docs/install.md` both recommend `@latest`.
- **Nothing says a release is out.** Preflight compares the binary to the
  installed plugin (`pluginVersion`, `skewComparison`). Neither is compared to
  what is published. Two halves that agree on an old version are silent.
- **Two ways to get the binary, one way to update it.** `go install`, or one
  of five stamped binaries attached to each release (`release.yml`). Nothing
  updates the second kind.
- **The binary already knows how it was built.** `polakoVersion`
  (`metrics.go`) falls through three tiers: the `-ldflags` stamp (a release
  binary), the module version (`go install`), the VCS revision (a clone).
- **Releases carry no checksums.** v0.23.0's assets are the five binaries and
  nothing else.
- **The README has no Update section.** It points at `docs/install.md`, which
  covers scopes and the hand install before it reaches updating.
- **There is no network code in the binary.** No `net/http` import anywhere.
  Everything remote goes through `gh`, which already holds the auth and the
  proxy settings.

## The answer in one paragraph

"Published" means the `ref` in `.claude-plugin/marketplace.json` on the
default branch — merging that line is the moment anybody is exposed to a
release. `polako update` reads it, updates the plugin, then brings the binary
to **that same version**, never `@latest`. `-check` reports and changes
nothing. `work` and `status` make the same read and print one line when
either half is behind. No cache, no state, no prompt.

## What update may do

This touches CLAUDE.md's invariants, so it is argued here and lands as a new
invariant bullet with ticket 1.

- **It never runs inside a drain, and `work` never updates itself.** A
  shift's binary and skill don't change under it by polako's hand. `update`
  is a verb a person or a script runs between shifts.
- **The check is a read, and it is best-effort.** One `gh api` call for a
  public file, under a 10s timeout (the `probeUsage` pattern in `usage.go`).
  A failure prints nothing and refuses nothing. It sends nothing about the
  run, so it is not a second destination under *one thing leaves the
  machine* — `docs/security.md` names it anyway, for the same reason that
  invariant exists.
- **Nothing durable remembers a check.** No last-checked file. A timestamp
  under `~/.polako` that preflight reads back is state, and the write-only
  invariant names exactly that. One read per `work` start and per `status`.
- **No stdin.** Attended or scripted, it behaves the same. `claude plugin
  update` runs without `-y`: if a marketplace ever declares an install
  command, the CLI refuses without a terminal, and `update` relays that
  rather than accepting it for the operator.
- **A missing plugin stays missing.** Installing one is a decision about the
  operator's Claude Code — the call `docs/plans/setup.md` already made.
  `update` prints the two install commands.
- **The write surface is the two halves.** The plugin, through the CLI. The
  binary, through `go install` or a verified swap of `os.Executable()`.
  Nothing under `-dir`, nothing on GitHub.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. `polako update`: the plugin, then a go-installed binary

**Problem.** Updating is three commands across two tools, and the documented
ones can leave the binary ahead of the skill.

**Shape.** A new verb on `tidy`'s shape.

- `cmd/polako/update.go`: `runUpdate(ctx, args, out, rpt)` on
  `flag.NewFlagSet`, `applyEnvDefaults`, `errFlagsReported`. Flags `-check`,
  `-claude`, `-gh`, in the `XxxVar(&target, "name"` form `declaredFlags`
  needs. Dispatch through `runReport` with its own `signal.NotifyContext`.
  `main` is a line under `funcBudget` (`sizebudget_test.go`), so the dispatch
  switch splits out first, unless the `setup` verb already did it.
- `verbUsage` gains the line; `TestVerbUsageListsUpdate` beside the `plan`
  and `health` ones.
- `publishedVersion(ctx, cfg)`: `gh api
  repos/scharissis/polako/contents/.claude-plugin/marketplace.json -H
  "Accept: application/vnd.github.raw"`, the `polako` entry's `ref`, its
  `polako--v` prefix cut, parsed by `releaseVersion` (`gate.go`). 10s
  timeout. Checked 2026-09-19 on `gh` 2.99.0: the call returns the file.
- Plugin half: `pluginVersion` and `installedVersion` give the installed
  version, the `plugin@marketplace` id and the scope. Run `claude plugin
  marketplace update <marketplace>`, then `claude plugin update <id> --scope
  <scope> --json`. An ambiguous id means say so and skip this half. No plugin
  installed means print the two install commands. A hand-installed skill
  (`-skill` with no plugin prefix) means binary only, and a line saying the
  skill copy is theirs to re-copy.
- Binary half, by build kind. Module version: `go install
  github.com/scharissis/polako/cmd/polako@v<published>`, with a warning when
  `os.Executable()` is not under `go env GOBIN` or `GOPATH/bin` — that
  install would land beside the running copy, not over it. `-ldflags` stamp:
  until ticket 4, print the release URL and the asset name for this
  `GOOS`/`GOARCH`. VCS revision: "built from source — rebuild it yourself".
- Already current on both halves: say so, run nothing.
- Ends with both versions and "restart Claude Code, or `/reload-plugins`".
- `-check`: the versions and what `update` would run. Exit 0 either way.
- `skewRemedy` names `polako update`. `TestVersionSkewRemedyAgreesWithInstallDocs`
  and `docs/install.md` move with it.
- `-check` is documented in `docs/install.md`. `docs/reference.md` is at its
  line budget, and `TestDocsDocumentEveryFlag` reads any page under `docs/`.
- CLAUDE.md gains the invariant bullet from "What update may do".
- Test seams: the fake `gh` gains an `api contents` route beside `api label`
  (`drain_test.go`) and a published-ref field in `ghState`; `fakeClaude`
  gains `plugin update` and `plugin marketplace update` argv routes; `go`
  gets an unexported config seam and a fourth `TestMain` re-entry, like
  `gh`'s.

**Done when.** With a fake published ref one minor ahead, `polako update`
runs the two `claude plugin` commands with the installed id and scope, then
`go install …@v<published>` — never `@latest` — and prints both versions;
`-check` prints the same plan and the fake logs show reads only; with both
halves current it runs nothing; the skew remedy and `docs/install.md` name
`polako update`.

Estimate: M

### 2. The notice: `work` start and `status`

**Problem.** A release reaches nobody who doesn't go looking. The first
signal today is a refusal.

**Shape.** Depends on ticket 1.

- In `preflight` (`main.go`), beside `warnOnVersionSkew`, after the shift log
  opens so the line lands in it. `narrate(sevWarning, …)`:
  ``update available: polako 0.24.0 is out (binary 0.23.0, plugin 0.23.0) —
  run `polako update` ``.
- Fires when the published version is ahead of either half. Silent when both
  are current, when the read fails or times out, when the binary is not a
  release, and when `-skill` names another plugin.
- No `preflightPairs` row. The recap lists what the operator asked for; this
  is news.
- `status` prints the same line under its banner (`status.go`) and carries
  the published version in `-json`.
- `docs/security.md` names the read under what leaves the machine.
  `docs/hardening.md` needs no new host — `api.github.com` is already
  required.

**Done when.** `polako work -dry-run` against a fake ref one minor ahead
prints the line once, with all three versions; with the ref equal it prints
nothing; with the `api contents` route failing it prints nothing and preflight
takes no longer than the timeout; `status -json` carries the published
version.

Estimate: S

### 3. Releases publish `checksums.txt`

**Problem.** A binary that replaces itself should check what it downloaded.
Releases publish nothing to check against.

**Shape.** `release.yml`'s build step writes `dist/checksums.txt` — sha256
and asset name per line, `sha256sum`'s format — and `gh release create`
attaches it with the rest. `scripts/release.sh` and `release.ps1` do the
same by hand. `scripts/smoke.sh` downloads it and checks the asset it already
fetches. `docs/releasing.md` lists the sixth asset.

**Done when.** The next release carries `checksums.txt` with five lines, and
the smoke run fails when a line doesn't match its asset.

Estimate: S

### 4. `update` replaces a prebuilt binary

**Problem.** An operator without Go downloaded a release binary once. Ticket
1 can only tell them to do it again.

**Shape.** Depends on tickets 1 and 3. Applies when `polakoVersion` came from
the `-ldflags` stamp.

- `gh release download v<published> --repo scharissis/polako --pattern
  polako_v<published>_<GOOS>_<GOARCH>[.exe] --pattern checksums.txt` into a
  temp dir beside `os.Executable()`, so the rename stays on one filesystem.
- Verify with `crypto/sha256`. A mismatch, or a release with no
  `checksums.txt`, is a refusal naming both sums or the missing file. The
  running binary is untouched.
- Swap by rename, mode copied from the old file. On Windows, rename the
  running `.exe` aside to `.old` first, and remove a leftover `.old` at the
  start of the next `update`.
- A directory it can't write to: say so, name the path, print the download
  command.
- The fake `gh` needs `"release"` in `ghSubcommands` (`drain_test.go`).
  Without it `TestMain` doesn't see a `gh` call and the child runs the whole
  suite.

**Done when.** Against a fake release, a stamped test binary ends up
byte-identical to the fake asset with its mode kept; a checksum mismatch
leaves the old file in place and exits non-zero naming both sums; the rename
path is covered on Windows in CI.

Estimate: M

### 5. Install docs, rewritten

**Problem.** The install page opens with scopes and hand copies. The common
path — install two things, update them later — is scattered over it, and the
README doesn't mention updating.

**Shape.** Depends on ticket 1; mentions ticket 4 once it lands.

- README: `## Install` stays three commands. A new `## Update` under it: one
  line, `polako update`, and a link.
- `docs/install.md`, common path first:
  1. Install — the plugin, the binary (`go install`, or a release binary).
  2. Update — `polako update`, `-check`, the notice, and what "published"
     means in two sentences. Then the by-hand commands, for a binary older
     than the verb, with `@v<version>` in place of `@latest`.
  3. Auto-update, pinning, uninstall — as today, shorter.
  4. By hand — the skill copy, the source build, `--scope`.
  5. Another project — already shrinking to a pointer under the `setup` plan.
- "Not mid-shift" goes in the Update section if open question 4 says it
  matters.
- `docs/releasing.md`'s operator-impact guidance says `polako update`.
- Line budgets hold (`docsbudget_test.go`). No flag tables in the README.

**Done when.** The README's Install and Update fit one screen; `docs/install.md`
reaches `polako update` before any mention of scopes; no page recommends
`@latest` for an update; `./scripts/check.sh` passes.

Estimate: S

## Considered and not proposed

**A last-checked file under `~/.polako`.** It would save one read per start.
It is also durable local state that changes what preflight does, which the
write-only invariant names as the design error. One bounded read is cheap.

**`work` updating itself.** A shift that changes its own binary or skill
partway is the skew problem on purpose. Updating is a human's call, like
merging.

**An opt-out flag.** Preflight already makes several `gh` calls; this is one
more, bounded and silent on failure. Add one if an operator behind a strict
proxy asks.

**`@latest` for the binary.** The window described above.

**`net/http` to the GitHub API.** The first network code in the binary, and
it would need its own auth, proxy and rate-limit handling. `gh` has all
three.

**A `curl | sh` installer, or a Homebrew tap.** A second channel to keep in
step with two tags. `gh` is already required, and `gh release download` is
one line.

**Installing a missing plugin.** See "What update may do".

**`-check` exit codes as a signal.** The output is the signal. Revisit if
someone scripts it.

**A notice in the exit summary.** The operator can't act on it until the
next shift, and the start line is already in the shift log.

## Open questions

Facts to check before the ticket that needs them:

1. Do `claude plugin marketplace update` and `claude plugin update` run
   without a terminal and without `-y` for a `github`-source marketplace?
   `plugin update --help` says `-y` is only needed for a marketplace-declared
   command; confirm by running both with stdin closed. (ticket 1)
2. What does `plugin update --json` print when there is nothing to update,
   and on the oldest CLI polako supports, does the flag exist? (ticket 1)
3. What does the raw-contents read return when the token is rate-limited, and
   is it distinguishable from a 404? Only matters for `update`'s error text;
   the notice stays silent either way. (tickets 1, 2)
4. Does updating the plugin mid-shift change what the next `claude -p` of a
   running drain loads? If so, `docs/install.md` says "not mid-shift".
   (tickets 1, 5)
5. Can a running `.exe` be renamed aside on the Windows CI runner, and does
   `gh release download` leave a quarantine attribute on macOS? (ticket 4)

## Work items

- [ ] `polako update`: plugin and go-installed binary, the published-version
  read, the invariant bullet (ticket 1)
- [ ] The "update available" line in `work` preflight and `status` (ticket 2)
- [ ] `checksums.txt` on every release (ticket 3)
- [ ] `update` replaces a prebuilt binary, verified (ticket 4)
- [ ] README and `docs/install.md` rewritten around `polako update` (ticket 5)
