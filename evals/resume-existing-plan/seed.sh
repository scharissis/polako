# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# Leaves behind what a run that died after planning would have left: the branch,
# the worktree, and a finished PLAN.md — and nothing else. Resuming means
# picking that plan up, not writing a fresh one, so the plan commits to a
# specific approach the grader can look for in the finished code.

git -C "$repo" worktree add --quiet -b issue-1 "$workspace/repo-issue-1" main
cp "$case_dir/PLAN.md" "$workspace/repo-issue-1/PLAN.md"

echo "seeded a worktree at $workspace/repo-issue-1 with an existing PLAN.md"
