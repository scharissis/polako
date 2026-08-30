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
  pull request. You can use it on its own, in any Claude Code session. Its
  companion `/plan-backlog` writes the issues in the first place, from a vision
  document — see [Planning a backlog](#planning-a-backlog).
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
  containers    1 issue — #12 (2/5 closed)
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
  parked  #16 ($4.20) — the run completed without opening a PR
```

## Planning a backlog

Somebody still has to write the issues polako works. The `/plan-backlog` skill
does the clerical half: point it at a vision or roadmap document and it reads
that against the code as it stands, decomposes the gap into issues sized to one
pull request each, groups anything cross-cutting under an epic whose body holds
the design, and files the lot as **proposals**.

```
/polako:plan-backlog docs/VISION.md
```

A proposal is an ordinary issue carrying a `proposed` label, and that label is
the point: `polako work` skips every issue that has one, so nothing a machine
proposed can reach an unattended run until you have looked at it. Curation is
ordinary GitHub triage, and there are three moves:

- **Approve** — remove the `proposed` label. On a `-label`-gated repository, add
  the gate label in the same command:
  `gh issue edit 27 28 --remove-label proposed --add-label ready`
- **Reject** — close the issue.
- **Rework** — edit the text. A run reads the issue when it picks it up, so your
  edits *are* the spec; there is no further step.

`polako status` lists what is waiting on you, proposals included, so a forgotten
batch surfaces rather than rots.

Each proposal carries acceptance criteria, pointers into the code, what is out
of scope, and a size — `Estimate: M — likely 1–2 runs`. The size is the model's
judgement of the work's shape, not a price; what a run actually costs comes from
your own history, via `polako stats`.

This is also a `polako plan` verb — the unattended half, with caps, milestones,
a much narrower tool allowlist, and a supervisor-side pass that enforces the
`proposed` gate rather than trusting the model to apply it. It records one line
of numbers per run like every other verb, and `-notify` fires a `proposed`
event when a run leaves a backlog to curate. See
[docs/reference.md](docs/reference.md#planning-a-backlog-unattended-polako-plan).

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

## Improving polako

Every run records what it did, and those records are only worth keeping if
something reads them. That something is you, on a cadence. The loop —
measure, review, change one thing, tag the next batch — runs through the
operator and the backlog, never through the supervisor reading its own
telemetry. [plans/continuous-improvement.md](plans/continuous-improvement.md)
is the long version and the reasoning.

**After a shift worth learning from**, the retro:

1. `polako stats -by shift` to find the batch, then
   `polako stats -shift <id> -by issue` to see inside it.
2. Read the **park reasons** line. A count of parks says how often; that line
   says which half of the tool the next change belongs in.
3. For every parked issue and every outlier — cost or runs well above the
   batch median — run `polako stats -runs`, take the session id from its row,
   and reopen the transcript:

   ```bash
   claude --resume 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9
   ```

   Answer one question: *what would have let this run finish?*
4. Classify the answer — skill wording, a missing tool, supervisor logic, an
   under-specified issue, a model too weak for that step. Each lands in a
   different place.
5. File it as an issue on this repository. polako then works it, which is why
   the loop is self-hosting. Do it promptly: the resumable transcript is the
   Claude CLI's, kept under the session id, and all polako keeps is the
   shift's own [log](docs/reference.md#the-shift-log--log) — the event stream
   as it was narrated, which you cannot resume from.

**Change one thing, then tag the next batch.** Any change to a `SKILL.md`, to
the model, or to a strategy knob — `-stall`, `-retries`, `-poll`, the spend
caps — runs its next batch under a fresh `-run-tag` and gets a row in
[plans/experiments.md](plans/experiments.md). An untagged batch after a change
can never be compared to anything, and a verdict nobody wrote down is one you
will pay to measure again next year.

**After a `claude` CLI upgrade**, count run statuses by version. The `no-skill`
status exists because an upgrade once changed behaviour silently, and this is
what catches the next one. `stats` keeps its `-by` list short on purpose, so
this is a one-liner over the JSONL rather than a flag:

```bash
jq -rs 'map(select(.kind=="run")) | group_by(.claude_version)[]
        | "\(.[0].claude_version)  \(length) runs  " +
          ([.[].status] | group_by(.) | map("\(.[0]) \(length)") | join(", "))' \
  ~/.polako/metrics/*.jsonl
```

```
2.1.84  1 runs  ok 1
2.1.85  3 runs  crash 1, no-skill 1, ok 1
```

**Occasionally, audit past the merge.** Merge rate is the headline number and
it is blind to the failure that matters most: a pull request that merged and
was then reverted, or hand-patched two days later, counts as a win. GitHub
knows, so ask it. Run these in the repository polako is working:

```bash
# what polako merged (issue- is -branch-prefix's default)
gh pr list --state merged --search 'head:issue-' --limit 20 \
  --json number,headRefName,title,mergedAt

# did anything revert one of those merges?
git log origin/main --oneline --grep='^Revert' --since=4.weeks

# did somebody patch the same files soon afterwards? (one PR at a time)
# The window is the fortnight after *that* merge, not the last fortnight:
# anchored to now, an older PR reports a clean bill it has not earned. git
# cannot do the arithmetic — it reads '<date> + 2 weeks' as nothing and says
# nothing — so jq adds the fortnight in seconds.
pr=111
from=$(gh pr view "$pr" --json mergedAt --jq '.mergedAt')
to=$(gh pr view "$pr" --json mergedAt --jq '.mergedAt | fromdate + 1209600 | todate')
paths=$(gh pr view "$pr" --json files --jq '.files[].path')
# An empty $paths would drop the pathspec and list every commit in the window.
[ -n "$paths" ] && git log origin/main --oneline --no-merges \
  --since="$from" --until="$to" -- $paths
```

A hit is a finding, and a finding becomes an issue. None of this is a verb or
a flag, deliberately: a recipe earns promotion by being typed often enough to
resent, the same rule that has kept `stats` small.

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
| [docs/hardening.md](docs/hardening.md) | Wrapping a shift in an egress firewall of your own, and why polako does not ship one. |
| [docs/releasing.md](docs/releasing.md) | Cutting a release: the two tags, the two PRs, and what to bump. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Running the tests, the eval suite, and both halves from a working tree. |

## License

MIT — see [LICENSE](LICENSE).
