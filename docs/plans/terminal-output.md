# Terminal output, second pass

Where the 0.10.0 output overhaul should go next. The first pass decided who
the `work` terminal is for — an operator glancing over — and gave everyone
else the per-shift log. Running real shifts on it surfaced four gaps, written
up here as one coherent direction so the backlog can be decomposed from it.

## What 0.10.0 got right, and keeps

These are constraints on everything below, not open questions:

- Milestones on the terminal, the full stream in the shift log, `-verbose` to
  mirror it back. The audience split is correct.
- Reports to stdout, narration to stderr. `polako work -dry-run | pbcopy` and
  `status`/`stats` piping must keep working byte-for-byte when redirected.
- The shift log's write-only rules (nothing reads it back, never leaves the
  machine) and the disclosure lines said every shift, unprompted.
- Piped stderr keeps the full `2006/01/02 15:04:05` stamp format for existing
  collection setups.

## 1. One presentation layer for every verb

`work` got the new sinks, styler and TTY awareness (`cmd/polako/ui.go`);
`status` and `stats` still print raw `fmt` text through `printPairs` /
`printTable` (`stats.go:~1084`, `status.go:~373-517`) — colourless,
structure-blind, and visually unrelated to the verb that shares their binary.
Worse, the narration a report emits on the way in (`ignoring 6 proposed
issue(s) awaiting curation…`) goes to stderr with none of the work verb's
presentation rules.

The fix is one presentation layer, used by all three verbs:

- Move the styler and TTY detection into a small report renderer that
  `status` and `stats` share with `work`. Reports detect their own TTY on
  **stdout** (narration detects stderr); when piped, output is byte-identical
  to today.
- Style the reports sparingly and consistently with `work`'s palette: bold
  section heads (`open prs on issue branches`, the repo line), the
  `needs you:` closing line highlighted (it is the report's whole point),
  dim column headers, yellow for attention states (`failing`, `changes
  requested`, `parked`).
- The pre-report narration lines (`ignoring N proposed…`, gh warnings) adopt
  the same rendering rules as `work` narration.
- A test pins that piped report output is unchanged from today, the same way
  the work verb's piped-stderr shape is pinned.

## 2. Timestamps everywhere, worn quietly

0.10.0 dropped the 19-character `2006/01/02 15:04:05` gutter on a TTY on the
grounds that the shift log holds every stamp. Real shifts showed the cost:
scrollback of a multi-hour session cannot answer "when did PR #110 open?"
without switching to the log, and the `-log off` special case (gutter kept)
gives the flag a side effect on presentation nobody would guess.

Replace the on/off rule with one rule: **every line carries a time; the
terminal wears it quietly.**

- TTY: a dim, time-only stamp — `15:04:05 ` (8 characters, styled dim) on
  every line. Readable when you need it, visually silent when you don't.
- Piped: the full date+time stamp, unchanged (compatibility).
- Shift log: the full stamp, unchanged.
- The `-log off` ⇒ keep-full-stamps special case dissolves; `-log` goes back
  to meaning only "where the log goes".

Pointers: `ui.emit` and the `stamp` field (`cmd/polako/ui.go`), the wiring in
`main.go` (`sinks.stamp = cfg.logDir == ""`), `keepStamps`, and the
docs/reference.md "terminal adapts" paragraph.

## 3. Structure at the source, not prose-sniffing

Two related problems, one cause: the narration is unstructured prose, so
everything downstream re-derives meaning from English wording.

**Inside the binary**: `styler.render` classifies lines by matching ~20
literal phrases (`"finished (ok"`, `"could not "`, …) scattered across
`main.go` — it already misses `session %s could not be resumed` (starts with
"session", renders plain while its siblings are yellow), and every future
rewording silently drops its colour with tests green. The call sites already
know a line's severity when they compose it; they should say so once.
Narration becomes typed at the source — the existing milestone/detail split
gains a severity the caller declares (success / warning / error / progress /
settings-pair), the styler maps severity to colour, and the wording-matching
rule table is deleted. The two-logger call-site convention stays; this is a
third dimension on the same mechanism, not a logging framework.

**Outside the binary**: the read-only reports gain a machine-readable form —
`status -json` and `stats -json` emit the same facts as the text report as
one JSON document on stdout, for dashboards, scripts and tests. Reports are
already assembled from typed structs before rendering, so this is a second
renderer, not a second pipeline. Narration (`work`) deliberately does **not**
get a JSON mode in this pass: its structured twin would be a JSONL shift-log
format, which is worth its own proposal only if a concrete consumer shows up
— the write-only invariant means nothing in polako itself may ever be that
consumer.

Pointers: `styler.render` and its rule table (`cmd/polako/ui.go:~150-185`),
`logEvent` (`main.go`), `runStatus`/`render` report assembly
(`status.go`, `stats.go`), the flag-documentation test.

## 4. A settings block instead of a startup essay

Preflight currently narrates six to eight full-sentence paragraphs
(`main.go:~1382-1440`) — accurate, but unscannable, and the screenshots show
operators reading past them. Replace the recap with a structured block in the
report style, dim on a TTY, one fact per line:

```
scharissis/polako — /polako:implement-issue per issue
  queue      label "ready" · poll 5m0s
  remote     on — watch from claude.ai/code or the app (-remote=false disables)
  run data   ~/.polako/metrics — numbers only, stays local (-metrics off)
  shift      d2d08b3a — `polako stats -shift d2d08b3a` reports on it alone
  shift log  ~/.polako/logs/scharissis--polako--d2d08b3a.log (-log off)
  caps       $20 max per issue
```

Rules the block must keep:

- The disclosure semantics survive restructuring: `remote`, `run data` and
  `shift log` appear every shift, unprompted, naming what leaves or stays on
  the machine and the flag that turns each off. Only the *shape* changes.
- Conditional lines (`caps`, `-notify`, `-ungated`, `-post-summary`,
  `-dry-run`, version skew) appear only when set, as rows in the same block —
  except warnings (`-ungated`, version skew), which stay full-width warning
  lines so they cannot be skimmed past as settings.
- `printPairs` (or its successor from workstream 1) is the layout engine, so
  the startup block, `status` and `stats` all align the same way.
- Doc transcripts pin the prose today (`docs/reference.md`,
  `docs/run-data.md:~256`, README) and must move with it, as must the tests
  asserting banner fragments.

## Sequencing

Workstream 1 (shared layer) and 3a (typed severity) reshape `ui.go` and are
the foundation; 2 (stamps) is a small change on top of that foundation; 4
(settings block) and 3b (`-json` reports) build on the shared layer and can
land independently after it. Nothing here changes what leaves the machine,
touches the queue rules, or adds a dependency — stdlib-only holds throughout.

## Out of scope

- A JSONL shift-log format (needs a named consumer first; see workstream 3).
- Windows ANSI/VT enablement (tracked separately; colour stays off there).
- Any change to `-remote`, the recorder, or what either discloses.
