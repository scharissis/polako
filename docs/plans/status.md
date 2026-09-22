# A status report that kept up

Scope: `status`'s text and `-json` reports, the plan-footer parser, the gate
label `setup` creates, one new reader of the run data · Behavior change:
`status` stops printing to stderr on a healthy read, gains rows and lines,
and scopes itself to a gate label it finds on GitHub; `setup -apply` may set
one label's description; `status` becomes the third reader of `~/.polako`

`status` was written when polako had one verb and no history. Since then the
binary gained `setup`, `tidy`, `update`, `plan`, `health`, run data and the
plan-document convention. The report didn't keep up: it hides open issues,
says things that are false, and can't tell which queue `work` would drain.

This document drafts the fixes, smallest first.

## What exists today

A bare `polako status` in this repo, 2026-09-21:

```
ignoring 4 proposed issue(s) awaiting curation — remove the proposed label to queue them
scharissis/polako
  ready       5 issues — #418, #432, #471, #477, #484
  parked      2 issues — #77, #390, labelled needs-human
  proposed    4 issues — #459, #460, #467, #469, labelled proposed
  containers  2 issues — #411 (6/7 closed), #429 (2/6 closed)
  next        #418

plan documents
  doc                             state   containers                                      open children
  docs/plans/backlog-fill.md      draft   —                                               —
  docs/plans/permission-parks.md  active  #429 (2/6 closed)                               4
  docs/plans/plan-conventions.md  done    #321 (6/6 closed — the next shift closes it)    0
  docs/plans/setup.md             active  #411 (6/7 closed)                               1
  docs/plans/token-spend.md       draft   —                                               —
  docs/plans/visual-evidence.md   active  #399 (6/6 closed — the next shift closes it)    0
  (gone — footer names a document no longer on disk: an (#270, …), docs/plans/model-and-effort.md (#360, …), … the (#281, …))
plan: session 0%, week 56% (resets Sep 23, 7pm) — polako was 60% of the last 24h

needs you: decide what to do about #77, #390 (drop needs-human to requeue); curate #459, #460, #467, #469 (drop proposed to queue them)
```

- **The first line is the drain's, not the report's.** `status` shares
  `openQueues` (`backlog.go`) with `work`, and `openQueues` calls
  `sayProposals`. That warning goes to stderr while GitHub is still being
  read, so it lands above the header. In `work`, "ignoring" means "won't work
  these". In `status` it reads as "won't show these", then the report shows
  them. The `proposed` row and `needs you` already say it.
- **Three open issues appear nowhere.** #433, #434 and #435 are open and
  `ready`, held back by `blockedBy`. `issueQueues.heldBack` counts them as
  open, but `queuePairs` (`status.go`) has no row for them and `-json` no
  field. Sixteen open issues, thirteen shown. Only `work` and `-dry-run`
  narrate them (`logHeldBack`).
- **"The next shift closes it" is false for #321 and #399.** Both are
  already closed. `planDocStatusFrom` (`plans.go`) builds a plan's containers
  without looking at the issue's state, and `containerRefs` assumes open.
- **`an` and `the` are not plan documents.** `parsePlanFooter` (`footer.go`)
  takes the first word after "Proposed by polako plan from" and checks
  nothing about it. A body line that starts with the phrase and carries on in
  prose parses. `footer_test.go` covers the phrase mid-sentence, not at the
  start of a line.
- **The `gone` line is mostly noise.** `plan-conventions.md` says a done plan
  leaves. So a gone document whose issues are all closed is the normal end
  state, and today that's every real entry on the line: five documents, about
  forty issue numbers, nothing to do about any of them. The line exists for
  leftovers — a deleted plan with issues still open.
- **`status` doesn't know the gate label.** `work` on this public repo needs
  `-label ready`. `status` learns a label only from `-label` or
  `POLAKO_LABEL`. A bare `status` reports every open issue as the queue, and
  never reads visibility, so it doesn't even say a real run would refuse.
  `setup` asks for the gate label and creates it, then remembers nothing —
  by design, and a config file has been turned down three times (`setup.md`
  and `visual-evidence.md`, both since retired, and `permission-parks.md`).
  It does leave
  one trace on GitHub: the description it creates the label with, "gate
  label for `polako work -label`". This repo's `ready` is older than `setup`
  and doesn't carry it.
- **Under a gate label, curation disappears.** `-label` becomes `--label` on
  the listing. A `proposed` issue without the gate label drops out, and so
  does the `curate` clause. Approving one takes two moves — drop `proposed`,
  add the gate label — and the report names neither.
- **Nothing from run data.** `status.go` and CLAUDE.md bar it: the metrics
  files have exactly two readers. So `status` can't say when the last shift
  ran, what it cost, or why #77 is parked, though all three are on disk.
- **Free data is thrown away.** The listing already returns every issue's
  labels; `selectableIssues` drops them after three checks. `model:` and
  `effort:` labels — the ones that make a run dearer — never reach the
  report. `updatedAt` is one more field on the same call.
- **`status` doesn't know `tidy` exists.** Finished `issue-N` branches pile
  up locally and nothing mentions them.

## The answer in one paragraph

`status` prints one report, on stdout, in order: notes sit under the header,
not above it. Every open issue lands in exactly one row. The plans section
says only what someone can act on. With no `-label`, `status` looks for the
label carrying `setup`'s marker and scopes itself to it, saying so in the
header; `setup -apply` offers to stamp the marker on a gate label that
predates it. Under a gate label, `proposed` stays visible and issues outside
the gate get a row. One line comes from run data — the last shift on this
machine — and two details ride on it: a price for the ready queue and a
reason beside each parked issue. A last line points at `tidy`.

What it should look like here (dollars, dates and park reasons are made up):

```
scharissis/polako — issues labelled ready (gate label, read from GitHub)
  ready             5 issues — #418, #432, #471, #477, #484 — about $38 at your median
  held back         3 issues — #433 (behind #432), #434 (behind #433), #435 (behind #434)
  parked            2 issues — #77 (budget, quiet 12d), #390 (permission_refused, quiet 2d)
  proposed          4 issues — #459, #460, #467, #469
  containers        2 issues — #411 (6/7 closed), #429 (2/6 closed)
  next              #418

plan documents
  doc                             state   containers         open children
  docs/plans/permission-parks.md  active  #429 (2/6 closed)  4
  docs/plans/setup.md             active  #411 (6/7 closed)  1
  docs/plans/visual-evidence.md   active  #399 (closed)      0
  docs/plans/backlog-fill.md      draft   —                  —
  docs/plans/token-spend.md       draft   —                  —
  docs/plans/plan-conventions.md  done    #321 (closed)      0
  (5 deleted plans, every issue closed)

last shift here: Sep 21 02:10, 6h12m — 4 merged, 1 parked, $31.20 — `polako stats -shift last`
plan: session 0%, week 56% (resets Sep 23, 7pm) — polako was 60% of the last 24h
tidy: 3 local issue branches whose issue is closed — `polako tidy` lists them

needs you: decide what to do about #77, #390 (drop needs-human to requeue); curate #459, #460, #467, #469 (drop proposed, add ready); retire docs/plans/plan-conventions.md (done — move what's still true into docs/, delete the file)
```

## What this may read and write

Two tickets change an invariant. Both are argued here and must be said again
in their PR bodies.

- **`status` becomes the third reader of the run data** (ticket 7). CLAUDE.md
  names exactly two: `stats` and `proposalPricingLine`. The rule's reason is
  that telemetry must not turn back into state. The new read keeps to that:
  it is rendering only, computed after the GitHub read, and feeds neither
  `next`, nor any queue row's membership, nor `needs you`. Deleting
  `~/.polako` changes that one line and the two details in ticket 8, nothing
  else, and a test holds it to that. `status.go`'s own objection — the drain
  may run on another machine — is answered by wording: the line says "here",
  and is absent when there is no local history. CLAUDE.md's text becomes
  "exactly three readers" and names `status`. The shift log stays unread by
  everything.
- **`setup -apply` may set one label's description** (ticket 5). Its write
  surface today is "labels, plus one PR", and the label half only creates.
  This adds `gh label edit <gate> --description …` behind its own `[Y/n]`.
  Still labels, still asked, still nothing on an issue. CLAUDE.md's `setup`
  invariant gains the clause.
- **A label description is not attacker text.** Only someone with triage
  rights can edit a label, the same people who can apply the gate label
  itself. `status` matches the description against one fixed string and
  prints the label's name, never the description.
- **All state stays in GitHub.** The marker is the whole mechanism: no file,
  no "setup was run" memory. `work` is not changed to read it — see
  "Considered and not proposed".
- **Reads only.** `status` adds one `gh api` labels read and one field each
  on two calls it already makes. `TestStatusMakesOnlyReadCalls` gains the
  labels path and nothing else.
- **No issue text.** Every new row is numbers, label names and states. The
  policy labels shown in ticket 9 go through `labelPolicy`'s shape check
  first.

## Drafted tickets

Ordered by dependency, bugs first. Sizes are the shape of the work, not
money. Numbers are for reference in this doc, not issue numbers.

Every ticket that changes the report also updates the byte-for-byte golden
in `TestStatusReportsWhereTheBacklogStands`, the JSON twin, the sample and
schema in `docs/reference.md` under `status`, and the README's sample.
Hermetic tests through `fakeCLI`.

### 1. One report, in order

**Problem.** A healthy `status` writes to two streams. The proposals warning
and the label note go to stderr during the read, so they land above the
header on a terminal and vanish from `polako status > file`.

**Shape.**

- `statusConfig` marks its `queueMemo` as having said the proposals line, so
  `sayProposals` stays quiet for `status` alone. `work` and `-dry-run` keep
  the line, word for word.
- `statusLabelNote` returns its note instead of narrating it. The snapshot
  carries `notes`; the text report prints them under the header, `-json`
  gains a `notes` array (empty, not null).
- stderr is left for reads that failed.

**Done when.** Against the fake repo with proposed issues, `status` writes
nothing to stderr, and a missing `-label` prints its note on stdout under the
header.

Estimate: S

### 2. The footer parser takes only a path

**Problem.** `parsePlanFooter` turns "Proposed by polako plan from an
earlier…" into a document called `an`.

**Shape.** Accept the parsed document only if it ends in `.md`, or a ` @ `
and a hex SHA follow it. Anything else is "no footer". `footer_test.go`
gains the line-initial prose case. The skill's wording and the
`repo_test.go` assertion over it don't change.

**Done when.** A body whose last matching line is prose parses as no footer;
every existing table case still passes.

Estimate: S

### 3. The plans section says only what's actionable

**Problem.** A closed container is promised a close. The `gone` line lists
finished work. Rows come out alphabetically, so `draft` sits above `active`.
A `done` plan still on disk is a chore nobody is told about.

**Shape.**

- `planDocStatusFrom` keeps the issue's state; a closed container renders
  `#321 (closed)`.
- The `gone` line names only documents with an open issue, and only those
  issues. The rest collapse to `(N deleted plans, every issue closed)`.
  `-json` keeps the full list, with a per-document `open` count.
- Rows sort `active`, `proposed`, `draft`, `done`, then by path.
- A `done` document still on disk adds a `needs you` clause naming it:
  "retire docs/plans/x.md (done — move what's still true into docs/, delete
  the file)".

**Done when.** The fake repo with one closed container, one all-closed gone
document and one gone document with an open issue prints `(closed)`, names
only the second gone document, and counts the first.

Estimate: S

### 4. A row for held-back issues

**Problem.** An issue waiting on a `blockedBy` prerequisite is open and
appears in no row.

**Shape.** `held back  3 issues — #433 (behind #432), …`, from
`issueQueues.heldBack`, under `ready`. `-json` gains `held_back`, each entry
a number and its blockers. `nextLine`'s "every open issue is …" list learns
"held back".

**Done when.** A test asserts every open issue in the fake listing appears
in exactly one queue row, held-back ones included.

Estimate: S

### 5. `setup` marks the gate label; `status` finds it

**Problem.** `status` can't know which label `work` runs with, so a bare
`status` on a gated repo describes the wrong queue. On a public repo it
doesn't even say so.

**Shape.**

- The marker description becomes one constant in `labels.go`; `setup`'s two
  uses read it.
- `status` with no `-label` makes one `gh api repos/{owner}/{repo}/labels`
  read. Exactly one label carries the marker: scope to it, and the header
  says "issues labelled ready (gate label, read from GitHub)". More than
  one: a note naming them, no scoping. None: carry on unscoped.
- `statusConfig`'s `repo view` call asks for `visibility` too. Public and
  still unscoped: `queueGate`'s text as a note, the way `-dry-run` prints it.
- `setup`: a `-label` that exists without the marker reports "exists, not
  marked as the gate label". `-apply` asks `[Y/n]`, then `gh label edit`.
  `-yes` takes the default, yes.
- `work`'s public-repo refusal names the marked label when there is one:
  "pass `-label ready`". `work` still never scopes itself.
- `-json` gains `scope`: the label and where it came from (`flag`, `env`,
  `github`).
- CLAUDE.md's `setup` invariant gains the description edit. Said in the PR
  body.

**Done when.** Fake repo, public, `ready` marked: bare `status` equals
`status -label ready` but for the header. Two marked labels: a note, the
unscoped report. No marker: the gate note. `setup -apply -yes` on an
unmarked `ready` makes exactly one `label edit` call.

Estimate: M

### 6. Gate-aware rows

**Problem.** Scoped to a gate label, the listing drops every issue without
it. Proposals vanish from the report, and an outside issue waiting for
triage is invisible.

**Shape.**

- Scoped, `status` lists once without `--label` and partitions locally. The
  in-gate subset goes through `selectableIssues` unchanged, so `status` and
  `work` can't disagree about the queue.
- `proposed` stays repo-wide. The curate clause becomes "drop proposed, add
  `<gate>`" for issues lacking the gate label.
- New row `outside the gate`: open issues with neither the gate label nor a
  hold. Numbers only, first ten, then "and N more". Absent when empty.
- `-json` gains `outside_gate`.

**Done when.** A test feeds one listing both ways and asserts the in-gate
queues equal what `openQueues` returns with `--label`. A proposed issue
without the gate label shows in `proposed` and in `needs you`.

Depends on 5.

Estimate: M

### 7. The last shift, from run data

**Problem.** "When did it last run, and how did it go?" is the first thing
an operator asks, and `status` can't say.

**Shape.**

- A `-metrics` flag, as `stats` has, `off` included. Documented in
  `docs/reference.md`.
- After the GitHub read, load this repo's records with `loadRecords` and
  `rollUpIssues` (`statsrecords.go`, `stats.go`). Take the newest shift.
- One line: `last shift here: Sep 21 02:10, 6h12m — 4 merged, 1 parked,
  $31.20 — polako stats -shift last`. Absent with no records or
  `-metrics off`. `-json` gains `last_shift`, null when absent.
- Rendering only. The snapshot's queues, `next` and `needs you` are computed
  before the read and never touched by it.
- The invariant text moves: CLAUDE.md ("exactly three readers"), the rules
  at the top of `status.go`, `docs/reference.md`, `docs/run-data.md`, the
  comment on `proposalPricingLine`. Said in the PR body.

**Done when.** With records, the line prints. A test runs `status` twice,
with and without the metrics directory, and the outputs differ by that line
alone.

Estimate: M

### 8. Price the queue, name the park reasons

**Problem.** "Five issues ready" doesn't say what a shift will cost, and
"parked" doesn't say why.

**Shape.**

- The `ready` row ends "about $38 at your median", from the same median
  `proposalPricingLine` prices proposals with. Share the function.
- A parked ref carries its `park_reason` when the newest local issue record
  for it is `needs_human`: `#77 (budget)`.
- Both absent without history. Both under ticket 7's rule and its test.
- #435, which names the grant a permission park needs from the GitHub
  thread, stays in epic #429. This ticket reads no comment.

**Done when.** With records, both details print. Without, the rows match
ticket 4's.

Depends on 7.

Estimate: S

### 9. Free detail from the listing

**Problem.** Age and cost policy are already in the payload `status` pays
for, or one field away.

**Shape.**

- `issueFields` gains `updatedAt`. Parked and proposed refs show `quiet
  12d`, the way `awaiting you` refs already do, without the comment read.
- `selectableIssues` keeps an issue's `model:` and `effort:` labels. Ready
  and held-back refs show them, `#471 (opus, high)`, parsed by `labelPolicy`
  so a malformed value shows nothing. An epic's inherited policy isn't
  chased; that's a read per issue.
- `-json` gains both per issue.

**Done when.** The fake listing with an `effort:max` issue and an old parked
one prints both details; the drain's tests don't change.

Estimate: S

### 10. A pointer to `tidy`

**Problem.** Finished branches and worktrees pile up. `tidy` exists;
nothing says when it's worth running.

**Shape.** When `-dir` is a checkout, list local branches matching
`<branch-prefix>N` and count those whose issue isn't in the open set
`status` already has. One line: "tidy: 3 local issue branches whose issue is
closed — `polako tidy` lists them". Local `git` only, no `gh` call. Absent at
zero, and under `-repo` with no checkout. It says "lists them", not
"reclaims them": `tidy` still decides what is safe.

**Done when.** A temp repo with `issue-7` (closed) and `issue-8` (open)
prints a count of one.

Estimate: S

## Considered and not proposed

**A `.polako` config file.** The obvious home for `-label`. Turned down three
times already because repo content becomes a source of flags. The label
marker gets the same result with the state in GitHub.

**`work` scoping itself from the marker.** It only narrows, so it's safe.
But a shift's scope should be typed, not discovered, and `work` refusing
with the exact flag to pass is one keystroke away from the same thing.
Worth another look once ticket 5 has run for a while.

**Liveness: "a shift is running".** `status` reports state, not liveness,
and the drain may be on another machine. A lock file is the local database
the invariants rule out.

**`status -watch`.** `watch -n 60 polako status` exists.

**Issue and PR titles.** The most-wanted missing thing, and
attacker-controllable on any repo that takes outside issues. Numbers only.

**Rewording `work`'s "ignoring" line.** It's right where it is. Three tests
and a doc pin it.

**Parked-since from the timeline API.** Exact, and a call per parked issue.
`updatedAt` is free and close enough.
