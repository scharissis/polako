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

### The skill

As a plugin — the repo doubles as its own marketplace, so this is two commands
and no clone:

```bash
claude plugin marketplace add scharissis/backlog-drain
```

```bash
claude plugin install backlog-drain@scharissis
```

Or copy the skill in by hand:

```bash
cp -r skills/implement-issue ~/.claude/skills/
```

```powershell
Copy-Item -Recurse skills\implement-issue $HOME\.claude\skills\
```

### The binary

```bash
go install github.com/scharissis/backlog-drain/cmd/backlog-drain@latest
```

Or build a local copy:

```bash
go build -o backlog-drain ./cmd/backlog-drain
```

Prebuilt binaries for Linux, macOS and Windows are attached to each tagged
release.

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
