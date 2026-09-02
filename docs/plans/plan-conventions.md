# Plan conventions: where plans live and how they say they are done

Scope: docs layout, `status`, the drain's container-close step, the
`plan-backlog` skill · Behavior change: `status` gains a section; a drain
files one extra `proposed` issue when an epic closes

Five plan documents sit under `plans/`, a sixth under `docs/plans/`. Every one
says `Status: proposed`, including two whose work shipped months ago. Nothing
reads that line, so nothing corrects it. A reader cannot tell what is done,
what is in progress, and what is a stale idea, and the repo has no rule for
when a plan document leaves.

This document fixes that with one convention for this repo and any repo
polako runs in. The rule that makes it hold: **a plan's status is never
written down. It is derived from the issues that name the plan.**

## The convention

- **`docs/VISION.md`** is the one long-range document: what stays true, where
  it goes next. It has no status and is never "done".
- **`docs/plans/<topic>.md`** is one document per initiative. A plan is the
  vision for its own batch: `polako plan -vision docs/plans/<topic>.md`.
- **No `Status:` line, no `Tracking:` line, no index file.** `polako status`
  is the index. A line nobody writes cannot go stale.
- **Phases are task lists.** `- [x]` / `- [ ]` inside the document, which
  GitHub renders. This is the one hand-maintained progress marker, and it is
  optional.
- **A done plan leaves.** Its durable content moves into `docs/` proper before
  it goes. Git keeps the design record.

The header keeps `Scope:` and `Behavior change:`. Those describe the plan,
not its progress, and do not go stale.

## Derived status

Every issue `plan` creates ends with a footer:

    Proposed by polako plan from docs/plans/foo.md @ 1a2b3c4 — edit freely; remove the `proposed` label to queue it.

That footer is the pointer, running from issue to document. Reverse it and a
document's state falls out of GitHub alone:

| issues naming the doc | state |
|---|---|
| none | draft |
| all still `proposed` | proposed |
| any open past the gate | active |
| all closed | done |

`polako status` gains a plans section: one line per file under
`docs/plans/`, its derived state, its container issues, and the count of open
children. A document with no issues prints as draft. An issue whose footer
names a document that no longer exists prints under a final "gone" line so a
deleted plan's leftovers are visible.

Performance: one call, not one per document. `gh issue list --state all
--search '"Proposed by polako plan from" in:body' --json
number,state,labels,body` returns every stamped issue in one search request
and the grouping by path happens locally. Bounded by `--limit`; a repo past
the bound prints a warning naming the bound rather than a wrong state. The
search index lags a write by seconds to a minute, which is fine for a
human-facing report. Nothing on the drain's per-issue path calls this.

The footer becomes a contract, like `issue-N` branch naming: the binary
parses it, `repo_test.go` asserts the skill still writes it, and `CLAUDE.md`
names it. A hand-written epic for a hand-written plan joins the derivation by
carrying the same line, which is the one manual step left, and only for
issues that were manual already.

## Retire on close: garbage collection

The drain already closes a container whose children have all closed and
comments on the thread first (`closeFinishedContainers`). Extend that step:
after the close, if the container's footer names a document and no other open
issue names it, file one issue:

    docs: retire docs/plans/foo.md — every issue it proposed is closed

Labelled `proposed`, like everything a machine creates. Its body names the
container that just closed, the document, and the rule: move what is still
true into `docs/`, delete the file, fix inbound links. The human lifts the
gate with one label removal and `implement-issue` does the work. That is
cleanup through the same gate as everything else. No commit lands on the
default branch by the drain's hand, and a human still decides.

The "no other open issue" check is one search call, made only when a
container closes, so it costs nothing on the hot path. If the search index
lags and the check is wrong, the outcome is one `proposed` issue a human
closes. Filed at most once per document: the footer of the retire issue
itself names the document, so a second close finds it and files nothing.

Two cases the rule does not reach, on purpose. A container a human holds
(`needs-human`, still `proposed`) is never auto-closed, so it never triggers
a retire. And a plan that shipped through hand-filed issues without the
footer is invisible to the derivation, which is the honest answer: nothing
names it, so nothing can say it is done.

## Work items

Each is one PR. Order matters only where noted.

- [ ] **Move plans under `docs/`.** `plans/*.md` and `docs/plans/*.md` become
  one `docs/plans/` directory. Fix the three inbound links (README,
  CLAUDE.md, docs/run-data.md). Strip the `Status:` lines. Delete
  `plans/run-data-capture.md`: the recorder and `stats` shipped and
  `docs/run-data.md` describes them. Move `plans/experiments.md` and
  `plans/continuous-improvement.md` to `docs/`: one is a ledger, the other
  is the ritual the README already links to as documentation. Neither is a
  plan.
- [ ] **Footer parsing as a contract.** A function that reads the footer out
  of an issue body and returns the document path and SHA, tolerant of
  trailing edits, strict about the leading phrase. `repo_test.go` asserts the
  skill template and the parser agree on the wording. `CLAUDE.md` lists it
  under invariants beside `issue-N` branch naming.
- [ ] **`status` plans section.** The derivation above, one search call,
  bounded, with the "gone" line. Also in `status -json`. Documented in
  `docs/reference.md` under `status`. Hermetic tests through `fakeCLI`.
- [ ] **Retire on close.** The extra step in `closeFinishedContainers`,
  gated on the footer, at most once per document. Named in the exit summary
  and in `docs/behaviour.md`. Depends on the footer parser.
- [ ] **The convention travels.** `plan-backlog`'s `SKILL.md` states the
  layout in a few sentences: vision at `docs/VISION.md`, one plan per
  document under `docs/plans/`, no status line, `polako status` is the index.
  `docs/reference.md`'s `plan` section says the same. Eval case for the skill
  wording change, scores quoted in the PR.
- [ ] **Triage the survivors.** `backlog-fill.md`: tick phases 0–2, leave
  phase 3 with its epic. `token-spend.md`: check whether its drafted tickets
  were filed; if so they carry no footer, and the doc gets a one-line note
  naming them. `terminal-output.md`: check against what 0.10.0's follow-ups
  shipped; delete if done, tick phases if partly.

## Out of scope

- A `polako plan` that edits the document it planned from. A plan run
  creates issues and nothing else.
- The drain committing a status change. Nothing merges itself, and the
  default branch is a mirror.
- A GitHub Action or hook. State would then live in two places and one of
  them off the machine.
- Health issues. They come from the code, not a document, and have no plan
  to derive.
