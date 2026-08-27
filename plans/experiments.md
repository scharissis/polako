# Experiments: the ledger

Status: living document · Scope: a record, not a mechanism · Behavior change:
none — nothing in the binary reads this file

polako can already compare two configurations: `-run-tag` labels a batch,
`stats -by tag` prices the batches against each other, and every record
snapshots the configuration that produced it. What the machinery cannot supply
is memory. A comparison run in one evening, concluded in a terminal and never
written down, is a comparison somebody pays for again a year later.

This is where it gets written down. One row per experiment, five columns:

- **tag** — the `-run-tag` its batch ran under. The batch is the evidence; the
  tag is the only thing that can find it again.
- **hypothesis** — what you expect to be true, stated before the batch runs.
  Stated afterwards it is a description, and it will always fit.
- **change** — what actually differed. One thing, or the verdict names nothing.
- **verdict** — what `stats -by tag` said, with the numbers.
- **decision** — kept, reverted, or still open. A verdict without a decision is
  half a row.

Rows are appended, never rewritten: a hypothesis that turned out wrong is the
most useful kind of row, and editing it away leaves the next operator to have
the same idea from scratch.

This is a document, versioned and reviewed like the rest of `plans/`. It is
not orchestration state — nothing reads it, no decision depends on it, and
deleting it changes no behaviour. Ten honest rows beat any dashboard at this
scale.

## The rule that fills it

Any change to a `SKILL.md`, to the model, or to a strategy knob — `-stall`,
`-retries`, `-poll`, the spend caps — runs its next batch under a fresh
`-run-tag`, and that batch gets a row here. See
[Improving polako](../README.md#improving-polako) for the retro this sits
inside.

## How to settle one

Run enough issues under the tag that the comparison is not one lucky night —
the batches in `run-data-capture.md` are the method, and it is batches over
time with honest labels, not A/B machinery. Then `polako stats -by tag`, and
fill in the verdict with the numbers rather than the impression. An issue
worked under two tags counts under each, and the table says so when it
happened; if that is most of the batch, the comparison is not one.

## The ledger

| tag | hypothesis | change | verdict | decision |
| --- | --- | --- | --- | --- |
| `remediation-sonnet` | Remediation runs — rebasing a conflict, fixing red checks, answering a review — are mechanical next to implementing an issue, so a smaller model finishes them at the same rate for less money. | Remediation runs dispatched on a smaller model than implementation runs. Needs the knob to exist first: `-model` is per-shift today, so this batch cannot run until something can vary the model by run reason. | *pending* — `reason` splits `remediate`, `checks` and `review` runs from `implement` ones, and `model_usage` prices each, so the baseline half is already answerable from records on disk. | *open* |
| `stall-30m` | The default `-stall` of 15m kills more healthy-but-quiet runs than it rescues hung ones, and the resume that follows pays to read the whole context again. Doubling it costs less than the resumes it avoids. | `-stall 30m` for a whole batch, against a batch at the default. | *pending* — compare the `stalled` status count and total cost per merged PR across the two tags. Watch wall clock too: a longer watchdog also means a genuinely hung run burns 30m before anyone notices. | *open* |

Both rows come from `plans/continuous-improvement.md`, pillar 4, which chose
them because the records can already answer them.
