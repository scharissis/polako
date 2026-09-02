# Continuous improvement: closing the loop from run data to better shifts

Status: proposed · Scope: mostly ritual, plus small additions to the recorder
and `stats` · Behavior change: none to the work loop — that is the point

polako now measures itself. Every run writes a record, every terminal issue
writes another, `stats` rolls them up by issue, model, tag, shift and reason,
`-runs` lists every run with the session id that reopens it, and `status` says
where the backlog stands from GitHub alone. The question that started this document
was "should we store and review logs of previous runs?" — and the honest
answer is that the storing is done, half by us and half by the Claude CLI,
which already keeps every session transcript on disk keyed by the session id
our records carry. What does not exist yet is the **loop**: nothing routinely
turns those records and transcripts into changes, verifies the changes did
what they promised, and ships them.

This document designs that loop — measure → review → hypothesize → change →
verify → ship — and is explicit about which parts are code and which are
ritual. Most of it is ritual. That is deliberate: the invariants that make
polako trustworthy (write-only telemetry, no text in records, state in GitHub)
survive precisely because the improvement loop runs through the operator and
the backlog, never through the supervisor reading its own telemetry.

## Goals

1. **Every shift teaches something.** A parked issue, an outlier-expensive
   run, or a crash is reviewed while its transcript is fresh, and the finding
   becomes an issue on this repository — which polako then works. The loop is
   self-hosting by construction.
2. **Skill and configuration changes are measured, not hoped.** The eval suite
   gates skill text; `-run-tag` discipline makes before/after batches
   comparable; the numbers that settle a comparison are trustworthy.
3. **The invariants come out untouched.** No new telemetry readers (the plan
   report's pricing line, argued in `backlog-fill.md`, would be the second and
   last), no work-loop reads, no self-tuning, no third destination off the
   machine, no issue or PR text in a record — reviewing text means following
   a session id to the CLI's own files, never copying text into ours.

## Pillar 1 — the eval suite's first green run

The highest-leverage item in this plan is not new; it is finishing one that
exists. `evals/` grades what a real `/polako:implement-issue` run leaves
behind, and it has **never been executed** — its README says to expect the
first run to be a debugging session and lists five likely corrections. Until
it is green, every change to `SKILL.md` is verified by hand against one real
issue, which measures nothing and does not repeat.

The work, in order:

- One budgeted debugging session: `--case clear-issue` first, then the other
  three. Fix what the run finds; delete the "Known-unverified" section.
- Record the baseline: scores and cost per case, in the PR body of whatever
  change the first green run rides on — the same "the PR body says what was
  verified" convention skill changes already follow.
- Then the standing rule this plan proposes for CLAUDE.md's checking section:
  **a PR that changes a skill's `SKILL.md` runs the suite — or at minimum the
  cases its change touches — and its body quotes the scores.** `--runs 3`
  when a case wobbles, because a flaky grader is worse than none: it teaches
  the habit of ignoring red.
- When `/plan-backlog` lands (issue #66), its `plan-vision/` eval case joins
  the suite under the same rule.

Issue #311 landed the enforcement half of that standing rule: `evals/run.sh`
takes `--plugin-dir` and `--max-cost`, `Bash(evals/run.sh:*)` is granted in
`defaultTools`, and Phase 3 has an `implement-issue` run execute the touched
cases against its own worktree, passing `--max-cost 5` (which stops before
the next case once the spend so far reaches the cap — not a hard ceiling
inside one case), when its commits change a shipped `SKILL.md`. What is still
owed is the first green run itself — the budgeted debugging session above.

The suite stays opt-in and out of CI, per the agreement on issue #9 — it
needs network, a real `claude` and money. The gate is the release ritual, not
the push.

## Pillar 2 — sums you can trust: the resume probe *(settled)*

**Closed 2026-08-27 on issue #78.** A `--resume`d run's result event reports
that invocation, not the session, so summing rows as `stats` already does is
correct and the footnote it used to print is gone. See open question 1 of
`run-data-capture.md` for the evidence — a recorded resume pair, not the
disposable probe this pillar asked for, which never ran.

Nothing here blocks pillar 4 any longer: a tag comparison across batches with
different resume rates is comparing the resumes, not a bug.

## Pillar 3 — the shift retro: reviewing runs without storing text

The machinery for review shipped with 0.8.0; what is missing is the habit and
one field. The ritual, after each shift worth learning from:

1. `polako stats -by shift` — find the batch; `-by issue` inside it.
2. For every **parked** issue and every **outlier** (cost or runs well above
   the batch median): `stats -runs`, take the session id, and reopen the
   transcript with `claude --resume <session>` — or read it — and answer one
   question: *what would have let this run finish?*
3. Classify the answer. Skill wording, missing tool, supervisor logic, issue
   under-specified, model too weak for the step — each lands differently.
4. File the finding as an issue on this repository. polako works it. Issues
   #55, #56 and #57 are exactly this loop run informally — findings from
   reading what parked runs left behind — so the ritual is already proven;
   this plan just names it and makes it cheap.

Two design points keep this inside the invariants:

- **The session id is the pointer; the transcript is the CLI's.** polako
  never copies session text anywhere — not into records, not into issues.
  A retro that quotes a transcript into a GitHub issue is quoting the
  operator's own session by the operator's own hand, which is their call,
  not the tool's.
- **The one code change is a `park_reason` field** on the terminal issue
  record: an enum drawn from the park callsites (questions unanswered,
  conflict remediation failed, checks remediation failed, budget cap, retries
  exhausted, run left nothing — the exact taxonomy comes from reading the
  callsites, open question 2). Today `needs_human` is one bucket, so the
  single most actionable ranking — *what parks issues most* — cannot be
  computed. An enum is an identifier, not text, so the record rules hold; the
  schema grows additively, so old readers skip it. `stats` gains one line
  under `issues` showing the breakdown. This is the durable twin of issue
  #56, which puts the same information in the shift's log line; the two
  should land together or reference each other.

## Pillar 4 — experiments: tags with discipline

The comparison machinery exists — `-run-tag`, `-model`, `stats -by tag` — and
`run-data-capture.md` already settled the method: batches over time with
honest labels, no A/B machinery. What is missing is the discipline that makes
the labels honest, and the ledger that stops the results evaporating:

- **The rule:** any change to a `SKILL.md`, the model, or a strategy knob
  (`-stall`, `-retries`, `-poll`, the caps) runs its next batch under a fresh
  tag. An untagged batch after a change is a batch that can never be compared.
- **The ledger:** `plans/experiments.md`, one row per experiment — tag,
  hypothesis, what changed, the `stats -by tag` verdict, decision. It is a
  document, versioned and reviewed like the rest of `plans/`; it is not
  orchestration state and nothing reads it. Ten rows of this beat any
  dashboard, at this scale.
- **The first two experiments**, chosen because the records can already
  answer them: *do remediation runs need the big model?* (the `reason` field
  splits `remediate`/`checks`/`review` runs from `implement` ones, and
  `model_usage` prices each) and *is the default `-stall` losing more to
  kills than it saves in hangs?* (the `stalled` status count against wall
  time, across two tagged batches).
- **Version drift is a recipe, not a flag.** Every record carries the three
  versions in play, and the `no-skill` status exists because a CLI upgrade
  (2.1.85) silently changed behavior — exactly the regression a per-version
  breakdown catches. But `stats` keeps its `-by` list short on purpose and
  points the long tail at jq and DuckDB, so this is a documented one-liner
  (group status counts by `claude_version` — see [Recipes](#recipes) below),
  run as part of the retro after any CLI upgrade. It becomes a `-by` group
  only if someone reaches for the recipe often enough to resent typing it.

## Pillar 5 — quality past the merge

Merge rate is the headline number, and it is blind to the failure that
matters most: a merged PR that gets reverted, or immediately patched by hand,
counts as a win. The signal exists in GitHub — was a drained PR's merge
commit later reverted; did a follow-up commit touch the same files within a
week — and reading GitHub is what polako does everywhere else.

Start as a documented recipe (a `gh` search over merge commits and reverts —
see [Recipes](#recipes) below), run occasionally as part of the retro.
Promote it to a verb or a `stats` enrichment only when the
recipe proves it earns one — the same wait-until-pulled-for that has kept
`stats` small. Whatever form it takes, it reads GitHub and the operator's
screen and feeds no decision, so it sits with `status` on the read-only side
of the house.

## The cadence

| When | Do | Artifact |
| --- | --- | --- |
| After each shift | The retro: `stats -by shift`, reopen parked/outlier sessions, classify | Issues on this repo |
| Before a PR that changes skill text | Eval suite (affected cases at least), fresh `-run-tag` on the next batch | Scores in the PR body, a ledger row |
| After changing a knob or model | Fresh `-run-tag` | A ledger row with the verdict |
| After a `claude` CLI upgrade | The per-version recipe | A finding, or nothing |
| Occasionally | The post-merge audit recipe; `stats -since 168h` | Findings become issues |

Issue #53's self-contained HTML report is the thing that makes the periodic
glance cheap, and is already filed; this plan leans on it rather than
duplicating it.

## Recipes

Two of the cadence table's rows are one-liners over data polako already
writes. Neither is a verb or a flag, deliberately: a recipe earns promotion
by being typed often enough to resent, the same rule that has kept `stats`
small.

**After a `claude` CLI upgrade**, count run statuses by version. The
`no-skill` status exists because an upgrade once changed behaviour silently,
and this is what catches the next one. `stats` keeps its `-by` list short on
purpose, so this is a one-liner over the JSONL rather than a flag:

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

A hit is a finding, and a finding becomes an issue.

## What this plan deliberately does not do

- **No work-loop reads of telemetry, ever** — no self-tuning `-stall`, no
  "skip issues that look expensive". The moment a decision depends on a
  record, deleting the directory mid-shift changes behavior, and the
  write-only invariant is gone. Improvement flows through the operator and
  the backlog.
- **No text in records, and no new record fields that tempt it.**
  `park_reason` is an enum precisely so it never becomes a message.
- **No new telemetry readers.** `backlog-fill.md` argues the second reader
  (the plan report's pricing line) out loud; this plan adds none, and the
  audit recipe reads GitHub, not the record files.
- **No metrics service, no dashboards, no CSV export yet** — all parked in
  `run-data-capture.md` phase 4 already, and nothing here pulls for them.

## Implementation phases

Each lands independently green — gofmt, vet, full suite, README rows for any
flag, `claude plugin validate .` — per the standing convention.

**Phase 1 — verification, no production code.** The eval suite's first green
run (a budgeted debugging session; fixes to `evals/` as they surface; the
"Known-unverified" section deleted). The resume question is already closed
(issue #78), so what remains of this phase is the eval suite alone.

**Phase 2 — the one schema addition.** `park_reason` on terminal issue
records, the enum derived from the park callsites; the `stats` breakdown
line; tests over fixture JSONL including records without the field; landed
with or beside issue #56 so the log line and the record tell the same story.

**Phase 3 — the ritual, written down.** A short "Improving polako" section in
the README: the retro checklist, the tag rule, the per-version and post-merge
recipes. `plans/experiments.md` seeded with the two named experiments. The
skill-change/eval rule added to CLAUDE.md's checking section. Documentation
only — the phase exists so the ritual survives contact with a future operator
who was not in this conversation. (Issue #283 later cut the README down to a
landing page and moved the checklist, tag rule and both recipes here, into
this document's own "The cadence" and "Recipes" sections above — the README
now just points at them.)

**Phase 4 — pulled-for, not promised.** A `-by` group for versions; an audit
verb; eval-score history beyond PR bodies; any retro automation (a skill that
drafts the finding issue from a transcript the operator hands it) — each
waits for the manual form to demonstrate the need.

## Open questions and verification tasks

1. **Resume semantics** — inherited from `run-data-capture.md`, restated here
   because pillar 4 depends on it. One disposable resume settles it.
2. **The `park_reason` taxonomy.** Derive from the actual park callsites at
   implementation time rather than guessing here; seed with the obvious
   values and let the schema's additive growth absorb the rest. Verify one
   thing early: that every park path can name its reason at the point the
   label is applied, or the field will be `unknown` exactly where it matters.
3. **Transcript lifetime.** The retro leans on `claude --resume` reaching
   sessions from past shifts. Verify how long the CLI keeps transcripts and
   whether resume works across CLI upgrades; if retention is short, the retro
   moves from "occasionally" to "promptly", and the checklist should say so.
4. **Eval report diffing.** Check what `claude plugin eval` emits as a
   machine-readable report and whether two runs are comparable without hand
   work; if they are, the PR-body scores can be generated rather than typed.

This document alone is a docs change and bumps nothing.
