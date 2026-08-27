# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# Adds the slow benchmark this case's issue asks for numbers from. It is seeded
# here rather than added to lib/fixture/ because it costs well over a minute
# every time it is invoked, and only this case has any reason to invoke it —
# in the other three it would be a tempting minute of nothing.

cp "$case_dir/bench.sh" "$repo/bench.sh"
chmod +x "$repo/bench.sh"
git -C "$repo" add bench.sh
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "chore: add the benchmark"
# Pushed, not merely committed: the run branches from refs/remotes/origin/HEAD,
# so a benchmark left only in the local checkout is one the worktree never sees.
git -C "$repo" push --quiet origin main

echo "seeded $repo/bench.sh, the slow benchmark this case's issue wants numbers from"
