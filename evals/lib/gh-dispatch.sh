#!/bin/sh
# The `gh` a `claude plugin eval` run finds first on its PATH.
#
# evals/plugin-eval.sh installs this outside the plugin tree, because the CLI
# hides PATH entries inside that tree from the run's shell. It routes each call
# to the stand-in the case's own scaffold installed, found by walking up from
# wherever the run called gh from, since each case runs in its own workspace
# and the workspace's path isn't known before the run starts. With no stand-in
# above it, it refuses. It never falls through to the real gh.
dir=$PWD
while :; do
  if [ -x "$dir/.eval/bin/gh" ]; then
    exec "$dir/.eval/bin/gh" "$@"
  fi
  [ "$dir" = / ] && break
  dir=$(dirname "$dir")
done
echo "polako eval gh: no .eval/bin/gh at or above $PWD. This gh routes to an eval" >&2
echo "workspace's stand-in and never to the real gh." >&2
exit 1
