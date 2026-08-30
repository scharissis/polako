# Reference

Every flag `polako` takes, and the two read-only reports. Flags also take
defaults from the environment; see [Setting defaults from the
environment](#setting-defaults-from-the-environment).

## Flags

These are `polako work`'s own. The two report subcommands take their own smaller
sets — see [`status`](#where-the-backlog-stands-polako-status) and
[`stats`](run-data.md#reading-it-back-polako-stats).

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `.` | Path to the repository's main checkout. |
| `-claude` | `claude` | The Claude Code binary to invoke. There is no pass-through for extra `claude` arguments; point this at a wrapper script when you need one — see [Running both halves from a working tree](../CONTRIBUTING.md#running-both-halves-from-a-working-tree). |
| `-skill` | `polako:implement-issue` | Slash command run once per issue. Plugin skills are namespaced `<plugin>:<skill>`; pass `-skill implement-issue` if you copied the skill into `~/.claude/skills` instead. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how PRs are matched back to issues. |
| `-label` | *(none)* | Only process issues carrying this label. Doubles as an access control — see [Security](security.md). |
| `-ungated` | `false` | Work a public repository without a `-label` gate. Without one or the other, `polako work` refuses to start on a public repo, because anyone who can open an issue could feed its queue — see [Security](security.md). |
| `-tools` | *(see below)* | `--allowedTools` for unattended runs. **Replaces** the default set. |
| `-add-tools` | *(none)* | Extra `--allowedTools` entries, **appended** to `-tools`. |
| `-permission-mode` | `acceptEdits` | Passed to `claude --permission-mode`. |
| `-model` | *(the CLI's own default)* | Passed to `claude --model`. Vary it between batches to compare models — see [Run data & cost tracking](run-data.md). |
| `-poll` | `5m` | Interval between GitHub checks while waiting. |
| `-retries` | `3` | Consecutive *fruitless* resume attempts after a crashed run — a crash that got real work done first resets the count rather than spending it — and the bound on remediation runs against an open PR that is conflicting, red, or carrying a request for changes. A run the API refused to authenticate is never one of them, and neither is one refused over the account's session limit, which is waited out instead — see below. |
| `-retry-wait` | `30s` | Wait before each resume attempt after a *crash*. A clean exit that left work behind is resumed straight away: nothing about it is transient, so there is nothing to wait for. |
| `-stall` | `15m` | Kill and resume a run that has emitted no events for this long (`0` disables). |
| `-max-cost` | *(no limit)* | Park an issue once this shift's runs on it have cost this many dollars — see [Capping what a shift spends](run-data.md#capping-what-a-shift-spends). |
| `-max-issue-time` | *(no limit)* | Park an issue once this shift's runs on it have taken this much *run time*, e.g. `-max-issue-time 90m`. Unlike `-stall`, it does not care whether events are arriving. |
| `-max-session-cost` | *(no limit)* | End the shift cleanly, between issues, once its runs have cost this many dollars. |
| `-max-session-usage` | *(no limit)* | End the shift cleanly, between issues, once the plan's current-session usage reaches this percent — see [Capping what a shift spends](run-data.md#capping-what-a-shift-spends). |
| `-max-week-usage` | *(no limit)* | End the shift cleanly, between issues, once the plan's current-week usage reaches this percent. |
| `-skip` | *(none)* | Comma-separated issue numbers to skip. Issues labelled `needs-human` are skipped anyway — see [How it works](behaviour.md). |
| `-once` | `false` | Process a single issue to a merge, a park or a question for you, then exit. |
| `-strict-order` | `false` | Work issues in strict ascending order: wait in place on an issue awaiting an answer instead of moving past it. |
| `-dry-run` | `false` | Resolve the next issue, print the `claude` invocation it would get, and exit. Runs nothing and writes nothing — see [Looking before you leap](#looking-before-you-leap--dry-run). |
| `-notify` | *(none)* | Command to run whenever polako needs a human, with context in `POLAKO_NOTIFY_*` — see [Being told when it needs you](#being-told-when-it-needs-you--notify). |
| `-remote` | `true` | Ask for each run to be watchable from claude.ai/code or the app. **Inert today** — no `claude` CLI registers headless runs, so nothing is sent and runs stay unwatched. See [Watching a shift from anywhere](#watching-a-shift-from-anywhere--remote). |
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

It resolves the next issue exactly as a real shift would — same queue, same
`-skip`, same `needs-human` exclusions, same preference for an issue waiting on
an answer when nothing else is ready — and then stops. Nothing is run and
nothing is written: every GitHub call it makes is a read, it declares no labels,
and run-data recording and the shift log are both forced off for the run, so
`-metrics` or `-log` in your environment cannot leave a record of a run that
never happened.

The narration goes to stderr and the invocation alone to stdout, so
`polako work -dry-run | pbcopy` gives you something to paste and run by hand.
It is printed with shell quoting for that reason; the real run passes those
arguments to the CLI directly, never through a shell.

If the next issue's branch already has a PR, you get that instead — because
that is what polako would do with it:

```
issue #12 already has PR #40 (OPEN) on branch issue-12 — it would wait on that PR rather than run claude: https://github.com/example/my-project/pull/40
```

It names what it would actually do with that PR, which is not the same in every
state: wait on an open one, close the issue behind a merged one, and park an
issue whose PR was closed without merging.

### Being told when it needs you: `-notify`

A shift left running overnight is quiet about the things you most want to know.
An issue parks, or stops to ask you a question, and polako does the right
thing — it works the queue behind it — so the only trace is a label on a thread
nobody is watching. `-notify` runs a command of your choosing at each of those
moments:

```bash
polako work -notify ~/bin/tell-me
```

It fires on six states, and nothing else:

| `POLAKO_NOTIFY_EVENT` | What happened |
| --- | --- |
| `parked` | An issue was parked for a human — including a run that crashed and used up its resumes. |
| `awaiting-answer` | A run stopped to ask something on the issue thread. Reply there and the next shift folds it in. |
| `cleared` | The backlog is empty. Nothing is left to work. |
| `stopped` | The shift ended before the backlog did: a fatal error, `-max-session-cost` spent, or the plan's own usage reaching `-max-session-usage`/`-max-week-usage`. |
| `epic-done` | An epic's last child closed and the drain closed the container, with a comment on the thread saying so. The one event above for something good rather than something stuck. Fires once, on the close; a container a human has held with `needs-human` or `proposed` is left open and fires nothing. |
| `proposed` | A [`polako plan`](#planning-a-backlog-unattended-polako-plan) run finished with proposals behind the `proposed` label, waiting to be curated. `ISSUE` is empty — it is the whole batch. The one event a plan run raises, and it fires only when the run actually proposed something. |

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

Three things to know about how the command is run:

- **It is not a shell.** The command line is split into a program and arguments,
  honouring quotes so a path with a space in it works, and run directly. There
  is no pipeline, no redirection and no `$VARIABLE` expansion — put anything
  like that in a script, which is where it can be tested on its own anyway.
- **A failing hook never breaks the shift.** A non-zero exit, or one that hangs
  past 30 seconds, costs you that notification and is logged; polako carries
  on. A `-notify` naming a program that is not on `PATH` is caught at startup
  instead, since a night of notifications nobody receives is the one failure the
  flag must not have.
- **It carries numbers, identifiers and this program's own words.** Issue,
  comment and PR text never reach it, for the same reason they never reach a
  run-data record: on a repository that accepts outside issues, that text is
  attacker-controllable.

`-notify` is deliberately quiet about the ordinary case. A PR waiting to be
merged is a human touchpoint too, but it happens on every healthy issue, and a
notifier that goes off every time is one you mute.

### Watching a shift from anywhere: `-remote`

A shift's runs are unattended by design and invisible with it: while a run is in
flight, its output exists only in the terminal that started it. `-remote` is the
flag that asks for those runs to show up in your session list on
[claude.ai/code](https://claude.ai/code) and in the mobile app instead — live,
readable, and steerable if you want to step in.

**It does nothing today, and that is not a bug you can fix at your end.** No
`claude` CLI registers headless runs with Remote Control. The one that ships now
accepts `--remote-control` under `-p`, starts a perfectly normal session, and
never brings the remote bridge up; the feature is scoped to interactive
sessions, and print mode is the whole differentiator. There is no field in the
session's `init` event to detect the difference from, so polako cannot even tell
you per-run whether it worked.

So polako does not pass the flag at all — with `-remote` on or off, the
invocation is the same, and nothing about your session leaves this machine by
this path. Startup says so once, rather than claiming a session list that will
stay empty:

```
  remote  on, but no claude CLI registers headless runs with Remote Control yet — runs stay on this machine and unwatched, and nothing is sent anywhere (-remote=false silences this line; a later polako lights the flag up once a CLI supports it)
```

`-remote=false` silences that line and changes nothing else.

The flag stays because it is interface, and because the argument for turning it
on was made and settled on [issue #52](https://github.com/scharissis/polako/issues/52):
the destination is your own claude.ai account, the channel is Claude Code's own,
and turning it off restores a byte-identical invocation. When a CLI registers
headless runs, that is the argument a later polako would pass the flag on — a
release you would upgrade to, not something that starts happening under a
binary you already have. See [security.md](security.md) for the trade it would
commit you to when it does.

Until then, [the shift log](#the-shift-log--log) is how you read a run you were
not watching, and it never leaves this machine either.

### The shift log: `-log`

Each shift writes one complete log of itself to a file, named at startup:

```
  shift log  /Users/you/.polako/logs/example--my-project--3f9a1c02.log — the whole claude transcript stream, kept on this machine (-log off to disable)
```

The file holds everything the shift narrates, timestamped: every terminal
line, plus the full `[claude]` event stream — one line per assistant message
and tool call — and anything the `claude` process printed to its own stderr.
The terminal, by contrast, shows milestones alone: issue started, run started
and finished with its cost, PR opened and merged, parks, warnings, the exit
summary. A healthy run is two lines there and its whole conversation here.
The file is the record to read when a run did something surprising, and
`tail -f` on it — or `-verbose`, which mirrors the stream to the terminal —
is how to watch a shift work rather than glance at it.

Like the run-data records it is write-only and stays put: nothing in `polako`
ever reads it back, deleting it mid-shift changes no behaviour, and it never
leaves this machine. Unlike them it contains transcript text, which is why it
gets the same private-by-default permissions (`0700` directory, `0600` files)
and lives deliberately outside any checkout. There is no rotation or
retention: one file per shift, yours to delete — a restarted shift starts a
fresh file under its new shift id, so sort by modification time to follow an
issue across restarts.

`-log <dir>` moves it, `-log off` disables it, and a directory that cannot be
written warns once and the shift continues on the terminal alone. A
`-dry-run` writes no log at all.

Every line carries a time; the terminal just wears it quietly. On a TTY the
gutter shrinks to a dim, time-only stamp — `15:04:05 ` — always, whether or
not a shift log is open this run: the trade is a quiet terminal, not a
promise that the date lives elsewhere, so `-log off` or a log that fails to
open (above) means the terminal's stamp is the least precise record there
is, not the only one made full again. Milestones are coloured too,
sparingly. Set `NO_COLOR` (to anything, even nothing) to keep a TTY plain,
and Windows is plain regardless — the stamp stays, just unstyled. Piped or
redirected stderr keeps the full `2006/01/02 15:04:05` timestamp and carries
no colour, so each line of `polako work 2> shift.err` is shaped exactly as
it always was — but the stream is the quiet one: the per-tool-call `[claude]`
lines live in the shift log now, so anything that grepped the old firehose
out of stderr should read the log instead, or run with `-verbose` to put the
stream back.

### Setting defaults from the environment

Any flag can take its default from `POLAKO_<FLAG>`, so a preference you
always want lives in your shell profile instead of on every command line:

```bash
export POLAKO_POST_SUMMARY=1
```

The name uppercases and swaps `-` for `_`: `-post-summary` reads
`POLAKO_POST_SUMMARY`, `-retry-wait` reads `POLAKO_RETRY_WAIT`.
An argument always wins, so a single run can still go the other way with
`-post-summary=false`, and `polako work -h` prints the defaults actually in
force — which is how you see what the environment is doing.

`POLAKO_METRICS` covers both halves at once: where a shift writes, and
where `stats` reads. `-version` and `-dry-run` are deliberately *not* settable
this way — both are actions rather than preferences, and either one left in a
profile would quietly turn every shift on that machine into something that
exits, successfully, before doing any work. `POLAKO_VERSION` is exactly
the variable a Dockerfile or CI job pins an install with, and
`POLAKO_DRY_RUN` is the one you export to preview an unfamiliar
repository once and then forget.

A value a flag cannot parse stops the run and names both the variable and the
flag it was setting, rather than being skipped: a preference that was set,
looks set, and quietly does nothing is worse than no preference at all.

The `POLAKO_NOTIFY_*` variables a hook receives sit deliberately clear of
this namespace: none of them is `POLAKO_<FLAG>` for any flag, so a
notification cannot reconfigure a `polako` you run from inside your own
hook. A test enforces it.

## Planning a backlog unattended: `polako plan`

`polako plan` runs the [`plan-backlog`](../README.md#planning-a-backlog) skill
the way `polako work` runs `implement-issue`: point it at a vision document and
it proposes a curated backlog — epics and one-PR issues — behind the `proposed`
label a human lifts to queue the work.

The run is one `claude` invocation through the same path `polako work` uses,
bracketed by two enforcement mechanisms that keep the curation gate structural
rather than something the model has to remember:

- **The cap.** The stream watcher counts `gh issue create` tool calls and kills
  the run at `-max-issues`, epics included, the way it kills a stalled one.
  Over-cap is loud, never destructive — nothing is closed.
- **The label pass.** Before the run, `polako plan` snapshots the open backlog.
  After it — always, even on a crash, the cap kill or a Ctrl+C — every issue
  this `gh` account created since is normalised to carry **exactly** the
  `proposed` label (a missing one added, any other stripped), and the batch
  milestone is attached to any the skill missed. A failure to label is reported
  loudly and makes `polako plan` exit nonzero; it is never swallowed.

The honest edge: an operator hand-filing an issue from the same account while a
run is going is caught in the sweep — logged, visible, reversible, rare.

When it ends — a clean finish, the `-max-issues` cap, a crash, a Ctrl+C — a
plan run leaves the two traces every run leaves: one `kind:"plan"` line in the
[run data](run-data.md), and, when it proposed something, one `proposed`
[notification](#being-told-when-it-needs-you--notify) naming what awaits
curation. A run that proposed nothing notifies nothing.

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

## Where the backlog stands: `polako status`

While a shift runs, the only view of it is the terminal it runs in. Everything
worth knowing is already on GitHub — that is where all the orchestration state
lives — but reassembling it means a queue page, a label search and a PR tab.
`status` prints the whole picture at once:

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

What it prints is what a shift starting right now would do next — which is the
same thing a running shift is already doing. It reports **state, not liveness**:
it never asks whether a shift is running, and says the same thing either way.
That is what makes it useful from a laptop about a shift running on a server.

The closing `needs you:` line is the point of the whole thing — the items only a
person can move. A PR polako would remediate itself (conflicting, red, or
carrying an unanswered review) is deliberately not on it: that one is still
polako's job.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-repo` | *(whatever `-dir` is a checkout of)* | Repository to report on, `owner/name`. Naming it is what lets the command run from anywhere — no checkout needed, just a `gh` authenticated for the repo. |
| `-dir` | `.` | Path to the repository's main checkout, used to resolve the repository when `-repo` is not given. |
| `-label` | *(none)* | Only count issues carrying this label, the same scoping `polako work`'s `-label` applies. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how open PRs are matched back to issues. |
| `-strict-order` | `false` | Report as a work run with `-strict-order` would: an issue awaiting an answer keeps its place, so `next` can name it rather than the ready issue behind it. |
| `-json` | `false` | Print one JSON document to stdout instead of the text report — see [As JSON](#as-json--json) below. |

They take environment defaults the same way `polako work`'s do, so a
`POLAKO_LABEL` that scopes your work scopes the report of it too — and
anything narrowing or reordering the snapshot is named on the header line, since
a flag left in a profile is otherwise invisible here.

**Reads only.** Every call it makes is one of the read subcommands polako
itself re-derives state with at startup — `gh issue list`, `gh pr list`, `gh pr
view`, and the REST read of a thread's comments — so nothing here can move an
issue, a label or a PR. A test asserts the list of calls, which is stronger than
checking that nothing changed: a write GitHub refuses changes nothing either.

It reads no run data. Those files are write-only outside `stats`, and a status
that opened them would be wrong anyway about a shift running on somebody else's
machine.

It prints no issue, PR or comment text — numbers, branches, labels and states
only. That is what you need in order to decide where to go next, and it keeps
text anybody on the internet can write out of your terminal.

`-skip` is deliberately not among the flags: it is a head-of-line escape hatch
typed on one invocation rather than a property of the backlog, and the person
running `status` is the one who typed it.

Two things worth knowing about the numbers. **Quiet** is the age of the newest
comment on a thread, which is a proxy for how long the question has waited:
which comment is the skill's question cannot be told apart from here, since the
polako asks under your own credentials, so this reports what it can actually see.
And the PR table details the first eight PRs it finds on issue branches — one
issue is in flight at a time, so there is normally one — with anything past that
listed by number and said out loud rather than silently dropped.

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

With `-json`, stdout carries exactly one document — no header, no `needs you:`
line, nothing else — so a pipe into `jq` sees only the facts.

It is the same snapshot the text report renders, field for field: `queue`
holds the same five lists (`ready`, `blocked`, `parked`, `proposed`,
`containers`), `next` names the issue a shift starting now would pick up and
why, `prs` is the same table (`mergeable`/`checks`/`review` are the exact
strings the text columns print, including `not read` for a PR past the
eight-PR cap whose state was never looked up — `unknown` is a different,
also-real state: gh was asked and does not know), and `needs_you` is the
closing line's clauses as an array instead of a `;`-joined sentence.

`queue.containers` is objects, not bare numbers — `{ "issue", "total",
"completed", "finished", "held" }` — so a caller can tell a finished container
from one still in progress without a second call. `finished` is `total > 0 &&
completed == total` computed once in Go rather than left for every consumer
to reimplement — a jq script checks the field rather than repeating the
comparison. `held` is `true` when `needs-human` or `proposed` is on the
container: a finished one with `held: false` is about to be closed by the next
shift, a finished one with `held: true` is the caller's to close. This narrows
the shape #118 originally published, where it was a plain array of issue
numbers like every other list here.

Every array field is always present as `[]`, never `null`, even when empty —
`.queue.ready[]` never needs a `// empty` guard. `quiet_seconds` is the one
field that can be *absent*: it is whole seconds since the thread's newest
comment, omitted rather than `0` when that comment's timestamp could not be
parsed, so "just replied" and "unreadable" cannot be confused. `plan` is
absent the same way, and for the same reason: a probe that could not read
the CLI's own `/usage` output — an old CLI, an account with no subscription,
a wording change — leaves the field out entirely, never `""` standing in for
"no usage at all". The same rule `status`'s text report follows applies here
too — no issue, PR or comment text, only numbers, branches, labels, states
and URLs.

## Reclaiming finished issues: `polako tidy`

Cleanup today only runs from inside a shift that watched a merge itself.
Every other run — and every interactive one — leaves its worktree and its
branch behind. `tidy` is what an operator points at that backlog once:

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

- the issue is closed, or its PR is merged — GitHub is the authority, as
  always;
- the branch is merged into the default branch (an ancestor of
  `origin/HEAD`'s branch, after a fast-forward refresh exactly like the one a
  shift does before picking up an issue). A branch merged via a squash is not
  literally an ancestor of anything, and this does not try to reason about
  that — it reports "not merged into the default branch" and leaves the
  branch alone;
- its worktree, if it has one, has no uncommitted or untracked changes
  (`PLAN.md` — the skill's own planning note, which nothing ever commits —
  does not count against it, the same exception `-dry-run`'s park messages
  already carry);
- nothing about it is unpushed.

Anything that fails one of those is named and left alone — that output is
the feature, not a diagnostic: it is how you learn `issue-178` still has two
files uncommitted. Refusing is always recoverable; removing wrongly is not.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-repo` | *(whatever `-dir` is a checkout of)* | Repository to reclaim in, `owner/name`. |
| `-dir` | `.` | Path to the repository's main checkout, used to resolve the repository when `-repo` is not given. |
| `-branch-prefix` | `issue-` | Branch prefix the skill uses; how a branch is matched back to an issue. |
| `-apply` | `false` | Actually remove worktrees and delete branches. Every other verb here defaults to acting; this is the one whose actions cannot be undone, so it defaults to only reporting what it would do. Cannot be set from the environment — see below. |

`-branch-prefix` and `-dir`/`-repo` take environment defaults the same way
every other verb's do. `-apply` deliberately does not: a `POLAKO_APPLY=1`
left in a shell profile would turn every future preview into a live deletion
run, which is exactly the mistake defaulting to dry-run exists to prevent.

A `.claude/worktrees/<slug>-<hash>` entry is matched the same way any other
worktree is — by the branch it has checked out, not by its directory name —
so one of those is reclaimed exactly when it carries a finished `issue-N`
branch, and left alone otherwise, including a detached one, which carries no
branch at all.

A repository with nothing to reclaim prints one line and exits 0.

