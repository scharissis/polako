#!/usr/bin/env bash
# Build the scratch world one eval case runs against.
#
# Everything here is local. The point of the exercise is to watch what the skill
# decides, and a case that reached the network would be grading GitHub's
# availability as much as the skill's behaviour — so `origin` is a bare repo on
# disk and `gh` is a stand-in that records instead of publishing.
#
# Invoked by a case's own scaffold.sh, which passes its own directory. The
# workspace is the cwd both runners start the scaffold in, and the run in.
#
# The workspace has to stand on its own: `claude plugin eval` sandboxes the
# run's shell away from the plugin tree, so nothing the run touches may point
# back into this checkout. The stand-in gh and the case's fixtures are copied
# in rather than referenced.
set -euo pipefail

case_dir=${1:?usage: scaffold.sh <case-dir>}
lib_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
plugin_root=$(cd "$lib_dir/../.." && pwd)
workspace=$PWD

record=$workspace/.eval
bin=$record/bin
origin=$record/origin.git
repo=$workspace/repo

# Refuse a workspace that already holds something, rather than half-building on
# top of it: `git commit` on an unchanged tree and a second `git remote add
# origin` both fail several lines later with errors that read as git problems
# rather than as "this directory has been used before, or is somebody's project".
for occupied in "$repo" "$record"; do
  if [ -e "$occupied" ]; then
    echo "scaffold: $occupied already exists, and this script would overwrite it." >&2
    echo "Run in an empty directory." >&2
    exit 1
  fi
done

mkdir -p "$record" "$bin" "$record/lib" "$record/fixture"

echo "scaffolding $(basename "$case_dir") into $workspace"

# --- the scratch repository ------------------------------------------------
# The skill fetches, resolves refs/remotes/origin/HEAD, branches from it and
# pushes. All of that needs a remote that answers; none of it needs GitHub.
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
cp "$lib_dir/gh-fake.sh" "$record/lib/gh-fake.sh"
for fixture in issue.json issues.json issues-closed.json; do
  if [ -f "$case_dir/$fixture" ]; then cp "$case_dir/$fixture" "$record/fixture/"; fi
done
cat > "$bin/gh" <<EOF
#!/usr/bin/env bash
exec bash "$record/lib/gh-fake.sh" "$record" "$record/fixture" "\$@"
EOF
chmod +x "$bin/gh"

# Graders read these logs with regexes, and a regex over a file that doesn't
# exist fails. A run that never labelled anything or posted anything has an
# empty log, not a missing one.
: > "$record/gh-calls.log"
: > "$record/labels.log"
: > "$record/posted.md"

# The run looks gh up on PATH, and nothing a scaffold writes can put the
# stand-in there. Both runners ignore a workspace settings file's env.PATH
# (issue #126), and `claude plugin eval` lets a case set EVAL_* variables and
# nothing else. So each runner does it at launch: run.sh puts $bin first, and
# plugin-eval.sh puts a dispatcher first that finds $bin from wherever the run
# is. This checks the route end to end before any model call is paid for. A
# run that reached the real gh would grade a failure the case never meant, or
# act on a real repository.
if [ "$(gh --polako-eval-stand-in 2>/dev/null || true)" != "$record" ]; then
  echo "scaffold: gh on PATH is $(command -v gh || echo nothing), not this workspace's stand-in." >&2
  echo "Run the suite through evals/run.sh or evals/plugin-eval.sh, which put it first." >&2
  exit 1
fi
# plugin eval also drops PATH entries inside the plugin tree from the run's
# shell, though not from this script's, so a route that works here could
# still reach the real gh there.
case $(command -v gh) in
"$bin/gh") ;;
"$plugin_root"/*)
  echo "scaffold: gh resolves to $(command -v gh), inside the plugin tree. plugin eval" >&2
  echo "hides that from the run, which would reach the real gh. Use evals/plugin-eval.sh." >&2
  exit 1
  ;;
esac

# --- per-case extras -------------------------------------------------------
# resume-existing-plan needs a worktree that already got as far as a plan;
# one-turn needs a benchmark too slow to be worth putting in the shared fixture.
if [ -f "$case_dir/seed.sh" ]; then
  # shellcheck source=/dev/null
  . "$case_dir/seed.sh"
fi

echo "scaffold ready: repo=$repo origin=$origin record=$record"
