---
description: Take a GitHub issue from planning to PR, resumable across clarification waits
argument-hint: [issue-number]
arguments: [issue]
disable-model-invocation: true
---

# Implement GitHub issue #$issue

## Phase 0 — Gather context (every run, before anything else)
1. Run `gh issue view $issue --json number,title,state,body,comments` and read it.
   Always use this --json form: the plain and --comments forms can print
   nothing on this setup.
2. Run `git worktree list`.
Use commands native to this session's shell (PowerShell on Windows, bash
elsewhere); do any text extraction yourself — no awk/sed/head pipelines.
Detect the current phase from what you found and resume from there.

## Phase 1 — Workspace
- If this session is already inside a Claude-managed worktree (cwd contains
  `.claude/worktrees/`): stay here. Fetch, then create branch issue-$issue
  from the remote default branch (`git symbolic-ref refs/remotes/origin/HEAD
  --short`).
- Else if a worktree for issue-$issue exists: cd into it.
- Else: take the repo name from the main checkout (first line of
  `git worktree list`), fetch, and `git worktree add` a sibling folder
  `<repo>-issue-$issue` with branch issue-$issue.

## Phase 2 — Plan
If PLAN.md doesn't exist in the worktree, or new answers have appeared:
1. Study the issue and relevant code. Write PLAN.md BEFORE implementing —
   even when the issue is clear. A few bullets is enough: approach, files
   to touch, scope decisions, anything deliberately left out. It's the
   resume point if this session dies.
2. If anything genuinely blocks implementation: record the questions in
   PLAN.md under "## Open questions", post them with
   `gh issue comment $issue` in terse, simple English, then STOP and tell
   me to re-run once there's a reply. Do not implement.
3. If the thread answers previously posted questions, fold them into
   PLAN.md and mark it FINAL.

## Phase 3 — Implement (only when PLAN.md exists and isn't blocked)
1. Implement the plan, committing in logical increments following the
   repo's commit conventions. Run the test suite, typecheck, and lint.
2. MANDATORY GATE — do not create a PR until this step has run:
   invoke `/code-review high --fix` and address its findings. If the
   code-review skill is not invocable in this session, say so explicitly,
   then perform a substitute review pass: re-read the full diff
   critically for correctness, edge cases, and convention violations,
   and fix what you find.
3. Open the PR with a real title and description — never bare --fill:
   - Title: one line in the repo's commit convention, stating the
     user-visible change (usually the primary commit subject).
   - Body: write it to PR_BODY.md using the Write tool (not a heredoc),
     then `gh pr create --title "..." --body-file PR_BODY.md`.
     Reuse the implementation summary you would report anyway, structured as:
       ## Summary — what changed and why, 2–4 sentences
       ## Design decisions — the choices a reviewer would question, and why
       ## Scope — anything deliberately left out, and the reasoning
       ## Verification — test/typecheck/lint results and manual checks done
     End the body with `Closes #$issue` on its own line — the merge
     auto-closing the issue is what advances the automation.
   - Delete PR_BODY.md afterwards; never commit it.
4. Report the PR URL.