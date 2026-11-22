# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# Adds triage.sh and the inbox it sorts. The tool prints only counts; what it
# decided about each message lands in triage-report.txt. This case's issue adds
# a rule and a flag, so a help screen and a missing-file error are real output
# a run can capture, and neither would read any differently if the rule were
# wrong. The decision only shows in the report. That's the trap, and the
# reasoning lives here rather than in triage.sh or the issue: both reach the
# run, and a fixture that names the graded behaviour grades the run's reading,
# not its judgement.
#
# 03-deals.txt is the message the new rule should drop. 04-review.txt has two
# links, under the issue's threshold of three, so it stays kept.

cp "$case_dir/triage.sh" "$repo/triage.sh"
chmod +x "$repo/triage.sh"
cp -R "$case_dir/inbox" "$repo/inbox"
# The report is output, not source, so a run's own triage doesn't land in its
# diff.
echo "/triage-report.txt" >"$repo/.gitignore"
git -C "$repo" add triage.sh inbox .gitignore
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "feat: add inbox triage"
# Pushed, not merely committed: the run branches from refs/remotes/origin/HEAD.
git -C "$repo" push --quiet origin main

echo "seeded $repo/triage.sh and $repo/inbox/"
