# How polako works

The [README](../README.md#how-it-works) has the one-page picture. This page is
the long version: what the supervisor does when a run crashes, when an issue
cannot be finished, when a PR goes red, and when it needs you.

**Your checkout is kept level with origin, and never written to.** Before each
issue and after each merge, polako fast-forwards `-dir`'s default branch. It
has to: polako never pulls — you merge on GitHub and it only watches — so that
branch falls a commit behind on every merge, and it is what the skill cuts a new
branch from and what a code review resolves a base against. Left behind, a
review silently diffs the branch against a base that predates the last merge and
folds an already-merged PR into what it reviews. It is `--ff-only` and nothing
else: if your default branch has a commit of its own, or work in the way, or you
are sitting on another branch, polako says so in its log and leaves it
exactly as it is.

**All state lives in GitHub** — issues, comments, PRs, branches. The process
itself is stateless and restart-safe: kill it at any point, rerun it later, and
it re-derives where things stand. If a PR already exists for `issue-N`, it
never re-runs Claude on that issue; it goes straight to waiting. (It writes two
things locally, and nothing reads either back: a line of numbers per run — see
[Run data & cost tracking](run-data.md) — and a per-shift log of everything it
narrated — see [The shift log](reference.md#the-shift-log--log).)

**Being interrupted is not the same as failing, and is not treated as one.**
Ctrl+C, a service manager stopping the unit, a shutdown, a terminal going away:
all of them take the running `claude` down with the supervisor, so no orphaned
run is left editing, pushing and opening PRs behind a restarted shift's back.
A laptop that sleeps mid-issue costs the run, not the issue — `-retries` bounds
crashes that got *nothing* done, so a run cut off after real work resets it
rather than spending it. If the crashed session turns out to be unresumable, the
next attempt starts fresh instead of failing on it three more times. And a `gh`
call that fails because the network has not come back yet is retried a few times
before it is believed, rather than ending a backlog that was clearing fine.

**One issue that cannot be finished does not end the run.** An issue whose run
produced nothing, whose retries ran out, whose PR was closed unmerged, whose
conflicts could not be rebased away, whose CI stayed red, or which ran past a
cap you set on it — see [Capping what a shift
spends](run-data.md#capping-what-a-shift-spends) — is *parked*:
`polako` labels it
`needs-human`, comments on the thread saying what happened, and moves on to the
next issue. The label is what takes it out of the queue, so a later run does not
pick it straight back up — remove the label to put it back in. The process exits
0 and prints a summary of what merged, what parked and why:

```
summary: 3 issues merged, 1 issue parked, $18.40 spent, 6h12m of wall clock
  merged  #14 ($4.90), #15 ($6.12), #17 ($5.11)
  parked  #16 ($2.27) — the run completed without opening a PR
```

Dollars appear only this shift spent some: one that merely waited on a PR
an earlier process opened prints the line without them rather than claiming a
free backlog.

**A run that ends cleanly without a PR is not automatically a failure.** Four
different runs end that way: one that was stuck or confused and decided nothing,
one that finished the change and then ended its turn believing something would
bring it back — under `claude -p` nothing will — one that simply ran out of
road mid-task, and one that asked to be granted a tool the allowlist never
included and got nobody to answer, since nothing was there to. The middle two
have the work sitting on disk, so before it parks, `polako` asks git what is there: commits
on `issue-N` ahead of the default branch, and uncommitted changes in the
worktree. The fourth is read off what the run itself said — its final words if
the ask ended there, any earlier turn otherwise, since a run can ask partway
through and then wrap up on some other sentence. When the ask *was* the final
word it parks straight away rather than being retried — resuming replays the
same session against the same allowlist and hits the identical wall, so only
`-add-tools` or a fix to the skill that reached for the tool gets past it:

```
  parked  #16 ($2.27) — the run stopped to ask for a permission this allowlist
  does not grant — add the missing tool with -add-tools, or fix the skill that
  reached for it, then remove needs-human to retry
```

When the ask was only an earlier turn, `polako` still resumes first if there is
work on disk to resume into; the reason above is what it lands on if that run,
or the lack of one, ends with the issue parked anyway.

If it finds work, it resumes the session to finish it, telling that run outright
that its turn ended the process and there is no later one. If it finds nothing,
it parks, because a machine cannot tell what "decided nothing" meant.

Those resumes are capped at **two per issue**, and they spend the same overall
resume allowance a crash resume does — not `-retries`, which counts consecutive
*fruitless crashes* and nothing else — so an issue that alternates crashing and
ending its turn cannot outlive both caps. When a cap is what stopped one, the
park says which:

```
  parked  #16 ($2.27) — the run completed without opening a PR;
  it has been resumed 2 times after ending a turn without opening a PR and has
  still not opened one, which needs a human; branch issue-16 has no commits and
  its worktree has uncommitted changes in 6 files — the run left work behind,
  so start there rather than from scratch
```

The worktree's path is printed beside the park in the terminal rather than said
here, for the same reason the `--resume` id is: this text is posted to the issue
thread, and a local path is nobody's business but yours.

Parking preserves the no-conflict guarantee: only one issue is ever in flight,
and a parked issue is simply not in flight.

**A `proposed` issue is one nobody has approved yet, and it is never worked.**
The label is the curation gate for issues a machine proposed rather than a
person: `polako` counts them, says so on startup, and leaves them alone until
you take the label off. Approving is ordinary GitHub triage — `gh issue edit 12
14 19 --remove-label proposed` approves three at once; rejecting is closing the
issue; reworking is editing its text, since the text is read at dispatch time.
Exclusion beats inclusion, so an issue carrying both the `-label` gate label
*and* `proposed` stays out. Nothing in this release applies the label — it is
the gate, shipped first, so no version of the binary exists that would work an
uncurated proposal:

```
ignoring 3 proposed issue(s) awaiting curation — remove the proposed label to queue them
```

**An issue with sub-issues is a container, and containers are never worked.**
A parent issue tracks the work rather than being it, so it is dropped from the
queue whatever its labels — which also protects the epics you group things under
by hand. That one is structural rather than labelled: it is read off GitHub's
own sub-issue rollup. A `gh` too old to report that rollup gets one warning and
carries on, with container issues treated as ordinary work; the `proposed`
exclusion is labels alone and never depends on it.

**A container all of whose sub-issues have closed is closed by the drain that
notices**, with a comment on its thread first saying why:

```
  epic    #113: all 6 sub-issues closed — closed it
```

The machine is not judging whether the work is done — the children decided that
by closing — it acts on the near-certain reading that "every child closed"
means "the epic is finished", which a reopen undoes for one click the rare time
it is wrong. The comment is posted first,
always: it is the record of why the close happened, and it carries a marker so
a shift whose close failed retries only the close next time rather than
commenting twice. It names how many children closed, not which ones — listing
them by number would cost an extra `gh` call. A comment that fails to post is a
warning and the close does not happen; a close that fails is a warning and the
next shift retries it. Neither parks anything and neither is fatal.

Sourced from the queue listing the drain already re-reads every pass, so
merging the last child of an epic mid-shift is enough — no extra `gh` call.
Scoped the same way the rest of the queue is: a container outside `-label` is
never in that listing, so it is neither commented on nor closed.

**A container a human has held is left alone.** `needs-human` or `proposed` on
the container means hands off — it is not commented on and not closed, and it
keeps the older exit-summary line naming it as yours to close:

```
  epic    #113: all 6 sub-issues closed — close it when the design is satisfied
```

That is the per-epic opt-out, and it needs no new flag: `needs-human` on the
container already means "hold this" everywhere else. `polako status` names a
held finished container in its `needs you` line for the same reason.

**A ready issue with an open `blockedBy` dependency is put down for this pass**
rather than worked — a listing that already flags a container also flags an
unmerged prerequisite, in the same call. Nothing is written anywhere: the next
listing that finds the blocker closed hands the issue straight back to ready,
the same shift or a later one. The log names what it is waiting on rather than
going quiet about it:

```
issue #171 blocked by #170 — skipping this pass
```

A blocker outside `-label`'s scope still blocks while it is open — the gate is
whether the work landed, not whether it was this shift's business — and two
issues blocking each other are both put down without a hang, the rest of the
backlog unaffected. That out-of-scope guarantee holds when GitHub's own state
comes back on the `blockedBy` connection, which is the ordinary case; a `gh`
old enough to omit it falls back to asking whether the blocker showed up
anywhere in this same listing, and an out-of-`-label` blocker has no row of
its own to be found by there — the one gap the no-second-request rule leaves
open on such a host. A container, `needs-human`, `proposed` or
`awaiting-answer` classification always wins over a blocker: `awaiting-answer`
in particular keeps its own poll for a reply running regardless of whether some
unrelated dependency has merged. `-strict-order` does not fold a held-back
issue into the queue the way it does an awaiting-answer one — running it again
this pass cannot show anything the same listing did not already know. A `gh`
too old to see `blockedBy` shares the sub-issue rollup's one warning rather
than raising a second, and carries on with blocked issues treated as ordinary
work.

**A run that has to ask something labels the issue `awaiting-answer`**, and the
supervisor keys off that label rather than off the thread getting busier. The
distinction matters because plenty of things comment on an issue that are not
answers to anything — CI, a linked-PR notice, a bot, a passer-by — and treating
those as a question left polako waiting on a reply nobody knew was expected.
The label is also the only sign on GitHub that an issue is waiting on *you*;
reply on the thread and the next check picks it up. `polako` declares the
label on startup if the repository does not have it yet, and the run that folds
your answer in is what removes it — or the park, if the issue is handed back
before anyone gets that far, since a parked issue waits on a decision rather
than on a reply.

**Ending that wait is the same judgement, made again.** A wait ends on a comment
written by a *person* after the question was flagged. GitHub Apps — Actions, a
CI reporter, a stale-bot nudge — are read and skipped, and the log says so
rather than going quiet:

```
issue #16 still awaiting a reply (2 new comment(s), none of them from a person)
```

Comments from the account polako itself authenticates as are *not* skipped,
because on most setups that account is yours — skipping them would swallow the
answer the wait exists for. Nothing polako writes can end a wait anyway: it
posts the question the wait starts after, a park notice that takes the issue out
of the queue, and a closing comment.

**A question does not hold up the queue either.** A flagged issue is put down
and the next one is worked, exactly the way a park advances past an
unimplementable issue. It is picked back up when the reply lands and nothing
else is left to work, or straight away once you remove the label by hand. The
guarantee that runs cannot conflict is that only one issue is ever *in flight*,
not that they are worked in strict numeric order — and an issue nobody is
working is not in flight.

One thing does weaken, and it is the reason `-strict-order` exists. A put-down
issue keeps the worktree and branch its first run created, so when work resumes
it resumes from the base that run started on — not from the merges that landed
while it waited. A textual clash with one of those shows up as a `CONFLICTING`
PR and is rebased automatically; a semantic one — a helper renamed by a merge in
between — is not, and lands as a PR that passed its own tests and breaks the
default branch.

Pass `-strict-order` to turn all of that off and get the old behaviour: the
queue stays in strict ascending order, an issue awaiting an answer blocks every
issue behind it until you reply, and nothing merges under a waiting branch. A
shift that ends with issues still waiting says so:

```
summary: 3 issues merged, 0 issues parked, 1 issue awaiting an answer, $12.86 spent, 4h02m of wall clock
  merged  #14 ($4.90), #15 ($6.12), #17 ($1.20)
  waiting #16 ($0.64) — reply on the thread and the next shift picks them up
```

One caveat worth knowing: the baseline a wait compares against lives in memory,
so a restarted shift cannot tell whether a reply arrived while it was down — it
cannot even pick its own question out of the thread, running as it does under
your credentials. It spends one run per already-flagged issue finding out — the
skill re-reads the thread and stops again without re-asking if the answer is not
there yet — and holds a baseline from then on.

**An open PR that goes red is repaired, not just watched.** Each poll reads the
PR's check rollup and its reviews as well as its mergeability. A conflict gets a
rebase; a failing check gets a run that reads the failing job logs, fixes the
cause, runs the suite locally and pushes. All three are bounded by `-retries`,
and none may merge, open a second PR, or rerun the workflow it was sent to
diagnose.

Two rules keep that from turning into a treadmill. Nothing is dispatched while a
check is still running — a suite that has not finished can only add to the list
of failures, and diagnosing half of one wastes the run. And a run that finishes
without moving the branch has said all it is going to: the same logs against the
same commit reach the same place, so the issue parks for a human instead of
going round again. Only conclusions a change to the branch could plausibly fix
count as failures at all — `NEUTRAL` and `SKIPPED` are green, and a check
stopped on a person (`CANCELLED`, or a deployment gate at `ACTION_REQUIRED` or
`WAITING`) is reported as `needs a human` rather than dispatched at. That last
distinction matters twice over: a gated check is not a suite still running, so a
real failure sitting beside one is still seen.

**Requesting changes on the PR is an instruction, not a dead end.** Review the
PR as you would anyone's; if you ask for changes, the next poll dispatches a run
that reads what you wrote — both the review bodies and the comments left on
individual lines of the diff — makes the changes, gets the suite passing, and
pushes. Then it goes back to waiting, because a re-review is yours to give: the
run may not dismiss or resolve the review, and still may not merge.

Whether a review has been answered yet is read off GitHub rather than remembered,
so a shift restarted mid-flight reaches the same conclusion: a review counts as
outstanding until the branch carries a commit newer than it. Two consequences
worth knowing. A rebase — including one the conflict remediation performs — gives
every commit a fresh date and so reads as an answer; that is deliberate, since
the review then points at a diff that no longer exists, but it does mean a
conflicting PR with a review open on it comes back to you for a fresh look. And
the same treadmill rule applies: a run that finishes without moving the branch
has said all it is going to, so the issue parks rather than re-reading the same
comments every poll. If your project sets no branch-protection review
requirement — most do not — this still works, because it reads the reviews
themselves and not just GitHub's summary `reviewDecision`, which is empty in
that case.

**Refused credentials stop the shift immediately.** A resume cannot mint a new
token, so retrying one spends `-retries` × several minutes reaching the
identical 401 and then reports it as a crash. Instead the run ends the shift
with the fix in the last line: check `claude auth status`, then
`claude auth login` — or `claude setup-token`, on an unattended host. State
lives in GitHub, so starting `polako work` again once the token works picks up
exactly where it stopped.

**A session limit is waited out, not retried against.** When the account is
over its usage limit, the CLI refuses every run the same way in seconds and
says when the limit resets. Retrying against that wall is how a healthy issue
used to burn its whole resume allowance and park; instead the supervisor reads
the reset time out of the refusal, sleeps until just past it, and resumes the
refused session. A refusal whose reset time it cannot read — a wording change,
a weekly limit's dated reset — falls back to one attempt per `-poll`. Neither
form of waiting spends `-retries` or the resume ceiling: those bound evidence
about the issue, and a limit is a fact about the account. Ctrl+C during the
wait is safe as ever — state lives in GitHub, and rerunning after the reset
picks the issue back up.

**Human touchpoints are deliberately just two**, both on GitHub:

1. Answering clarification questions the skill posts on an issue thread.
2. Merging the PR. Nothing merges itself.

Neither one is on a clock: an unanswered question or an unmerged PR simply
waits. Both wait out of the queue rather than in it, as a parked issue does, so
none of them holds anything else up.

