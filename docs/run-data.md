# Run data and cost tracking

Every run writes one line of numbers, so you can answer what a cleared backlog
actually cost — and which settings are worth changing.

**What is written, in full:** for each `claude` invocation, one JSON object
holding the repository and issue number, a random id for the shift that wrote
it, why the run happened and what it left behind (a PR, questions, or neither),
its status and exit code, turns, tool-use count, wall and API duration, tokens
(in / out / cache read / cache write, plus the per-model split), dollars, and
the configuration under test — skill, model, permission mode, `-run-tag`, a
hash of the tool allowlist, the strategy knobs, and the three versions in play
(this binary, the installed skill, and the
Claude CLI). One more object per issue records how it ended —
`merged`, `closed_unmerged` or `needs_human` — and, when it was handed back, why
(`park_reason`: `budget`, `retries_exhausted`, `produced_nothing`, `no_skill`,
`auth`, `conflict_remediation`, `checks_remediation`, `review_remediation`,
`pr_state`, or `unknown` when the path could not say) — and, when GitHub could
be asked, what the PR turned out to be: additions, deletions, changed files,
how many reviews it drew, and when it opened and merged. That is one extra `gh pr view`
as each issue ends, and none at all under `-metrics off`.

**What is never written:** issue titles, issue bodies, PR titles, comment text,
review text, diffs, or anything the model said. Reviews are counted, never
quoted. Records hold numbers, identifiers and labels you chose. That is what
makes one of these files safe to hand to a teammate, or paste into an analysis
session, without re-reading it first.

**Where it goes:** `~/.polako/metrics/<owner>--<repo>.jsonl`, one
append-only file per repository — so deleting one project's data is `rm` on one
file, and aggregating across projects is a glob. Created `0700`, so on a shared
machine the records stay yours. Never inside your checkout: the skill commits
things there, and cost data must not become committable by accident.

**Run data never leaves your machine unless you ask it to.** There is no
telemetry endpoint, no phone-home, and no network path out of the recorder — the
binary is the only thing that meters anything, and it writes to your disk. The
single exception is [`-post-summary`](#putting-it-on-the-pr--post-summary), off
unless you turn it on, which puts one line of numbers on your own merged PR. The
skill half, the part you can install from a marketplace, carries no data
collection at all: it is a prompt.

[`-remote`](reference.md#watching-a-shift-from-anywhere--remote) used to be
named here as the one other thing about a shift visible off this machine. It is
not one today: no `claude` CLI registers headless runs with Remote Control, so
polako no longer passes the flag and nothing about the session goes anywhere.
The flag stays for the CLI that supports it, and
[security.md](security.md#-remote-and-why-it-is-not-in-that-table) is where the
trade is argued for the day it does.

To write nothing at all:

```bash
polako work -metrics off
```

Records are write-only by design. The work loop never reads them, no decision
depends on them, and deleting the directory mid-shift changes nothing about
what the supervisor does next — that is what keeps run data compatible with
"all state lives in GitHub". Writes are best-effort: a failure warns once and
polako carries on.

## Capping what a shift spends

All three caps are off unless you set one, so a shift that sets none behaves
exactly as it always did.

```bash
polako work -max-cost 15 -max-issue-time 90m -max-session-cost 200
```

- **`-max-cost`** — dollars one issue may cost before it is parked.
- **`-max-issue-time`** — how much *run time* one issue may consume before it
  is parked. Not the wall clock since the issue was picked up: an issue spends
  most of its life waiting for you to merge its PR, and parking issues over how
  long that took would punish nobody's slowness but the reviewer's.
- **`-max-session-cost`** — dollars this shift may spend before it stops.

`-max-issue-time` is the one that catches what `-stall` cannot. That watchdog
kills a run that has gone *silent*; an agent looping productively but uselessly
for three hours emits events the whole way through and is invisible to it. This
cap does not care whether events are arriving, so it kills that run and parks
the issue for you.

The two per-issue caps read the tally of every run this shift dispatched for
the issue — the first attempt, its resumes, the re-run that folded your answer
in, and any conflict, CI or review remediation against its PR. They gate work
about to be dispatched and never work already done, which is why a run that
overspends but *opens a PR* is not parked: the work is on GitHub and waiting
for a merge costs nothing more. What is parked is the issue whose next run
would take it further over. The reason goes in the park comment on the thread
and in the exit summary, the same as any other park:

```
  parked  #16 ($15.40) — this shift has spent $15.40 on it, the whole of its -max-cost of $15.00
```

`-max-session-cost` is checked between issues rather than inside one, because
ending a shift cleanly means declining to take on more work rather than killing
a run part-way and having to park a healthy issue over it. So one issue can
carry the total past the budget, by whatever that issue costs. `-max-cost` does
not bound the overrun to itself, either: it gates the *next* run rather than the
one in flight, so an issue can end at its `-max-cost` plus the whole of the run
that carried it over. Size the budget with a run's worth of headroom under it.
Nothing is parked when it trips: polako logs what it spent, prints its
summary and exits 0, and since all state is on GitHub, raising the budget and
starting it again picks up exactly where it stopped.

One honest limitation, in the safe direction. A run that crashed, stalled or
was interrupted never emitted a `result` event, so it reported no cost —
pricing belongs to the Claude CLI and this binary will not guess at it. Its
tokens are still counted (as observed) and its duration is timed from the
clock, but its dollars are zero. A cost cap is therefore a ceiling on what was
*observed*, and a shift that keeps dying spends more than the number admits.
The summary says so when it happened:

```
summary: 2 issues merged, 1 issue parked, $9.10 spent (3 runs reported none, so that is an undercount), 5h40m of wall clock
```

Caps in force are named at startup, because
[the environment can set any flag](reference.md#setting-defaults-from-the-environment) and
a park whose reason quotes a `-max-cost` you never typed is a mystery worth
pre-empting.

## Putting it on the PR: `-post-summary`

Off by default. Turned on, each merged PR gets one comment:

```bash
polako work -post-summary
```

> **polako** — 3 runs, 1 question round, 12.4M tokens, $6.12, 2h14m of
> run time.
>
> <sub>Recorded by polako v0.5.0, covering the runs this shift
> supervised. Dollars are the Claude CLI's API-equivalent pricing.</sub>

Numbers only, on the PR they describe, readable by exactly the people who can
already see that PR. A run that crashed, stalled or was interrupted never
reported a cost, so when the tally holds one the comment says how many and that
its tokens and dollars are undercounts. It covers the runs *this* shift supervised, and says so: a
supervisor restarted mid-issue reports what it saw, and one that only waited on
a PR an earlier process opened comments nothing rather than claiming a free PR.

To make it your default without typing it, export
`POLAKO_POST_SUMMARY=1` — see
[Setting defaults from the environment](reference.md#setting-defaults-from-the-environment).
Startup says when it is on, so a variable you set months ago in a profile is
never a mystery.

It is independent of `-metrics`, so `-metrics off -post-summary` is the
combination for wanting team visibility and no local files at all. Best-effort
like the rest of run data: a comment that cannot be posted is a log line, never
a failed shift.

## Reading it back: `polako stats`

`stats` is the only thing that ever reads those files — its sibling
[`status`](reference.md#where-the-backlog-stands-polako-status) reads GitHub and
never touches them. A bare `polako` prints the verb table; nothing about the
report touches GitHub or starts a run.

```bash
polako stats
```

```
run data from /Users/you/.polako/metrics
  read    2 files, 11 records (1 unreadable line skipped)
  window  2026-08-20T09:00:00Z → 2026-08-24T11:03:11Z (4.1d)
  repos   scharissis/polako, scharissis/other

issues
  terminal          4 — merged 3 (75%), needs human 1
  park reasons      produced nothing 1
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
| `-metrics` | `~/.polako/metrics` | Directory to read records from — the same path a shift writes to. |
| `-repo` | *(every repository)* | Only count records for one repository, `owner/name`. |
| `-since` | *(all of it)* | Only count records newer than this, e.g. `-since 168h`. |
| `-shift` | *(every shift)* | Only count records from one shift — its id, or `last` for the newest shift in scope. |
| `-by` | *(none)* | Add a breakdown table: `issue`, `model`, `tag` or `shift`. |
| `-runs` | *(off)* | Add the run log: one row per run, with the session id that reopens it — see [Reopening a past run](#reopening-a-past-run--runs). |
| `-html` | *(off)* | Also write the report to this path, as one self-contained HTML file — see [Keeping a copy](#keeping-a-copy--html). |
| `-json` | `false` | Print one JSON document to stdout instead of the text report — see [As JSON](#as-json--json) below. |

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

**On park reasons:** `needs human` is one bucket on the `terminal` line, and
**park reasons** is what it is made of — `budget 3, checks remediation 1` says
which half of the tool the next change belongs in, which the count alone never
could. The value is an identifier chosen at the park itself, never the sentence
posted to the issue thread: that text quotes issue numbers, dollar figures and
branch names, and records hold none of those. A park path that genuinely cannot
say records `unknown`, which is deliberately not the same as a record written
before the field existed — those count as `unrecorded`, so an old file cannot
pass for a supervisor that stopped classifying its parks. The line is absent
when nothing in the window was parked.

**On PR size:** an issue whose terminal record carries GitHub's answer about
its PR adds a **change per issue** line — the median additions, deletions,
changed files and reviews. Records written before that enrichment existed carry
none, and neither does one whose lookup failed, so the line counts its own
issues and says how many. The same two timestamps give **PR open to merge** the
authoritative span, which is right even when the run that opened the PR falls
outside the window or belonged to a shift on another machine.

**On resumed sessions:** a crashed run and the `--resume` that finishes its
work are two records, and `stats` sums both. The same goes for the `unfinished`
reason, which is a `--resume` of a run that ended its turn without a PR rather
than one that crashed — counting how often that happens is how you tell whether
the skill's one-turn rule is landing.

Summing both is right, and that took measuring. A `--resume`d run's `result`
event reports **that invocation, not the session it continued**, so the two
rows do not overlap. The evidence is a real resume pair — same session id,
both sides reaching a priced `result` event — in which the resumed half
reported *fewer* turns (31 against 62) and fewer tokens on every field of
`usage` (15,651 output against 54,077; 4.9M cache reads against 6.2M) than the
run it resumed. A session total cannot go down. `total_cost_usd` rides the same
event and is exactly the sum of `modelUsage`'s `costUSD`, whose map on the
resumed run carried a single model key — not the key the earlier half had been
billed under, which a cumulative map would still be carrying. Reports used to
count the resumes and warn about this; they no longer need to, though the
`reasons` line still says how many there were.

One thing to keep straight when reading a record: its `tokens` and its
`cost_usd` do not cover the same work. `tokens` is the CLI's main-loop `usage`
block; `cost_usd` is `total_cost_usd`, which matches `modelUsage` — the
per-model breakdown that also aggregates whatever subagents the run spawned. So
a run that used subagents is billed for them and does not count their tokens,
and dividing one figure by the other is not a price per token.

**On approximated runs:** a run that crashed, stalled or was interrupted never
emitted a `result` event. Its tokens are the tally seen streaming past, an
undercount, and its dollars read as zero, because pricing belongs to the CLI
and this binary never guesses at it. Those runs are counted out separately
rather than mixed in silently — a crash-prone configuration should not get to
look cheap.

## Telling one shift from another: `-shift`

Records from last night's batch, this morning's restart and a shift still
running all interleave in one file, and `-since` cannot separate them: shifts
that ran back to back, or overlapped, defeat a time window. So every shift
stamps its records with a random id and says so once at startup:

```
  run data  /Users/you/.polako/metrics — numbers only, never leaves this machine (-metrics off to disable)
  shift     7f3a91c4 — `polako stats -shift 7f3a91c4` reports on it alone
```

That line is the only place the id appears — nothing reads it back, and the
shift keeps no note of it anywhere else. Two questions it makes exact:

```bash
polako stats -shift last     # what has the shift running right now spent?
polako stats -by shift       # what did each shift do?
```

`last` is whichever shift wrote the newest record *in scope*, so it composes
with the other filters rather than overriding them —
`stats -shift last -repo owner/name` is the last shift to touch that
repository, which is not always the last shift overall. Whichever way it
resolves, the report names the id it landed on rather than the word you typed:

```
  filtered  for scharissis/polako from shift 7f3a91c4
```

Records written before ids existed group and filter as `(none)`, the same
spelling an untagged run gets, so older files still load and still count.

An issue picked up by one shift and finished by another after a restart is
counted under each, and `-by shift` says when that happened. The two views of
such an issue differ on purpose. In `-by shift`, **merged** is the issue's own
final outcome — the same rule as `-by tag`, and what makes `$/merged` "spent by
this shift per issue of theirs that shipped" — so both shifts count the merge.
Under `-shift <id>` the records *are* that shift's, so the report shows what
that shift concluded, which for the one that handed the issue on is
`needs human`. One asks what became of the issues a shift worked; the other
asks what that shift did.

## Reopening a past run: `-runs`

Everything above is a rollup. `-runs` adds the ledger those numbers were
derived from — one row per run, in the order they happened, under whatever
`-repo`, `-since` and `-shift` are already in force:

```bash
polako stats -runs -since 48h
```

```
run log
  started               issue                 reason     status    outcome           session                               attempt   cost  tokens  wall
  2026-08-24T09:00:00Z  scharissis/polako#48  implement  ok        posted questions  0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9        0  $1.10    4.2M   20m
  2026-08-24T12:30:00Z  scharissis/polako#48  answers    ok        opened pr         6a1d90f3-77b2-4e58-8a0c-1b93ce4d2f71        0  $2.50    6.4M   30m
  2026-08-25T09:00:00Z  scharissis/polako#49  implement  crash     nothing           b2e7c045-19af-4d6a-b7f1-8c02ea3169d4        0  $0.00  746.5k    5m
  2026-08-25T09:06:00Z  scharissis/polako#49  resume     ok        opened pr         b2e7c045-19af-4d6a-b7f1-8c02ea3169d4        1  $3.00    5.5M   34m
```

The `session` column is the point of it — the last of the text columns, before
the numbers. A session id is what the Claude CLI keeps the transcript under, so
any row turns back into the whole run:

```bash
claude --resume 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9
```

That opens the run in Claude Code exactly as it ended — every message, every
tool call, every file it read — which is the intended way to find out what a
run actually did. Nothing extra is stored to make it work: the CLI already
keeps the transcript, and the records already keep the id.

The two rows above sharing an id are a crash and the `--resume` that finished
its work, which is what that pairing looks like from here. A run that reported
no session renders `—`: records written before this column existed carry none,
and neither does a run that died before the CLI announced itself.

The id is in the live log too, on every run's first line, and again beside a
park — the two moments somebody wants to read a transcript:

```
[claude] session started (model claude-opus-5, session 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9)
issue #48 needs a human: claude crashed and 3 resume attempts failed — parking it and moving on
issue #48: `claude --resume 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9` reopens what the last skill run on it did
```

It stays local, like every other number here: the id goes to the terminal and
to the record file, never onto the issue thread the park comment goes to.

## Keeping a copy: `-html`

The text report answers one question per invocation, sized to a terminal.
`-html` writes the same report as one HTML file you can keep, send to a
teammate, or open next month without still having the records that produced it:

```bash
polako stats -html ~/polako-report.html
```

The text report still prints — `-html` adds an output, it does not swap one out
— and the last line names the file it wrote. The page holds everything the text
report does, laid out rather than listed: the headline numbers as cards, spend
over time as a chart, how the issues and runs ended as proportion bars, then
the same summary sections and the `by shift` and `by issue` tables. Issue
numbers link out to GitHub. `-repo`, `-since` and `-shift` narrow it exactly as
they narrow the text; `-by model` or `-by tag` adds that table too, and `-runs`
adds the run log.

**Self-contained means self-contained.** No script, no external stylesheet, no
webfont, no image — the chart is inline SVG, and the whole file is markup and
styles in one document. It renders with the network cable pulled, and opening
it tells nobody that you did. A test asserts that against the rendered bytes,
because the file holds your private numbers and "it happens to work offline" is
not the same promise. The links to github.com are the one thing that names a
remote host, and a link is fetched when it is clicked, not when the page loads.

The file is written `0600`, like the records themselves — it is those same
numbers laid out, and the same rule applies to who on the machine can read it.
It contains no issue, comment or PR text, for the same reason [the records
don't](#run-data-and-cost-tracking): numbers, identifiers and your own labels, so
it is safe to hand to a teammate without re-reading it first.

An empty window still gets a file, saying so. A nightly `stats -html` that
skipped the write would leave yesterday's numbers on disk looking like today's.

## As JSON: `-json`

```bash
polako stats -json | jq .
```

```json
{
  "dir": "/Users/you/.polako/metrics",
  "scope": {},
  "source": {
    "files": 2,
    "records": 11,
    "skipped": 1,
    "unread": [],
    "window_from": "2026-08-20T09:00:00Z",
    "window_to": "2026-08-24T11:03:11Z",
    "repos": ["scharissis/other", "scharissis/polako"]
  },
  "issues": {
    "terminal": { "merged": 3, "needs_human": 1 },
    "done": 4,
    "in_flight": 1,
    "park_reasons": { "produced_nothing": 1 },
    "priced": 4,
    "runs_per_issue": { "mean": 1.5, "median": 1.5 },
    "cost_per_issue_usd": { "mean": 1.925, "median": 2 },
    "tokens_per_issue": { "mean": 4620475, "median": 3921950 },
    "tokens_per_issue_split": { "in": 2250, "out": 35225, "cache_read": 4362500, "cache_write": 220500 }
  },
  "runs": {
    "total": 7,
    "statuses": { "crash": 1, "no-turns": 1, "ok": 5 },
    "reasons": { "answers": 1, "implement": 4, "remediate": 1, "resume": 1 },
    "outcomes": { "nothing": 3, "opened_pr": 3, "posted_questions": 1 },
    "turns": 131,
    "tool_uses": 115,
    "approximated": 1
  },
  "cost": {
    "total_usd": 8.1,
    "per_day_usd": 1.98,
    "merged": 3,
    "per_merged_usd": 2.7,
    "tokens": { "in": 9400, "out": 145900, "cache_read": 18050000, "cache_write": 912000 },
    "total_tokens": 19117300
  },
  "latency": {
    "blocked_on_answers": { "count": 1, "median_seconds": 11400, "max_seconds": 11400 },
    "pr_to_merge": { "count": 3, "median_seconds": 4800, "max_seconds": 7200 }
  }
}
```

That is `polako stats` above, field for field: `source` is the `read`/`window`/`repos`
line, `issues`/`runs`/`cost`/`latency` are the four summary sections, and
every number is the one the text renderer printed — not a second computation
of it. `-by` and `-runs` add their own top-level fields, present only when the
flag was given:

```bash
polako stats -json -by tag -runs | jq '.by, .run_log[0]'
```

```json
{
  "kind": "tag",
  "groups": [
    { "name": "baseline", "issues": 3, "merged": 2, "runs": 5, "cost_usd": 6.7, "per_merged_usd": 3.35, "tokens": 16889000 },
    { "name": "terse-plan", "issues": 2, "merged": 1, "runs": 2, "cost_usd": 1.4, "per_merged_usd": 1.4, "tokens": 2228300 }
  ]
}
{
  "started": "2026-08-20T09:00:00Z", "repo": "scharissis/polako", "issue": 12,
  "reason": "implement", "status": "ok", "outcome": "posted_questions",
  "session": "s12a", "attempt": 0, "cost_usd": 1.1, "tokens": 4232000, "wall_seconds": 1200
}
```

`.by.issues` holds the rows instead of `.by.groups` when `-by issue` was
given. Outcome, status, reason and park-reason values are the raw on-disk
vocabulary this page documents (`opened_pr`, not "opened pr") — the JSON is
for scripts, and a script comparing against `"opened_pr"` should not also
have to know the text report's prose.

Every array and breakdown map defaults to `[]` / `{}`, never `null`, even
when empty — `.source.repos[]` never needs a `// empty` guard. A handful of
fields are conditionally *absent* rather than a zero value standing in for
"not applicable" — `park_reasons`, `change_per_issue`, `per_day_usd`,
`per_merged_usd`, and the four `*_per_issue*` fields together, which are all
absent exactly when the text report's matching line is absent ("nothing to
price", no park reasons, nothing merged, no PR-size data, less than an hour
of window). `scope.shift` is the *resolved* id (`ds.shift`), never the
literal `last`, the same rule [`-shift`](#telling-one-shift-from-another--shift)
follows in text. `-by` and `run_log` are omitted entirely, not empty, when
their flag was not given, so `.by == null` is how a script asks "was `-by`
used".

With `-json` and `-html` together, the file still gets written, but the
"wrote the HTML report to …" confirmation moves to stderr — stdout carries
exactly the one document a `| jq` pipeline expects, never a line of prose
after it.

## Comparing configurations

`-run-tag` labels a batch so you can price one setup against another later:

```bash
polako work -model claude-opus-5 -run-tag baseline
```

Change one thing — the model, the skill's wording, `-stall` — tag the next
batch differently, and the two sets of records are comparable. Note that the
binary's version does not pin the skill's text: you can run any binary against
any installed version of the plugin, so tag discipline is what makes
skill-wording experiments mean anything.

Then compare them:

```bash
polako stats -by tag
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
cat ~/.polako/metrics/*.jsonl | jq -s 'map(select(.kind=="run")) | map(.cost_usd) | add'
```

Tagging is a habit rather than a flag, and the habit is written down: the
README's [Improving polako](../README.md#improving-polako) has the rule for
when a batch needs a fresh tag, the retro that reads these reports, and two
recipes over the raw JSONL. [plans/experiments.md](../plans/experiments.md) is
where the verdicts land.

**On dollars:** `cost_usd` is the CLI's API-equivalent pricing — real money on
API-key auth, notional on a subscription plan. Tokens are the ground truth;
dollars are derived from them. This binary never hardcodes a price sheet, since
prices change and the CLI already applies the current ones.

