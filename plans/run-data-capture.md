# Run-data capture: measuring what a drained backlog costs, and what to improve

Status: proposed · Scope: supervisor binary only · Behavior change: none until phase 1 ships

backlog-drain currently throws away every number it sees. `logEvent` already
parses `total_cost_usd`, `num_turns` and `duration_ms` from each run's final
stream-json `result` event, renders one log line, and discards them. So today
there is no way to answer:

- What does an issue cost — in tokens, dollars, wall time, retries?
- What are we spending per hour, per day, per project?
- Did the last skill-wording change make runs better or worse?
- Which Claude model is cheapest *per merged PR*, not per token?
- Which orchestrator settings (`-stall`, `-retries`, `-poll`) waste the most money?

This document designs the capture, storage and analysis needed to answer those
questions, and settles two policy questions on the way: how telemetry coexists
with the "writes no state files" invariant, and what happens to data collection
if the plugin ever goes public on the marketplace.

## Goals

1. **Informational.** Per run and per issue: tokens, dollars, duration, turns,
   retries, question rounds. Per day/project: totals and rates.
2. **Optimization (the primary goal).** Compare configurations against
   outcomes over time: Claude model, skill wording variants, orchestrator
   strategy. An optimization dataset has one hard requirement the informational
   one doesn't: it must not be biased. Crashed runs that recorded nothing, or
   failed issues that never produced a record, would make exactly the
   configurations we need to price look cheap.
3. **Trust.** The answer to "what does this tool record and where does it go"
   must be one short paragraph, enforced by tests, and unchanged by publishing
   the plugin.

## Where capture happens: the supervisor, never the skill

The skill is a prompt. It cannot meter anything, and a marketplace artifact
must not try — every installer would be shipping our telemetry decisions
inside their Claude sessions. The supervisor is the only metering point, and
it already has everything needed: `execClaude` scans every stream-json event
of every run.

Publishing the skill publicly therefore changes *nothing* about data
collection. That separation is load-bearing; keep it.

### What one run yields

`execClaude` today unmarshals each line up to three times (`extractSessionID`,
`resultTurns`, `logEvent`) and returns only `(sessionID string, err error)`.
Refactor to one parse per line feeding a `runReport` struct, returned to the
caller **valid even on error** — the crash-resume path depends on the session
ID surviving a crash, and now the partial usage data must survive with it.

| Source | Fields |
| --- | --- |
| `result` event | subtype, is_error, num_turns, duration_ms, duration_api_ms, total_cost_usd, `usage` (input / output / cache-read / cache-creation tokens), `modelUsage` (per-model tokens + costUSD) |
| `system/init` event | the model actually used (which may differ from the one requested) |
| streamed `assistant` events | a running token tally and a tool_use counter, kept as the run progresses |
| the process itself | exit code; whether the stall watchdog fired; whether the context was cancelled |

The streamed tally exists because three of the five ways a run can end —
crash, stall, interrupt — never emit a `result` event, yet burned real tokens.
Each record says which source its numbers came from (`usage_source:
"result"` or `"observed"`), so analysis can treat approximations as such.

Two stream-json details to honour: `modelUsage` uses camelCase keys
(`inputTokens`, `costUSD`) unlike the snake_case top-level `usage`, and it is
absent on older CLI versions — parse it as optional.

### What the supervisor adds

Context the events can't know: repo (`nameWithOwner`, which `preflight`
already fetches and currently discards), issue number, start/end timestamps,
why this run happened, and the exact configuration under test.

Run **reason** is one of:

| Reason | Trigger |
| --- | --- |
| `implement` | fresh skill run on an issue |
| `resume` | `--resume <session>` after a crash or stall |
| `answers` | fresh re-run after a human replied on the issue thread |
| `remediate` | conflict-remediation prompt while a PR is open |

(`answers` needs a small addition in `processIssue`: today the answers path
just resets `attempt` and loops, and nothing at the callsite distinguishes it
from a first run.)

Run **status**, with defined precedence so one run maps to one value:
`no-skill > interrupted > stalled > crash > error (result.is_error) >
no-turns > ok`. Crash records also keep the raw exit code. (`no-skill` was
added when Claude Code 2.1.85 stopped reporting an unknown slash command as
zero turns: it marks a run the supervisor stopped because the init event's
command inventory did not list the -skill.)

Run **outcome** — the single most predictive signal for optimization — is
what the run left behind: `opened_pr | posted_questions | nothing`, plus the
PR number when one exists. `processIssue` already computes exactly this
(`prForBranch`, the `commentCount` delta) right after every run; it just never
wrote it down.

**Configuration under test**, snapshotted into every run record so each line
is self-describing:

- skill name, permission mode, requested model, `-run-tag` (below)
- the strategy knobs: `-poll`, `-retries`, `-retry-wait`, `-stall`
- an FNV-1a hash of the resolved `--allowedTools` list (equality is all
  comparisons need; the full list would triple the line length)
- backlog-drain version via `runtime/debug.ReadBuildInfo` (module version when
  installed with `go install`, VCS revision when built from a clone — stdlib)
- claude CLI version, from one `claude --version` at preflight

### What is never captured

No issue or PR titles, bodies, or comment text. They are sensitive, and per
the standing invariant they are attacker-controllable data on any repo that
accepts outside issues. Records hold numbers, identifiers, and
operator-chosen labels only. This is also what makes a record file safe to
share with a teammate or paste into an analysis session without re-reading it
first.

## Record schema

JSONL — one self-contained JSON object per line, append-only, versioned with
`"v":1` and discriminated by `"kind"`. Readers ignore unknown fields and
unknown kinds, so the schema can grow additively without migrations.

One `run` record per claude invocation, written when it ends, whatever its
status:

```json
{"v":1,"kind":"run","ts":"2026-08-24T10:15:00Z","ended":"2026-08-24T10:34:02Z",
 "repo":"scharissis/backlog-drain","issue":12,"pr":34,
 "reason":"implement","attempt":0,"session":"a1b2-…","resumed_from":"",
 "status":"ok","subtype":"success","exit_code":0,
 "outcome":"opened_pr","turns":74,"tool_uses":63,
 "wall_ms":1141000,"api_ms":812000,
 "cost_usd":4.12,"usage_source":"result",
 "tokens":{"in":2143,"out":48210,"cache_read":8123400,"cache_write":401200},
 "model_usage":{"claude-opus-5":{"in":2143,"out":47000,"cache_read":8000000,"cache_write":400000,"cost_usd":4.01},
                "claude-haiku-4-5":{"in":0,"out":1210,"cache_read":123400,"cache_write":1200,"cost_usd":0.11}},
 "model":"claude-opus-5","requested_model":"","skill":"backlog-drain:implement-issue",
 "permission_mode":"acceptEdits","tag":"baseline","tools_hash":"c39f1a02",
 "poll_s":300,"retries":3,"retry_wait_s":30,"stall_s":900,
 "drain_version":"v0.2.0","claude_version":"2.1.34"}
```

One `issue` record when an issue reaches a terminal state — **including the
failures**, which are the most valuable data points and today's design would
otherwise lose entirely (the "needs a human" paths exit the process):

```json
{"v":1,"kind":"issue","ts":"2026-08-24T11:03:11Z",
 "repo":"scharissis/backlog-drain","issue":12,"pr":34,
 "outcome":"merged","tag":"baseline"}
```

`outcome` is one of `merged | closed_unmerged | needs_human`. The issue
record deliberately holds only what run records cannot derive: the terminal
outcome, the PR number, and (phase 3) GitHub enrichment. Everything summable
— cost per issue, runs per issue, crash count, question rounds (= count of
`answers` runs) — is always derived from `run` records at read time. That one
decision removes in-memory rollup state, `"partial"` flags for rollups lost to
restarts, and most duplicate handling: the only dedupe rule left is
latest-wins by `(repo, issue)` for `issue` records, covering the narrow
window where the supervisor is killed between merge and record.

Telemetry is **best-effort by construction**: a failed write logs a warning
once (not once per record — unattended logs are a cost too) and never fails a
run. A record can therefore be missing; it can never be required.

Known gap, accepted: a PR merged while the supervisor is down auto-closes its
issue via `Closes #N`, so the supervisor never revisits it and no `issue`
record is written. Phase 4 sketches a `gh`-based backfill if this turns out
to matter in practice.

## Storage

### The invariant tension, named

CLAUDE.md: *"All state lives in GitHub — the process keeps no durable state
and writes no state files."* A metrics file breaks the letter of that rule,
so this plan says so here and in the PR body rather than doing it quietly.

The invariant's *purpose* is orchestration correctness: kill the process at
any point, rerun it, and it must re-derive where things stand from GitHub
alone. Telemetry can coexist with that purpose under one discipline:

> **Proposed amendment.** Orchestration state lives only in GitHub, as
> before. Write-only telemetry is permitted: the drain loop never reads it,
> no decision depends on it, and deleting it at any moment changes no
> behavior. (`stats`, a separate human-invoked subcommand, is the only
> reader.)

That discipline is testable — "with `-metrics off`, byte-identical behavior;
delete the directory mid-drain, nothing changes" — and phase 1 tests it.

### Options considered

| | Where records live | Verdict |
| --- | --- | --- |
| **A. GitHub-only** | machine-readable comments on each PR/issue; `stats` re-derives from GitHub | Rejected as the default. Cost data becomes visible to everyone with repo read access (unacceptable on a public repo), every run adds thread noise to the two deliberately-quiet human touchpoints, API rate limits apply, and runs that die before a PR exists have nowhere natural to write. |
| **B. Local JSONL** | `~/.backlog-drain/metrics/<owner>--<repo>.jsonl` | **Recommended.** Private by default, works offline, append-only, survives supervisor restarts trivially. Costs the invariant amendment above. |
| **C. Opt-in PR summary** | one compact comment per issue at merge | Phase 3, default off. Team visibility for those who want it; also the escape hatch for "no local files at all" (`-metrics off` + `-post-summary`). |
| **D. External sinks** | shared/synced directory, private metrics repo, OTLP | Deferred. `-metrics <dir>` pointed at a synced folder already covers the team case with zero new code; anything fancier waits for a real need. |

### Option B, concretely

- One file per repository, named `<owner>--<repo>.jsonl`. Per-project
  deletion is `rm` on one file; cross-project aggregation is a glob over the
  directory. Filenames are only partitioning — the `repo` field inside each
  record is authoritative, so nothing ever parses a filename.
- Location `~/.backlog-drain/metrics/` via `os.UserHomeDir`: one documented,
  predictable path on every platform beats per-OS idiom for a tool whose
  README promises "here is everything we write". Never inside the checkout —
  the skill commits things there, and cost data must not be committable by
  accident.
- New flag `-metrics`: a directory path, or `off`. **Default: on.** The
  primary goal is a dataset; a default-off dataset never exists. The user's
  stated concern is cross-repo and central collection, not their own local
  files — and `off` is one flag away, with a test pinning that `off` writes
  nothing.
- Appends of one line are effectively atomic at these sizes on both platforms
  (`O_APPEND`); no locking. Readers tolerate a torn final line from a
  hard kill.

## Cross-repo aggregation and the marketplace question

Split the question into who runs the code and whose data it is:

- **The skill (the marketplace artifact) carries zero telemetry.** Making the
  repo public, or listing the plugin on a marketplace, changes nothing about
  data collection, because installers of the plugin run only the prompt.
- **The binary meters, and its data is the operator's own**, about their own
  runs, on their own machine.

Options, in increasing order of reach:

1. **Off.** `-metrics off`: nothing is ever written.
2. **Local-only — the default.** Records never leave the machine. Cross-
   *project* insight ("what did August cost across everything I drain?") is
   reading your own directory, and wanting to stop is deleting it. No consent
   question arises because no second party is involved.
3. **Opt-in sharing, operator's choice of blast radius.** Point `-metrics` at
   a synced/shared directory for a team dataset, or enable `-post-summary`
   (phase 3) to put one cost/outcome comment on each merged PR — visible to
   exactly the people who can already see the PR.
4. **Central collection by the plugin author: recommended never.** A
   marketplace tool that phones home burns the one asset a marketplace tool
   has, and cost-plus-repo-name is genuinely sensitive data. If a future
   need ever forces the question, the floor is: opt-in only, a published
   schema, and no repo-identifying fields. Until then the README makes the
   inverse promise explicitly — *nothing leaves your machine* — and lists
   exactly what is written and where, so the trust answer stays one
   paragraph long.

## Analysis: `backlog-drain stats`

The binary's first subcommand (`os.Args[1]` dispatch; the bare invocation
keeps draining, so nothing existing changes). `flag.NewFlagSet` gives it its
own flags:

```bash
backlog-drain stats                     # everything in ~/.backlog-drain/metrics
backlog-drain stats -repo scharissis/backlog-drain -since 168h
backlog-drain stats -by model           # or: -by tag, -by issue
```

Headline aggregates: issues reaching terminal state, merge rate, runs per
issue, cost per issue (mean and median — one pathological issue skews a
mean), tokens per issue, cost per day, dollars per *merged PR* (the number
that actually prices the tool), and the human-latency spans — time blocked on
answers (gap between a `posted_questions` run and its `answers` re-run) and
PR-open-to-merge — both derivable from record timestamps.

Kept deliberately small: `-by issue|model|tag`, `-repo`, `-since`, plain
aligned-text output. JSONL is already jq / DuckDB / spreadsheet food, and the
README will show one or two such examples rather than growing a query
language here. CSV export and day/reason groupings wait until someone
actually reaches for them.

Reader rules: skip torn or non-JSON lines, ignore unknown fields and kinds,
dedupe `issue` records latest-wins, order by timestamps (never by `attempt`,
which resets across supervisor restarts).

**Cost caveat, documented in the README**: `total_cost_usd` is the CLI's
API-equivalent pricing — real money on API-key auth, notional on
subscription plans. Tokens are ground truth; dollars are derived. The binary
never hardcodes a price sheet — prices rot, and the CLI already applies the
current ones.

## The optimization loop this enables

Comparisons key on `(model, tag)` against outcomes:

- **`-model`** (new flag, passed through as `claude --model`) varies the
  model per supervisor invocation. Records keep both requested and actual
  model.
- **`-run-tag`** (new flag, freeform label recorded verbatim, e.g.
  `skill-v3-terse-plan`) is the handle for skill-wording and strategy
  experiments. Be explicit in the docs: the binary version does **not** pin
  the skill text — an operator can run binary vX against plugin vY — so tag
  discipline is what makes skill experiments comparable.
- **Quality signals**, all captured or derivable: PR-produced rate, merge
  rate, runs-to-PR, crash count, question rounds, cost per merged PR.
  Time-to-PR is the automation metric; time-to-merge is reported but flagged
  as confounded by human availability.

Workflow: run a batch of issues tagged `baseline`, change one thing (model,
skill wording, `-stall`), run the next batch under a new tag, then
`stats -by tag`. No A/B machinery in the binary — batches over time with
honest labels are enough at this scale, and anything cleverer (alternating
assignment per issue) would break the one-issue-in-flight simplicity for
marginal statistical gain.

## Implementation phases

Each phase lands independently green: `gofmt`, `go vet`, full suite, README
rows for any new flag, `claude plugin validate .` untouched.

**Phase 1 — capture and storage.**
- `cmd/backlog-drain/metrics.go`: record types, best-effort append-only
  recorder, `off` handling.
- `execClaude` refactor: one event parse per line; running usage tally;
  returns a `runReport` (valid on error). Callers (`runClaude`,
  `remediateConflicts`, `processIssue`) attach context and outcome, then hand
  the record to the recorder. Terminal `issue` records on the merge path
  *and* on the needs-a-human / closed-unmerged exits.
- New flags: `-metrics`, `-model`, `-run-tag`. `preflight` keeps
  `nameWithOwner` and captures `claude --version`.
- Docs: README section ("Run data & cost tracking": what is written, where,
  the never-phones-home promise, `-metrics off`), CLAUDE.md invariant
  amendment as worded above.
- Tests: fakeClaude gains `usage`/`modelUsage`/`duration_api_ms` in its
  canned result, plus two new modes — crash-after-partial-stream (exercises
  the observed-usage tally) and result-without-modelUsage (older CLIs). Unit
  tests for record marshalling, recorder append, `off` writes nothing,
  `buildArgs` with `-model`. The README honesty test needs its regexp
  extended: scan all `*.go` in the package and match `FlagSet` receivers
  (`fs.StringVar`), which the current `flag\.\w+Var\(` pattern misses.

**Phase 2 — `stats`.** Subcommand, aggregation, human-latency spans; golden
tests over fixture JSONL covering torn lines, unknown fields, duplicate
`issue` records, and a resumed-session chain.

**Phase 3 — GitHub enrichment and opt-in visibility.** One `gh pr view
--json additions,deletions,changedFiles,createdAt,mergedAt,reviews` at
terminal state, folded into the `issue` record. `-post-summary` (default
off): one compact comment on the merged PR — runs, question rounds, tokens,
dollars, wall time.

*Shipped in 0.5.0, with three things worth writing down.* The summary is
built from an in-memory tally that `processIssue` keeps while it works one
issue — **not** by reading the record files back, which would have turned
telemetry into state for the sake of a comment. The cost is that the comment
covers the runs one process supervised rather than the issue's whole history,
so it says exactly that, and a drain that only waited on a PR an earlier
process opened comments nothing rather than reporting a free PR. The
enrichment call is made only when a record is going to be written, so
`-metrics off` still makes no extra GitHub call; reviews are counted, never
quoted. `stats` reports the medians as a *change per issue* line, and takes
GitHub's `createdAt`/`mergedAt` over its own inference for open-to-merge —
those are right even when the run that opened the PR is outside the window.

**Phase 4 — deferred until pulled for.** External sinks; `stats` CSV/day
groupings; `gh` backfill for issues merged while the supervisor was down;
any A/B aids.

## Open questions and verification tasks

1. ~~**Resume semantics.**~~ **Settled 2026-08-27 (issue #78): a `--resume`d
   run's `result` event reports that invocation, not the session.** Summing
   rows as written is correct, so no per-session maximum is needed and the
   caveat has come out of `stats` and `docs/run-data.md`.

   The question was whether naive summing double-counts every resumed session.
   Two attempts at the disposable probe the plan called for did not happen: the
   2026-08-25 run hit `401 OAuth access token is invalid` on every `claude`
   invocation, from a clean shell as well as under the supervisor, and the
   2026-08-27 run could not spawn `claude` at all, because it is not in the
   `implement-issue` skill's `--allowedTools` set and the invocation raised a
   permission prompt an unattended run has nobody to answer. The evidence came
   instead from a resume that had already been recorded — better provenance
   than a synthetic probe, since it is a real implement run.

   `scharissis/backlog-drain#3`, session `072dd60e-…`, CLI 2.1.245. Both halves
   carry `usage_source: "result"`, so both reached a priced result event:

   | field | run 0 (`implement`, crash) | run 1 (`resume`, ok) |
   | --- | --- | --- |
   | `num_turns` | 62 | 31 |
   | `usage.output_tokens` | 54,077 | 15,651 |
   | `usage.cache_read_input_tokens` | 6,194,619 | 4,947,563 |
   | `total_cost_usd` | 5.7561 | 8.8630 |
   | `modelUsage` key | `claude-opus-5[1m]` | `claude-opus-5` |

   `num_turns` and every field of `usage` **fell** across the resume. A
   session-cumulative counter cannot go down, which settles those outright.
   Cost is the one field that rose, so its own magnitude settles nothing —
   note that "below the naive run0+run1 sum" is no argument either, since a
   cumulative total is *by construction* below a sum that counts run 0 twice.
   What settles it is where the number comes from: `total_cost_usd` equals the
   sum of `modelUsage`'s `costUSD` in both records, and the resumed run's
   `modelUsage` map holds a single key that is not even the one run 0 was
   billed under — a cumulative map would still carry `claude-opus-5[1m]` and
   its 6.3M cache reads, and would still be priced under it.

   Two facts worth keeping from the attempts. From 2026-08-25: a `--resume`d
   run's init event reports the *same* `session_id`, so `session` alone would
   have been a sufficient grouping key had one been needed. From this pair: on
   the resumed run `modelUsage` exceeded the top-level `usage` about twofold,
   because `modelUsage` aggregates subagent usage the main-loop `usage` block
   omits. A record takes `cost_usd` from `total_cost_usd`, which matches that
   aggregate, and `tokens` from `usage`, which does not — so a record's dollars
   cover the subagents its token counts leave out. That is now stated in
   `docs/run-data.md` rather than left to be rediscovered.
2. **`modelUsage` availability.** Treat as optional everywhere; note the
   minimum CLI version once known.
3. **Versioning.** New flags are a minor bump per the README's pre-1.0 rules:
   phase 1 ships as 0.2.0. This document alone is a docs change and bumps
   nothing.
