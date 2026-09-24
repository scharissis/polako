# Sourced by lib/scaffold.sh, with $repo, $workspace, $case_dir and $origin in
# scope.
#
# Adds the static page this case's issue restyles: index.html and style.css,
# plus a package.json whose `dev` script serves them with serve.py, a few lines
# of python's stdlib HTTP server — no package to install, unlike a real dev
# server. Not `python3 -m http.server`: piped, it block-buffers the line naming
# its URL, and it looks the host's name up before printing it at all, which took
# 35 seconds on one Mac. The skill shoots only a URL the server printed, so that
# server left a run with nothing to shoot.
#
# Origin then gets a GitHub-shaped URL so a run can read an owner and a repo
# out of it, with a url.insteadOf redirect keeping every actual git operation
# landing on the real bare repo on disk — the same local-only trick
# lib/scaffold.sh's own origin already relies on, just under a URL that looks
# like it came from GitHub. `git config --get remote.origin.url` (never
# `remote get-url`, which expands the redirect) is the form a run is meant to
# read, and that form returns the alias, not the rewrite.

cp "$case_dir/index.html" "$repo/index.html"
cp "$case_dir/style.css" "$repo/style.css"
cp "$case_dir/package.json" "$repo/package.json"
cp "$case_dir/serve.py" "$repo/serve.py"
git -C "$repo" add index.html style.css package.json serve.py
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "chore: add the static page this case's issue restyles"
git -C "$repo" push --quiet origin main

git -C "$repo" remote set-url origin https://github.invalid/eval/fixture.git
git -C "$repo" config "url.$origin.insteadOf" https://github.invalid/eval/fixture.git

echo "seeded $repo/index.html, style.css, package.json; origin aliased to https://github.invalid/eval/fixture.git"
