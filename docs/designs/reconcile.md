# `plan` reconciles before it proposes

Scope: `polako plan`, `polako status`, the plan-backlog skill · Behavior
change: on a doc with numbered milestones or drafted tickets, `plan` shows
a table and asks before it writes anything; it can now edit and comment on
existing issues; GitHub milestones follow the doc's milestones

`plan` reads the doc, skims open issues and 50 closed ones, and leaves
dedup to the model. Re-plan from an edited or half-built doc and it
re-proposes shipped work. This makes a re-plan safe: work out what exists
first, deterministically, in Go, then file only what's missing.

## Where it lives

All reconciliation is binary-side: no model, so it's free, hermetic to
test, and the same code serves `plan` and `status`.

- `planitems.go`: parse a doc into items.
- `footer.go`: the item marker, a contract like the plan footer.
- `reconcile.go`: gather, match and classify. Pure functions over gh JSON.
- `reconcile_render.go`: the table, text and `-json`.
- `reconcile_apply.go`: the approved writes.
- `plan.go`, `intake.go`, `status.go`: wiring.
- `skills/plan-backlog/SKILL.md`: a fourth argument, the items to file.

## Items

Two shapes count, each scoped to its heading:

- **Milestones.** A numbered entry under a heading containing "milestone"
  is item `M<n>`, `n` being the number written in the source. Several
  entries on one line count when each number is the previous one plus one,
  which job-trawler's `8. … 9. … 10. … 11. …` line needs. `### Milestone 3:
  Filters` is `M3` too.
- **Tickets.** A `### N. title` heading under a heading containing "ticket"
  is item `T<n>`. Every design-plan doc has these, under `## Drafted
  tickets`.

Anything else can opt in with `<!-- polako:item=ID -->` on the entry's
line. A duplicate ID is an error naming both lines. A doc with no items
plans exactly as today, plus one line saying how to opt in. polako's own
VISION.md is one of those.

An item's text is its entry: the list entry, or a heading and everything
under it down to the next heading at that level. For comparing versions
it's lowercased, with markdown and extra whitespace stripped. Its name is
the text before the first colon, or else the first clause, backticks and
quotes stripped, capped at 50 characters (`briefTitle`).

### Several docs

Every doc numbers from 1, so the doc's slug scopes every ID. The slug is
the file name without its extension, as written: `docs/VISION.md` is
`VISION`, `docs/designs/db-migration.md` is `db-migration`. Full IDs read
`VISION/M1` and `db-migration/T3`. The slug survives a move to
`docs/designs/done/`, and footers still naming `docs/plans/`, since both
keep the base name. Two docs with the same file name in different folders
are an error. A doc can pin its slug with `<!-- polako:doc=SLUG -->`,
which is also how a renamed doc keeps its issues.

Tables and prompts show bare IDs (`M13`), since the header names the doc.

## The marker

    <!-- polako:item=VISION/M23 -->

On its own line, above the plan footer, outside code fences. A marker
quoted inline in prose doesn't count. One issue may carry several:
job-trawler's #9 covers `VISION/M12` and `VISION/M13`. Reconciling one doc
ignores every other doc's markers. The binary may add ` at=<sha>`: the doc
commit the issue was last synced to. Without it, the plan footer's sha
stands in. With neither, the issue can't go stale.

On a public repo a marker counts only on an issue the operator's account
wrote or one carrying the gate label. Anyone can type an HTML comment, but
outsiders can't label. Other markers are treated as guesses.

## Gathering

Four reads per run:

- All issues, open and closed: number, title, body, state, stateReason,
  labels, milestone, closedByPullRequestsReferences, subIssuesSummary.
- Open and merged PRs: number, state, headRefName, closingIssuesReferences.
- The doc at HEAD, and at each distinct marker or footer sha (`git show`).
- Stubs: `git grep -n -i "not implemented\|unimplemented"` over tracked
  non-test files.

Reads ask for one row past the limit, as `plans.go` does, so truncation is
reported rather than silent.

## Matching

1. **Marker.** An issue whose marker names an item matches it.
2. **Renumbered.** A marked issue whose item text changed, where the old
   text is now exactly another item's, moves to that item. Without this,
   inserting milestone 5 would shift every later ID and re-file M24.
3. **Guess, strong.** An unmarked issue whose footer names this doc's slug
   and whose body says "milestone N" (or "milestones 12 and 13") guesses
   `M<N>`; "ticket N" guesses `T<N>`. All 16 of job-trawler's child issues
   say this, and so do polako's own (#530: "ticket 1", footer
   `docs/plans/unpark.md`).
4. **Guess, weak.** Otherwise, word overlap between the item name and the
   issue title: at least 0.6 is a guess; 0.3 to 0.6 only shows "closest
   #N". Weak guesses pair one to one, best score first.
5. **Missing.** Anything left.

Containers are never guessed: an epic groups items, it isn't one. Guesses
always need confirming.

## States

| state | when |
| --- | --- |
| done | issue closed as completed (merged PRs shown as evidence) |
| dropped | closed as not planned or duplicate: never re-filed |
| in progress | open PR closing it or on its `issue-N` branch, or `awaiting-answer` |
| parked | `needs-human` |
| proposed | `proposed` |
| queued | open, none of the above |
| missing | no match |

`stale` stacks on any matched state: the item's text at the marker's sha
differs from HEAD. It looks at the item's own entry only, so a change
elsewhere in the doc doesn't count. Several issues on one item: open work
wins over done, done over dropped. A stub whose path or nearby lines name
a word only that item's name has gets attached to it. On a done item that
reads `done, stub left`. Unmatched stubs go under the table.

## Actions

| row | action |
| --- | --- |
| missing | **create**, through one skill run |
| guess, confirmed | **mark**: insert the marker, then **move** to the item's milestone |
| renumbered | **mark**: rewrite the marker's ID |
| stale, queued or proposed | **edit**: append a `## Doc update` with old and new text, bump `at` |
| stale, otherwise | **comment** with old and new text, bump `at` |
| anything else | nothing |

Edits re-read the body just before writing and only insert or append, so a
human's edits survive. Nothing closes, reopens, relabels or touches
`proposed` on an existing issue.

**Milestones.** A milestone item with an issue of its own gets a GitHub
milestone, `[VISION] M4: Ashby adapter with fixture`, found by its
`[VISION] M4:` prefix. The slug keeps two docs' `M1` apart, which GitHub
needs anyway: milestone titles are unique per repo. A renamed item renames
its milestone instead of orphaning it. Tickets are issue-sized, so a ticket
doc keeps today's one batch milestone, named for its slug (`unpark`).

An issue moves only off an empty milestone or its doc's batch one
(`VISION`), never off one a human picked. One issue on two items sits in
the lower item's milestone. The emptied `VISION` milestone stays; deleting
it is yours. An explicit `-milestone X` keeps today's single batch
milestone, and `off` still means none.

## CLI

```
polako plan -design docs/VISION.md [-dry-run | -yes]
polako status -design docs/VISION.md [-json]
```

`plan` prints the table, then asks twice: `Accept these 17 guesses? [y/N,
or IDs to drop, or M13=#10]`, then `Apply these actions? [y/N]`. Nothing is
written before the second yes. That includes the `proposed` label and
milestones, which preflight creates today. `-dry-run` prints the table, the
actions and the claude invocation, then stops. `-yes` applies without
asking, except guesses: those need a terminal. A guessed item is neither
marked nor created under `-yes`, so nothing gets duplicated. A non-terminal
without `-yes` refuses, like `setup -apply` and `unpark -apply`.

`status -design` prints the same table, read-only, with no model, and needs
a checkout. Plain `status` doesn't change. Like the rest of status, the
table prints no issue text: IDs and names from the doc, states, numbers. The
one place issue text appears is plan's guess prompt, which shows each
candidate's title with control characters stripped, clipped to 70.

```
docs/VISION.md (VISION) @ 61671a0: 24 items, 17 done, 7 missing
 ID   item                                    state    issue  PR   action
 M1   Scaffold                                done     #3?    #11  mark
 M7   Workable adapter with fixture           missing  –      –    create
 M12  State store and lifecycle, with…        done     #9?    #17  mark
 M13  Health report and suspect detection     done     #9?    #17  mark  (shares #9)
 M22  explain command                         missing  –      –    create  stub cmd/trawler/main.go:78
 …
```

## The skill

plan-backlog takes a fourth argument: a manifest at
`.polako-scratch/plan-items.md`, written by the binary. It holds each item
to file (full ID, name, doc text), the matched issue numbers for
`--blocked-by`, and stub paths for `## Pointers`. That's doc text, numbers
and paths: no issue text. With it, the skill files one issue per item,
flat, the marker above the footer. It may use an epic only when one item
breaks the sizing contract, and then the epic carries the marker. An item
it finds already built gets no issue, just a report line. Without the
argument it behaves as today, so standalone use still works. Its `gh`
surface doesn't change.

After the run, the label pass also:

- attaches each new issue to its item's milestone;
- adds a missing marker when the issue matches exactly one listed item;
- reports a second issue for the same item as a duplicate, loudly. It
  doesn't close it.

## Invariants this changes

- **"A plan or health run creates issues and nothing else."** The model's
  half still does only that; its allowlist is unchanged. The binary gains
  marker inserts, `Doc update` appends, comments, milestone moves and
  milestone renames, all after an approval and all built from the doc and
  fixed templates, never from issue text. No label or state change on an
  existing issue means no self-approval path, which is the risk the
  invariant guards against. This needs arguing in the PR body.
- **New contract: the item marker**, same shape as the plan footer. The
  skill writes it, `parseItemMarker` reads it, and a test holds SKILL.md to
  the parser's wording.
- **`plan` on a doc with items needs a terminal or `-yes`.** Scripted plan
  runs have to add the flag.

## Tests

Hermetic, with fixtures under `cmd/polako/testdata/reconcile/`: two
versions of the doc, `issues.json`, `prs.json`. Parse, match and classify
are pure, so most cases are table tests: done, parked, missing, renamed
(marked and legacy), renumbered, dropped, in progress, container skipped,
duplicate marker, forged marker on a public repo, two docs that both
number `M1`. Apply runs through `POLAKO_FAKE_GH` and `_LOG`. A second run
over the post-apply fixtures makes zero writes and spawns no claude. The
skill change gets an `evals/plan-reconcile` case, three runs, with the
spend quoted in its PR.

## PRs

1. docs: this design.
2. feat: doc items and the item marker. Parser and contract only, no
   behaviour change.
3. feat: `status -design` prints the reconciliation table (read-only).
4. feat(plan-backlog): file exactly the items handed in, each with its
   marker. SKILL.md plus the eval case.
5. feat: `plan` reconciles first: table, approval, binary-side writes,
   skill run for missing items only, post-run marker pass, the second-run
   test, and the CLAUDE.md amendment.

The release bump stays your call.

## Out of scope

- Renaming a doc without pinning its slug. Its old issues then only match
  as weak guesses, so they need confirming again.
- Staleness from changes outside an item's own entry.
- Matching merged PRs that close no issue. They show as evidence only.
- A vision summary line in plain `status`.
