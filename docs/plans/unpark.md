# `unpark` for every park, not just permission ones

Scope: the park comment, `unpark`, `status`'s needs-you line · Behavior
change: every park comment gains one `Park: <category>` line; `unpark`'s
listing gains columns and makes more `gh` reads; nothing else changes

`polako unpark` was built for one park: a permission park whose refused tool
polako could name (`docs/plans/permission-parks.md`, epic #429). For that park
it prints the `-add-tools` line to rerun with. For every other park it's
`gh issue edit N --remove-label needs-human` with a `[y/N]` in front.

This document drafts the rest: `unpark` says, for any parked issue, why it
parked, where its work stands, and what removing the label will do.

## What exists today

Every park comment on `scharissis/polako`, counted 2026-09-22 — 23 comments:

| park | count | what `unpark` adds today |
| --- | --- | --- |
| permission, fallback reason, no tool named | 9 | nothing |
| CI still red after remediation | 5 | nothing |
| `-max-issue-time` cap | 4 | nothing |
| clean run, no PR, no question | 4 | nothing |
| retries exhausted | 1 | nothing |
| permission, with a `Refused:` footer | 0 | the rerun line |

- **The one park it serves hasn't happened yet.** The footer shipped
  2026-09-21 (#518). No permission park since. Zero of 23.
- **The permission parks weren't permission problems.** #390's real blocker
  was an unreachable SSH agent (`permission-parks.md` says so). #318, #372,
  #404, #417 and #420 ended mid-review-gate with the ask in prose — the #461
  and #472 shapes, both since closed. No grant clears any of them.
- **What clearing one actually took.** Each time, by hand: is `issue-N` on
  origin, how far ahead is it, is there a PR, is CI red, will the next shift
  resume the branch or wait on the PR. Then the label. `unpark` answers none
  of that.
- **The category is off the thread.** `parkedError.category` (`park.go`) goes
  to run data only. The thread gets prose. `unpark` may not read run data, so
  it can't tell a budget park from a CI park without matching prose that
  drifts.
- **`status` has the same gap.** A parked issue with no `Refused:` footer
  gets "decide what to do about #N". `docs/plans/status.md` ticket 8 fills it
  from run data — which works on one machine, and only with history.

## The answer in one paragraph

The park comment names its own category in a fixed footer line, `Park:
budget`, under the same contract as `Refused:`. `unpark` reads it, reads the
branch and PR off GitHub, and prints per issue: why it parked, where the work
is, what the next shift will do once the label is gone, and the one next step
that category calls for. `status` reuses the read. `unpark` still writes one
thing: the label removal.

## What this may read and write

- **All state lives in GitHub.** Branch, PR and checks come from `gh`. The
  category comes from the thread. No run data, no shift log — the reason the
  category has to go on the thread at all.
- **The park footer is a contract, and it grows a line.** `parkIssue` writes
  `Park: <category>`; `unpark` and `status` parse it; one test holds both
  sides to the wording. CLAUDE.md's park-footer invariant is amended in
  ticket 1 to name both lines.
- **What goes on a public thread.** One identifier from the fixed list in
  `metrics.go` (`budget`, `permission_refused`, `checks_remediation`, …). No
  issue text, no path, no session id. It's the same word the prose above it
  already spells out.
- **Issue and comment text is data.** Same rules as `Refused:`: only a
  comment authored by the `gh` viewer, only the last matching line, only a
  value from the fixed list. Anything else reads as no category. A category
  picks which canned sentence `unpark` prints. It never widens an allowlist,
  raises a cap, or picks a model.
- **The write surface doesn't move.** `gh issue edit N --remove-label
  needs-human`, and nothing else. `unpark` doesn't rebase, push, raise a cap
  or start a drain.
- **Cost.** Two or three `gh` reads per parked issue, on an attended verb.
  Nothing on the drain path.
- **Local git is a courtesy, not state.** Ticket 5 reads the checkout at
  `-dir` with `inspectLeftWork`, which already exists and already argues this
  (`park.go`). Terminal only, skipped under `-repo`.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. The park comment names its category

**Problem.** A park's category never reaches GitHub, so nothing reading the
thread can tell parks apart.

**Shape.**

- `parkIssue` takes the category — `parkAndMoveOn` (`drain.go`), its one
  caller, can get it from `parkCategoryOf` — and `parkCommentBody` appends
  `Park: <category>` after the `Refused:` line, or as the only footer when
  there are no entries.
- `footer.go` gets `parkCategoryFooterPrefix` and `parseParkCategory`: last
  matching line via `lastFooterLine`, value checked against the known
  categories, else `""`. `unknown` is written as-is and parses as itself.
- `parseParkFooter` must still find `Refused:` when it's no longer the last
  line. It already matches by prefix, not position; a test pins it.
- The footer contract test covers the new line. CLAUDE.md's invariant names
  it. `docs/behaviour.md` shows the comment's new shape.

**Done when.** A drain test's park comment ends with the right `Park:` line
for a budget park and a permission park; a comment with both footers parses
both; a forged value (`Park: opus`) parses as no category.

Estimate: S

### 2. `unpark` shows where the work stands

**Problem.** The listing doesn't say whether there's a branch or a PR —
the first thing a human checks.

**Shape.**

- Per parked issue: `prForBranch` for `<branch-prefix>N`, then `prView` for
  its checks when there is one. No PR: `gh api
  repos/{repo}/compare/{default}...{branch}` for `ahead_by`; a 404 is "no
  branch on origin".
- `unpark` gains `-branch-prefix`, default `issue-`, the same flag `work`
  has. Documented in `docs/reference.md`.
- Table: a `work` column — `PR #84, CI red`, `issue-390, 4 commits, no PR`,
  `nothing pushed`. One-issue view: PR link, failing check names, branch and
  count, each on its own line.
- Best effort per field, like `readParkListItem`: a read that fails prints
  `not read`, never drops the row.

**Done when.** Against a fake repo with three parked issues — a PR with a
failing check, a pushed branch with no PR, nothing at all — each row says
so, and the one-issue view names the failing check.

Estimate: M

### 3. `unpark` says what the next shift will do

**Problem.** "Remove the label" doesn't say what happens next. With a PR the
drain never re-runs the skill (restart safety); without one it does.

**Shape.**

- One line per issue under the one-issue view and after each `-apply`
  approval: `next shift: waits on PR #84 and remediates its red CI` /
  `resumes issue-390 from its 4 commits` / `starts over — nothing was
  pushed`.
- The rule comes from the drain, not a copy of it: pull the PR-exists
  decision in `issue.go` into a function both call, so the two can't
  disagree.
- A red-CI park with an unchanged branch gets a warning: the next shift
  will remediate the same red and likely park again, so fix the branch
  first.

**Done when.** The three fixtures from ticket 2 each print their line, and
a test fails if the drain's PR check and `unpark`'s stop sharing a function.

Depends on 2.

Estimate: S

### 4. A next step per category

**Problem.** The one-issue view ends with "fix what it names". For most
parks the comment names nothing fixable.

**Shape.** A fixed table, category to one sentence, printed in the one-issue
view and beside each `-apply` question:

| category | next step |
| --- | --- |
| `permission_refused`, entries | grant them — the rerun line below |
| `permission_refused`, none | the refused tool is in that shift's log; or the ask was prose — read the run's last comment |
| `budget` | the work is on the branch — finish it by hand, or rerun with a higher `-max-issue-time` / `-max-cost` |
| `checks_remediation` | fix the failing check on the PR, then unpark |
| `conflict_remediation`, `review_remediation` | resolve it on the PR, then unpark |
| `produced_nothing` | check the issue says what to change; unpark to retry |
| `retries_exhausted` | check `claude` runs at all on this machine; unpark to retry |
| `pr_closed`, `pr_state` | reopen or delete the PR, then unpark |
| `no_skill`, `auth`, `unknown` | read the thread — these end a shift, they rarely park |
| none, or a hand label | read the thread |

Wording is settled in the PR, under the writing rule: what a human should
do, in one short sentence. A category without a row is a test failure, so a
new park category can't ship without its sentence.

**Done when.** Every category in `metrics.go` has a row and a test says so;
the one-issue view for a budget park prints the budget sentence.

Depends on 1.

Estimate: S

### 5. Unpushed work, when there's a checkout

**Problem.** Every park but the budget one can leave commits or edits that
never reached origin. GitHub can't see them, so ticket 2 says `nothing
pushed` over work that exists.

**Shape.** When `-dir` is a checkout of the repo and `-repo` wasn't given,
call `inspectLeftWork` per parked issue and add to the one-issue view:
`local: 3 commits not pushed, 2 files uncommitted, in <path>`. Terminal
only. The `next shift` line from ticket 3 says `resumes from the local
worktree` when that's what the skill will find.

**Done when.** A fixture with an unpushed local branch prints the line; the
same run under `-repo` doesn't, and makes no git call.

Depends on 2.

Estimate: S

### 6. `status` reuses the read

**Problem.** `status` still says "decide what to do about #N" for every
park without a `Refused:` footer.

**Shape.**

- `readParkListItems` already feeds `status`. It now carries the category,
  so the needs-you clause becomes per-category: `#413 hit its time cap —
  polako unpark 413`. `-json`'s `queue.parked` gains `category`.
- No branch or PR reads here beyond what `status` already makes.
- This replaces the park half of `docs/plans/status.md` ticket 8, which
  reads `park_reason` from run data. The thread read works on any machine
  and needs no history. That ticket is filed as #515, still `proposed`:
  edit it down to the pricing half when this one is approved.

**Done when.** `status` against a fake repo with a budget park and a
hand-labelled issue prints the budget clause for one and today's line for
the other; `-json` carries the category.

Depends on 1.

Estimate: S

## Considered and not proposed

**Reading the category from run data.** It's there already. It's also local,
absent on a second machine, and `unpark` isn't one of its readers. One more
reader is how telemetry turns into state.

**Matching the category out of the reason prose.** Works today. The prose is
written for people and gets reworded; the footer is a contract with a test.

**A label per category** (`parked:budget`). Labels are orchestration state.
That's a dozen more for `setup` to create, and a human can strip one and leave
`needs-human` behind. The comment is enough.

**`unpark` doing the fix.** Rebase the branch, push the worktree, rerun with
a raised cap. Each is a judgment a human made differently every time in the
23 parks above. The verb lists, asks, and removes a label.

**`unpark -apply` starting the drain.** `permission-parks.md` already turned
this down: it prints the line, the human runs it.

**More work on the `-add-tools` half.** No `Refused:` park has happened.
Wait for one before tuning it.

**Rewording the fallback permission reason.** It fired 9 times, mostly on
the #461 and #472 shapes, both fixed. See whether it recurs first. Ticket
4's "none" row covers reading it in the meantime.

**Issue titles in the listing.** A title is anyone's to write on a public
repo, and this prints to a terminal. The one-issue view links the thread.
