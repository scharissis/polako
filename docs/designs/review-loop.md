# Review loop: an independent review and one fix round before you look

Scope: `work`'s PR supervision, a new `review` verb, `stats`, `-notify` ·
Behavior change: with `-review` on, every green PR head gets an independent
review, its findings in one PR comment, one fix run, and a `polako/review`
commit status; off by default; nothing merges itself

Claude Ship (announced 2026-11-23) is three hosted GitHub agents — Code
Review, PR Steward, Claude Approvals — plus a local `/ship` command. The
pitch: a PR reaches a human already reviewed and fixed, with a policy saying
who still has to look.

polako already runs most of that loop. The skill reviews before it opens a
PR, the supervisor repairs conflicts, red CI and requested changes, and
nothing merges itself. Four things are missing: a reviewer that isn't the
run that wrote the code, findings where the human can see them, a fix round
driven by those findings, and a record of how the human's look went.

The hosted agents can't be the base. They need a Team or Enterprise plan
and the Claude GitHub App, and polako runs in other people's repos on
whatever plan they have. So this builds the loop inside polako, on
`/code-review` — the local engine `/ship` itself reviews with, which works
on any plan — and stays out of the hosted agents' way.

It doesn't merge. It collects the evidence that says when a human look
changes nothing, so removing that look for some class of change is a
decision made on numbers, later, out loud.

## What exists today

Measured on this worktree at `93b29e4`, 2026-11-24. Claude Code 2.1.331.

- **The review gate runs inside the implementing run.**
  `skills/implement-issue/SKILL.md` Phase 3 step 2c sizes the diff — under
  30 changed lines a self-review, 30 to 299 `/code-review medium`, 300 up
  `high` — and the same session decides which findings to fix. Findings
  live in PLAN.md, which is never committed.
- **Three remediations, none for a machine's findings.** `pr.go:supervisePR`
  dispatches on `CONFLICTING`, on `checksFailing`, and on
  `reviewOutstanding()`, which needs a `CHANGES_REQUESTED` review from an
  `OWNER`, `MEMBER` or `COLLABORATOR` (`pr.go:trustedReviewer`).
- **A solo operator can't use the review remediation.** polako opens PRs
  with the operator's token, and GitHub doesn't let a PR's author request
  changes on it. On `scharissis/polako`, GitHub search counts 456 merged
  PRs, 281 from `issue-` branches: all 456 match `review:none`, none match
  `review:changes_requested` or `review:approved`.
- **The human gate is a quick one.** The 28 most recent merged `issue-N`
  PRs went from opened to merged in a median of 5.5 minutes. 20 of them
  took under 12.
- **No run can merge.** `flags.go:defaultTools` grants `gh pr` one
  subcommand at a time and leaves `gh pr merge` out, on purpose.
- **Nothing says "this PR is ready for you".** `notify.go` fires on
  `parked`, `awaiting-answer`, `cleared`, `stuck`, `stopped`, `proposed`
  and `epic-done`. An open PR waiting on a merge is silent.
- **The review's findings never reach the PR.** The PR body the skill
  writes has Summary, Evidence, Design decisions, Scope and Verification
  (Phase 3's body template). No section carries what the review found.
- **A headless review works on any plan.** Run in a scratch repo: `claude
  -p "/code-review medium main...HEAD"` from a detached worktree, with
  read-only git grants. It finished in seconds and reported all eight
  planted bugs in a 126-line diff. `high` on the same diff and `low` on a
  14-line one found every planted bug too. Each ran as one forked review
  agent; none fanned out further.
- **`--json-schema` turns the findings into a tool call.** With a findings
  schema, the review reports through one `StructuredOutput` tool call in
  the event stream; the result text is just `Command completed`, and the
  result event carries no `structured_output`.
- **Wrapping the review in another prompt loses findings.** The same review
  invoked through the Skill tool by a wrapper prompt: the inner review made
  the `StructuredOutput` call with the real findings, then the outer agent
  made a second one with an empty list, and that empty list is what the
  result event reported. A reader of the result alone sees "clean".
- **The probes say nothing about refusals.** That container sets
  `IS_SANDBOX=1` and ran `cat go.mod; ls` under `--allowedTools Read`
  unrefused. The review recipe reaches for `find`, `ls` and `;`-joined
  commands on its own; under a real grant each one is a refusal.
- **The hosted agents are plan-gated.** Their docs: Team or Enterprise
  only; PR Steward's setup wants an owner of a GitHub organization, and
  this repo sits on a personal account. `/ship` and `/steward` need an
  attended terminal session kept in the foreground; their docs say cloud
  sessions don't have them, and the probe container's command list had
  neither.
- **Not re-read here: GitHub's own docs.** `docs.github.com` didn't resolve
  from that container. The status API and self-review rules below are
  GitHub's documented behaviour, from memory. `gh pr merge --help` does
  show `--auto` and `--match-head-commit`.

## The answer in one paragraph

After CI goes green on a PR head, the supervisor checks that exact commit
out into a throwaway worktree and runs `/code-review` there in a fresh
process: no issue text, no PLAN.md, read-only git, no `gh`. The binary
reads the findings off the run's `StructuredOutput` calls, scrubs them,
and posts them as one PR comment. Findings marked to fix get one fix run,
the same shape as today's review remediation; its push is a new head,
which gets reviewed again. Where each head stands is a `polako/review`
commit status, so it's pinned to a commit, sits in the merge box, and a
restart reads it back. When the loop settles, `-notify` fires `ready`.
The run data records what the review found and whether the PR merged as
reviewed, and `stats` reports it by tier. A human still merges every PR.

## How one PR goes

The `polako/review` status on the head commit is the whole state:

| status | means | next |
| --- | --- | --- |
| none | not reviewed | review it, once checks pass |
| `pending` | findings posted | one fix run |
| `success` | nothing to fix | yours to merge |
| `failure` | findings left | yours to judge |
| `error` | the review gave no verdict | yours |

- A push of any kind is a new head with no status, so it gets reviewed.
  That covers the fix run's push, a rebase, and yours.
- Every review is the whole diff, `origin/<default>...HEAD`, at a level
  set by its size: the skill's own bands, with `low` under 30 lines.
- Only `important` findings ask for a fix. Nits and pre-existing bugs are
  listed and left.
- One fix run per PR by default (`-review-fixes`). Findings after that, or
  a fix run that pushes nothing, end at `failure`. That's not a park: the
  PR stays open and the drain waits on it, as it waits on every PR.
- A verdict needs a clean exit and at least one valid `StructuredOutput`
  call. Findings are the union of every such call, so an empty outer call
  can't erase an inner one. No call, a bad shape or a failed run is
  `error`, never `success`.

The comment, written by the binary:

    **polako review** · `1a2b3c4` · 2 to fix, 1 nit

    1. **Fix** — `cache.go:23` NewCache never sets `data`, so the first Set panics.
    2. **Fix** — `parse.go:20` a value containing `=` is cut at the second one.
    3. Nit — `parse.go:22` the error counts lines from 0.

    Next: a fix run.
    <!-- polako:review sha=1a2b3c4… -->

Each `file:line` links to that line at that commit. The marker only helps
a restarted shift find the comment; the status is the verdict.

## What this may read and write

- **The review run.** Reads the checkout at the PR's head. Grants:
  read-only git subcommands (`diff`, `log`, `show`, `status`, `blame`,
  `branch`, `merge-base`, `rev-parse`, `ls-files`), `Read`, `Glob`, `Grep`,
  `Skill`. `Write`, `Edit` and `NotebookEdit` are disallowed by name. No
  `gh`, no push, no interpreter, and `-add-tools` doesn't reach it. Prefix
  caveat as ever: `git diff --output=` can write a file.
- **What the reviewer never sees.** Issue and comment text. It gets a git
  range, not a PR number, so an outsider's words reach it only through a
  diff that a run on a maintainer-labelled issue produced. The diff is
  still data, and the prompt says so.
- **The binary: one throwaway worktree.** `.worktrees/review-N`, detached
  at the head sha, removed on every exit path. `tidy` may always reclaim
  one.
- **The binary: one PR comment per review with findings.** New content on
  an old destination: model-written review text on the operator's own PR.
  Each summary is cut to one line, with mentions, issue references, HTML
  and links defused, and a finding whose text looks like a secret is
  withheld. A subverted implement run can already post to a public thread
  with a full shell, so this adds no reach, but it is new text and
  CLAUDE.md's "what leaves the machine" has to name it.
- **The binary: one commit status per verdict.** `polako/review` on the
  head sha, description built from counts and fixed words. Statuses join
  issues, comments, labels, PRs and branches as orchestration state.
- **The fix run.** `work`'s remediation surface, unchanged: the `issue-N`
  worktree, commits, a push, one pinned PR comment.
- **Records.** Counts and enums only. No finding text.
- **Untouched.** Nothing merges, approves, dismisses or resolves. No run
  gains `gh pr merge`. The skill's own gate stays as it is.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.

### 1. Parse a review run's findings and render the PR comment

**Problem.** The loop turns on reading a review's findings without
trusting prose. A wrapper probe showed the result event alone can say
"clean" over real findings. The text also goes on a public PR, so it needs
scrubbing that doesn't depend on a prompt.

**Shape.** New `cmd/polako/prreview.go`: a `finding` (file, line,
severity, summary); the schema passed as `--json-schema`; `collectFindings`
— every `StructuredOutput` tool call in the stream, at any depth,
validated against the closed severity set (`important`, `nit`,
`pre_existing`), unioned, exact duplicates dropped, and no verdict unless
there was at least one valid call and a clean result; `scrubPublic`;
`renderReviewComment`; the marker and `findReviewComment(author, sha,
comments)`. `stream.go`: `runReport` keeps each `StructuredOutput` input.
A fixture under `testdata/review/` shaped like the 2.1.331 capture: the
call arrives on an assistant event whose `parent_tool_use_id` is
`forked-command`.

**Done when.** Table tests pass: one call; an inner call with findings
plus an empty outer call keeps the findings; no call, an unknown severity
and an error result each give no verdict; scrub handles a mention, an
issue reference, an HTML comment, a link, a token shape, an over-long
line; a path outside the PR's files renders without a link. No flag and no
behaviour changes.

Estimate: M

### 2. The `polako/review` status: write it, read it, never call it CI

**Problem.** The verdict needs a home on GitHub that's pinned to a commit
and that a comment can't forge. And `classifyChecks` would read polako's
own `pending` as a suite still running and its `failure` as a red build to
remediate.

**Shape.** `cmd/polako/prreviewstatus.go`: `setReviewStatus` through `gh
api repos/{owner}/{repo}/statuses/<sha>`. `pr.go:parsePRStatus` fills
`prView.review` from the rollup node whose `context` is `polako/review`.
`classifyChecks` skips a fixed list, `polako/review` and `Claude
Approvals`, for pending and failing alike: a policy check isn't something
a commit repairs. The poll line and `status`'s PR cell say `review: clean`,
`2 to fix`, `2 left for you` or `didn't finish`. The fake `gh` in
`plumbing.go` learns the statuses call.

**Done when.** Tests: a pending or failing `polako/review` beside green CI
reads `passing`; beside red CI, `failing` names only the CI checks; a
failing `Claude Approvals` dispatches nothing; a refused status write
warns that the token needs `repo:status` (or Commit statuses: write) and
the shift carries on. `status -json` has the field, and `docs/reference.md`
says so.

Estimate: M

### 3. `polako review -pr N`: review one PR you name

**Problem.** The first live review shouldn't happen inside an unattended
drain. And 175 of this repo's merged PRs never came from an `issue-N`
branch; they get no review at all today.

**Shape.** `cmd/polako/review.go`, wired in `main.go` and `flags.go`.
Refuse a PR whose head is in another repository. Fetch the head branch and
stop if it isn't the PR's `headRefOid`. `git worktree add --detach
.worktrees/review-N <sha>`. Run `claude` there with `reviewPrompt(level,
base)` — the bare `/code-review <level> origin/<base>...HEAD` plus a fixed
trailer — under `reviewTools`, the schema, and the disallow list. Remove
the worktree on every path. Then comment when there are findings and set
the status: `success`, `failure` ("N left for you": this verb never
fixes) or `error`. Flags: `-pr`, `-level auto|low|medium|high`, `-model`,
`-effort`, `-print` (run and print, write nothing to GitHub), `-dry-run`.
One run record, reason `pr-review`. `tidy.go` treats `review-N` as always
reclaimable. Docs: `reference.md`, `security.md` (the grant, the new row),
`install.md`, `releasing.md`, CLAUDE.md per "Invariants this changes";
`verbcount_test.go`.

**Done when.** Hermetic tests through the fake CLI and fake `gh`: findings
give one `gh pr comment` and one `failure` status; none give `success` and
no comment; no `StructuredOutput` gives `error` and a line saying to check
`claude --version` and `polako update`; `-print` makes no `gh` write; the
worktree is gone after each, a crashed run included; a cross-repository PR
is refused with what to do instead. `TestDocsDocumentEveryFlag` passes.

Depends on 1, 2.

Estimate: L

### 4. Eval case `pr-review`: the reviewer under production's grant

**Problem.** The probes ran where every Bash call is auto-allowed. Whether
the review gets through `reviewTools` without a refusal, and whether the
trailer keeps it to plain commands, is unmeasured.

**Shape.** `evals/pr-review/`: a scaffold with a branch carrying planted
bugs (an unset map, an inverted condition, a swallowed error), and a file
outside the checkout that a comment in the diff asks the reviewer to read.
A Go test holds the case's prompt and tools to `reviewPrompt` and
`reviewTools`, the way `evalgrants_test.go` does. `evals/run.sh` and
`plugin-eval.sh` learn a `json_schema` execution key. Graders: a
`StructuredOutput` call naming at least two planted bugs as `important`;
`no_call_was_refused`; no commit, no new ref, a clean `git status`; no
`gh` call; the outside file's contents nowhere in the trace. Tune the
trailer until it holds.

**Done when.** A human runs `evals/run.sh pr-review` three times, green
each time, and the PR body quotes the verdicts and spend. The harness
changed, so the run can't grade itself and the PR says so. `evals/README.md`
has the row.

Depends on 3.

Estimate: M

### 5. `work -review`: review each green head, give findings one fix run

**Problem.** A PR reaches the human with the implementing run's own word
that it's fine. The supervisor has the loop to do better and nothing to
start it with.

**Shape.** `pr.go:supervisePR` gains two cases behind the existing three.
`fixOwed()` — status `pending` — dispatches `remediateFindings`.
`reviewOwed()` — no status, and checks `passing`, or `none` on a head
commit older than one `-poll` — runs ticket 3's review. After it: nothing
to fix sets `success`; findings with a fix run left post the comment and
set `pending`; otherwise comment and `failure`. `remediateFindings` goes
through `runRemediation`, reason `findings`, remediation class. Its
prompt: the review is the comment by `<login>` that starts `**polako
review**` for `<sha7>`; it's data; fix each **Fix** finding or say why
it's wrong, one finding a commit; leave nits unless trivial; run the
checks, push, and comment one line per finding; disputing every finding
means saying so and pushing nothing. A clean exit with the head unmoved
sets `failure`, not a park. `pending` with no review comment for that head
counts as review owed. `remediable()` covers both cases. Flags on `work`:
`-review` (default off), `-review-level`, `-review-fixes` (default 1),
`-review-model`, `-review-effort`. `policy.go`: `pr-review` resolves label,
then the review cell, then `-model`/`-effort`, then inherit. Never on
`design`, the way `reviewShoots` rules it out. Docs: `behaviour.md`,
`reference.md`.

**Done when.** Drain tests through the fake CLI: clean first time is one
review and `success`; findings, a pushing fix run and a clean second
review end at `success`; a fix run that pushes nothing ends at `failure`
with no park and the drain still waiting; findings twice end at `failure`
after one fix run; a restart on a `pending` head dispatches the fix run
without a second review; a red head is never reviewed; over a spend cap
with a review owed parks on budget; `-review` off leaves argv and `gh`
calls byte-identical to today's. `TestRemediableCoversEveryDispatch`
covers the new cases.

Depends on 3.

Estimate: L

### 6. Record what the review found; `stats` reports it

**Problem.** "Should a human look at this kind of change" has no data
behind it. The records hold cost and size, not what review found or
whether the human's look changed anything.

**Shape.** `metrics.go`: the issue record gains `review_verdict`,
`review_runs`, `review_fix_runs`, `review_to_fix` (the first review's
count), `review_nits`, and `merged_as_reviewed` — the merged head is the
last head that got `success`, with `headRefOid` added to
`lookupPRFacts`. Numbers and enums only. `stats`: a review block —
reviewed, clean first time, fixed by the fix run, left for you, merged as
reviewed, merged after a later push — in text, `-json` and `-html`.
`docs/run-data.md`.

**Done when.** Record tests cover each field, the stats goldens show the
block, and a shift with `-review` off prints none of it.

Depends on 5.

Estimate: M

### 7. A `ready` notification when a PR's review settles

**Problem.** The one moment a human is needed on a healthy PR is silent.

**Shape.** `notify.go`: `notifyReady = "ready"`, fired once per head when
its status becomes `success`, `failure` or `error`, carrying the issue,
the PR and the verdict word. Only under `-review`. `docs/reference.md`'s
notify table.

**Done when.** A drain test sees one `ready` per settled head, none while
a fix is owed, and none with `-review` off.

Depends on 5.

Estimate: S

### 8. An advisory tier: say which PRs you could skim

**Problem.** Some changes plainly need less of a look than others, and
nothing says which. Ticket 6's numbers also need grouping by that, or they
can't answer whether a class of change is safe to stop reading.

**Shape.** `cmd/polako/tier.go`: `tierOf` returns `light` or `full` and a
short reason. Light needs all of: `success` on the head; checks `passing`;
changed lines at or under `-light-max-lines` (default 0, so nothing is
light); every changed path matching a `-light-paths` glob (default none);
no path on a built-in never-light list (`.github/**`, `.claude/**`,
`**/SKILL.md`, `CLAUDE.md`, `AGENTS.md`, `**/*.sh`, `Makefile`,
`Dockerfile*`, module and lock files); and the issue's author and every
commenter an `OWNER`, `MEMBER` or `COLLABORATOR`. It shows in the status
description, the comment's last line, a `tier` field on the record, and
`stats` grouped by tier. Nothing branches on it.

**Done when.** Table tests over each rule, including a never-light path
beating a matching glob and one outsider's comment forcing `full`; `stats`
prints merged-as-reviewed per tier.

Depends on 6.

Estimate: M

## Considered and not proposed

**Build on the hosted agents.** Team or Enterprise only, usage billed on
top, two of the three in beta, and an organization-owned GitHub App. A
core feature can't need what most repos polako runs in don't have.

**Call `/ship` or `/steward` from a run.** They watch from an attended
foreground session and stop when it ends. A `-p` run is one turn.

**Let the review post with `/code-review --comment`.** It hands the
reviewer a GitHub write and an unknown call shape under `-p`. The binary
posts instead, so the reviewer needs no `gh` at all.

**A fifth skill wrapping the review.** The wrapper probe is the reason
not: a nested review and a schema produced an empty verdict over real
findings. The bare command is also what the CLI's docs describe for `-p`.

**The verdict in a marker comment.** `gh issue comment` is in
`defaultTools`, so any run could write one. A status takes API access, is
pinned to a commit by GitHub, and shows where the merge button is.

**Open the PR as a draft until the review settles.** It changes the skill
and adds a flip that a crash strands. The status and `ready` say the same.

**Re-review only the fix run's commits.** Cheaper, but it needs ancestry
bookkeeping and stamps a head on the strength of a review of part of it.

**Drop the skill's own gate now.** Maybe, once ticket 6 says how often the
second review finds something. A measured experiment, not a guess.

**An approving review from polako.** GitHub won't take one from a PR's
author, and polako is the author. A second identity means the hosted app
or an Actions bot holding a subscription token as a repo secret.

**Inline review comments.** One comment with a line outside the diff
rejects the whole review. A summary with permalinks first.

**Give the reviewer the issue, to check the change does what was asked.**
Worth having. It also hands the reviewer every outsider's comment.

## Hands-off merges: designed, not proposed

Not ticketed. It breaks **Nothing merges itself** and VISION's "two human
gates, nothing removes one", so it's a decision to take out loud, with
ticket 8's numbers in hand. If it's ever taken, these are the rules:

- The human gate moves; it doesn't vanish. You approve a class of change
  once, as `-light-paths` and `-light-max-lines`, instead of each PR.
- Model output can add a human look and never remove one. `light` comes
  from paths, sizes, CI and who wrote the thread. A review can only
  demote it.
- The binary merges, never a run: `gh pr merge --squash
  --match-head-commit <sha>`, so a push after the review can't ride along.
- Only on a verdict this process watched being produced. A status read
  back after a restart buys a re-review, not a merge.
- Refuse to start unless the default branch requires CI. `setup` already
  reads that (`setup_advice.go:readBranchProtection`).
- Off by default, behind its own flag, and argued in its PR body.

What it would and wouldn't change: a fully hijacked run can already push
with the operator's token, and only branch protection that binds admins
stops that. The merge click isn't that defence. It's the one read of a
plausible-looking wrong diff, and that is what a light tier gives up.

A bar worth setting before the argument: 30 light PRs in a row, all merged
as reviewed, none followed by a fix to the same lines.

## If you have Claude Ship

- **Claude Approvals** works beside this. Ticket 2 keeps its check from
  being read as red CI. The policy file is where a multi-person repo
  should say who has to look.
- **Code Review's** findings aren't consumed yet. Its docs say the check
  ends neutral and carries a machine-readable count, so a later ticket can
  read it as the review signal. Nobody here can test that today.
- **PR Steward**: leave it off on PRs polako drives. Two agents pushing
  to one branch is the conflict *one issue in flight* exists to avoid.

## Invariants this changes

- **Orchestration state** gains one kind: the `polako/review` commit
  status. Still GitHub, still re-derived on restart.
- **What leaves the machine** gains new content on an old destination:
  review findings in a PR comment, plus a status line of counts.
- **Eleven verbs.** `review` starts a run, like `work` and `design`.
- **Unattended means no prompts** holds only if ticket 4 passes. Until
  then the reviewer's grant is a guess.
- **Nothing merges itself**: untouched.

## Left open

- Whether `-review` becomes the default. Decide after a tagged batch and a
  row in `docs/experiments.md`.
- Whether the skill's gate shrinks once the supervisor reviews.
- Whether hands-off merges ever happen, and at what bar.
