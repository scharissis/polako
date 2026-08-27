# polako

[![ci](https://img.shields.io/github/actions/workflow/status/scharissis/polako/ci.yml?branch=main&label=ci)](https://github.com/scharissis/polako/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/scharissis/polako?color=orange)](https://github.com/scharissis/polako/releases/latest)
[![license](https://img.shields.io/github/license/scharissis/polako)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/scharissis/polako)](go.mod)
[![claude code plugin](https://img.shields.io/badge/claude%20code-plugin-8A63D2)](#install)
[![docs](https://img.shields.io/badge/docs-polako-blue)](docs/)

**polako works a GitHub issue backlog to zero.** It takes the lowest open issue,
hands it to Claude Code, waits for you to merge the pull request, and moves on
to the next one. It runs unattended, one issue at a time. It never merges
anything itself.

*Polako* is Croatian for "take it slow".

![polako status printing the state of a backlog, then polako work -dry-run resolving the next issue and printing the exact claude invocation it would run](docs/demo.gif)

There are two halves, and they ship as one release:

- **The `/implement-issue` skill** takes one issue from research to a plan to a
  pull request. You can use it on its own, in any Claude Code session.
- **The `polako` binary** supervises the whole queue. It runs the skill on the
  next issue, watches the pull request, repairs it when CI goes red, and
  advances when you merge. One stdlib-only Go binary, no dependencies.

## How it works

```
lowest open issue with no sub-issues and no `needs-human`, `proposed`
or `awaiting-answer` label
   ↓
claude -p "/implement-issue N"        ← headless; milestones on your terminal,
   ↓                                    the full stream in a per-shift log
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

Only one issue is ever in flight. polako moves on when the issue it is working
on has merged, or when it has given up on it and left it for you. So every run
starts from a branch that already contains the previous merge, and two runs
cannot conflict with each other.

You have two jobs, both on GitHub. Answer a question when a run asks one on an
issue thread, and merge the pull requests. Neither is on a clock. Nothing else
needs you.

[docs/behaviour.md](docs/behaviour.md) is the long version: what happens when a
run crashes, when an issue cannot be finished, when a PR goes red, and when
polako needs you.

## Requirements

- [`claude`](https://claude.com/claude-code), authenticated
- [`gh`](https://cli.github.com), authenticated (`gh auth login`)
- `git`
- Go 1.26+, only if you build from source

polako checks for all three at startup, rather than failing an hour into an
unattended run.

## Install

The skill installs as a Claude Code plugin. This repository is its own
marketplace, so there is nothing to clone:

```bash
claude plugin marketplace add scharissis/polako
claude plugin install polako@scharissis
```

Restart Claude Code and `/polako:implement-issue 48` is available. Claude
prefixes plugin skills with the plugin name, so the command is not
`/implement-issue` on this path.

Then the binary:

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

Prebuilt binaries for Linux, macOS and Windows are attached to every release,
which is easier on a machine without Go.

Both halves come from the same release and are meant to move together.
Installing the skill by hand, updating, pinning a version and setting a project
up for your team are all in [docs/install.md](docs/install.md).

## Try it

Start by looking. `-dry-run` resolves the next issue and prints the command it
would run, and does nothing else:

```bash
polako work -dir ../my-project -dry-run
```

Then work a single issue and stop:

```bash
polako work -dir ../my-project -once
```

When you trust it, let it work the whole backlog and tell you when it needs
you:

```bash
polako work -notify ~/bin/tell-me
```

You can ask where things stand at any time, from any machine, including about a
shift running somewhere else:

```bash
polako status -repo scharissis/polako
```

```
scharissis/polako
  ready         3 issues — #14, #19, #23
  awaiting you  1 issue — #9 (quiet 26h)
  parked        1 issue — #5, labelled needs-human
  proposed      2 issues — #27, #28, labelled proposed
  containers    1 issue — #12, tracking sub-issues rather than work
  next          #14 — its branch already has PR #61, so it would wait on that rather than run the skill again

open prs on issue branches
  pr   branch    issue  mergeable  checks              review                       url
  #61  issue-14  #14    mergeable  failing (test-mac)  clear                        https://github.com/scharissis/polako/pull/61
  #58  issue-19  #19    mergeable  passing             answered, awaiting re-review  https://github.com/scharissis/polako/pull/58

needs you: reply on #9; review and merge PR #58; decide what to do about #5 (drop needs-human to requeue); curate #27, #28 (drop proposed to queue them)
```

A shift ends by telling you what it did:

```
summary: 3 issues merged, 1 issue parked, $35.00 spent, 6h12m of wall clock
  merged  #14 ($9.80), #15 ($12.40), #17 ($8.60)
  parked  #16 ($4.20) — the run completed but produced no PR and no questions
```

## The rules it follows

- **One issue at a time.** Never two. That is what makes the runs unable to
  conflict.
- **Nothing merges itself.** polako opens, updates and repairs pull requests. It
  never merges one, and it never commits to your default branch.
- **All the state is in GitHub** — issues, labels, comments, branches, PRs. Kill
  polako at any point and start it again later. It works out where things stand
  by asking GitHub, not by reading anything it saved.
- **An issue it cannot finish is parked, not retried forever.** It gets a
  `needs-human` label and a comment saying what happened, and the shift carries
  on with the rest of the backlog.
- **Your checkout is never written to.** polako fast-forwards your default
  branch so a review has the right base, and refuses rather than rebase, reset
  or commit.
- **Issue text is data, not instructions.** On a repo that takes issues from
  outside your team, that text is written by strangers, and the skill is told to
  read it as a description of a change rather than as orders.

## Why "polako"?

In the Balkans, "polako!" is what you say to someone who's rushing. Slow down,
you'll get there. It's more a way of living than a word.

It turns out to be a good way to ship code, too. One pull request at a time is
one you'll actually read. Ten at once and you skim, and the stuff you skim is
the stuff that bites you later.

## What it costs

Real money, and more than you might guess. Every run records what it spent, so
these are measured rather than estimated — from one 33-hour shift on a small Go
project, eight issues finished:

| | |
| --- | --- |
| Issues that merged | 7 of 8 |
| Cost per issue | $10.38 mean, $11.31 median |
| Runs per issue | 1.4 mean |
| Size of the change | +505 / −40 across 5 files, median |
| Time from PR opened to merged | 10m median, because someone was watching |

Your numbers will differ, and the ones that move them most are how big your
issues are and how often runs crash. A crashed run is resumed, and the resume
pays to read the context again, so a rough night costs noticeably more per
merged PR than a smooth one.

Dollars are the Claude CLI's API-equivalent pricing. On an API key that is real
money; on a subscription plan it is notional. Run `polako stats` for your own
figures, and set `-max-cost`, `-max-issue-time` or `-max-session-cost` if you
want a ceiling. [docs/run-data.md](docs/run-data.md) has the whole report.

## What it will not do

- **It will not merge for you**, and there is no flag that changes that.
- **It is not a sandbox.** The tool allowlist narrows what a run can do, but
  build commands run whatever your repository's scripts contain. Point `-dir` at
  repositories you would run `make test` in yourself.
- **It is not finished.** This is pre-1.0. Flags and defaults still change, and
  the release notes say when.
- **It cannot tell a good issue from a bad one.** A vague issue produces either
  a question on the thread or a park, and both cost money to find out.
- **The skill's eval suite has not had a green run yet.** Changes to the skill
  are still verified by driving a real issue by hand. See
  [evals/README.md](evals/README.md).

## Flags

`polako work` takes around two dozen flags, and `status` and `stats` have their own
smaller sets. They are all in [docs/reference.md](docs/reference.md), together
with `-dry-run`, `-notify`, `-remote` and the `POLAKO_*` environment defaults.
Any flag can take its default from the environment, so a preference you always
want can live in your shell profile.

## Security

An unattended run is a Claude session whose only input is issue and comment
text. On a repository that accepts issues from outside your team, anyone writes
that input. Two things bound it. The tool allowlist is enforced by Claude Code
rather than by the model behaving well, and `-label` means a maintainer has to
opt each issue in before polako will touch it. On a public repository that
label gate is required, and `polako work` refuses to start without one.

Nothing you run leaves your machine unless you ask. Two exceptions are named
outright: `-remote`, on by default, registers each run with Remote Control so
you can watch it from claude.ai or the app under your own account, and
`-remote=false` turns it off; `-post-summary`, off by default, comments one line
of numbers on your own merged PR. [docs/security.md](docs/security.md) has the
reasoning and the limits, and [SECURITY.md](SECURITY.md) says how to report a
vulnerability privately.

## Questions

**Can I use the skill without the binary?** Yes. Run
`/polako:implement-issue 48` in Claude Code and it takes that one issue to a PR.
The binary exists to run it over a whole backlog while you are asleep.

**What happens if it breaks something at 3am?** It cannot merge, so nothing it
does reaches your default branch without you. An issue it cannot finish is
parked and the shift carries on. `-notify` runs a command of yours when that
happens, so you can be told rather than find out in the morning.

**Does it work with my language?** Yes — `-dir` points anywhere. The one thing
worth tuning per project is the tool allowlist, so a build command it needs
never stops on a permission prompt.

**How is this different from a hosted coding agent?** Those run on someone
else's machine and keep their own state. polako runs on yours, under the Claude
Code login you already have, and keeps every piece of orchestration state in
GitHub itself. There is no database and no dashboard: kill it whenever, restart
whenever, and read the whole picture off the issue tracker.

**Can I run it on a public repository?** Yes, with `-label`. Anyone can open an
issue on a public repo, and open issues are what a shift works, so polako
refuses to start there without a maintainer-applied label gate — or an explicit
`-ungated` if you mean it.

**"Wouldn't ten agents be ten times faster?"** They'd open ten pull requests,
which isn't the same thing. Somebody still has to read them, and they all
branched from a version of the code that stopped being true the moment the
first one merged.

polako does one. When you merge it, the next one starts from what you just
merged.

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
