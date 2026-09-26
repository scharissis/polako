# Sourced by evals/run.sh and evals/plugin-eval.sh: the operator's tool grant,
# the same under both runners.
#
# `claude plugin eval` gives a case exactly these tools plus the ungated ones
# its own execution.allowed_tools names, and withholds every other tool from
# the model. run.sh builds the same set (grade.py meta). So this is production's
# grant, prefix by prefix, never plain Bash: a command a real run would have
# refused is refused here too, and the implement-issue cases' no_call_was_refused
# grader fails on it. TestEvalGrantsMatchTheVerbsGrants (cmd/polako) holds this
# list to the Go constants it copies, so the two can't drift: its failure names
# each entry to add or drop.
#
# The CLI takes one grant for a whole invocation, so this is every verb's grant
# at once, not one per case: an implement-issue case also gets the few reads and
# the `gh issue create` the intake verbs add, and plan-vision and review-health
# get all of defaultTools. The last three groups are in no verb's grant. Each
# stands in for something a production run has without one.
#
# The bare tools a case names itself (Read, Glob, Grep, TodoWrite, Skill) are
# ungated and stay out of this list.
eval_grants=(
  # defaultTools (flags.go), the grant of `work` and `design`.
  'Bash(git:*)'
  'Bash(gh issue view:*)'
  'Bash(gh issue comment:*)'
  'Bash(gh pr create:*)'
  'Bash(gh pr view:*)'
  'Bash(gh pr list:*)'
  'Bash(gh pr diff:*)'
  'Bash(gh pr checks:*)'
  'Bash(gh run list:*)'
  'Bash(gh run view:*)'
  'Write'
  'Edit'
  'Bash(npm:*)'
  'Bash(npx:*)'
  'Bash(pnpm:*)'
  'Bash(yarn:*)'
  'Bash(go:*)'
  'Bash(cargo:*)'
  'Bash(make:*)'
  'Bash(python:*)'
  'Bash(python3:*)'
  'Bash(pytest:*)'
  'Bash(uv:*)'
  'Bash(dotnet:*)'
  'Bash(mvn:*)'
  'Bash(gradle:*)'
  'Bash(evals/run.sh:*)'

  # issueLabelTools and issueCloseTool (claude.go), which the binary pins to
  # the issue it dispatches. Every case here works issue 1.
  'Bash(gh issue edit 1 --add-label:*)'
  'Bash(gh issue edit 1 --remove-label:*)'
  'Bash(gh issue close 1:*)'

  # What designTools, planTools and healthTools grant beyond the above.
  'Bash(gh issue list:*)'
  'Bash(gh search issues:*)'
  'Bash(gh issue create:*)'
  'Skill(claude-api)'

  # The file commands acceptEdits lets through. That's polako's
  # -permission-mode default, but the CLI runs every case under dontAsk
  # (run.sh copies it), so the grant has to name them instead. The list is the
  # CLI's own, read from 2.1.283; a probe there refused `mkdir` under dontAsk
  # and let it through under acceptEdits.
  'Bash(mkdir:*)'
  'Bash(touch:*)'
  'Bash(rm:*)'
  'Bash(rmdir:*)'
  'Bash(mv:*)'
  'Bash(cp:*)'
  'Bash(sed:*)'

  # The scratch repo's own scripts: lib/fixture's greet.sh and test.sh, and
  # one-turn's bench.sh. No defaultTools prefix covers a repo's own script, so
  # an operator whose repo is driven by one grants it with -add-tools. A run
  # names the worktree's copy by path, so each is a wildcard on the name,
  # matching ./test.sh, <worktree>/test.sh and sh <worktree>/test.sh alike.
  'Bash(*/greet.sh*)'
  'Bash(*/test.sh*)'
  'Bash(*/bench.sh*)'

  # How implement-issue's review gate confirms a backgrounded /code-review has
  # finished (issue #472). A real session has it without a grant; the CLI
  # withholds every tool it isn't given.
  'ListAgents'
)
