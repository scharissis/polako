# Sourced by lib/scaffold.sh, with $repo, $workspace, $case_dir and $origin in
# scope.
#
# visual-change's seed, for a page whose change shows only on keyboard focus:
# index.html, style.css, and the same package.json and serve.py, so a `dev`
# script serves the page on loopback and prints its URL at once. See
# visual-change/seed.sh for why serve.py and not `python3 -m http.server`, and
# for the GitHub-shaped origin alias below.

cp "$case_dir/index.html" "$repo/index.html"
cp "$case_dir/style.css" "$repo/style.css"
cp "$case_dir/package.json" "$repo/package.json"
cp "$case_dir/serve.py" "$repo/serve.py"
git -C "$repo" add index.html style.css package.json serve.py
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "chore: add the page this case's issue fixes"
git -C "$repo" push --quiet origin main

git -C "$repo" remote set-url origin https://github.invalid/eval/fixture.git
git -C "$repo" config "url.$origin.insteadOf" https://github.invalid/eval/fixture.git

echo "seeded $repo/index.html, style.css, package.json; origin aliased to https://github.invalid/eval/fixture.git"
