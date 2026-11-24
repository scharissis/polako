# Reconciling a plan doc with GitHub

`polako status -design` prints, for each item of a plan doc, the issues that
serve it, where it stands, and what reconciling it would change — the read a
re-plan needs so it files only what's missing:

```bash
polako status -design docs/VISION.md
```

```
docs/VISION.md (VISION) @ 1a2b3c4 — 12 items: 1 in progress, 5 done, 1 dropped, 1 missing, …
  id   item                          state             issue  pr   action      note
  M1   Scaffold                      done              #3     #11
  M2   HTTP fetcher with retries     done              #4?    #12  mark, move
  M3   Widget importer with fixtures queued, stale     #5          edit, move
  M9   explain command               missing                       create      stub cmd/app/main.go:3
  M10  Nightly workflow              done              #10    #14  mark, move  marked M9 before the doc was renumbered
? is a guess, 5 in all — matched by the issue's own words, not a marker
```

One row per item of the doc: each numbered entry under a heading that
starts with "Milestones" (`M<n>`), each `### N.` heading under `## Drafted
tickets` (`T<n>`), and anything marked `<!-- polako:item=ID -->`.

An issue serves an item when its body carries the item's marker,
`<!-- polako:item=VISION/M3 -->`, on a line of its own. The slug before the
slash is the doc's file name, so two docs' `M1` never meet. An older issue
with no marker is guessed instead, and shows a `?`: its plan footer names
the doc and its body says "milestone N" or "ticket N", or its title shares
most of the item's name. A marker whose text now belongs to a later item —
the doc was renumbered — follows the text. On a public repo, a marker only
counts on your own issue or one with the gate label; anyone else's is a
guess.

`state` is the deciding issue's: `in progress` (an open PR), `parked`,
`queued`, `proposed`, `done` (closed as completed), `dropped` (closed as not
planned — never re-filed), or `missing`. `stale` means the item's entry
changed since the issue was filed or synced, and `stub left` a `not
implemented` in the code names a done item. `action` is what reconciling
would change: `create` an issue, `mark` one with its item, `move` it to the
item's milestone, or `edit` or `comment` on a stale one. Stubs no single
item claims, and markers for items the doc no longer has, are listed under
the table.

Reads only, like the rest of `status`: two `gh` listings, the repository's
visibility and your login, and `git show` and `git grep` in the checkout.
No issue text is printed. `-json` prints the same table as one document,
every list present.

## What `polako plan` does with it

`polako plan -design <doc>` on a doc with items prints this table first, then
asks before it writes anything:

```
The guesses, with each issue's title:
  M2    #4     feat: add the http fetcher
  ...
Confirm them? [y = all, N = none, or name the wrong ones: ID drops its guess, ID=#N points it at #N]

  milestones  make sure 7 item milestones exist, named as the doc names them now
  mark        6 issues with their items: #4 (M2), #8 (M6), #9 (M7), #9 (M8), #10 (M10), #13 (M12)
  move        6 issues to their item's milestone
  edit        1 issue whose item changed, adding the doc's new text: #5 (M3)
  create      1 issue, one plan-backlog run capped at 11: M9
Make these changes? [y/N]
```

The guess prompt is the one place polako prints issue text: you can't
confirm a match without its title. Answer `M2` to drop that guess and file M2
fresh, or `M2=#7` to point it at #7; `N` leaves guessed items alone this run.
Nothing is written before the second yes, not even the `proposed` label.

Then, in that order:

- **Milestones.** One per milestone item, `[VISION] M4: CSV importer with
  fixtures`, found again by its `[VISION] M4:` prefix and renamed when the
  item is. A ticket doc's issues share one milestone, named for the doc.
- **Issue bodies.** Each matched issue gets its marker on its own line
  above the plan footer, with `at=` naming the doc commit it now matches. A
  stale open issue gets a `## Doc update` with the item's old and new
  text; a stale closed or running one gets that as a comment instead. Each
  body is re-read just before the edit and only added to.
- **Moves.** Off no milestone or the doc's own, never off one you picked.
- **Missing items.** One plan-backlog run handed `.polako-scratch/plan-items.md`,
  which lists them with the marker each issue must carry. After it, every
  new issue is checked for its marker and put in its item's milestone.
  The run is capped at the number of items to file plus 10, room for an
  item that turns into an epic and its children. An explicit
  `-max-issues`, or `POLAKO_MAX_ISSUES`, is used as is; at or below the
  item count, the `create` line says the run files items in order up to
  the cap, epics included, and to rerun plan for any left over.

An item the run finds already built gets no issue, just a `built:` line in
its report. Add that item's marker line to the issue that built it, and the
next run counts it.

Nothing closes, reopens or relabels an existing issue. A change that
doesn't take is reported and the rest carry on; rerunning plan picks up
whatever is left. Once everything is in place, plan says `nothing to change`
and writes nothing.

`-yes` makes the changes without asking, for a script. A guess needs a
person, so under `-yes` its item is neither marked nor filed. With no
terminal and no `-yes`, plan refuses. `-dry-run` prints the table and what a
run would change if you confirmed every guess, then stops.
