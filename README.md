# backlog-drain

Point it at any GitHub repository and it works the issue backlog to zero — one
issue at a time, strictly in ascending order, unattended.

It has two halves:

| Half | What it does |
| --- | --- |
| **`/implement-issue` skill** | Takes a *single* issue from research → `PLAN.md` → implementation → code review → pull request. Usable on its own, interactively. |
| **`backlog-drain` binary** | Supervises the *whole queue*: runs the skill on the lowest open issue, waits for that PR to merge — or parks the issue for a human — then advances. Stdlib-only Go, single binary, any platform. |

The binary never has two issues in flight: it advances only once the issue it is
working is merged, or parked for a human. Every run therefore branches from a
default branch that already contains the previous merge, so sequential runs
cannot conflict with each other.

## How it works

```
lowest open issue without a `needs-human` or `awaiting-answer` label
   ↓
claude -p "/implement-issue N"        ← headless, streamed to your terminal
   ↓
PR opened?  ──no──►  issue labelled `awaiting-answer`?  ──yes──►  put it down, advance to the next
   │                          │                                  (re-run it when the reply lands)
   │                          └──no──►  crashed? resume the same session (-retries)
   │                                       │
   │                                       └──out of attempts──►  park it, advance to the next
   ↓ yes
wait for merge (-poll)               ← rebases if GitHub reports CONFLICTING,
   ↓                                   fixes + re-pushes if the checks go red
   ↓                                   or a reviewer requests changes
close the issue, remove the worktree, advance to the next
```

**Your checkout is kept level with origin, and never written to.** Before each
issue and after each merge, the drain fast-forwards `-dir`'s default branch. It
has to: a drain never pulls — you merge on GitHub and it only watches — so that
branch falls a commit behind on every merge, and it is what the skill cuts a new
branch from and what a code review resolves a base against. Left behind, a
review silently diffs the branch against a base that predates the last merge and
folds an already-merged PR into what it reviews. It is `--ff-only` and nothing
else: if your default branch has a commit of its own, or work in the way, or you
are sitting on another branch, the drain says so in its log and leaves it
exactly as it is.

**All state lives in GitHub** — issues, comments, PRs, branches. The process
itself is stateless and restart-safe: kill it at any point, rerun it later, and
it re-derives where things stand. If a PR already exists for `issue-N`, it
never re-runs Claude on that issue; it goes straight to waiting. (The one thing
it writes locally is a line of numbers per run, which nothing reads back — see
[Run data & cost tracking](#run-data--cost-tracking).)

**One issue that cannot be finished does not end the run.** An issue whose run
produced nothing, whose retries ran out, whose PR was closed unmerged, whose
conflicts could not be rebased away, or whose CI stayed red is *parked*:
`backlog-drain` labels it
`needs-human`, comments on the thread saying what happened, and moves on to the
next issue. The label is what takes it out of the queue, so a later run does not
pick it straight back up — remove the label to put it back in. The process exits
0 and prints a summary of what merged, what parked and why:

```
summary: 3 issues merged, 1 issue parked, 6h12m of wall clock
  merged  #14, #15, #17
  parked  #16 — the run completed but produced no PR and no questions
```

Parking preserves the no-conflict guarantee: only one issue is ever in flight,
and a parked issue is simply not in flight.

**A run that has to ask something labels the issue `awaiting-answer`**, and the
supervisor keys off that label rather than off the thread getting busier. The
distinction matters because plenty of things comment on an issue that are not
answers to anything — CI, a linked-PR notice, a bot, a passer-by — and treating
those as a question left the drain waiting on a reply nobody knew was expected.
The label is also the only sign on GitHub that an issue is waiting on *you*;
reply on the thread and the next check picks it up. `backlog-drain` declares the
label on startup if the repository does not have it yet, and the run that folds
your answer in is what removes it — or the park, if the issue is handed back
before anyone gets that far, since a parked issue waits on a decision rather
than on a reply.

**A question does not hold up the queue either.** A flagged issue is put down
and the next one is worked, exactly the way a park advances past an
unimplementable issue. It is picked back up when the reply lands and nothing
else is left to work, or straight away once you remove the label by hand. The
guarantee that runs cannot conflict is that only one issue is ever *in flight*,
not that they are worked in strict numeric order — and an issue nobody is
working is not in flight.

One thing does weaken, and it is the reason `-strict-order` exists. A put-down
issue keeps the worktree and branch its first run created, so when work resumes
it resumes from the base that run started on — not from the merges that landed
while it waited. A textual clash with one of those shows up as a `CONFLICTING`
PR and is rebased automatically; a semantic one — a helper renamed by a merge in
between — is not, and lands as a PR that passed its own tests and breaks the
default branch.

Pass `-strict-order` to turn all of that off and get the old behaviour: the
queue stays in strict ascending order, an issue awaiting an answer blocks every
issue behind it until you reply, and nothing merges under a waiting branch. A
drain that ends with issues still waiting says so:

```
summary: 3 issues merged, 0 issues parked, 1 issue awaiting an answer, 4h02m of wall clock
  merged  #14, #15, #17
  waiting #16 — reply on the thread and the next drain picks them up
```

One caveat worth knowing: the comment count a wait compares against lives in
memory, so a restarted drain cannot tell whether a reply arrived while it was
down. It spends one run per already-flagged issue finding out — the skill
re-reads the thread and stops again without re-asking if the answer is not there
yet — and holds a baseline from then on.

**An open PR that goes red is repaired, not just watched.** Each poll reads the
PR's check rollup and its reviews as well as its mergeability. A conflict gets a
rebase; a failing check gets a run that reads the failing job logs, fixes the
cause, runs the suite locally and pushes. All three are bounded by `-retries`,
and none may merge, open a second PR, or rerun the workflow it was sent to
diagnose.

Two rules keep that from turning into a treadmill. Nothing is dispatched while a
check is still running — a suite that has not finished can only add to the list
of failures, and diagnosing half of one wastes the run. And a run that finishes
without moving the branch has said all it is going to: the same logs against the
same commit reach the same place, so the issue parks for a human instead of
going round again. Only conclusions a change to the branch could plausibly fix
count as failures at all — `NEUTRAL` and `SKIPPED` are green, and a check
stopped on a person (`CANCELLED`, or a deployment gate at `ACTION_REQUIRED` or
`WAITING`) is reported as `needs a human` rather than dispatched at. That last
distinction matters twice over: a gated check is not a suite still running, so a
real failure sitting beside one is still seen.

**Requesting changes on the PR is an instruction, not a dead end.** Review the
PR as you would anyone's; if you ask for changes, the next poll dispatches a run
that reads what you wrote — both the review bodies and the comments left on
individual lines of the diff — makes the changes, gets the suite passing, and
pushes. Then it goes back to waiting, because a re-review is yours to give: the
run may not dismiss or resolve the review, and still may not merge.

Whether a review has been answered yet is read off GitHub rather than remembered,
so a drain restarted mid-flight reaches the same conclusion: a review counts as
outstanding until the branch carries a commit newer than it. Two consequences
worth knowing. A rebase — including one the conflict remediation performs — gives
every commit a fresh date and so reads as an answer; that is deliberate, since
the review then points at a diff that no longer exists, but it does mean a
conflicting PR with a review open on it comes back to you for a fresh look. And
the same treadmill rule applies: a run that finishes without moving the branch
has said all it is going to, so the issue parks rather than re-reading the same
comments every poll. If your project sets no branch-protection review
requirement — most do not — this still works, because it reads the reviews
themselves and not just GitHub's summary `reviewDecision`, which is empty in
that case.

**Refused credentials stop the drain immediately.** A resume cannot mint a new
token, so retrying one spends `-retries` × several minutes reaching the
identical 401 and then reports it as a crash. Instead the run ends the drain
with the fix in the last line: check `claude auth status`, then
`claude auth login` — or `claude setup-token`, on an unattended host. State
lives in GitHub, so starting the drain again once the token works picks up
exactly where it stopped.

**Human touchpoints are deliberately just two**, both on GitHub:

1. Answering clarification questions the skill posts on an issue thread.
2. Merging the PR. Nothing merges itself.

Neither one is on a clock: an unanswered question or an unmerged PR simply
waits. Both wait out of the queue rather than in it, as a parked issue does, so
none of them holds anything else up.

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

Restart Claude Code, and `/backlog-drain:implement-issue 48` is available.

Note the namespace. Claude prefixes plugin skills with the plugin name, so the
command is *not* `/implement-issue` on this path. The supervisor's `-skill`
default matches the plugin form; see [the hand install](#the-skill-by-hand) for
the other one. To see what a session actually has, the `init` event lists them:

```bash
claude -p "hi" --output-format stream-json --verbose | head -1
```

Both commands take a `--scope`:

| Scope | Where it is declared | Use it for |
| --- | --- | --- |
| `user` *(default)* | `~/.claude/settings.json` | Your own machine, every project. |
| `project` | the repo's `.claude/settings.json` | Committing the marketplace + plugin so collaborators on *that* repo get the skill automatically. |
| `local` | the repo's git-ignored local settings | Trying it on one project without committing anything. |

So to make every contributor to some project pick the skill up, run both
commands with `--scope project` inside that project and commit the resulting
`.claude/settings.json`. They still each need read access to this repo.

To update, see [Getting updates](#getting-updates). To remove:

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

A skill installed this way is invoked bare, with no plugin prefix, so the
supervisor needs telling:

```bash
backlog-drain -skill implement-issue
```

Do one or the other, not both — two copies of the same skill drift apart
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
release, and are the easiest option on a machine without Go. They are stamped
with their tag, so `backlog-drain -version` tells you what you are running.

### Getting updates

**Nothing updates on its own by default.** Auto-update is off for third-party
marketplaces, so an installed plugin stays exactly where it is until you ask:

```bash
claude plugin marketplace update scharissis && claude plugin update backlog-drain
```

Then `/reload-plugins`, or restart. **Upgrade the binary in the same breath** —
the two halves are one release, and mixing them is not a supported combination:

```bash
GOPRIVATE=github.com/scharissis/* go install github.com/scharissis/backlog-drain/cmd/backlog-drain@latest
```

If they end up mismatched anyway, the supervisor says so at startup and names
both versions. It is a warning, not a refusal — but the supervisor finds a PR by
the branch name the skill chooses, so a mismatched pair is a bug waiting for a
confusing moment.

To let it happen automatically instead: `/plugin` → **Marketplaces** →
`scharissis` → **Enable auto-update**. Claude Code then checks after a session
starts, with a random delay of up to ten minutes, and the new version loads on
`/reload-plugins` or at the next launch — never mid-session. The binary is not
covered; that is still yours to run.

To hold a machine at one release, pin the marketplace itself and it stops
moving:

```bash
claude plugin marketplace add scharissis/backlog-drain#backlog-drain--v0.4.0
```

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

Drive a repository somewhere else, and stop after the first issue is done with
— merged, or parked for a human. A good way to try it out:

```bash
backlog-drain -dir ../my-project -once
```

Only work issues carrying a label, and check GitHub more often:

```bash
backlog-drain -label ready-for-claude -poll 90s
```

Leave a couple of issues alone this time round:

```bash
backlog-drain -skip 12,17
```

Work strictly lowest-first, waiting on any issue that stops to ask you
something:

```bash
backlog-drain -strict-order
```

Ask what all of that cost, once some runs have been recorded:

```bash
backlog-drain stats
```

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `.` | Path to the repository's main checkout. |
| `-claude` | `claude` | The Claude Code binary to invoke. |
| `-skill` | `backlog-drain:implement-issue` | Slash command run once per issue. Plugin skills are namespaced `<plugin>:<skill>`; pass `-skill implement-issue` if you copied the skill into `~/.claude/skills` instead. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how PRs are matched back to issues. |
| `-label` | *(none)* | Only process issues carrying this label. Doubles as an access control — see [Security](#security). |
| `-tools` | *(see below)* | `--allowedTools` for unattended runs. **Replaces** the default set. |
| `-add-tools` | *(none)* | Extra `--allowedTools` entries, **appended** to `-tools`. |
| `-permission-mode` | `acceptEdits` | Passed to `claude --permission-mode`. |
| `-model` | *(the CLI's own default)* | Passed to `claude --model`. Vary it between batches to compare models — see [Run data & cost tracking](#run-data--cost-tracking). |
| `-poll` | `5m` | Interval between GitHub checks while waiting. |
| `-retries` | `3` | Resume attempts after a crashed run, and the bound on remediation runs against an open PR that is conflicting, red, or carrying a request for changes. A run the API refused to authenticate is never one of them — see below. |
| `-retry-wait` | `30s` | Wait before each resume attempt. |
| `-stall` | `15m` | Kill and resume a run that has emitted no events for this long (`0` disables). |
| `-skip` | *(none)* | Comma-separated issue numbers to skip. Issues labelled `needs-human` are skipped anyway — see [How it works](#how-it-works). |
| `-once` | `false` | Process a single issue to a merge, a park or a question for you, then exit. |
| `-strict-order` | `false` | Work issues in strict ascending order: wait in place on an issue awaiting an answer instead of moving past it. |
| `-run-tag` | *(none)* | Freeform label recorded with every run, so one batch can be compared against another. |
| `-metrics` | `~/.backlog-drain/metrics` | Directory for run-data records, or `off` to write nothing. |
| `-post-summary` | `false` | Comment one line of run numbers on each merged PR. The only thing that shows run data to anybody but you — see [Run data & cost tracking](#run-data--cost-tracking). |
| `-version` | `false` | Print which release this binary is, then exit. Use it when startup warns that the binary and the skill disagree — see [Getting updates](#getting-updates). |

### Setting defaults from the environment

Any flag can take its default from `BACKLOG_DRAIN_<FLAG>`, so a preference you
always want lives in your shell profile instead of on every command line:

```bash
export BACKLOG_DRAIN_POST_SUMMARY=1
```

The name uppercases and swaps `-` for `_`: `-post-summary` reads
`BACKLOG_DRAIN_POST_SUMMARY`, `-retry-wait` reads `BACKLOG_DRAIN_RETRY_WAIT`.
An argument always wins, so a single run can still go the other way with
`-post-summary=false`, and `backlog-drain -h` prints the defaults actually in
force — which is how you see what the environment is doing.

`BACKLOG_DRAIN_METRICS` covers both halves at once: where a drain writes, and
where `stats` reads. `-version` is deliberately *not* settable this way — it is
an action rather than a preference, and `BACKLOG_DRAIN_VERSION` is exactly the
variable a Dockerfile or CI job pins an install with.

A value a flag cannot parse stops the run and names both the variable and the
flag it was setting, rather than being skipped: a preference that was set,
looks set, and quietly does nothing is worse than no preference at all.

## Run data & cost tracking

Every run writes one line of numbers, so you can answer what a drained backlog
actually cost — and which settings are worth changing.

**What is written, in full:** for each `claude` invocation, one JSON object
holding the repository and issue number, why the run happened and what it left
behind (a PR, questions, or neither), its status and exit code, turns, tool-use
count, wall and API duration, tokens (in / out / cache read / cache write, plus
the per-model split), dollars, and the configuration under test — skill, model,
permission mode, `-run-tag`, a hash of the tool allowlist, the strategy knobs,
and the three versions in play (this binary, the installed skill, and the
Claude CLI). One more object per issue records how it ended —
`merged`, `closed_unmerged` or `needs_human` — and, when GitHub could be asked,
what the PR turned out to be: additions, deletions, changed files, how many
reviews it drew, and when it opened and merged. That is one extra `gh pr view`
as each issue ends, and none at all under `-metrics off`.

**What is never written:** issue titles, issue bodies, PR titles, comment text,
review text, diffs, or anything the model said. Reviews are counted, never
quoted. Records hold numbers, identifiers and labels you chose. That is what
makes one of these files safe to hand to a teammate, or paste into an analysis
session, without re-reading it first.

**Where it goes:** `~/.backlog-drain/metrics/<owner>--<repo>.jsonl`, one
append-only file per repository — so deleting one project's data is `rm` on one
file, and aggregating across projects is a glob. Created `0700`, so on a shared
machine the records stay yours. Never inside your checkout: the skill commits
things there, and cost data must not become committable by accident.

**Nothing leaves your machine unless you ask it to.** There is no telemetry
endpoint, no phone-home, and no network path out of the recorder — the binary is
the only thing that meters anything, and it writes to your disk. The single
exception is [`-post-summary`](#putting-it-on-the-pr--post-summary), off unless
you turn it on, which puts one line of numbers on your own merged PR. The skill
half, the part you can install from a marketplace, carries no data collection at
all: it is a prompt.

To write nothing at all:

```bash
backlog-drain -metrics off
```

Records are write-only by design. The drain loop never reads them, no decision
depends on them, and deleting the directory mid-drain changes nothing about
what the supervisor does next — that is what keeps run data compatible with
"all state lives in GitHub". Writes are best-effort: a failure warns once and
the drain carries on.

### Putting it on the PR: `-post-summary`

Off by default. Turned on, each merged PR gets one comment:

```bash
backlog-drain -post-summary
```

> **backlog-drain** — 3 runs, 1 question round, 12.4M tokens, $6.12, 2h14m of
> run time.
>
> <sub>Recorded by backlog-drain v0.5.0, covering the runs this drain
> supervised. Dollars are the Claude CLI's API-equivalent pricing.</sub>

Numbers only, on the PR they describe, readable by exactly the people who can
already see that PR. A run that crashed, stalled or was interrupted never
reported a cost, so when the tally holds one the comment says how many and that
its tokens and dollars are undercounts. It covers the runs *this* drain supervised, and says so: a
supervisor restarted mid-issue reports what it saw, and one that only waited on
a PR an earlier process opened comments nothing rather than claiming a free PR.

To make it your default without typing it, export
`BACKLOG_DRAIN_POST_SUMMARY=1` — see
[Setting defaults from the environment](#setting-defaults-from-the-environment).
Startup says when it is on, so a variable you set months ago in a profile is
never a mystery.

It is independent of `-metrics`, so `-metrics off -post-summary` is the
combination for wanting team visibility and no local files at all. Best-effort
like the rest of run data: a comment that cannot be posted is a log line, never
a failed drain.

### Reading it back: `backlog-drain stats`

`stats` is the binary's one subcommand, and the only thing that ever reads
those files. A bare `backlog-drain` still drains; nothing about the report
touches GitHub or starts a run.

```bash
backlog-drain stats
```

```
run data from /Users/you/.backlog-drain/metrics
  read    2 files, 11 records (1 unreadable line skipped)
  window  2026-08-20T09:00:00Z → 2026-08-24T11:03:11Z (4.1d)
  repos   scharissis/backlog-drain, scharissis/other

issues
  terminal          4 — merged 3 (75%), needs human 1
  in flight         1
  runs per issue    1.5 mean, 1.5 median
  cost per issue    $1.92 mean, $2.00 median
  tokens per issue  4.6M mean, 3.9M median (in 2.2k, out 35.2k, cache read 4.4M, cache write 220.5k)

runs
  total         7 — ok 5, no-turns 1, crash 1
  reasons       implement 4, resume 1, answers 1, remediate 1
  outcomes      opened pr 3, posted questions 1, nothing 3
  work          131 turns, 115 tool uses
  approximated  1 of 7 runs priced from the streamed tally, not a result event

cost
  total          $8.10 over 4.1d ($1.98/day)
  per merged PR  $2.70 across 3 merges
  tokens         19.1M (in 9.4k, out 145.9k, cache read 18.1M, cache write 912k)

human latency
  blocked on answers  1 span — 3h10m median, 3h10m max
  pr open to merge    3 spans — 1h20m median, 2h max (human availability, not the tool)
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-metrics` | `~/.backlog-drain/metrics` | Directory to read records from — the same path the drain writes to. |
| `-repo` | *(every repository)* | Only count records for one repository, `owner/name`. |
| `-since` | *(all of it)* | Only count records newer than this, e.g. `-since 168h`. |
| `-by` | *(none)* | Add a breakdown table: `issue`, `model` or `tag`. |

A window can keep an issue's terminal record while clipping away the runs that
produced it. Those issues still count toward the merge rate, but they cannot be
priced, so the per-issue figures cover only issues with runs inside the window
and say when that differs. Files in the directory that cannot be opened — the
normal case in a shared one, since records are written `0600` — are named in
the report rather than failing it.

Every summable number is derived at read time from the run records — cost per
issue, runs per issue, question rounds, the spans below. That is why an issue
record carries no totals of its own: there are none to go stale, and none the
supervisor has to keep across a restart.

Two of the numbers measure people rather than the tool. **Blocked on answers**
is the gap between the run that posted questions and the re-run that folded the
reply in. **PR open to merge** ends whenever somebody got round to pressing the
button — reported because it is part of the elapsed time, but it is not a
property of the automation, and no change to the skill will move it.

**On PR size:** an issue whose terminal record carries GitHub's answer about
its PR adds a **change per issue** line — the median additions, deletions,
changed files and reviews. Records written before that enrichment existed carry
none, and neither does one whose lookup failed, so the line counts its own
issues and says how many. The same two timestamps give **PR open to merge** the
authoritative span, which is right even when the run that opened the PR falls
outside the window or belonged to a drain on another machine.

**On resumed sessions:** a crashed run and the `--resume` that finishes its
work are two records, and `stats` sums both. If a resumed run's `result` event
turns out to report the whole session's total rather than that invocation's,
those two rows overlap and the sum reads high — so whenever a report contains
resumes, it says how many.

**On approximated runs:** a run that crashed, stalled or was interrupted never
emitted a `result` event. Its tokens are the tally seen streaming past, an
undercount, and its dollars read as zero, because pricing belongs to the CLI
and this binary never guesses at it. Those runs are counted out separately
rather than mixed in silently — a crash-prone configuration should not get to
look cheap.

### Comparing configurations

`-run-tag` labels a batch so you can price one setup against another later:

```bash
backlog-drain -model claude-opus-5 -run-tag baseline
```

Change one thing — the model, the skill's wording, `-stall` — tag the next
batch differently, and the two sets of records are comparable. Note that the
binary's version does not pin the skill's text: you can run any binary against
any installed version of the plugin, so tag discipline is what makes
skill-wording experiments mean anything.

Then compare them:

```bash
backlog-drain stats -by tag
```

```
by tag
  tag         issues  merged  runs   cost  $/merged  tokens
  baseline         3       2     5  $6.70     $3.35   16.9M
  terse-plan       2       1     2  $1.40     $1.40    2.2M
```

An issue worked under two tags is counted under each, and the table says so
when it happens. For anything `stats` does not answer, the files are JSONL,
which jq, DuckDB and every spreadsheet already read. What a day cost:

```bash
cat ~/.backlog-drain/metrics/*.jsonl | jq -s 'map(select(.kind=="run")) | map(.cost_usd) | add'
```

**On dollars:** `cost_usd` is the CLI's API-equivalent pricing — real money on
API-key auth, notional on a subscription plan. Tokens are the ground truth;
dollars are derived from them. This binary never hardcodes a price sheet, since
prices change and the CLI already applies the current ones.

## Using it on another project

Nothing here is tied to one repository or language — `-dir` points anywhere.
The one thing worth tuning per project is the tool allowlist, because an
unattended run stalls if a command it needs would raise a permission prompt.

The default `-tools` set covers git, the handful of gh subcommands the skill
uses (`gh issue view`/`comment`, `gh pr create`, plus read-only `gh pr
view`/`list`/`diff`), the tools the skill itself needs (`Read`, `Write`,
`Edit`, `Glob`, `Grep`, `Skill`, `TodoWrite`), and the usual entry points for
npm/pnpm/yarn, Go, Cargo, Make, Python/uv/pytest, dotnet, Maven and Gradle.
One more entry is added per run and is not in `-tools`: the run may add and
remove labels on the single issue it was dispatched for, which is how it raises
`awaiting-answer`. For anything else, widen it rather than replacing it:

```bash
backlog-drain -add-tools "Bash(bazel:*),Bash(just:*)"
```

Two other knobs matter when moving between repos:

- `-branch-prefix` must match what the skill names its branches, since that is
  how a PR is matched back to its issue.
- `-label` is the cleanest way to opt individual issues in on a busy repo.

## Security

An unattended run is a Claude session with `--permission-mode acceptEdits`
whose only input is issue bodies and comments. On any repository that accepts
issues from outside the team, that input is attacker-controllable. Two things
constrain it, and they work at different layers:

**The tool allowlist bounds what a run can do.** `-tools` is enforced by Claude
Code itself, not by the skill's good behaviour, so it does not depend on the
model declining a request. gh is granted per subcommand — `Bash(gh issue
view:*)`, `Bash(gh pr create:*)` and a few more — rather than as a blanket
`Bash(gh:*)`, which would also permit `gh api`, `gh secret set` and `gh repo
delete`. Even a per-verb grant is too wide: `Bash(gh pr:*)` includes
`gh pr merge`, `Bash(gh issue:*)` includes `gh issue edit --add-label`, which is
enough to pull an unlabelled issue into a `-label`-gated queue, and
`Bash(gh run:*)` includes `gh run rerun`, `gh run cancel` and `gh run delete` —
so a red build is diagnosed with `gh pr checks`, `gh run list` and
`gh run view`, which only read. If
your project genuinely needs more, add it explicitly with `-add-tools` rather
than widening back to a whole verb.

The skill does need one label command, to raise `awaiting-answer` when it stops
to ask something. Rather than granting `gh issue edit` at large, the supervisor
mints that grant per run and pins it to the issue number that run was
dispatched for — `Bash(gh issue edit 42 --add-label:*)` and its `--remove-label`
twin. Ordinarily the furthest attacker-supplied issue text can then reach is the
issue the run is already working on, where the worst it can do is park or unpark
itself. Like every entry in the list this is a prefix, not a signature: `gh issue
edit` takes several numbers, and one appended *after* the flag still starts with
the granted prefix. So read it as narrowing the blast radius from every issue in
the repository to something an audit of the run's own commands would catch —
which is what the rest of this section says about the allowlist generally.

A review remediation gets one more, minted and pinned the same way. Most of a
review's substance is in the comments left on individual lines of the diff, and
gh has no `pr` subcommand that prints them, so that run alone is granted
`Bash(gh api repos/you/project/pulls/42/comments:*)` — one PR of one repository,
where a blanket `Bash(gh api:*)` would be the entire GitHub API, secrets and
repository deletion included. Being a prefix rather than a signature, the honest
worst case is writing or deleting a comment on that same PR. Granting nothing
would not be safer: an unattended run that trips a permission prompt hangs in
silence until the stall watchdog kills it.

What the allowlist does *not* close, and cannot: `Bash(git:*)` includes
`git push`, which is what opening a PR requires; the build commands run
whatever the checked-out repo's scripts contain; and `Bash(python:*)`,
`Bash(npx:*)`, `Bash(uv:*)` and `Bash(go:*)` are arbitrary code execution by
construction — `python -c` can run anything the user can, gh included. So the
allowlist is a narrowing, not a sandbox. Point `-dir` at repositories you would
run `make test` in yourself, and drop the interpreter entries from `-tools` if
your project does not need them.

**`-label` bounds *which* issues are eligible.** Applying a label takes triage
permission or better, so requiring one means a maintainer has to opt each issue
in before the supervisor will touch it. An outsider can still file an issue;
they just cannot start a run with it — unless an issue template hands them the
label, since the `labels:` key on a template or issue form is applied on
creation whoever files it. Keep the gate label out of your templates.

```bash
backlog-drain -label ready-for-claude
```

On any repository open to issues from outside the team, run it that way. It is
the difference between "anyone can queue work for an unattended agent" and
"a maintainer chose this one".

Beyond those, the skill is told in Phase 0 to read issue and comment text as a
description of a change to make, never as instructions addressed to it, and to
report anything that tries to be — rather than obey it — in the PR body. That
is a mitigation, not a boundary: treat it as defence in depth behind the two
above, and keep the human merge step as the last check on what actually lands.

## Publishing and versioning

The repo ships two artefacts — a Claude plugin and a Go binary — and they share
one version number, the `version` field in
[`.claude-plugin/plugin.json`](.claude-plugin/plugin.json). That field is the
source of truth; everything else is derived from it.

The plugin is the versioned unit and the skill versions with it — skill
frontmatter carries no version of its own, and should not grow one. The binary
takes the same number. **One number, one commit, two artefacts.**

That version is also the *update signal*: Claude Code caches an installed plugin
under its version and skips anything that resolves to the same string. Commits
that land on `main` without a bump reach nobody. Bumping the field is the only
thing that moves an installed user.

### Cutting a release

A release is that number, bumped and committed, plus two tags on the same
commit, plus a separate commit that publishes it.

| Tag | Who needs it |
| --- | --- |
| `backlog-drain--v0.4.0` | The Claude plugin tooling. `claude plugin tag` creates it, and refuses if `plugin.json` and the marketplace entry disagree. It is also what the marketplace entry's `ref` pins to, and what a `dependencies` range would resolve against. |
| `v0.4.0` | Go modules — `go install ...@v0.4.0` only resolves semver tags — and the trigger for the binary release workflow. |

1. **Release PR** — a `chore(release): 0.4.0` commit that bumps
   [`plugin.json`](.claude-plugin/plugin.json) and writes the
   [`CHANGELOG.md`](CHANGELOG.md) section, and does nothing else. That is what
   makes the tag boundary mean something; a bump folded into a feature commit
   leaves no commit that means "this is 0.4.0".
2. **Tag it**, which two tags for one release makes worth scripting:

   ```bash
   ./scripts/release.sh
   ```

   ```powershell
   .\scripts\release.ps1
   ```

   Each refuses on a dirty tree, on a missing changelog section, and on a
   Claude Code too old for `claude plugin tag`; each runs
   `claude plugin validate .` before either tag exists, because both tags are
   pushed before CI sees them, so a manifest the plugin tooling rejects has to
   be caught locally or not at all. Pushing `v0.4.0` starts the release
   workflow, which validates again on a clean machine with current tooling,
   cross-compiles the five targets with the tag stamped in, and attaches them to
   a GitHub release whose body is that changelog section.
3. **Smoke-test it**, while nobody is on it yet:

   ```bash
   ./scripts/smoke.sh
   ```

   ```powershell
   .\scripts\smoke.ps1
   ```

   Everything CI cannot reach, because it needs the network, `gh` and a real
   `claude`: both tags exist and name one commit that is on `main`; the release
   is published with all five binaries and a body that is the changelog section,
   not generated commit subjects; the downloaded binary reports the right
   version, which is the only place the `-ldflags` stamp is ever exercised;
   `go install ...@v0.4.0` resolves; the plugin installs with the `ref` moved
   and reports the same version the binary does; and a session lists
   `/backlog-drain:implement-issue`. It writes nothing outside a temporary
   directory — the plugin installs into a throwaway `CLAUDE_CONFIG_DIR`, so
   smoke-testing a release never moves *your* machine onto it.

   It cannot exercise the skill, so **drive one real issue through the release
   before publishing** — see [Smoke-testing the skill](#smoke-testing-the-skill).
4. **Publish PR** — move the `ref` in
   [`marketplace.json`](.claude-plugin/marketplace.json) to the new tag.
   Merging that one line is the moment anybody is exposed to the release.

Steps 2 and 4 are separate on purpose. A `ref` bumped in the release commit
would have `main` advertising a tag that does not exist from the moment it
merges until the tag is pushed, and every install in that window fails.
Splitting them is also what buys step 3 a window at all. **Rolling back** is
reverting the publish commit; tags never move.

**Installs resolve to a tag, not to `main`.** The marketplace entry is an
explicit `github` source with a `ref`, so a version number identifies exactly
one commit and two people reporting `0.4.0` are running the same thing. This is
also what keeps the two halves together: `go install ...@latest` resolves to the
newest `vX.Y.Z` tag, so a plugin tracking `main` would drift from its binary by
construction. A test holds the `ref` to never being *ahead* of `plugin.json` —
lagging is the normal state between steps 2 and 4.

To develop against `main` rather than a release, add the clone as a local
marketplace (`claude plugin marketplace add ./`) or use the
[hand install](#the-skill-by-hand).

### Smoke-testing the skill

`smoke.sh` proves the artefacts are real, installable and consistent. It proves
nothing about whether the skill still takes an issue to a PR, and nothing ever
will fully automate that: **nothing merges itself**, so the last link in the
loop is a human. The cheapest honest test is therefore not a scratch repo and a
seeded fake issue — it is to make the first issue you were going to drain
anyway the smoke test.

After step 3, install the release for real and run one issue, watched rather
than unattended:

```bash
backlog-drain -once
```

What to watch for, in order:

- Startup names the repository, `/backlog-drain:implement-issue`, and the same
  version on both halves — no `version skew` line.
- The skill opens a PR whose head branch is `issue-N`. That name is the
  contract between the two halves; a change there breaks PR discovery.
- The supervisor finds that PR and *waits* on it, rather than re-running the
  skill over the top of it.
- You merge. The supervisor notices and exits.
- `backlog-drain stats` counts that issue as `merged`.

Then open the publish PR, and say in its body which issue you drove. If the
backlog is empty at release time, say **that** instead — "the skill half went
unexercised" is worth recording rather than leaving to be inferred from
silence.

### What to bump

Pre-1.0, **minor is the breaking axis**. That is the npm-semver reading —
`^0.3.0` means `0.3.x` — and plugin `dependencies` ranges resolve with npm
semver against `backlog-drain--v*` tags, so it is the reading that makes a
constraint on this plugin behave.

- **Patch** — bug fixes, doc changes, anything invisible to a caller.
- **Minor** — new flags, changed defaults, changes to the skill's phases. The
  `-tools` default counts: widening it changes what unattended runs may do.
- **Major** — `1.0.0` when the skill's contract with the supervisor settles.
  The coupling to watch is the branch name: the supervisor finds a PR by its
  head branch, so if the skill ever stops naming branches `issue-N`, that is a
  breaking change on both sides at once. After 1.0, that is what major means.

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
CI runs, on Linux, macOS and Windows. `smoke` is its opposite number and is not
part of this loop: it needs the network, `gh` and a real `claude`, and it only
has anything to check once a release is tagged — see
[Cutting a release](#cutting-a-release). The plugin side has its own validator,
which `release.sh` and the release workflow both run for you:

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
