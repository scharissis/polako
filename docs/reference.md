# Reference

Every flag `polako` takes, and the two read-only reports. Flags also take
defaults from the [environment](#setting-defaults-from-the-environment).

## Flags

These are `polako work`'s own; the two report subcommands take smaller sets —
see [`status`](#where-the-backlog-stands-polako-status) and
[`stats`](run-data.md#reading-it-back-polako-stats).

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `.` | Path to the repository's main checkout. |
| `-claude` | `claude` | The Claude Code binary to invoke. No pass-through for extra `claude` arguments — point this at a wrapper script instead; see [Running both halves from a working tree](../CONTRIBUTING.md#running-both-halves-from-a-working-tree). |
| `-skill` | `polako:implement-issue` | Slash command run once per issue. Plugin skills are namespaced `<plugin>:<skill>`; pass `-skill implement-issue` if you copied the skill into `~/.claude/skills` instead. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how PRs are matched back to issues. |
| `-label` | *(none)* | Only process issues carrying this label. Doubles as an access control — see [Security](security.md). |
| `-ungated` | `false` | Work a public repository without a `-label` gate. Without one or the other, `polako work` refuses to start on a public repo — see [Security](security.md). |
| `-ignore-skew` | `false` | Start even when the installed skill is older than this binary. Without it, that mismatch refuses to start — a stale skill risks missing fixes and the shared branch-name contract ([issue #239](https://github.com/scharissis/polako/issues/239)); see [Getting updates](install.md#getting-updates). |
| `-tools` | *(see below)* | `--allowedTools` for unattended runs. **Replaces** the default set. |
| `-add-tools` | *(none)* | Extra `--allowedTools` entries, **appended** to `-tools`. |
| `-permission-mode` | `acceptEdits` | Passed to `claude --permission-mode`. |
| `-model` | *(the CLI's own default)* | Passed to `claude --model`. Vary it between batches to compare models — see [Run data & cost tracking](run-data.md). |
| `-poll` | `5m` | Interval between GitHub checks while waiting. |
| `-retries` | `3` | Consecutive *fruitless* resume attempts after a crash — one that got real work done resets the count instead of spending it. Also the bound on remediation runs against a conflicting, red, or changes-requested PR. Excludes an auth refusal and a session-limit refusal, which wait instead — see `-max-session-usage` below. |
| `-retry-wait` | `30s` | Wait before each resume attempt after a *crash*. A clean exit that left work behind resumes right away — nothing about it is transient. |
| `-stall` | `15m` | Kill and resume a run that has emitted no events for this long (`0` disables). |
| `-heartbeat` | `5m` | Terminal-only `still working` note while a run is quiet, repeated on this interval (`0` disables); silent under `-verbose`. See [The shift log](#the-shift-log--log). |
| `-max-cost` | *(no limit)* | Park an issue once this shift's runs on it have cost this many dollars — see [Capping what a shift spends](run-data.md#capping-what-a-shift-spends). |
| `-max-issue-time` | *(no limit)* | Park an issue once this shift's runs on it have taken this much *run time*, e.g. `-max-issue-time 90m` — unlike `-stall`, regardless of whether events are arriving. |
| `-max-session-cost` | *(no limit)* | End the shift cleanly, between issues, once its runs have cost this many dollars. |
| `-max-session-usage` | *(no limit)* | Pause the shift, between issues, once the plan's current-session usage hits this percent — waits out the reset, then carries on. Starting already over it waits rather than running nothing. See [Capping what a shift spends](run-data.md#capping-what-a-shift-spends). |
| `-max-week-usage` | *(no limit)* | Same, for the plan's current-week usage. A weekly reset can be days off, so the wait can be long; Ctrl+C is safe, state is on GitHub. |
| `-skip` | *(none)* | Comma-separated issue numbers to skip. Issues labelled `needs-human` are skipped anyway — see [How it works](behaviour.md). |
| `-once` | `false` | Process a single issue to a merge, a park or a question for you, then exit. |
| `-strict-order` | `false` | Work issues in strict ascending order: wait in place on an issue awaiting an answer instead of moving past it. |
| `-dry-run` | `false` | Resolve the next issue, print the `claude` invocation it would get, and exit. Runs nothing and writes nothing — see [Looking before you leap](#looking-before-you-leap--dry-run). |
| `-notify` | *(none)* | Command to run whenever polako needs a human, with context in `POLAKO_NOTIFY_*` — see [Being told when it needs you](#being-told-when-it-needs-you--notify). |
| `-remote` | `true` | Ask for each run to be watchable from claude.ai/code or the app. **Inert today** — no `claude` CLI registers headless runs, so nothing is sent. See [Watching a shift from anywhere](#watching-a-shift-from-anywhere--remote). |
| `-run-tag` | *(none)* | Freeform label recorded with every run, so one batch can be compared against another. |
| `-metrics` | `~/.polako/metrics` | Directory for run-data records, or `off` to write nothing. |
| `-log` | `~/.polako/logs` | Directory for the full per-shift log, or `off` to write none — see [The shift log](#the-shift-log--log). |
| `-verbose` | `false` | Mirror the full `[claude]` event stream to the terminal as well as the shift log. The default terminal shows milestones only. |
| `-post-summary` | `false` | Comment one line of run numbers on each merged PR. The only thing that shows run data to anybody but you — see [Run data & cost tracking](run-data.md). |
| `-version` | `false` | Print which release this binary is, then exit. Use it when startup warns that the binary and the skill disagree — see [Getting updates](install.md#getting-updates). |

### Looking before you leap: `-dry-run`

Pointing an unattended agent at a repository you have not worked before is a
leap of faith. `-dry-run` takes it out:

```bash
$ polako work -dir ../my-project -dry-run
example/my-project — running /polako:implement-issue per issue, polling every 5m0s
  dry-run  resolving the next issue only — no claude run, no GitHub write, no run data
  remote   on, but no claude CLI registers headless runs with Remote Control yet — runs stay on this machine and unwatched, and nothing is sent anywhere (-remote=false silences this line; a later polako lights the flag up once a CLI supports it)
ready: #12, #14, #19
waiting on an answer: #9
issue #12 would be worked next; the invocation follows on stdout
claude -p '/polako:implement-issue 12' --permission-mode acceptEdits --allowedTools '…' --output-format stream-json --verbose
```

It resolves the next issue exactly as a real shift would — same queue,
`-skip`, `needs-human` exclusions, same preference for an issue awaiting an
answer — then stops: every call is a read, no labels declared, run-data
recording and the shift log both forced off. Narration goes to stderr, the
shell-quoted invocation alone to stdout, so `polako work -dry-run | pbcopy`
gives you something to run by hand; the real run passes those arguments to
the CLI directly, never through a shell.

If the next issue's branch already has a PR, you get what polako would
actually do with it instead — wait on an open one, close behind a merged
one, or park behind one closed unmerged:

```
issue #12 already has PR #40 (OPEN) on branch issue-12 — it would wait on that PR rather than run claude: https://github.com/example/my-project/pull/40
```

### Being told when it needs you: `-notify`

A shift left running overnight goes quiet about what you most want to
know — an issue parks, or a run stops to ask something — leaving only a
label on a thread nobody's watching. `-notify` runs a command at each of
those moments:

```bash
polako work -notify ~/bin/tell-me
```

It fires on six states, and nothing else:

| `POLAKO_NOTIFY_EVENT` | What happened |
| --- | --- |
| `parked` | An issue was parked for a human — including a run that crashed and used up its resumes. |
| `awaiting-answer` | A run stopped to ask something on the issue thread. Reply there and the next shift folds it in. |
| `cleared` | The backlog is empty. Nothing is left to work. |
| `stopped` | The shift ended before the backlog did: a fatal error, or `-max-session-cost` spent. (`-max-session-usage`/`-max-week-usage` does *not* fire this — the shift waits the reset out and carries on.) |
| `epic-done` | An epic's last child closed and the drain closed the container, with a comment saying so. Fires once, on the close; a container a human has held with `needs-human` or `proposed` is left open and fires nothing. |
| `proposed` | A [`polako plan`](#planning-a-backlog-unattended-polako-plan) run finished with proposals behind the `proposed` label. `ISSUE` is empty — it names the whole batch — and it fires only when the run actually proposed something. |

The context arrives in the environment, so the command needs no arguments:

| Variable | Value |
| --- | --- |
| `POLAKO_NOTIFY_EVENT` | One of the six above. |
| `POLAKO_NOTIFY_ISSUE` | The issue number, or empty when the whole shift rather than one issue needs you. |
| `POLAKO_NOTIFY_REPO` | `owner/name`. |
| `POLAKO_NOTIFY_REASON` | One line of English saying what happened and what to do about it. |

So a hook is usually a three-line script:

```bash
#!/bin/sh
# ~/bin/tell-me
terminal-notifier -title "polako: $POLAKO_NOTIFY_EVENT" \
  -message "${POLAKO_NOTIFY_REPO} #${POLAKO_NOTIFY_ISSUE:-—}: $POLAKO_NOTIFY_REASON"
```

Three things about how the command runs: it's **not a shell** — program and
arguments only, no pipeline, redirection or `$VARIABLE` expansion, so put
that in a script; **a failing hook never breaks the shift** — a bad exit or
a 30-second hang costs that notification and gets logged (a `-notify` naming
a program not on `PATH` is caught at startup instead); and it carries
**numbers, identifiers and polako's own words only** — issue, comment and PR
text never reach it, attacker-controllable on a repo taking outside issues.

It stays quiet about the ordinary case too: a PR waiting to be merged
happens on every healthy issue, and a notifier that fires every time gets
muted.

### Watching a shift from anywhere: `-remote`

A shift's runs are unattended and invisible — output exists only in the
terminal that started it. `-remote` asks for runs to show up in your session
list on [claude.ai/code](https://claude.ai/code) and the mobile app instead.
**It does nothing today**, though: no `claude` CLI registers headless runs
with Remote Control, so polako never passes the flag — same invocation
whether `-remote` is on or off, nothing leaves this machine. Startup says so
once:

```
  remote  on, but no claude CLI registers headless runs with Remote Control yet — runs stay on this machine and unwatched, and nothing is sent anywhere (-remote=false silences this line; a later polako lights the flag up once a CLI supports it)
```

`-remote=false` silences that line and changes nothing else. The flag stays
as interface ([issue #52](https://github.com/scharissis/polako/issues/52),
see [security.md](security.md) for the trade); until a CLI supports it, [the
shift log](#the-shift-log--log) reads a run you weren't watching.

### The shift log: `-log`

Each shift writes one complete log of itself to a file, named at startup:

```
  shift log  /Users/you/.polako/logs/example--my-project--3f9a1c02.log — the whole claude transcript stream, kept on this machine (-log off to disable)
```

The file holds everything the shift narrates, timestamped: every terminal
line, the full `[claude]` event stream, and `claude`'s own stderr. The
terminal shows milestones alone: start/finish with cost, the
`implement-issue` phase reached, PR opened and merged, parks, warnings, the
exit summary. A healthy run narrates each phase once:

```
15:32:18 [claude] session started (model claude-sonnet-5, session b0f87c49-…)
15:32:19 [claude] reading the issue…
15:32:35 [claude] preparing the branch…
15:33:00 [claude] reading the code…
15:38:54 [claude] writing the plan…
15:39:04 [claude] implementing…
15:48:04 [claude] running the review gate…
16:00:47 [claude] opening the PR…
16:01:12 [claude] finished (ok) — 223 turns, 28m57s, $14.03
```

— a run that ends in a question says `asking on the issue thread…` instead; a
resumed run re-reads the issue and says so again. Between milestones,
`-heartbeat` adds a periodic `still working — 12m in, 118 tool calls,
implementing` so a long phase isn't silence. Read the file when a run did
something surprising, and `tail -f` it (or `-verbose`) to watch a shift live.

It's write-only, like the run-data records: nothing reads it back, deleting
it mid-shift changes no behaviour, and it never leaves this machine. Unlike
them it holds transcript text, hence the same `0700`/`0600` permissions, kept
outside any checkout. No rotation — one file per shift, yours to delete; a
restarted shift starts a fresh one, so sort by modification time to follow
an issue across restarts. `-log <dir>` moves it, `-log off` disables it, a
bad directory warns once and the shift continues on the terminal alone, and
`-dry-run` writes no log at all. The terminal wears its own timestamp
lightly — a dim `15:04:05` on a TTY (plain under `NO_COLOR` or Windows), the
full `2006/01/02 15:04:05` with no colour when piped — but the per-tool-call
`[claude]` lines live in the shift log now, not stderr, so grep the log, or
run `-verbose` to put the stream back.

### Setting defaults from the environment

Any flag can take its default from `POLAKO_<FLAG>`, so a preference you
always want lives in your shell profile instead of on every command line:

```bash
export POLAKO_POST_SUMMARY=1
```

The name uppercases and swaps `-` for `_`: `-post-summary` reads
`POLAKO_POST_SUMMARY`. An argument always wins — `-post-summary=false` on
one run, or `polako work -h` to see the defaults in force. `POLAKO_METRICS`
covers both halves: where a shift writes, where `stats` reads.
`-version`/`-dry-run` are deliberately not settable this way — both are
actions, and leaving one in a profile would silently turn every shift into
an exit. `POLAKO_VERSION` pins a Dockerfile or CI install; `POLAKO_DRY_RUN`
previews a repo once, then forget.

A value a flag can't parse stops the run and names the variable and flag it
was setting — a preference that looks set and quietly does nothing is worse
than none at all. And `POLAKO_NOTIFY_*` sits clear of this namespace: none
of it is `POLAKO_<FLAG>`, so a notification can't reconfigure a `polako` run
from inside your own hook.

## Planning a backlog unattended: `polako plan`

`polako plan` runs the [`plan-backlog`](../README.md#planning-a-backlog)
skill the way `polako work` runs `implement-issue`: point it at a vision
document and it proposes a curated backlog — epics and one-PR issues —
behind the `proposed` label a human lifts to queue.

One `claude` invocation, bracketed by two enforcement mechanisms that keep
the curation gate structural rather than something the model has to
remember:

- **The cap.** Counts `gh issue create` calls and kills the run at
  `-max-issues`, epics included — loud, never destructive.
- **The label pass.** Snapshots the open backlog before the run; after it —
  always, even on a crash or Ctrl+C — every issue this `gh` account created
  since is normalised to **exactly** the `proposed` label and the batch
  milestone. A labelling failure is reported loudly and exits nonzero.

The honest edge: an operator hand-filing an issue from the same account
while a run is going gets caught in the sweep too — rare, but logged. When it
ends — clean finish, cap, crash, or Ctrl+C — a plan run leaves one
`kind:"plan"` line in [run data](run-data.md), and, if it proposed
something, one `proposed`
[notification](#being-told-when-it-needs-you--notify).

After the label pass, a run that proposed something prints one line pricing
the batch from your own history — median cost and run time of a merged
issue here, times the number of proposals:

```
plan: your last 14 merged issues ran $2.70 and 38m median — 7 proposals ≈ $19 and 4½h of run time, before curation cuts
```

Rounded coarse on purpose — curation fuel, not a quote, and the only place
`polako plan` states a dollar figure. With no history, or none ever priced
(`-metrics off`, or only crashed runs), it says so instead:

```
plan: no run history to price against — work a few issues and future plans will estimate themselves
```

`-dry-run` prints the exact `claude` invocation a run would make and touches
nothing — no label, no milestone, no process, no record, no notification.

```bash
polako plan -vision docs/VISION.md -dry-run
polako plan -vision docs/VISION.md            # the real thing
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-vision` | *(none)* | Path, resolved under `-dir`, to the vision or roadmap document to plan from. Exactly one of `-vision` / `-brief` is required — never a does-the-file-exist guess, so a typo fails loudly. |
| `-brief` | *(none)* | Inline vision text in place of `-vision`, e.g. `-brief "a dating app for horses"` — the greenfield story, same trust tier as the document. Past ~2000 characters, put it in a file. |
| `-focus` | *(none)* | Free-text steer for the run, e.g. `-focus "only the observability section"`. |
| `-milestone` | *(derived)* | Batch milestone title, created idempotently at preflight and attached to every issue the run files by the label pass. Defaults to the vision file's name, or the brief's first words. `-milestone off` skips the milestone entirely. |
| `-max-issues` | `10` | Ceiling on the issues a run may create, epics included. A ceiling, not a target — fewer, sharper issues beat coverage. |
| `-model` | `opus` | Passed to `claude --model`. An alias, not a pinned id: a plan run happens once per batch and steers every run downstream, so it defaults to the strongest tier. |
| `-skill` | `polako:plan-backlog` | Slash command the run invokes. |
| `-tools` / `-add-tools` | *(the plan allowlist)* | `--allowedTools` for the run. The default is a fraction of `work`'s: repo reads, `gh issue list` / `view` / `search`, `Write`, and `gh issue create` — nothing that commits, pushes, opens a PR, edits a thread, or reaches `gh api`. |
| `-stall` | `15m` | Kill a run with no output events for this long — the same silence watchdog `polako work` uses. `0` disables it. |
| `-max-cost` | `0` | Warn once the run has cost this many dollars. Advisory only: a plan run is one `claude` invocation with no next run to decline, so unlike `polako work` there is nothing for the cap to stop — it is reported, not enforced. |
| `-dir`, `-claude`, `-permission-mode`, `-dry-run` | | Same meaning as `polako work`'s flags above. |
| `-metrics` | `~/.polako/metrics` | Directory for the one `kind:"plan"` record the run writes, or `off`. Same meaning and default as `polako work`'s. |
| `-run-tag` | *(none)* | Label recorded with the `plan` record, so one batch's plan run can be compared against another in `polako stats`. |
| `-notify` | *(none)* | Command run when the plan run finishes with proposals to curate — the `proposed` event above. Checked at preflight like `polako work`'s. |

## Auditing repository health unattended: `polako health`

`polako health` runs the [`review-health`](../README.md#planning-a-backlog)
skill the way `polako plan` runs `plan-backlog`: point it at a repository via
`-dir` and it measures that repo's own shape (file and function sizes,
duplicated helpers, abstractions nothing uses), filing what it finds as
**proposals** under `plan`'s own curation gate and sizing contract.

It differs from `plan` only in what it plans from and what it attaches: no
`-vision` / `-brief` / `-milestone` — it reads the repository `-dir` already
names, and attaches no milestone; `-focus` is the only free-text steer. The
default `-tools` allowlist is narrower too: review-health's own SKILL.md
bounds its `gh` surface to three call shapes — two `issue list` reads and
one `issue create` — plus repo reads and the scratch body file. Otherwise
the shape is identical to [`polako
plan`](#planning-a-backlog-unattended-polako-plan)'s, cap and label pass
included.

```bash
polako health -dir ~/code/some-repo -dry-run
polako health -dir ~/code/some-repo            # the real thing
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-focus` | *(none)* | Free-text steer for the run, e.g. `-focus "only cmd/polako"`. |
| `-max-issues` | `10` | Ceiling on the issues a run may create, epics included. |
| `-model` | `opus` | Passed to `claude --model` — a health run happens periodically and steers every run downstream, so it defaults to the strongest tier, the same reasoning `plan` uses. |
| `-skill` | `polako:review-health` | Slash command the run invokes. |
| `-tools` / `-add-tools` | *(the health allowlist)* | `--allowedTools` for the run — see above. |
| `-stall` | `15m` | Kill a run with no output events for this long. `0` disables it. |
| `-max-cost` | `0` | Warn once the run has cost this many dollars. Advisory only, the same reasoning `plan`'s carries. |
| `-dir`, `-claude`, `-permission-mode`, `-dry-run` | | Same meaning as `polako work`'s flags above. |
| `-metrics` | `~/.polako/metrics` | Directory for the one `kind:"health"` record the run writes, or `off`. |
| `-run-tag` | *(none)* | Label recorded with the `health` record, so one run can be compared against another in `polako stats`. |
| `-notify` | *(none)* | Command run when the health run finishes with proposals to curate — the `proposed` event. Checked at preflight like `polako work`'s. |

## Where the backlog stands: `polako status`

While a shift runs, the only view of it is its terminal. Everything worth
knowing is already on GitHub, but reassembling it means a queue page, a
label search and a PR tab — `status` prints the whole picture at once:

```bash
polako status
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

What it prints is what a shift starting right now would do next, the same
thing a running shift is already doing. It reports **state, not liveness** —
never asking whether a shift is running — so it's useful from a laptop about
a shift running on a server. The closing `needs you:` line is the point of
the whole thing — items only a person can move. A PR polako would remediate
itself (conflicting, red, or an unanswered review) is deliberately not on
it: that's still polako's job.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-repo` | *(whatever `-dir` is a checkout of)* | Repository to report on, `owner/name`. Naming it is what lets the command run from anywhere — no checkout needed, just a `gh` authenticated for the repo. |
| `-dir` | `.` | Path to the repository's main checkout, used to resolve the repository when `-repo` is not given. |
| `-label` | *(none)* | Only count issues carrying this label, the same scoping `polako work`'s `-label` applies. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how open PRs are matched back to issues. |
| `-strict-order` | `false` | Report as a work run with `-strict-order` would: an issue awaiting an answer keeps its place, so `next` can name it rather than the ready issue behind it. |
| `-json` | `false` | Print one JSON document to stdout instead of the text report — see [As JSON](#as-json--json) below. |

They take environment defaults the same way `polako work`'s do — a
`POLAKO_LABEL` that scopes your work scopes the report too, named on the
header line. `-skip` is deliberately not among them: a head-of-line escape
hatch typed on one invocation, not a property of the backlog.

**Reads only.** Every call is a read subcommand polako itself re-derives
state with at startup — `gh issue list`, `gh pr list`, `gh pr view`, the REST
read of a thread's comments — so nothing moves an issue, label or PR. It
reads no run data either (those files are for `stats` and `polako plan`'s
pricing line) and prints no issue, PR or comment text — numbers, branches,
labels and states only, enough to decide where to go next.

Two things about the numbers. **Quiet** is the age of the newest comment on
a thread, a proxy for how long a question has waited — which comment is the
skill's own can't be told apart from here, since polako asks under your own
credentials. The PR table details the first eight PRs on issue branches —
normally one, since one issue is in flight at a time — anything past that
listed by number rather than dropped.

### As JSON: `-json`

```
polako status -json | jq .
```

```json
{
  "repo": "scharissis/polako",
  "scope": { "label": "", "strict_order": false },
  "queue": {
    "ready": [14, 19, 23],
    "blocked": [{ "issue": 9, "quiet_seconds": 93600 }],
    "parked": [5],
    "proposed": [27, 28],
    "containers": [{ "issue": 12, "total": 5, "completed": 2, "finished": false, "held": false }]
  },
  "next": {
    "issue": 14,
    "reason": "#14 — its branch already has PR #61, so it would wait on that rather than run the skill again"
  },
  "prs": [
    {
      "number": 61, "branch": "issue-14", "issue": 14,
      "url": "https://github.com/scharissis/polako/pull/61",
      "mergeable": "mergeable", "checks": "failing (test-mac)", "review": "clear"
    }
  ],
  "undetailed_prs": [],
  "needs_you": [
    "reply on #9",
    "review and merge PR #58",
    "decide what to do about #5 (drop needs-human to requeue)",
    "curate #27, #28 (drop proposed to queue them)"
  ],
  "plan": "plan: session 42%, week 52% (resets Sep 2, 6pm) — polako was 29% of the last 24h"
}
```

With `-json`, stdout carries exactly one document — no header, no
`needs you:` line — the same snapshot the text report renders, field for
field: `queue` holds the same five lists, `next` names the issue a shift
would pick up and why, `prs` matches the text columns exactly (`not read`
for a PR past the eight-PR cap; `unknown` means gh doesn't know), and
`needs_you` is the closing line's clauses as an array.

`queue.containers` is objects, not bare numbers — `{ "issue", "total",
"completed", "finished", "held" }` — so a caller can tell a finished
container from one in progress without a second call; `held: false` means
the next shift is about to close it, `held: true` means it's the caller's.
Every array field is always `[]`, never `null`; `quiet_seconds` and `plan`
are the two fields that can be *absent* instead of a fake zero or empty
string. Same rule as the text report: no issue, PR or comment text, only
numbers, branches, labels, states and URLs.

## Reclaiming finished issues: `polako tidy`

A shift runs this sweep itself — once at start, once after each merge it
observes (see [How polako works](behaviour.md)) — so a drained backlog needs
no follow-up. `tidy` is the same sweep pointed at a repository by hand:
after an interactive run, a killed shift, or just to preview what's
reclaimable.

```bash
polako tidy
```

```
scharissis/polako

would reclaim (-apply to do it)
  issue  branch     why               action
  #115   issue-115  closed            worktree removed, branch deleted
  #130   issue-130  merged (PR #201)  worktree removed, branch deleted

skipped
  issue  branch     reason
  #171   issue-171  still open
  #178   issue-178  2 uncommitted files
```

Before touching anything it proves the branch safe to remove — **all** of
these, not any one:

- no human has put `needs-human` or `proposed` on the issue — a hold only a
  human clears, outranking even a merged PR;
- the issue is closed, or its PR is merged;
- the branch is merged into the default branch, after the same fast-forward
  refresh a shift does before picking up an issue — except right after a
  merge a shift itself watched, where GitHub's event is proof enough;
- its worktree, if it has one, has no uncommitted or untracked changes
  (`PLAN.md` doesn't count, same exception `-dry-run`'s park messages carry);
- nothing about it is unpushed.

Anything that fails is named and left alone — the feature, not a
diagnostic. Refusing is recoverable; removing wrongly is not.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-repo` | *(whatever `-dir` is a checkout of)* | Repository to reclaim in, `owner/name`. |
| `-dir` | `.` | Path to the repository's main checkout, used to resolve the repository when `-repo` is not given. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how a branch is matched back to an issue. |
| `-apply` | `false` | Actually remove worktrees and delete branches. Every other verb here defaults to acting; this is the one whose actions cannot be undone, so it defaults to only reporting what it would do. Cannot be set from the environment — see below. |

`-branch-prefix` and `-dir`/`-repo` take environment defaults the same way
every other verb's do; `-apply` deliberately doesn't, since a
`POLAKO_APPLY=1` in a shell profile would turn every future preview into a
live deletion run. A worktree is matched by the branch it has checked out,
not its directory name — so a sibling-folder worktree, or a
`.claude/worktrees/<slug>-<hash>` one from a desktop session, is reclaimed
exactly when it carries a finished `issue-N` branch, and left alone
otherwise, including a detached one.

A repository with nothing to reclaim prints one line and exits 0.

