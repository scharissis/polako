# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# Puts the vision document in the repository. It is seeded here rather than
# added to lib/fixture/ because it is the input under test: in the five
# implement-issue cases a roadmap sitting in the checkout would be a distraction
# the run has to decide to ignore.
#
# The document deliberately asks for four things, one of which — the --help flag
# — the seeded open issue in issues.json already covers. Whether the run
# re-proposes that one is the dedupe half of this case, and it is why the
# overlap is a real overlap rather than a near-miss: same flag, same script.

cp "$case_dir/VISION.md" "$repo/VISION.md"
git -C "$repo" add VISION.md
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "docs: add the vision document"
# Pushed so the short SHA the provenance footer names is one that exists on the
# remote, the way it would on a real repository.
git -C "$repo" push --quiet origin main

echo "seeded $repo/VISION.md, the document this case plans from"
