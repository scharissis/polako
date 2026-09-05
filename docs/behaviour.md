# How polako works

The [README](../README.md#how-it-works) has the one-page picture. This page
is the long version: what the supervisor does when a run crashes, an issue
can't finish, a PR goes red, or it needs you. A short
[Questions](#questions) section closes it out.

## State lives in GitHub, not on disk

**All state lives in GitHub** — issues, comments, PRs, branches. Kill the
process anywhere, rerun it later, and it re-derives where things stand from
GitHub alone. If a PR already exists for `issue-N`, it never re-runs Claude
on that issue — it goes straight to waiting. It writes two things locally
that nothing reads back: a line of numbers per run (see
[Run data & cost tracking](run-data.md)) and a per-shift log of everything
it narrated (see [The shift log](reference.md#the-shift-log--log)).

**Being interrupted is not failing.** Ctrl+C, a service manager stopping
the unit, a shutdown — all take the running `claude` down with the
supervisor, so no orphaned run keeps editing, pushing or opening PRs
unsupervised. A laptop that sleeps mid-issue costs the run, not the issue:
`-retries` bounds crashes that got *nothing* done, so real work already
saved resets the count instead of spending it. A `gh` call that fails
before the network's back is retried a few times before it's believed.

**Your checkout stays level with origin and is never written to.** polako
fast-forwards `-dir`'s default branch before each issue and after every
merge — it never pulls, so that branch falls a commit behind on every merge
it only watches, and it's what the skill branches from and a review diffs
against. Left stale, a review folds an already-merged PR into what it's
diffing. It's `--ff-only` and nothing else: a commit of its own, work in
the way, or a checkout on another branch, and polako logs it and leaves the
checkout alone.

**A finished issue's worktree and branch get reclaimed, not left to pile
up.** At shift start — catching PRs merged by hand between shifts — and
after each merge it observes, polako runs the same sweep
[`polako tidy`](reference.md#reclaiming-finished-issues-polako-tidy) does:
for every `issue-N` branch it can prove finished — closed, or merged with a
clean worktree and nothing unpushed — it removes the worktree and deletes
the branch. For the issue whose merge the shift just watched, GitHub's
merge event stands in for the ancestor check, so a squash merge is
reclaimed too. A sweep that can't run at all, and the just-merged
worktree specifically failing to reclaim — usually uncommitted work the
merge didn't take — are both named in the shift log rather than swallowed;
neither ends a shift, since a tidy-up must not take a backlog down. A
put-down issue is never finished, so its worktree stays put (see
`-strict-order` below).

`implement-issue` puts that worktree at `.worktrees/issue-N`, inside the
main checkout — gitignored rather than merely untracked, so **`git clean
-xfd` there deletes it and every in-flight issue's uncommitted work along
with it**, worktree admin records included.

## When an issue can't be finished

**One issue that cannot finish does not end the run.** A run that produced
nothing, ran out of retries, had its PR closed unmerged, couldn't rebase
away a conflict, stayed red on CI, or ran past a cap you set — see
[Capping what a shift spends](run-data.md#capping-what-a-shift-spends) —
gets *parked*: `needs-human` goes on, a comment explains why, and the drain
moves on. The label takes it out of the queue; remove it to put the issue
back. The process exits 0 and summarizes what merged, what parked, and why:

```
summary: 3 issues merged, 1 issue parked, $18.40 spent, 6h12m of wall clock
  merged  #14 ($4.90), #15 ($6.12), #17 ($5.11)
  parked  #16 ($2.27) — the run completed without opening a PR
```

Dollars appear only when this shift spent some — a shift that only waited
on an earlier PR prints the line without them.

**An issue that needs no code change is closed, not parked.** Some issues
describe a change that already happened — shipped elsewhere, a duplicate, a
false premise — and a run that verifies that closes the issue instead of
opening a PR. The bar is narrow: only a merged PR or commit this run itself
read, or a human's own reply saying to close it — never the issue's own
text claiming the work is done, exactly the self-serving evidence a run
ducking its own work would produce. Doubt still goes to a park or a
question. The evidence is on the issue thread, in the comment the run
leaves before closing, and it counts as finished in the summary:

```
summary: 2 issues merged, 0 issues parked, 1 issue closed with no change needed, $12.10 spent, 4h02m of wall clock
  merged  #14 ($4.90), #15 ($6.12)
  closed  #18 ($1.10)
```

**A run that ends without a PR isn't automatically a failure.** Four kinds
end that way: one that got stuck and decided nothing, one that finished the
change but expected a later turn `claude -p` never gives, one that ran out
of road mid-task, and one that asked for a tool the allowlist didn't grant
and got no answer. The middle two leave work on disk, so before parking,
polako checks for commits on `issue-N` ahead of the default branch and
uncommitted changes in the worktree. The fourth is read off what the run
said — its final words if the ask was the last thing it did, an earlier
turn otherwise. An ask that was the final word parks immediately rather
than retrying — resuming replays the same session against the same
allowlist and hits the same wall, so only `-add-tools` or a skill fix gets
past it:

```
  parked  #16 ($2.27) — the run stopped to ask for a permission this allowlist
  does not grant. To fix it: find the tool it reached for — named in the
  terminal right after this park, and saved in the shift log named right
  after that — then either grant it with -add-tools (for a Bash command, add
  an entry shaped like `-add-tools "Bash(<command>:*)"`) and remove
  needs-human to retry, or, if the skill should not have reached for that
  tool at all, fix the skill instead
```

When the ask was only an earlier turn, polako resumes first if there's work
to resume into, telling it outright that its turn ends the process. If it
finds nothing, it parks — a machine can't tell what "decided nothing"
meant.

Resumes are capped at **two per issue**, off the same allowance a crash
resume uses, not `-retries` (which counts consecutive *fruitless crashes*
only) — so alternating crashes and turn-endings can't outlive both caps.
The park says which cap stopped it:

```
  parked  #16 ($2.27) — the run completed without opening a PR;
  it has been resumed 2 times after ending a turn without opening a PR and has
  still not opened one, which needs a human; branch issue-16 has no commits and
  its worktree has uncommitted changes in 6 files — the run left work behind,
  so start there rather than from scratch
```

The worktree's path is printed beside the park in the terminal, not here —
this text is posted to the issue thread, and a local path is nobody's
business but yours.

Parking preserves the no-conflict guarantee: only one issue is ever in
flight, and a parked issue isn't in flight.

## The `proposed` and container gates

**A `proposed` issue is one nobody has approved yet, and it's never
worked.** The label gates issues a machine proposed rather than a person:
polako counts them on startup and leaves them alone until you remove it.
Approving is ordinary GitHub triage — `gh issue edit 12 14 19 --remove-label
proposed` approves three at once; rejecting is closing the issue; reworking
is editing its text, read fresh at dispatch time. Exclusion beats
inclusion, so an issue carrying both the `-label` gate label *and*
`proposed` stays out:

```
ignoring 3 proposed issue(s) awaiting curation — remove the proposed label to queue them
```

**An issue with sub-issues is a container, and containers are never
worked** — whatever their labels, which also protects hand-made epics. This
is structural, read off GitHub's sub-issue rollup, not a label. A `gh` too
old to report it treats containers as ordinary work after one warning; the
`proposed` exclusion is label-only and never depends on it.

**A container whose sub-issues have all closed gets closed by the drain
that notices**, with a comment first saying why:

```
  epic    #113: all 6 sub-issues closed — closed it
```

The machine isn't judging whether the work is done — the children did, by
closing — reading "every child closed" as "the epic is finished," a
misread a reopen undoes in one click. The comment posts first and carries
a retry marker, and names how many children closed, not which, to save a
`gh` call. A failed comment is a warning and the close doesn't happen; a
failed close is a warning the next shift retries. Neither parks anything or
is fatal. Detection costs nothing extra: it's sourced from the queue
listing the drain already re-reads every pass, so merging an epic's last
child mid-shift is enough — no separate `gh` call. Scoped like the rest of
the queue: a container outside `-label` is never listed, so it's neither
commented on nor closed.

**A container a human has held is left alone.** `needs-human` or
`proposed` on it means hands off, and the exit summary keeps naming it as
yours to close:

```
  epic    #113: all 6 sub-issues closed — close it when the design is satisfied
```

No new flag needed: `needs-human` already means "hold this" everywhere
else. `polako status` names a held finished container in its `needs you`
line for the same reason.

**A container that just closed can be the last thing naming its plan
document — the drain files a retire issue when that happens.** If the
closed container's body carries a plan footer
(`docs/plans/plan-conventions.md`) and no other open issue names the same
document, one more `proposed` issue gets filed:

    docs: retire docs/plans/foo.md — every issue it proposed is closed

with a footer of its own naming the same document. That's what stops a
second container closing for the same document from filing a duplicate —
the next search finds this issue and files nothing, so at most one retire
issue exists per document, ever. Best-effort like the close's own comment: a
failed search or a failed create is a warning, not a park, and the exit
summary names what was filed, right after the epic's own line:

    epic    #113: all 6 sub-issues closed — closed it
      retire  #114: docs/plans/foo.md — every issue it proposed is closed

A held container never reaches this — it's never auto-closed, so it never
triggers a retire.

**A ready issue with an open `blockedBy` dependency is put down for this
pass**, flagged by the same listing call that flags a container. Nothing is
written anywhere — the next listing that finds the blocker closed hands the
issue back to ready. The log names what it's waiting on:

```
issue #171 blocked by #170 — skipping this pass
```

A blocker outside `-label`'s scope still blocks while open, and two issues
blocking each other are both put down without a hang. This holds when
GitHub's own `blockedBy` state comes back; a `gh` too old for it falls back
to checking the same listing, which misses an out-of-`-label` blocker — the
one gap that leaves open, sharing the sub-issue rollup's one warning rather
than a second. A container, `needs-human`, `proposed` or `awaiting-answer`
classification always wins over a blocker; `awaiting-answer` keeps its own
poll running regardless of an unrelated dependency merging, and
`-strict-order` doesn't fold a held-back issue into the queue the way it
does an awaiting-answer one.

## `plan` and `health`: proposing work, never doing it

`polako plan` and `polako health` fill the same backlog `work` drains —
`plan` from a vision document, `health` from the repository's own shape.
Both create issues behind the `proposed` label above and do nothing else:
no commits, no pushes, no PRs, no edits to threads that already exist. The
whole write surface is `gh issue create` plus a scratch body file the run
deletes — a fully subverted run's blast radius is spam behind a label.

The gate doesn't depend on the model remembering it. A shared label pass
(`labelpass.go`) runs after every `plan` or `health` run — however it
ended: normal, capped at `-max-issues`, crashed, or Ctrl+C'd — and forces
every issue that run created to carry *exactly* the `proposed` label,
stripping anything else and adding it if the run forgot. The cap counts a
`gh issue create` once it actually finishes, not once the run dispatches it,
so a cap of N always leaves N issues for this pass to find, never N-1. A
failure to label is reported loudly, never swallowed.

See [`polako
plan`](reference.md#planning-a-backlog-unattended-polako-plan) and
[`polako
health`](reference.md#auditing-repository-health-unattended-polako-health)
for the flags each takes.

## Questions on the thread

**A run that has to ask something labels the issue `awaiting-answer`**,
and the supervisor keys off the label rather than the thread getting
busier — plenty of comments on an issue aren't answers: CI, a linked-PR
notice, a bot, a passer-by. The label is also the only sign on GitHub that
an issue is waiting on *you*; reply and the next check picks it up. polako
declares the label on startup if the repo lacks it. The run that folds
your answer in removes it — or the park does, if the issue is handed back
first, since a parked issue waits on a decision rather than a reply.

**Ending that wait is the same judgement, made again.** A wait ends on a
comment from a *person* after the question was flagged. GitHub Apps —
Actions, a CI reporter, a stale-bot nudge — are read and skipped, and the
log says so:

```
issue #16 still awaiting a reply (2 new comment(s), none of them from a person)
```

Comments from the account polako authenticates as are *not* skipped, since
on most setups that account is yours. Nothing polako writes can end a wait
anyway: it only posts the question, a park notice, or a closing comment.

**A question doesn't hold up the queue.** A flagged issue is put down and
the next one worked, the way a park advances past an unimplementable
issue. It's picked back up when the reply lands and nothing else is left
to work, or straight away once you remove the label. Only one issue is
ever *in flight* — not that they run in strict numeric order.

One thing does weaken, which is why `-strict-order` exists. A put-down
issue keeps the worktree and branch its first run created, so work resumes
from that original base, not from merges landed while it waited. A textual
clash shows up as a `CONFLICTING` PR and rebases automatically; a semantic
one — a helper renamed by a merge in between — doesn't, and lands as a PR
that passes its own tests and breaks the default branch.

Pass `-strict-order` for the old behaviour: the queue stays strictly
ascending, an awaiting-answer issue blocks everything behind it, and
nothing merges under a waiting branch. A shift that ends with issues still
waiting says so:

```
summary: 3 issues merged, 0 issues parked, 1 issue awaiting an answer, $12.86 spent, 4h02m of wall clock
  merged  #14 ($4.90), #15 ($6.12), #17 ($1.20)
  waiting #16 ($0.64) — reply on the thread and the next shift picks them up
```

One caveat: the wait's baseline lives in memory, so a restarted shift can't
tell whether a reply arrived while it was down. It spends one run per
flagged issue finding out — the skill re-reads the thread and stops again
without re-asking if the answer isn't there yet — and holds a baseline from
then on.

## Keeping an open PR green

**An open PR that goes red is repaired, not just watched.** Each poll
reads the PR's check rollup, reviews, and mergeability. A conflict gets a
rebase; a failing check gets a run that reads the failing job logs, fixes
the cause, runs the suite locally, and pushes. All three are bounded by
`-retries`, and none may merge, open a second PR, or rerun the workflow it
was sent to diagnose.

Two rules stop that becoming a treadmill: nothing is dispatched while a
check is still running, since diagnosing half a suite wastes the run; and a
run that finishes without moving the branch has said all it's going to, so
the issue parks instead of going round again. Only conclusions a branch
change could plausibly fix count as failures — `NEUTRAL` and `SKIPPED` are
green, and a check stopped on a person (`CANCELLED`, or a deployment gate
at `ACTION_REQUIRED` or `WAITING`) reports as `needs a human` instead, so a
real failure beside a gated check is still seen.

**Requesting changes on the PR is an instruction, not a dead end.** Review
it as you would anyone's; asking for changes gets the next poll to
dispatch a run that reads what you wrote — bodies and line comments —
makes the changes, gets the suite passing, and pushes, then waits again: a
re-review is yours to give, and the run may not dismiss, resolve, or merge
it.

Whether a review's answered is read off GitHub, not remembered, so a
restarted shift reaches the same conclusion: a review counts as outstanding
until the branch carries a commit newer than it. A rebase gives every
commit a fresh date, so it reads as an answer — deliberate, since the
review then points at a diff that no longer exists — and a conflicting PR
with an open review comes back to you for a fresh look. The same treadmill
rule applies: a run that finishes without moving the branch parks rather
than re-reading the same comments every poll. This works with no
branch-protection review requirement too, since it reads the reviews
themselves rather than GitHub's summary `reviewDecision`, empty in that
case.

## Which model and effort a run gets

Every run polako dispatches is one `claude -p` process, and two arguments
decide most of what it costs: which model, and how hard it thinks. Both are
resolved once per pickup — the choice is fixed for every run on that issue in
that leg, and a resume keeps the choice of the run it resumes.

Five levels can set them, most specific first:

| Level | Where it lives | Who sets it | May it make a run dearer? |
| --- | --- | --- | --- |
| 1. Ticket | `model:<value>` / `effort:<level>` labels on the issue | A maintainer, at curation | Yes |
| 2. Epic | The same labels on the parent issue, taken by a child without its own | A maintainer | Yes |
| 3. Run reason | `-remediation-model` / `-remediation-effort`, for the rebase, red-check and review runs against an open PR | The operator | Yes |
| 4. Command | `-model` / `-effort` on `work`, `plan`, `health` | The operator | Yes |
| 5. Inherit | Nothing passed — the CLI resolves it from Claude Code settings, the repo's `.claude/settings.json`, the account tier | — | — |

The last column is the rule: **labels and flags may make a run dearer; issue
text never may.** A label needs triage rights and a flag needs the operator's
shell, but a body or comment is anyone's to write on a public repo — so "run
this on the most expensive model at `max`" is not a thing issue text gets to
ask for.

When nobody chooses, the defaults are:

| Run | Model | Effort |
| --- | --- | --- |
| `work`, implementation runs | inherit | inherit |
| `work`, remediation runs | inherit — until a ledger row says otherwise | inherit — until a ledger row says otherwise |
| `plan`, `health` | `opus` | inherit |

`opus` on `plan` and `health` is a tier alias, not a pinned id: those runs
happen once and steer everything downstream, so they take the strongest tier
whatever it is called this year. Everything else inherits, because the CLI's
own default follows the account tier and moves when a generation ships — a
number polako hardcoded would be right for one generation and silently wrong
for the next.

A dispatch logs one line, and only when something other than inherit resolved:

```
issue #42: model sonnet (label), effort medium (epic)
```

The word in parentheses is the level that won — `label`, `epic`, `remediation`,
`flag`.

**Two label families let a maintainer steer one issue's run.** `model:<value>`
— `model:opus`, `model:sonnet`, `model:haiku`, `model:best`, `model:default`,
or `model:<full id>` — and `effort:<level>`, one of `low`, `medium`, `high`,
`xhigh`, `max`. They are read once when the issue is picked up and beat the
`-model` / `-effort` flags: a maintainer who knows one issue is subtle, or
trivial, outranks the shift default. `model:default` is the explicit "use the
account's own default here" — it passes no `--model` even when the flag is set.

**An epic's labels reach its children.** A child issue that carries no label
of a family takes its parent epic's — so `model:sonnet` on the epic runs all
ten children on sonnet, and its record names the source `epic`. The two
families resolve on their own: a child with `effort:high` and no model label
still takes the epic's model. A child overrides with its own label, or with
`model:default` to escape the epic and use the account default. One hop only,
no grandparents.

polako never creates these labels: applying one for the first time creates it
in the same GitHub gesture, and only someone with triage rights can. The prefix
matches case-insensitively. Two labels of one family, or a typo like
`effort:medim`, warns and falls through rather than guessing. A label that
fails to read falls through too — it is a preference, not a gate.

## Account-level stops

**Refused credentials stop the shift immediately.** A resume can't mint a
new token, so retrying would just spend `-retries` × several minutes
reaching the same 401 as a crash. Instead the run ends the shift with the
fix in the last line: check `claude auth status`, then `claude auth login`
— or `claude setup-token` on an unattended host. State lives in GitHub, so
starting `polako work` again once the token works picks up exactly where
it stopped.

**A session limit is waited out, not retried against.** When the account
is over its usage limit, the CLI refuses every run the same way in seconds
and says when the limit resets. Retrying against that wall used to burn a
healthy issue's whole resume allowance and park it; instead the supervisor
reads the reset time from the refusal, sleeps past it, and resumes the
refused session. A refusal whose reset time it can't read — a wording
change, a weekly limit's dated reset — falls back to one attempt per
`-poll`. Neither form spends `-retries` or the resume ceiling: those bound
evidence about the issue, a limit is a fact about the account. Ctrl+C
during the wait is safe — rerunning after the reset picks the issue back
up.

**`-max-session-usage` and `-max-week-usage` are the fence in front of
that wall.** The refusal above stops a run mid-issue once the account is
already over; these act between issues, once the plan's own `/usage`
reports either pool at or over the percentage you set. Off by default,
checked where `-max-session-cost` is, and never a park — nothing's wrong
with the issue. Like the wall, the drain waits the tripped pool's reset
out and carries on rather than ending the shift; a weekly pool's reset can
be days away, and Ctrl+C stays safe throughout. A probe that can't
answer — an old CLI with no `/usage`, no subscription, an unparseable
reply — trips nothing: it logs once and carries on as if neither flag were
set, for that pass.

## Human touchpoints

Deliberately just two, both on GitHub:

1. Answering clarification questions the skill posts on an issue thread.
2. Merging the PR. Nothing merges itself.

Neither is on a clock: an unanswered question or an unmerged PR simply
waits. Both wait out of the queue rather than in it, as a parked issue
does, so neither holds anything else up.

## Questions

**Can I use the skill without the binary?** Yes. Run
`/polako:implement-issue 48` in Claude Code and it takes that one issue to a
PR. The binary exists to run it over a whole backlog while you are asleep.

**Does it work with my language?** Yes — `-dir` points anywhere. The one
thing worth tuning per project is the tool allowlist, so a build command it
needs never stops on a permission prompt.

**How is this different from a hosted coding agent?** Those run on someone
else's machine and keep their own state. polako runs on yours, under the
Claude Code login you already have, and keeps every piece of orchestration
state in GitHub itself. There is no database and no dashboard: kill it
whenever, restart whenever, and read the whole picture off the issue
tracker.
