# Concurrent runs: one issue in flight, across labels and processes

Scope: a lock in `work` preflight, one check in the drain's pickup, `-label`
renamed to `-labels` on `work`, `status` and `setup` · Behavior change: a
second `polako work` on the same checkout refuses to start; a drain waits on
an open issue-branch PR outside its labels; an issue with an open PR is picked
before a lower-numbered ready one; `-label` and `POLAKO_LABEL` go away, no
alias

Start `polako work -label a`. It opens a PR for #10 and waits for the merge.
Start `polako work -label b` beside it. The second run picks #20 and works it,
from a main that doesn't hold #10's merge. That is two issues in flight, and
the no-conflict guarantee is gone without a word from either run.

This document drafts the fix. Who manages it: mostly polako, a little the
operator.

## What exists today

- **One in flight is a property of one process.** The drain loop (`drain`,
  `drain.go`) is single-threaded, and that is the whole enforcement. Nothing
  looks across processes.
- **The label filter is applied at the list call.** `listOpenIssues`
  (`backlog.go`) passes `--label` to `gh issue list`. A run with `-label b`
  can't see #10 at all — not ready, not parked, not anywhere.
- **Restart safety asks about one branch.** `prForBranch` (`pr.go`) is
  `gh pr list --head issue-N` for the issue just picked (`processIssue`,
  `issue.go`). It never asks "is any issue-branch PR open?".
- **The one repo-wide PR listing belongs to `status`.** `readStatusPRs`
  (`status.go`) lists every open PR, then `branchPRs` cuts it down to the
  label-scoped queue. `work` never calls it, and `status -label b` wouldn't
  show #10's PR either.
- **The hole is wider than labels.** One label, #10's PR open, the drain
  killed. A human approves #5, which was `proposed`. Restart: `pickLowest`
  (`drain.go`) takes #5 and runs it. Same two-in-flight, no second label
  needed.
- **No lock of any kind.** No lockfile, pid file or `flock`. No `in-progress`
  label, no assignee. `metrics.go` expects concurrent supervisors — appends
  are whole lines, drain ids are random — but only so the records survive
  them. Nothing stops them.
- **Two supervisors also share one checkout.** Both run `syncDefaultBranch`
  (`sync.go`) and `tidySweep` (`tidy.go`), which ends in `git worktree prune`,
  against the same `-dir`. That races whatever the labels are.
- **Other designs lean on one in flight.** The evidence ref's push recipe
  (`docs/plans/visual-evidence.md`, `skills/implement-issue/SKILL.md`) treats
  a non-fast-forward as a concurrent pusher because there shouldn't be one.
- **`-label` takes one value.** `flag.StringVar` in `flags.go`; a repeated
  flag keeps the last. So the only way to work two labels today is two runs —
  which is how an operator lands here.

## The answer in one paragraph

Two layers, because each covers what the other can't. `work` takes an OS lock
on the checkout at preflight and refuses to start when another `work` holds
it — that covers a live run at any phase, PR or no PR yet. At each pickup the
drain lists the repo's open PRs once, label or no label: an open PR on an
issue branch is the issue in flight, so the drain supervises it if it's in the
queue and waits on it if it isn't. Then the motive goes away: `-labels
hotfix,bug,ready` lets one run work several labels, in that order. What's
left for the operator is two machines starting in the same minute, before
either has a PR. That is documented, not solved.

## What this does to the invariants

Argued here; the text lands in CLAUDE.md with ticket 2.

- **One issue in flight gets enforcement, not a new meaning.** The bullet
  gains how it's held: the lock for one checkout, the pickup check for
  everything GitHub can see. And what isn't held: two machines inside the
  same pre-PR window.
- **The lock is not state.** The kernel drops it when the process dies, so
  there is no stale lock to clear. The file's content is never read. Kill the
  run anywhere, rerun it, and it still re-derives from GitHub alone. Delete
  the file mid-drain and the drain carries on. It is not a third write-only
  artifact either: nothing is written to it.
- **The pickup check is GitHub-derived.** One `gh pr list`, nothing
  remembered between passes.
- **Parking still preserves the guarantee.** A PR whose issue is
  `needs-human`, `proposed` or `awaiting-answer` doesn't hold the drain. One
  unfinishable issue parks; it never ends the session.
- **The label gate holds.** The drain waits on a PR outside its labels. It
  never remediates it: that would be a run on an issue the operator didn't
  opt in.
- **Issue text is data.** A fork's PR from a branch named `issue-999` is
  ignored. Otherwise a stranger stalls every drain on a public repo with one
  PR.
- **Exclusion beats inclusion**, under any number of labels. Priority order
  comes from the operator's flag, never from an issue.
- **No override.** There is no `-allow-concurrent`. If labels ever split
  truly separate code, that is its own argument.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. Shift lock: one `work` per checkout

**Problem.** Two `polako work` runs against one `-dir` each believe they are
alone. They put two issues in flight and race on the shared checkout.

**Shape.** An exclusive OS lock, taken in `preflight` (`main.go`), held until
the process exits.

- The file is `<git-common-dir>/polako.lock`, from `git rev-parse
  --git-common-dir`. Common dir, so a `-dir` that names a linked worktree
  still collides with the main checkout. It sits under `.git`, so it needs no
  `.gitignore` line.
- `cmd/polako/lock_notwindows.go`: `syscall.Flock(fd, LOCK_EX|LOCK_NB)`.
  `cmd/polako/lock_windows.go`: `syscall.CreateFile` with share mode 0. Same
  split as `ui_notwindows.go` / `ui_windows.go`. Stdlib only.
- Held means fatal, and the error says what to do: another `polako work` is
  running against this checkout, one issue in flight at a time, stop it or
  let it finish. Fatal fits — nothing this shift does can succeed.
- A lock that can't be taken for another reason (read-only `.git`) is a
  logged warning and the shift carries on. The PR check in ticket 2 still
  stands behind it.
- `-dry-run` takes no lock. It runs nothing.
- The handle lives on `config` or in `run`, closed on the way out. No
  explicit unlock on signal paths; process exit does it.
- Docs: a short "one run per checkout" section in `docs/behaviour.md`.

**Done when.** A test takes the lock twice from one process on separate
handles and the second fails with the message above, on all three CI
platforms. A `-dry-run` started while the lock is held still prints its plan.

Estimate: S

### 2. Pickup check: an open issue-branch PR goes first

**Problem.** The drain picks the lowest ready issue without asking whether
another issue's PR is open. A different label, or a lower number approved
late, starts a second issue.

**Shape.** One repo-wide PR read per pass in `drain` (`drain.go`), between
`openIssues` and `pickLowest`.

- The read is `readStatusPRs`'s list call plus one field: `gh pr list --state
  open --limit 200 --json number,headRefName,url,isCrossRepository`. Split
  the call out of `readStatusPRs` so both use it. Map heads to issues with
  `issueForBranch` (`status.go`), so `-branch-prefix` keeps working. Do not
  reuse `branchPRs` — it drops everything outside the label-scoped queue,
  which is the bug.
- `isCrossRepository: true` is skipped.
- For each PR left, by its issue N:
  - N is in `ready`: pick N now. Restart safety in `processIssue` sends it
    straight to `supervisePR`.
  - N is parked, proposed, awaiting an answer, held back or a container:
    skip. Somebody set it aside on purpose.
  - N isn't in this queue: `gh issue view N --json state,labels`. Closed, or
    carrying one of the three hold labels: skip. Otherwise wait — log once
    per PR, `sleep(ctx, cfg.poll)`, `continue`, so the next pass re-derives
    everything.
- The wait message names the PR and the issue, says this run won't fix it
  up, and says who will: `polako work` with that issue's label, or a human.
- After the foreign PR merges, the next pickup's `syncDefaultBranch` brings
  main forward. Nothing new needed.
- `-once` waits too. `-dry-run` (`dryrun.go`) reports the same verdict: "would
  wait on PR #55 (issue #10)".
- `status` gets the fork filter in the same change, since it shares the call.
- CLAUDE.md: the *One issue in flight* bullet gains the enforcement text from
  the section above. `docs/behaviour.md`: the pick order changes — open PR
  first, then lowest ready. `docs/reference.md`: the status PR table's
  "normally one" stays true and now says why.
- Tests: the fake `gh` already answers `pr list` without `--head` by walking
  every branch (`drain_test.go`). `fakePR` gains a cross-repository field.
  Cases: foreign PR then `MergeOnRead` — no claude run until it merges; fork
  PR — ignored; parked issue's PR — ignored; own ready issue with a PR beats
  a lower ready number, `runs: 0` for it.

**Done when.** A drain with `-label b`, a ready #20, and an open PR on
`issue-10` (label a) dispatches nothing until the fake PR merges, then works
#20. The same drain with the PR marked cross-repository works #20 at once.

Depends on nothing; lands before 3 because both touch `drain.go`.
Estimate: M

### 3. `-labels`: several labels, in priority order

**Problem.** One label per run is why an operator starts a second run. After
tickets 1 and 2 that second run refuses or waits, so there has to be a right
way to work several labels.

**Shape.** Rename, then widen. No alias: there are no users to break, and an
alias is interface to document for good.

- `-label` becomes `-labels` on `work` (`flags.go`), `status` (`status.go`)
  and `setup` (`setup.go`). `POLAKO_LABEL` becomes `POLAKO_LABELS` through
  the same `applyEnvDefaults`. `cfg.label string` becomes `cfg.labels
  []string`.
- The value is comma-separated: `-labels hotfix,bug,ready`. Trim each, drop
  empties, drop repeats. `gh` splits `--label` on commas itself, so a label
  with a comma was never usable here.
- Order is priority. The pick is the lowest-numbered ready issue under the
  first label that has one. An issue carrying two of the labels ranks under
  the earlier. All `hotfix` work drains before any `bug`.
- `listOpenIssues` (`backlog.go`) runs one `gh issue list --label X` per
  label and merges by number, keeping the best rank. Classification in
  `selectableIssues` is per issue and doesn't change. `pickLowest`
  (`drain.go`) takes the rank. `-strict-order` folds `blocked` into `ready`
  as before; rank applies to both.
- Each label goes through `labelExists` and `labelGate` (`gate.go`,
  `labels.go`) at preflight. `queueGate` asks "any label set?".
- `setup -labels a,b -apply` creates each (`setup_apply.go`); the suggested
  command prints `-labels` (`setup.go`).
- `status`: the header reads "issues labelled a, b"; the `-json` scope's
  `Label` becomes `Labels []string`. The startup recap (`preflightPairs`,
  `main.go`) shows the order: `queue: labels hotfix > bug > ready`.
- One label behaves exactly as `-label` did, under the new name.
- Docs sweep: `docs/reference.md`, `docs/security.md`, `docs/install.md`,
  `docs/hardening.md`, `docs/setup.md`, `docs/behaviour.md`,
  `docs/experiments.md`, `docs/demo.tape`, README, `scripts/smoke.sh`,
  `scripts/smoke.ps1`. The flag-documentation test catches a miss. No
  `SKILL.md` names the flag, so no skill changes and no eval runs.

**Done when.** A drain with `-labels b,a`, ready #5 (a) and ready #9 (b),
works #9 first. An issue carrying `a` and `proposed` stays out. `-labels
a,typo` fails preflight naming `typo`. `grep -rn -- '-label\b'` over the repo
finds only `gh` flags.

Depends on 2. Estimate: M

## Considered and not proposed

**An `in-progress` label as the in-flight marker.** It would cover the one
gap left: two machines, same minute. But a `kill -9` leaves it behind, and a
stale marker stops every later run until a human clears it. A third
orchestration label and a new write, for a case nobody has hit. Worth another
look if it happens.

**`-allow-concurrent`.** An operator's word that the labels touch separate
code. It needs a checkout per run, breaks the evidence ref's one-pusher
assumption, and turns the first invariant into a default. Not proposed.

**Adopting the foreign PR.** The second run could supervise and remediate it.
That is a claude run on an issue outside the operator's `-labels`, and if the
first run is alive, two remediations on one branch. Wait and say so instead.

**A pid file.** It goes stale, and checking it is reading state back. The OS
lock has neither problem.

**One search call instead of a list per label.** `gh issue list --search
'label:a,b'` is one round trip. It goes through the search index, which lags
and has its own rate limit. A queue reader can't be behind. Labels per run
are few.

**Keeping `-label` as an alias.** Two spellings to document and test, for
nobody.

**Docs only.** Tell operators not to run two. The failure is silent, shows up
as a merge conflict days later, and this tool runs unattended. Its output is
often the only diagnostic, so the refusal has to come from the binary.

## Open questions

Facts to check before the ticket that needs them:

1. Does the oldest `gh` polako supports know `isCrossRepository` on `pr
   list`? If not, reuse the `unknownJSONField` fallback (`backlog.go`) and
   decide what the fallback does about forks. (ticket 2)
2. Does a share-mode-0 `CreateFile` refuse a second open from the same
   process on the Windows CI runner, as the test needs? (ticket 1)
3. Should `tidy -apply`, `setup -apply` and `update` respect the lock?
   `update` "runs only between shifts" could be enforced with it. Left out of
   ticket 1 on purpose. (after ticket 1)

### Answers

One checked while drafting, 2026-09-21:

- Does any shipped `SKILL.md` name polako's `-label` flag? No. Every hit under
  `skills/` and `evals/` is `gh`'s own `--label`, `--add-label` or
  `--remove-label`. Ticket 3 changes no skill and owes no eval runs.
  `scripts/smoke.sh` does pass `-label` and is in the sweep. **Unchecked:**
  `evals/*/case.yaml` prompts were grepped, not read.

## Work items

- [ ] Shift lock on the checkout, refusal message, behaviour docs (ticket 1)
- [ ] Pickup check, fork filter, CLAUDE.md enforcement text (ticket 2)
- [ ] `-labels` rename and priority order, docs sweep (ticket 3)
- [ ] Open questions 1 and 2 have answers recorded here
