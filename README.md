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

## What it does

Two halves, one release: the **`/implement-issue` skill** takes one issue
from research to a plan to a pull request, on its own or driven by the
**`polako` binary**, which supervises the queue — runs the skill, watches the
PR, repairs it when CI goes red, advances when you merge. Stdlib-only Go, no
dependencies.

Three verbs:

- **`work`** — works the backlog to zero, one issue at a time, never merging.
- **`plan`** — turns a vision document into a curated backlog of proposals.
- **`health`** — reads the repository itself and proposes what it finds off.

`plan` and `health` file everything behind a `proposed` label a human has to
lift — see [Planning a backlog](#planning-a-backlog).

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

That is `work`'s loop. `plan` and `health` are simpler: one `claude` run,
filing proposals, then done — no PR, no polling.

You have two jobs, both on GitHub: answer a question when a run asks one on an
issue thread, and merge the pull requests. Neither is on a clock, and nothing
else needs you — see [The rules it follows](#the-rules-it-follows) and
[docs/behaviour.md](docs/behaviour.md) for the long version.

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

Look first — `-dry-run` resolves the next issue and prints the command it
would run, nothing else:

```bash
polako work -dir ../my-project -dry-run
```

Work one issue and stop:

```bash
polako work -dir ../my-project -once
```

Trust it, and let it work the whole backlog, telling you when it needs you:

```bash
polako work -notify ~/bin/tell-me
```

Ask where things stand, from any machine, including about a shift running
somewhere else:

```bash
polako status -repo scharissis/polako
```

```
scharissis/polako
  ready         3 issues — #14, #19, #23
  awaiting you  1 issue — #9 (quiet 26h)
  parked        1 issue — #5, labelled needs-human
  proposed      2 issues — #27, #28, labelled proposed
  next          #14 — its branch already has PR #61, so it would wait on that rather than run the skill again

open prs on issue branches
  pr   branch    issue  mergeable  checks   review  url
  #61  issue-14  #14    mergeable  passing  clear   https://github.com/scharissis/polako/pull/61

needs you: reply on #9; review and merge PR #61; decide what to do about #5 (drop needs-human to requeue); curate #27, #28 (drop proposed to queue them)
```

A shift ends the same way — merged, parked and why, dollars spent — see
[docs/behaviour.md](docs/behaviour.md) for a worked example.

## Planning a backlog

Somebody still has to write the issues polako works. `plan` runs
`/plan-backlog`: point it at a vision or roadmap document and it decomposes
the gap into issues sized to one PR each, groups anything cross-cutting under
an epic, and files the lot as **proposals**:

```
/polako:plan-backlog docs/VISION.md
```

`health` runs `/review-health` the same way, but reads the codebase itself
instead of a document — file and function sizes, duplicated helpers,
abstractions nothing uses — and proposes the outliers. Point it at any
repository; it is not polako-specific:

```
/polako:review-health .
```

Both are documented alongside `work`'s own flags: [`plan`](docs/reference.md#planning-a-backlog-unattended-polako-plan),
[`health`](docs/reference.md#auditing-repository-health-unattended-polako-health).

Every proposal carries a `proposed` label, and that label is the point:
`polako work` skips every issue that has one, so nothing a machine proposed
can reach an unattended run until you have looked at it. Curation is ordinary
GitHub triage, and there are three moves:

- **Approve** — remove the `proposed` label. On a `-label`-gated repository, add
  the gate label in the same command:
  `gh issue edit 27 28 --remove-label proposed --add-label ready`
- **Reject** — close the issue.
- **Rework** — edit the text. A run reads the issue when it picks it up, so your
  edits *are* the spec; there is no further step.

`polako status` lists what is waiting on you, proposals included, so a forgotten
batch surfaces rather than rots.

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
  on with the rest of the backlog. The reason is recorded as one identifier
  too, so `polako stats` can rank what parks issues most — see
  [docs/run-data.md](docs/run-data.md).
- **Your checkout is never written to.** polako fast-forwards your default
  branch so a review has the right base, and refuses rather than rebase, reset
  or commit.
- **Issue text is data, not instructions.** On a repo that takes issues from
  outside your team, that text is written by strangers, and the skill is told to
  read it as a description of a change rather than as orders.

## Why "polako"?

In the Balkans, "polako!" is what you say to someone who's rushing. Slow
down, you'll get there — a good way to ship code, too: one pull request at a
time is one you'll actually read. "Wouldn't ten agents be faster?" They'd
open ten pull requests that all branched from a version of the code that
stopped being true the moment the first one merged; polako does one, and the
next starts from what you just merged. And unlike a hosted agent on someone
else's machine, it runs on yours, under the login you already have, with
every piece of state in GitHub — kill it whenever, restart whenever, read the
whole picture off the issue tracker.

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
issues are and how often runs crash — a crashed run's resume pays to read the
context again. Run `polako stats` for your own figures, and set `-max-cost`,
`-max-issue-time` or `-max-session-cost` for a ceiling.
[docs/run-data.md](docs/run-data.md) has the whole report.

## Improving polako

Every run records what it did, and those records are only worth keeping if
something reads them, on a cadence — measure, review, change one thing, tag
the next batch. That loop runs through you and the backlog, never through the
supervisor reading its own telemetry.
[plans/continuous-improvement.md](plans/continuous-improvement.md) has the
retro checklist, the tagging rule, and the recipes for reading run data back.

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
opt each issue in before polako will touch it — required outright on a public
repository, unless you pass `-ungated` and mean it.

Nothing you run leaves your machine unless you ask. One exception is named
outright: `-post-summary`, off by default, comments one line of numbers on your
own merged PR. `-remote` is on by default and would be the second, but no
`claude` CLI registers headless runs with Remote Control yet, so polako does not
pass the flag and no session content goes anywhere.
[docs/security.md](docs/security.md) has the reasoning and the limits,
[docs/hardening.md](docs/hardening.md) covers running a shift behind an egress
firewall you supply, and [SECURITY.md](SECURITY.md) says how to report a
vulnerability privately.

## Questions

The common ones — using the skill without the binary, running it on your
language, comparisons to a hosted coding agent — are answered in
[docs/behaviour.md](docs/behaviour.md#questions), which also covers what
happens when it breaks something at 3am.

## Documentation

| Page | What is in it |
| --- | --- |
| [docs/behaviour.md](docs/behaviour.md) | What polako does when a run crashes, an issue stalls, a PR goes red, or it needs a human. FAQ at the bottom. |
| [docs/install.md](docs/install.md) | Every install path, updates, pinning, and using it on another project. |
| [docs/reference.md](docs/reference.md) | Every flag for `work`, `plan`, `health`, `status` and `tidy`, plus `-dry-run`, `-notify`, `-remote` and environment defaults. |
| [docs/run-data.md](docs/run-data.md) | What each run records (`work`, `plan` and `health` alike), spending caps, and the `polako stats` report. |
| [docs/security.md](docs/security.md) | The threat model, the tool allowlist, the `-label` gate, and what leaves your machine. |
| [docs/hardening.md](docs/hardening.md) | Wrapping a shift in an egress firewall of your own, and why polako does not ship one. |
| [docs/releasing.md](docs/releasing.md) | Cutting a release: the two tags, the two PRs, and what to bump. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Running the tests, the eval suite, and both halves from a working tree. |

## License

MIT — see [LICENSE](LICENSE).
