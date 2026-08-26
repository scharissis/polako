#!/usr/bin/env bash
# Build the scratch world one eval case runs against.
#
# Everything here is local. The point of the exercise is to watch what the skill
# decides, and a case that reached the network would be grading GitHub's
# availability as much as the skill's behaviour — so `origin` is a bare repo on
# disk and `gh` is a stand-in that records instead of publishing.
#
# Invoked by a case's own scaffold.sh, which passes its own directory. The
# workspace root is where the eval runner dropped us; EVAL_WORKSPACE overrides
# it, which is the escape hatch if the runner's cwd turns out not to be it.
set -euo pipefail

case_dir=${1:?usage: scaffold.sh <case-dir>}
lib_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
workspace=${EVAL_WORKSPACE:-$PWD}

record=$workspace/.eval
bin=$record/bin
mkdir -p "$record" "$bin"

echo "scaffolding $(basename "$case_dir") into $workspace"

# --- the scratch repository ------------------------------------------------
# The skill fetches, resolves refs/remotes/origin/HEAD, branches from it and
# pushes. All of that needs a remote that answers; none of it needs GitHub.
origin=$record/origin.git
repo=$workspace/repo

git init --quiet --bare --initial-branch=main "$origin"
git init --quiet --initial-branch=main "$repo"
git -C "$repo" config user.name "Eval Fixture"
git -C "$repo" config user.email "eval@example.invalid"
cp -R "$lib_dir/fixture/." "$repo/"
# The issue fixtures tell a run to invoke `./greet.sh`, so the bit has to be
# there however this checkout was obtained.
chmod +x "$repo"/*.sh
git -C "$repo" add -A
# gpgsign off explicitly: a global commit.gpgsign would otherwise make the
# scaffold fail on a signing prompt, which is a confusing way to learn that your
# own git config is what broke an eval run.
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "chore: scratch fixture"
git -C "$repo" remote add origin "$origin"
git -C "$repo" push --quiet -u origin main
# Without this, `git symbolic-ref refs/remotes/origin/HEAD` has nothing to
# resolve and Phase 1 cannot find the default branch to branch from.
git -C "$repo" remote set-head origin main

# --- the stand-in gh -------------------------------------------------------
# Baked as a wrapper with absolute paths rather than passed through the
# environment: the run reaches `gh` several processes deep, and an exported
# variable is one `env -i` away from being lost.
cat > "$bin/gh" <<EOF
#!/usr/bin/env bash
exec bash "$lib_dir/gh-fake.sh" "$record" "$case_dir" "\$@"
EOF
chmod +x "$bin/gh"

# The run inherits PATH from the session, not from this script, so putting the
# stand-in first has to go through settings the session reads. If this stops
# working the case fails loudly rather than quietly: the real gh finds a remote
# that is a local path, not a GitHub repo, and refuses.
mkdir -p "$workspace/.claude"
cat > "$workspace/.claude/settings.json" <<EOF
{
  "env": {
    "PATH": "$bin:$PATH"
  }
}
EOF

# --- per-case extras -------------------------------------------------------
# resume-existing-plan needs a worktree that already got as far as a plan.
if [ -f "$case_dir/seed.sh" ]; then
  # shellcheck source=/dev/null
  . "$case_dir/seed.sh"
fi

echo "scaffold ready: repo=$repo origin=$origin record=$record"
