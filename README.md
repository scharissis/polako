# backlog-drain

Point it at any GitHub repository and it works the issue backlog to zero — one
issue at a time, strictly in ascending order, unattended.

It has two halves:

| Half | What it does |
| --- | --- |
| **`/implement-issue` skill** | Takes a *single* issue from research → `PLAN.md` → implementation → code review → pull request. Usable on its own, interactively. |
| **`backlog-drain` binary** | Supervises the *whole queue*: runs the skill on the lowest open issue, waits for that PR to merge, then advances. Stdlib-only Go, single binary, any platform. |

The binary never advances past an unmerged issue. Every run therefore branches
from a default branch that already contains the previous merge, so sequential
runs cannot conflict with each other.

## How it works

```
lowest open issue
   ↓
claude -p "/implement-issue N"        ← headless, streamed to your terminal
   ↓
PR opened?  ──no──►  questions posted on the issue?  ──yes──►  wait for a human reply, re-run
   │                          │
   │                          └──no──►  crashed? resume the same session (-retries)
   ↓ yes
wait for merge (-poll)               ← rebases + re-pushes if GitHub reports CONFLICTING
   ↓
close the issue, remove the worktree, advance to the next
```

**All state lives in GitHub** — issues, comments, PRs, branches. The process
itself is stateless and restart-safe: kill it at any point, rerun it later, and
it re-derives where things stand. If a PR already exists for `issue-N`, it
never re-runs Claude on that issue; it goes straight to waiting.

**Human touchpoints are deliberately just two**, both on GitHub:

1. Answering clarification questions the skill posts on an issue thread.
2. Merging the PR. Nothing merges itself.

## Requirements

- [`claude`](https://claude.com/claude-code), authenticated
- [`gh`](https://cli.github.com), authenticated (`gh auth login`)
- `git`
- Go 1.26+ — only to build from source

All three must be on `PATH`; `backlog-drain` checks at startup rather than
failing an hour into an unattended run.

## Install

> **This repository is private.** Installing it requires a GitHub account with
> read access — see [Access](#access) below. Nothing here is published to any
> public registry.

### The skill, as a plugin (recommended)

The repo doubles as its own marketplace, so there is no clone step. Register
the marketplace once:

```bash
claude plugin marketplace add scharissis/backlog-drain
```

Then install the plugin from it:

```bash
claude plugin install backlog-drain@scharissis
```

`backlog-drain` is the plugin, `scharissis` is the marketplace it came from —
the name declared in [`.claude-plugin/marketplace.json`](.claude-plugin/marketplace.json),
not the GitHub username, though here they happen to match. The plugin ships one
component, the `implement-issue` skill, and costs ~40 tokens of always-on
context; the skill body is only loaded when it fires.

Restart Claude Code, and `/implement-issue 48` is available.

Both commands take a `--scope`:

| Scope | Where it is declared | Use it for |
| --- | --- | --- |
| `user` *(default)* | `~/.claude/settings.json` | Your own machine, every project. |
| `project` | the repo's `.claude/settings.json` | Committing the marketplace + plugin so collaborators on *that* repo get the skill automatically. |
| `local` | the repo's git-ignored local settings | Trying it on one project without committing anything. |

So to make every contributor to some project pick the skill up, run both
commands with `--scope project` inside that project and commit the resulting
`.claude/settings.json`. They still each need read access to this repo.

To update later, and to remove:

```bash
claude plugin marketplace update scharissis && claude plugin update backlog-drain
```

```bash
claude plugin uninstall backlog-drain && claude plugin marketplace remove scharissis
```

### The skill, by hand

If you would rather not involve the plugin system, copy the skill directory in.
It behaves identically; it just will not update itself.

```bash
cp -r skills/implement-issue ~/.claude/skills/
```

```powershell
Copy-Item -Recurse skills\implement-issue $HOME\.claude\skills\
```

Do one or the other, not both — two copies of the same skill name drift apart
silently.

### The binary

```bash
go install github.com/scharissis/backlog-drain/cmd/backlog-drain@latest
```

For a private module, `go install` needs to be told not to consult the public
proxy, and to use your git credentials:

```bash
GOPRIVATE=github.com/scharissis/* go install github.com/scharissis/backlog-drain/cmd/backlog-drain@latest
```

Or build from a clone, which avoids the question entirely:

```bash
go build -o backlog-drain ./cmd/backlog-drain
```

Prebuilt binaries for Linux, macOS and Windows are attached to each tagged
release, and are the easiest option on a machine without Go.

### Access

The repository is private, so:

- **Other people cannot install this** unless you grant them access. Adding the
  marketplace runs a `git clone` as them; without access it fails there.
- **You can**, on any machine where `git` can already clone your private repos —
  an SSH key, or the credential helper `gh auth login` sets up.
- To share it with named people, add them as collaborators
  (`gh repo add-collaborator`) or move the repo into an organisation.
- To make it installable by anyone, publish it: `gh repo edit --visibility public`.
  The skill, the README and the plugin metadata all become public at that point,
  so read them once with that in mind first.

## Usage

Drain the whole backlog of the repository in the current directory:

```bash
backlog-drain
```

Drive a repository somewhere else, and stop after the first issue reaches
merge — a good way to try it out:

```bash
backlog-drain -dir ../my-project -once
```

Only work issues carrying a label, and check GitHub more often:

```bash
backlog-drain -label ready-for-claude -poll 90s
```

Skip an issue that is blocking the queue:

```bash
backlog-drain -skip 12,17
```

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `.` | Path to the repository's main checkout. |
| `-claude` | `claude` | The Claude Code binary to invoke. |
| `-skill` | `implement-issue` | Skill run once per issue. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how PRs are matched back to issues. |
| `-label` | *(none)* | Only process issues carrying this label. |
| `-tools` | *(see below)* | `--allowedTools` for unattended runs. **Replaces** the default set. |
| `-add-tools` | *(none)* | Extra `--allowedTools` entries, **appended** to `-tools`. |
| `-permission-mode` | `acceptEdits` | Passed to `claude --permission-mode`. |
| `-poll` | `5m` | Interval between GitHub checks while waiting. |
| `-retries` | `3` | Resume attempts after a crashed run. |
| `-retry-wait` | `30s` | Wait before each resume attempt. |
| `-stall` | `15m` | Kill and resume a run that has emitted no events for this long (`0` disables). |
| `-skip` | *(none)* | Comma-separated issue numbers to skip. |
| `-once` | `false` | Process a single issue to merge, then exit. |

## Using it on another project

Nothing here is tied to one repository or language — `-dir` points anywhere.
The one thing worth tuning per project is the tool allowlist, because an
unattended run stalls if a command it needs would raise a permission prompt.

The default `-tools` set covers git, gh, the tools the skill itself needs
(`Read`, `Write`, `Edit`, `Glob`, `Grep`, `Skill`, `TodoWrite`), and the usual
entry points for npm/pnpm/yarn, Go, Cargo, Make, Python/uv/pytest, dotnet,
Maven and Gradle. For anything else, widen it rather than replacing it:

```bash
backlog-drain -add-tools "Bash(bazel:*),Bash(just:*)"
```

Two other knobs matter when moving between repos:

- `-branch-prefix` must match what the skill names its branches, since that is
  how a PR is matched back to its issue.
- `-label` is the cleanest way to opt individual issues in on a busy repo.

## Publishing and versioning

The repo ships two artefacts — a Claude plugin and a Go binary — and they share
one version number, the `version` field in
[`.claude-plugin/plugin.json`](.claude-plugin/plugin.json). That field is the
source of truth; everything else is derived from it.

A release is that number, bumped and committed, plus two tags on the same
commit:

| Tag | Who needs it |
| --- | --- |
| `backlog-drain--v0.2.0` | The Claude plugin tooling. `claude plugin tag` creates it, and refuses if `plugin.json` and the marketplace entry disagree. |
| `v0.2.0` | Go modules — `go install ...@v0.2.0` only resolves semver tags — and the trigger for the binary release workflow. |

Two tags for one release is a footgun, so a script does both:

```bash
./scripts/release.sh
```

```powershell
.\scripts\release.ps1
```

Each refuses to run on a dirty tree, so the version bump has to be committed
first. Pushing `v0.2.0` starts the release workflow, which cross-compiles the
five targets and attaches them to a GitHub release with generated notes.

**Installs track the default branch, not tags.** The marketplace entry uses
`"source": "./"` — the plugin lives in the same repo as the marketplace — so
`claude plugin marketplace update` fetches whatever is on `main`. The tags are
release markers and rollback points, not what the installer resolves. If you
ever want installs pinned to a reviewed release instead, change the entry in
`marketplace.json` from `"./"` to an explicit git source with a `ref`, and
moving that `ref` becomes the act of publishing.

What to bump, pre-1.0:

- **Patch** — bug fixes, doc changes, anything invisible to a caller.
- **Minor** — new flags, changed defaults, changes to the skill's phases. The
  `-tools` default counts: widening it changes what unattended runs may do.
- **Major** — deferred until the skill's contract with the supervisor settles.
  The coupling to watch is the branch name: the supervisor finds a PR by its
  head branch, so if the skill ever stops naming branches `issue-N`, that is a
  breaking change on both sides at once.

## Development

```bash
go test ./...
```

```bash
./scripts/check.sh
```

```powershell
.\scripts\check.ps1
```

`check` runs `gofmt`, `go vet` and the full test suite — the same three things
CI runs, on Linux, macOS and Windows. The plugin side has its own validator:

```bash
claude plugin validate .
```

The suite is hermetic: no network, no `gh`, no real `claude`. Tests that need a
Claude process re-execute the test binary as a fake CLI that streams canned
`stream-json` events, which covers the streaming, session-capture, crash and
stall-watchdog paths for real. A second group of tests keeps the repository
honest — the plugin manifest, the shipped skill and the documented flags all
have to agree with the code.

## License

MIT — see [LICENSE](LICENSE).
