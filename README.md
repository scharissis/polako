# polako

*Polako* is Croatian for "take it slow". Point it at any GitHub repository and
it works the issue backlog to zero — one issue at a time, strictly in
ascending order, unattended, with a human at every gate.

It has two halves:

| Half | What it does |
| --- | --- |
| **`/implement-issue` skill** | Takes a *single* issue from research → `PLAN.md` → implementation → code review → pull request. Usable on its own, interactively. |
| **`polako` binary** | Supervises the *whole queue*: `polako work` runs the skill on the lowest open issue, waits for that PR to merge — or parks the issue for a human — then advances. `status` and `stats` are its two read-only reports. Stdlib-only Go, single binary, any platform. |

The binary never has two issues in flight: it advances only once the issue it is
working is merged, or parked for a human. Every run therefore branches from a
default branch that already contains the previous merge, so sequential runs
cannot conflict with each other.

## How it works

```
lowest open issue with no sub-issues and no `needs-human`, `proposed`
or `awaiting-answer` label
   ↓
claude -p "/implement-issue N"        ← headless, streamed to your terminal
   ↓
PR opened?  ──no──►  issue labelled `awaiting-answer`?  ──yes──►  put it down, advance to the next
   │                          │                                  (re-run it when the reply lands)
   │                          └──no──►  crashed, or left work on the branch?
   │                                       │        resume the same session
   │                                       └──out of attempts──►  park it, advance to the next
   ↓ yes
wait for merge (-poll)               ← rebases if GitHub reports CONFLICTING,
   ↓                                   fixes + re-pushes if the checks go red
   ↓                                   or a reviewer requests changes
close the issue, remove the worktree, advance to the next
```

[docs/behaviour.md](docs/behaviour.md) is the long version of that picture: what
happens when a run crashes, when an issue cannot be finished, when a PR goes
red, and when polako needs you.

## Requirements

- [`claude`](https://claude.com/claude-code), authenticated
- [`gh`](https://cli.github.com), authenticated (`gh auth login`)
- `git`
- Go 1.26+ — only to build from source

All three must be on `PATH`; `polako` checks at startup rather than
failing an hour into an unattended run.


## Install

The skill installs as a Claude Code plugin. This repository doubles as its own
marketplace, so there is no clone step:

```bash
claude plugin marketplace add scharissis/polako
claude plugin install polako@scharissis
```

Restart Claude Code, and `/polako:implement-issue 48` is available. Claude
prefixes plugin skills with the plugin name, which is why the command is not
`/implement-issue` on this path.

Then the binary:

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

Prebuilt binaries for Linux, macOS and Windows are attached to each tagged
release, and are the easiest option on a machine without Go.

The two halves are one release and are meant to move together. Installing the
skill by hand, updating, pinning to a version, and setting a project up for
your whole team are all in [docs/install.md](docs/install.md).

## Usage

The binary takes a verb — `work`, `status` or `stats` — and a bare `polako`
prints that table rather than starting anything: an unattended agent loop
should take a word that says so.

Work the whole backlog of the repository in the current directory:

```bash
polako work
```

Drive a repository somewhere else, and stop after the first issue is done with
— merged, or parked for a human. A good way to try it out:

```bash
polako work -dir ../my-project -once
```

Only work issues carrying a label, and check GitHub more often:

```bash
polako work -label ready-for-claude -poll 90s
```

Leave a couple of issues alone this time round:

```bash
polako work -skip 12,17
```

Work strictly lowest-first, waiting on any issue that stops to ask you
something:

```bash
polako work -strict-order
```

See what it would do to an unfamiliar repository, without doing any of it:

```bash
polako work -dir ../someone-elses-project -dry-run
```

Be told when it needs you, instead of finding out in the morning:

```bash
polako work -notify ~/bin/tell-me
```

Ask where the backlog stands, from anywhere — including about a shift running
on another machine:

```bash
polako status -repo scharissis/polako
```

Ask what all of that cost, once some runs have been recorded:

```bash
polako stats
```


## Flags

`polako work` takes about twenty flags, and `status` and `stats` take their own
smaller sets. They are all in [docs/reference.md](docs/reference.md), along with
`-dry-run`, `-notify`, `-remote` and the `POLAKO_*` environment defaults.

## Security

An unattended run is a Claude session whose only input is issue and comment
text, which on a repository that accepts outside issues is written by anyone.
Two things bound it: the tool allowlist, enforced by Claude Code rather than by
the model's good behaviour, and `-label`, which means a maintainer has to opt
each issue in. On a public repository the second one is required, and `polako
work` refuses to start without it. The reasoning, and what those bounds do not
cover, is in [docs/security.md](docs/security.md).

## Documentation

| Page | What is in it |
| --- | --- |
| [docs/behaviour.md](docs/behaviour.md) | What polako does when a run crashes, an issue stalls, a PR goes red, or it needs a human. |
| [docs/install.md](docs/install.md) | Every install path, updates, pinning, and using it on another project. |
| [docs/reference.md](docs/reference.md) | Every flag, plus `-dry-run`, `-notify`, `-remote`, environment defaults and `polako status`. |
| [docs/run-data.md](docs/run-data.md) | What each run records, spending caps, and the `polako stats` report. |
| [docs/security.md](docs/security.md) | The threat model, the tool allowlist, the `-label` gate, and what leaves your machine. |
| [docs/releasing.md](docs/releasing.md) | Cutting a release: the two tags, the two PRs, and what to bump. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Running the tests, the eval suite, and both halves from a working tree. |

## License

MIT — see [LICENSE](LICENSE).
