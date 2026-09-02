# Where polako is going

Polako works a GitHub issue backlog to zero, one issue at a time, and never
merges anything itself. Humans decide what to build and whether to merge it.
Everything between those two decisions is the machine's job.

## What stays true

- **Two human gates, nothing else.** Approving a proposal (lifting `proposed`)
  and merging a PR. Every feature either widens what the machine does between
  the gates or makes the gates cheaper to stand at. Nothing removes one.
- **State lives in GitHub.** Issues, labels, comments, PRs, branches. Kill it
  anywhere, rerun it later, it re-derives where it was. Local files are
  write-only telemetry or nothing.
- **Correctness over speed.** One issue in flight, branched from the last
  merge. Slow is fine. Wrong costs a reviewer's time, which is the one thing
  we are here to save.
- **It runs in other people's repos.** Every convention polako relies on has
  to travel: a skill carries its own rules, a label is spelled the same
  everywhere, a footer is a contract.

## Where it goes next

- **The backlog fills itself, behind the gate.** `plan` from a document,
  `health` from the code. The next step is that a plan's own progress is
  visible from GitHub alone, so nobody has to maintain a status line by hand.
- **The loop closes.** Every run records what it cost and what it produced.
  Those records should drive the next change to the skills, and the next
  batch should prove the change paid for itself.
- **Cheaper shifts.** The same merged PR for fewer tokens, without touching
  the review gate.
- **Conventions that outlive this repo.** Where a vision lives, where plans
  live, how a plan says it is done. Simple enough that a repo adopts them by
  copying one directory.

## How a direction becomes work

One document per initiative under [`docs/plans/`](plans/). `polako plan
-vision docs/plans/<doc>.md` turns it into proposals. Lift the gate on what
survives review, and `polako work` does the rest. A plan is done when the
issues it proposed are closed, and `polako status` says so. This document
sets direction and says what will not change. It does not have a status.
