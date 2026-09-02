# Run data and cost tracking

Every run writes one line of numbers, so you can answer what a cleared backlog
actually cost — and which settings are worth changing.

**What is written, in full:** for each `claude` invocation, one JSON object
with the repo and issue number, a random shift id, why the run happened and
what it left behind (a PR, questions, a closed issue, or neither), status,
exit code, turns, tool-use count, wall and API duration, tokens (in / out /
cache read / cache write, per model), dollars, and the configuration under
test (skill, model, permission mode, `-run-tag`, a tool-allowlist hash, the
strategy knobs, and the three versions of binary, skill and Claude CLI in
play). One more object per issue records how it ended (`merged`,
`closed_no_change`, `closed_unmerged` or `needs_human`, with a
`park_reason` when parked) and, when GitHub can answer (one extra `gh pr
view`, skipped under `-metrics off`), the PR's additions, deletions,
changed files, review count, and open/merge times. When
[the usage gate](#capping-what-a-shift-spends) is on, it also carries the
plan's week-usage percent at pickup and at the terminal state.

[`polako plan`](reference.md#planning-a-backlog-unattended-polako-plan) and
[`polako health`](reference.md#auditing-repository-health-unattended-polako-health)
each write their own kind, `plan` and `health` — one line per run, plus
`issues_created`, `epics_created`, `cap` (`-max-issues`) and
`labels_enforced`; `plan` also carries `vision` and `milestone`, `health`
neither, and neither carries an issue or PR number. `stats` skips both
kinds today.

**What is never written:** issue titles, bodies, PR titles, comment text,
review text, diffs, or anything the model said — reviews are counted, never
quoted. Records hold numbers, identifiers and labels you chose, which is
what makes one of these files safe to hand to a teammate unread. They live
at `~/.polako/metrics/<owner>--<repo>.jsonl`, one append-only file per
repository, created `0700`, never inside your checkout.

**Run data never leaves your machine unless you ask it to** — no telemetry
endpoint, no phone-home. The one exception is
[`-post-summary`](#putting-it-on-the-pr--post-summary), off by default,
which puts one line of numbers on your own merged PR; the installed skill
itself collects nothing, it's a prompt.
[`-remote`](reference.md#watching-a-shift-from-anywhere--remote) isn't a
second exception today: no `claude` CLI registers headless runs with Remote
Control, so nothing about the session goes anywhere — see
[security.md](security.md#-remote-and-why-it-is-not-in-that-table) for the
day a CLI does.

To write nothing at all:

```bash
polako work -metrics off
```

Records are write-only, with exactly two readers — `stats`, and the cost
line `polako plan` prints after its label pass. Deleting the directory
mid-shift changes nothing about what the supervisor does next. Writes are
best-effort: a failure warns once and polako carries on.

## Capping what a shift spends

`-max-issue-time` defaults to `45m`; the rest are off unless you set one.

```bash
polako work -max-cost 15 -max-issue-time 90m -max-session-cost 200 \
  -max-session-usage 90 -max-week-usage 90
```

- **`-max-cost`** — dollars one issue may cost before it is parked.
- **`-max-issue-time`** — run time (not wall clock) one issue may consume
  before it is parked, so a slow reviewer never costs it the park. Catches
  what `-stall` can't: an agent looping productively but uselessly for
  hours still emits events, so that watchdog never fires; defaults to `45m`.
- **`-max-session-cost`** — dollars this shift may spend before it stops.
- **`-max-session-usage`** / **`-max-week-usage`** — the plan's own limits,
  read off `claude`'s `/usage` between issues. Unlike the cost caps these
  never park an issue: whichever pool is over its ceiling, the drain waits
  that pool's reset out and carries on (see [behaviour.md](behaviour.md), "A
  session limit is waited out, not retried against"); Ctrl+C is safe
  throughout, since all state is on GitHub. A probe that can't answer trips
  nothing.

The two per-issue caps read the tally of every run dispatched for the issue
— first attempt, resumes, remediation — and gate work about to be
dispatched, never work already done: an overspending run that *opens a PR*
isn't parked, since the work is on GitHub already. The reason goes on the
park comment and the exit summary:

```
  parked  #16 ($15.40) — this shift has spent $15.40 on it, the whole of its -max-cost of $15.00
```

`-max-session-cost` is checked between issues, not inside one, so one issue
can carry the total past budget by whatever it costs; size it with a run's
worth of headroom. Nothing is parked when it trips — polako logs what it
spent and exits 0; starting again picks up exactly where it stopped.

One honest limitation: a crashed, stalled or interrupted run never emitted
a `result` event, so it reports zero cost, and a shift that keeps dying
spends more than the number admits:

```
summary: 2 issues merged, 1 issue parked, $9.10 spent (3 runs reported none, so that is an undercount), 5h40m of wall clock
```

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

Numbers only, on the PR they describe. A crashed, stalled or interrupted run
never reported a cost, so the comment says how many and that its tokens and
dollars are undercounts. It covers only the runs *this* shift supervised, so
a restarted supervisor reports what it saw rather than claim a free PR.

Export `POLAKO_POST_SUMMARY=1` for a default — see
[Setting defaults from the environment](reference.md#setting-defaults-from-the-environment).
Independent of `-metrics`, so `-metrics off -post-summary` gives team
visibility with no local files. Best-effort: a comment that can't post is a
log line, never a failed shift.

## Reading it back: `polako stats`

`stats` is the only thing that reads those files — its sibling
[`status`](reference.md#where-the-backlog-stands-polako-status) reads GitHub
instead.

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
| `-window` | *(off)* | Report a calendar-aligned window instead of `-since`: `today`, `week`, `month` or `session` — see [Calendar windows](#calendar-windows--window) below. Errors if `-since` is also given. |
| `-shift` | *(every shift)* | Only count records from one shift — its id, or `last` for the newest shift in scope. |
| `-by` | *(none)* | Add a breakdown table: `issue`, `model`, `tag` or `shift`. |
| `-runs` | *(off)* | Add the run log: one row per run, with the session id that reopens it — see [Reopening a past run](#reopening-a-past-run--runs). |
| `-html` | *(off)* | Also write the report to this path, as one self-contained HTML file — see [Keeping a copy](#keeping-a-copy--html). |
| `-json` | `false` | Print one JSON document to stdout instead of the text report — see [As JSON](#as-json--json) below. |

A window can keep an issue's terminal record while clipping the runs that
produced it — those issues still count toward the merge rate but can't be
priced, and the report says when that differs. Unreadable files (`0600`
records in a shared directory) are named rather than failing the report.
**Blocked on answers** is the gap between the run that posted questions and
the re-run that folded the reply in; **PR open to merge** ends whenever
somebody pressed the button — part of the elapsed time, not a property of
the automation.

**On park reasons:** `needs human` is one bucket on the `terminal` line;
**park reasons** breaks it down (`budget 3, checks remediation 1`), an
identifier chosen at the park itself, never the sentence posted to the
thread. A park that can't say records `unknown`. **On PR size:** a terminal
record carrying GitHub's answer about its PR adds a **change per issue**
line — median additions, deletions, changed files and reviews; records
from before that enrichment carry none.

**On resumed sessions:** a crashed run and the `--resume` that finishes its
work are two records, and `stats` sums both — including `unfinished`, a
`--resume` of a run that ended its turn without a PR rather than crashed.
Summing is correct: a `--resume`d run's `result` event reports that
invocation alone, not the session it continued (measured on issue #258).

**On `model_usage`:** it is the authoritative figure and `tokens` is not.
`tokens` is the main conversation loop alone; `model_usage` also counts
subagents and the CLI's sidecar models, so it never reads below `tokens`
(~1.3x median, 2x through the review gate) and its `cost_usd` entries sum to
the record's own exactly, in every record on hand. A run with subagents
streams several `result` events: `turns`, `wall_ms`, `api_ms` and `tokens`
sum across them, while `cost_usd` and `model_usage` take the last event's
process-cumulative figure (#227, which had undercounted sixfold). Neither
reconciles against a session transcript, which repeats one `usage` block
over several lines and omits subagent turns.

**On approximated runs:** a crashed, stalled or interrupted run's tokens
are the tally seen streaming past, dollars read zero — counted out
separately so a crash-prone configuration doesn't get to look cheap.

## Calendar windows: `-window`

`-since` looks back a fixed span from now. A plan's own limits reset on a
calendar boundary instead, so `-window` names one:

```bash
polako stats -window today    # local midnight to now
polako stats -window week     # the plan's own week, or Monday 00:00 local
polako stats -window month    # the 1st of the month, local, to now
polako stats -window session  # an approximation of the plan's 5h block
```

`-window` and `-since` are mutually exclusive — giving both is a flag error.
`today` and `month` use calendar arithmetic, so they land correctly across
a DST change or a 28/30/31-day month. `week` anchors to the plan's own
weekly reset when the same usage probe
[`-max-week-usage`](#capping-what-a-shift-spends) reads can answer, or the
most recent Monday 00:00 local when it can't. `session` approximates the
plan's five-hour block, anchored to the earliest run seen in the last 5h,
always labelled an approximation.

The header names the resolved bounds and how far through the window falls:

```
  window  2026-08-24T00:00:00Z → 2026-08-25T00:00:00Z (today; 9h12m elapsed, 14h48m left, 38% through)
```

`-json` carries the same facts as a typed `window` object (`kind`, `from`,
`period_end`, `anchor`, `elapsed_seconds`, `remaining_seconds`), present only
when `-window` was given.

## What an issue costs the plan

When [the usage gate](#capping-what-a-shift-spends) is on, each issue's
terminal record carries two samples of the plan's week-usage percentage, at
pickup and at the terminal state. The delta is what that issue cost the
plan, as a percentage of a week:

```
  plan cost per issue  1.4% mean, 1.7% median of a week — about 59 issues to a full week (upper bound: counts everything the account did meanwhile, not just this issue; polako's own share was 29% of the last 24h)
```

**Stated as the upper bound it is** — the delta counts everything the
account did during that span, not just this issue. The parenthetical is a
cross-check: the usage probe's own attribution figure ("polako was N% of
the last 24h"), shown beside the mean/median but never folded into it.
Absent when no terminal issue in scope has a usable sample; a mid-issue
reset drops that reading rather than average it into a meaningless
negative.

`-json` and `-html` carry the same figures, as a typed `plan` object or a
card respectively, present only when at least one issue has a usable
sample.

## Telling one shift from another: `-shift`

Records from last night's batch and a shift still running interleave in one
file, and `-since` can't separate them. So every shift stamps its records
with a random id, once at startup:

```
  run data  /Users/you/.polako/metrics — numbers only, never leaves this machine (-metrics off to disable)
  shift     7f3a91c4 — `polako stats -shift 7f3a91c4` reports on it alone
```

Nothing else reads that id back. Two questions it makes exact:

```bash
polako stats -shift last     # what has the shift running right now spent?
polako stats -by shift       # what did each shift do?
```

`last` is whichever shift wrote the newest record *in scope*, so it composes
with other filters — `stats -shift last -repo owner/name` is the last shift
to touch that repository. The report names the id it resolved to:

```
  filtered  for scharissis/polako from shift 7f3a91c4
```

Records written before ids existed group and filter as `(none)`.

An issue picked up by one shift and finished by another is counted under
each, and `-by shift` says when that happened: **merged** is the issue's
own final outcome there, so both shifts count the merge, but under `-shift
<id>` the report shows only what that shift concluded (`needs human` for
the one that handed the issue on).

## Reopening a past run: `-runs`

Everything above is a rollup. `-runs` adds the ledger those numbers were
derived from — one row per run, in order, under whatever `-repo`, `-since`
and `-shift` are already in force:

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

`session` is the point of it — a session id is what the Claude CLI keeps the
transcript under, so any row reopens the whole run exactly as it ended:

```bash
claude --resume 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9
```

The two rows sharing an id above are a crash and the `--resume` that
finished its work. A run that reported no session renders `—`: it died
before the CLI announced itself. The id is also in the live log, on every
run's first line and beside a park:

```
[claude] session started (model claude-opus-5, session 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9)
issue #48 needs a human: claude crashed and 3 resume attempts failed — parking it and moving on
issue #48: `claude --resume 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9` reopens what the last skill run on it did
```

It stays local: the id goes to the terminal and the record file, never onto
the issue thread.

## Keeping a copy: `-html`

`-html` writes the same report as one self-contained HTML file you can
keep, send to a teammate, or open next month without the records that
produced it:

```bash
polako stats -html ~/polako-report.html
```

The text report still prints — `-html` adds an output, it doesn't swap one
out. The page holds everything the text report does, laid out rather than
listed: headline numbers as cards, spend over time as a chart, how issues
and runs ended as proportion bars, then the summary sections and the `by
shift`/`by issue` tables. Issue numbers link out to GitHub; `-repo`,
`-since`, `-shift`, `-by` and `-runs` narrow it as they narrow the text.

**Self-contained means self-contained** — no script, no external stylesheet,
no webfont, no image; the chart is inline SVG. Links to github.com are the
one thing naming a remote host, fetched only when clicked.

Written `0600`, the same as the records themselves, holding no issue,
comment or PR text either. An empty window still gets a file, saying so.

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

Field for field, this is `polako stats` above: `source` is the
`read`/`window`/`repos` line, `issues`/`runs`/`cost`/`latency` the four
summary sections. `-by` and `run_log` are top-level fields present only
when given:

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
vocabulary this page documents (`opened_pr`, not "opened pr"). Every array
and breakdown map defaults to `[]`/`{}`, never `null`; a handful of fields
are conditionally *absent* instead — `park_reasons`, `change_per_issue`,
`per_day_usd`, `per_merged_usd`, the four `*_per_issue*` fields — exactly
when the text report's matching line is. `-by` and `run_log` are omitted,
not empty, when their flag wasn't given. With `-json -html` together, the
"wrote the HTML report to …" line moves to stderr, so stdout carries
exactly the document a `| jq` pipeline expects.

## Comparing configurations

`-run-tag` labels a batch so you can price one setup against another later:

```bash
polako work -model claude-opus-5 -run-tag baseline
```

Change one thing — model, skill wording, `-stall` — tag the next batch
differently, and the two sets of records are comparable. The binary's
version doesn't pin the skill's text, so tag discipline is what makes
skill-wording experiments mean anything.

```bash
polako stats -by tag
```

```
by tag
  tag         issues  merged  runs   cost  $/merged  tokens
  baseline         3       2     5  $6.70     $3.35   16.9M
  terse-plan       2       1     2  $1.40     $1.40    2.2M
```

An issue worked under two tags is counted under each. For anything `stats`
doesn't answer, the files are JSONL, readable by jq, DuckDB or any
spreadsheet:

```bash
cat ~/.polako/metrics/*.jsonl | jq -s 'map(select(.kind=="run")) | map(.cost_usd) | add'
```

Tagging is a habit, not a flag: see
[plans/continuous-improvement.md](../plans/continuous-improvement.md) for
when a batch needs a fresh tag, and
[plans/experiments.md](../plans/experiments.md) for the verdicts.

**On dollars:** `cost_usd` is the CLI's API-equivalent pricing — real money
on API-key auth, notional on a subscription plan. Tokens are the ground
truth; dollars are derived from them.
