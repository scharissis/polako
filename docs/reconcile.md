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
