# Token spend: is a drain more expensive than it needs to be?

Scope: a diagnosis, plus drafted tickets — no code in this
document · Behavior change: none

Issue #239 reports a shift that worked two issues (#102, #103) and left the
5-hour session pool at 84%, and asks whether polako spends more than it has to
and, if so, for tickets to fix it. This document is the answer to the first
half and the draft of the second.

## What the numbers actually say

The shift log in #239:

```
14:20:11 [claude] finished (ok) — 144 turns, 30m10s, $8.41     # issue #102
15:04:53 [claude] finished (ok) — 167 turns, 34m29s, $10.20    # issue #103
15:15:01 session usage is at 84% … stopping
summary: 2 issues merged, $18.61 spent, 1h25m wall clock
```

Two things are worth establishing before calling this a bug.

**These numbers are real, not a measurement artifact.** Each issue ran as a
single `claude` process — one `finished (ok)` line each, no `resume` in
between. `total_cost_usd` last-wins per process (`main.go:3288`), so with no
resume there is nothing for the accumulation bug in #227 to inflate. $8.41 and
$10.20 are what those two runs cost. (That is *not* true once a run stalls and
resumes — see ticket 5 below.)

**~$9 and ~40% of a session pool per issue is the honest price of the current
cycle.** One `/implement-issue` run does: Phase 0 context, Phase 2 plan, Phase
3 implementation, the mandatory review gate (a forked agent with its own
subagent fan-out), fix commits, and `gh pr create` — 144–167 assistant turns,
each re-sending the conversation so far. On Sonnet, at this repo's size, that
is a plausible cost rather than a leak. The supervisor then waited on two
green PRs for free (no model calls in `supervisePR`'s happy path).

So: **there is no single smoking-gun overspend.** What there is instead is a
stale skill, a review gate that costs the same on a one-line fix as on a
rewrite, no ceiling by default, and a cost-accounting gap that means we cannot
actually measure the resumed-run case we most want to. Each is a ticket.

## The one that costs nothing to fix: the skill was three releases behind

The shift's own startup line:

```
version skew: this binary is 0.14.0 but the installed polako plugin is 0.12.1
```

Between 0.12.1 and 0.15.0 the skill picked up, specifically, cost work:

- **#217 (0.14.0)** — a polling floor for the one-turn wait. Before it, one run
  "spent an eighth of its tool calls on `sleep` and status polls".
- **#216 (0.14.0)** — a resume point for the review gate. Before it, a run
  killed anywhere after the review re-ran the entire review sweep — the
  single most expensive part — from scratch on resume.
- **#225 (0.15.0)** — the review gate's level scales to the diff: `medium`
  under 300 changed lines, `high` at or above. Before it, *every* issue,
  including a one-line `docs:` fix, ran `/code-review high` with the full
  subagent fan-out.
- **#227 (0.15.0)** — the metrics accumulation fix (see ticket 5).

#102 and #103 ran without any of this. The first and cheapest move is to
upgrade the plugin — but "upgrade the plugin" is not a fix, because nothing
made the operator do it. That is ticket 1.

## Drafted tickets

Ordered by value-per-effort. Numbers are for reference in this doc, not issue
numbers.

### 1. Make a skill that is behind the binary hard to ignore

**Problem.** Version skew is one `narrate(sevWarning, …)` line at startup
(`main.go` `warnOnVersionSkew`), buried in the eight-line preflight banner. A
supervisor started from a service file never sees it, and #239 is what a
whole shift on the stale skill looks like: the pre-#225 review gate on every
issue, no #216 resume point, no #217 polling floor. The branch-name contract
is the stated reason the warning exists; the cost regression is a second one
and is not called out.

**Shape.** When the installed skill's version is *older* than the binary
(not merely different — a newer skill or a hand-installed one is the operator
doing something deliberate, `main.go:2107`), escalate: either refuse to start
without `-ignore-skew`, or repeat the warning after every Nth issue so a long
shift keeps surfacing it. Keep `-skill` pointing anywhere as an escape hatch.

**Done when.** A drain whose plugin is behind its binary either does not start
or says so more than once, and the message names the cost behaviour, not only
the branch contract.

Filed as #254, closed.

### 2. Let the review gate be cheaper on a small diff than "medium"

**Problem.** #225 scaled the gate's *level* (medium/high) to the diff, but both
levels still fork an agent and fan out subagents. #227's own evidence: the
review gate is "every run that reaches it", and a gate run there recorded 731
tool calls and $29.57 for a shift. On a genuinely small change — the kind
`/implement-issue` is sized to produce — that fan-out can dwarf the
implementation it is checking.

**Shape.** A skill-side change (so it runs its eval cases and earns a row in
`docs/experiments.md`). Under some small-diff threshold, have Phase 3's gate
do a single-pass self-review of the diff instead of invoking `/code-review` at
all — the substitute pass the skill already defines for when the review skill
is unavailable, promoted to the deliberate path for trivial changes. Measure
merged-PR quality across a tagged batch before and after.

**Done when.** A batch of small issues worked with the cheap path holds its
eval scores against a batch worked with the full gate, and `stats -by tag`
shows the spend difference.

Filed as #255, closed.

### 3. A default ceiling, or a preflight that recommends one

**Problem.** `-max-cost`, `-max-issue-time` and `-max-session-cost` all default
to off — a deliberate choice on #7 ("existing behaviour is unchanged"). The
only always-on guard is `-stall`, which by construction catches *silence*
only: "an agent looping productively but uselessly for three hours emits
events the whole time and is therefore invisible". The #216 shift ($29.57,
90m30s, 731 tool calls on one issue) is that failure mode, and it cost real
money before anyone noticed.

**Shape.** One of, in increasing intrusiveness:
- Preflight prints a recommended `-max-issue-time` / `-max-session-cost` for
  the operator to copy, derived from `stats` history if there is any (the
  pricing-line precedent from `backlog-fill.md` — human-facing, computed after
  the fact, influencing nothing).
- A soft mid-run log line the first time a single issue's tally crosses some
  dollar figure, without killing it.
- A conservative default `-max-issue-time` (not cost — wall time needs no
  pricing and cannot be gamed by the accounting gap in ticket 5), overridable
  and disableable with `0`.

**Done when.** A drain left running unattended cannot spend the #216 shift's
$29.57 on one issue without either warning or stopping, and the default (if
any) is documented and `0`-disableable.

Filed as #256, closed.

### 4. `stats -by reason`

**Problem.** Every run record carries `Reason` —
`implement`/`resume`/`unfinished`/`answers`/`remediate`/`checks`/`review`
(`metrics.go:47`) — but `stats -by` only accepts `issue`/`model`/`tag`/`shift`
(`stats.go:56`). So "how much of the batch went to the review-gate remediation
loop vs first-pass implementation vs stall resumes" is a `jq` exercise, not a
flag. That is exactly the breakdown #239 needs to target further work — and
`unfinished` (a `--resume` after a clean exit that opened no PR) is the row
that says whether the skill's one-turn rule is holding, so a `-by reason`
implementation must cover all seven, not drop it.

**Shape.** Add `reason` to `byGroups` and its `-drain` echo, document it beside
the others in `docs/run-data.md`, extend the `-by` test. Small, self-contained,
no recorder change — the data is already on disk.

**Done when.** `polako stats -by reason` prints a row per reason with runs,
cost and tokens, and a `docs/` test covers the new value.

Filed as #257, closed.

### 5. Settle #227's open questions before trusting any resumed-run cost

**Problem.** #227 fixed per-turn field accumulation across the multiple
`result` events one process emits. It explicitly left two questions open, and
they are the ones that matter for #239:

1. **Is `total_cost_usd` cumulative across resumed *processes*, not just
   prompts?** `tally.add` does `t.costUSD += rec.CostUSD` (`metrics.go:363`)
   once per `execClaude` call. If process 2 of a resumed session reports
   process 1 + process 2's cost, every stall-and-resume double-counts the
   pre-stall spend — and `-max-session-cost`, `-max-cost`, `-post-summary` and
   the exit summary all inherit it. `defaultResumeCeiling` is 20, so a
   pathological session could report many times its true cost.
2. **`model_usage` cannot be reconciled with session transcripts at all** —
   #227 recorded `mu_out` figures exceeding every total on disk.

#102/#103 had no resumes, so #239's headline numbers are safe — but the moment
a shift hits a stall, its reported spend and its `-max-session-cost` gate stop
being trustworthy, which undermines every other ticket here that wants to
measure something.

**Shape.** One controlled experiment first: a run with a known single-process
cost, killed once and resumed, with the two process costs and the tally
compared. Q2 is a separate investigation into what `model_usage` counts.

**Settled (2026-09-02, CLI 2.1.252, issue #258).** Both answers are no-change.

*Q1 — not cumulative across processes.* One session driven through three
processes billed $0.0695 (a 900-word essay), then $0.0165, then $0.0091 (a
one-word reply each). A counter carrying the session's history cannot go down,
so `total_cost_usd` is **process**-cumulative: it accumulates across the
prompts one process dequeues and resets when the process does. Summing the
three processes' `modelUsage` reproduces the session transcript's deduped
totals exactly on all four token fields, so the processes partition the session
rather than overlapping. `tally.add`'s `+=` is therefore correct as it
stands, and the per-session-delta rewrite this ticket sketched would have
*undercounted* every resume. The trap was one loose word: `stream.go` said
"session-cumulative" where it meant "process-cumulative", which is what left
#227's question open. The word is now fixed, and the measurement is in the
comment beside it.

*Q2 — `model_usage` is authoritative; `tokens` is not.* Across all 144
records on hand that carry one, `model_usage`'s `cost_usd` entries sum to
the record's `cost_usd` **exactly**, with zero mismatches, and its token
counts are never below `tokens` (median ~1.3x, max 2x post-#227). The reason
it "cannot be reconciled with session transcripts" is that the transcript is
not a complete record of a run: one API response is written as several lines
repeating a single `usage` block (95 of 126 responses on the run measured,
83.8k output tokens of double-count), and subagent turns are never written at
all — 0 of 195 transcript files in this repo's project directory hold a
sidechain entry. Deduped on `message.id`, the main chain of session
`3a06e67c` matches its record's `tokens` block field for field (in 252,
out 75,919, cache read 22,151,813, cache write 221,223) while `model_usage`
is larger (out 135,983). So `tokens` is the main loop, `model_usage` is the
main loop plus subagents and sidecar models, and the gap is real work rather
than a measurement error.

**Done when.** ~~A deliberately resumed run records its true end-to-end cost
once, and `docs/run-data.md` says whether `model_usage` is authoritative.~~
Done: the resume was run, no code change was needed, and `docs/run-data.md`
now says `model_usage` is the authoritative figure.

## Considered and not proposed

- **The two `claude -p /usage` probes per issue** (`usageGateReason` and
  `sampleWeekUsage`, deliberately not sharing a snapshot — `main.go:449`).
  Each is a headless session, so a gated shift pays two extra tiny model calls
  per issue. The comment argues the correctness case for not caching, the
  cost is a rounding error next to a 150-turn implement run, and collapsing
  them reintroduces the stale-reading bug the split fixed. Left alone.
- **Running planning or the review gate on a cheaper model.** Plausible, but
  it is a skill-side model-selection design question of its own, not a bug
  fix, and it belongs in the `docs/experiments.md` loop with a real A/B
  rather than in a token-spend bug. Drafted in
  [model-and-effort.md](model-and-effort.md); the `remediation-effort-medium`,
  `remediation-sonnet` and `plan-best` rows in `docs/experiments.md` are the A/B.

## Recommendation

Ticket 1 and ticket 5 first: ticket 1 stops the most common way a shift
overspends (a stale skill), and ticket 5 is the prerequisite for measuring
whether tickets 2 and 3 did anything. Tickets 2, 3 and 4 then follow with
real before/after numbers behind them.
