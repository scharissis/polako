#!/usr/bin/env bash
# Run the eval suite through `claude plugin eval`, the CLI's own runner.
#
# The CLI can't be pointed at the suite bare, for two reasons:
#
#   - The stand-in gh has to come first on the run's PATH, and a case can't put
#     it there: the CLI ignores a workspace settings file's env.PATH (issue
#     #126) and lets a case set EVAL_* variables only. So this installs a
#     dispatcher first on PATH (lib/gh-dispatch.sh) that finds each case's own
#     stand-in. It lives outside the plugin tree, because the CLI hides PATH
#     entries inside that tree from the run's shell. Each case's scaffold
#     refuses to start if the route is wrong, before any model call is paid for.
#     git and python3 go in the same directory, pinned by their resolved paths:
#     on macOS the CLI's sandbox can't run Apple's /usr/bin shims, and its
#     shell can't see through Homebrew's symlinks, so a PATH lookup of git
#     lands on Apple's shim even with Homebrew's git ahead of it. So do
#     git-upload-pack and git-receive-pack, which git runs by name for a fetch
#     or push to the scratch origin, and which the same lookup gets wrong.
#   - The target is evals/, not the plugin root. The CLI walks its whole target
#     for cases, skipping only .git, node_modules, .claude, results and mocks,
#     so the root would also run every stale copy of the suite under
#     .worktrees/. The plugin still loads from the root above evals/.
#
# Every other flag is the CLI's own and passes straight through. A first run
# looks like:
#
#   evals/plugin-eval.sh --case clear-issue --ablation none --no-publish \
#     --keep-temp --max-cost-usd 5
#
# Mind the defaults: --ablation runs every case a second time without the
# plugin, and the HTML report publishes to your claude.ai account unless
# --no-publish says otherwise. evals/README.md, "Running it", has the rest.
# visual-change isn't in this runner's reach (by-hand.yaml, not case.yaml);
# evals/run.sh runs it.
set -euo pipefail

evals_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
plugin_root=$(dirname "$evals_dir")
# shellcheck source=lib/grants.sh
. "$evals_dir/lib/grants.sh"

for tool in claude python3; do
  command -v "$tool" >/dev/null || {
    echo "plugin-eval.sh: needs $tool on PATH — install it and rerun" >&2; exit 2; }
done

tools_dir=${XDG_CACHE_HOME:-$HOME/.cache}/polako-evals/bin
case "$tools_dir/" in
"$plugin_root"/*)
  echo "plugin-eval.sh: $tools_dir is inside the plugin tree, where the CLI" >&2
  echo "hides it from the run. Point XDG_CACHE_HOME somewhere else." >&2
  exit 2
  ;;
esac
mkdir -p "$tools_dir"
cp "$evals_dir/lib/gh-dispatch.sh" "$tools_dir/gh"
chmod +x "$tools_dir/gh"

# pin NAME COMMAND: a NAME in tools_dir that execs COMMAND, already quoted.
pin() {
  printf '#!/bin/sh\nexec %s "$@"\n' "$2" > "$tools_dir/$1"
  chmod +x "$tools_dir/$1"
}

for tool in git python3; do
  # Resolved past every symlink, and never to a pin a previous launch left.
  real=$(python3 -c '
import os, shutil, sys
tool, pins = sys.argv[1], os.path.realpath(sys.argv[2])
path = os.pathsep.join(d for d in os.environ["PATH"].split(os.pathsep)
                       if os.path.realpath(d) != pins)
found = shutil.which(tool, path=path)
print(os.path.realpath(found) if found else "")
' "$tool" "$tools_dir")
  if [ -z "$real" ]; then
    echo "plugin-eval.sh: needs $tool on PATH — install it and rerun" >&2
    exit 2
  fi
  if [ "$(uname -s)" = Darwin ] && [ "$real" = "/usr/bin/$tool" ]; then
    echo "plugin-eval.sh: the $tool here is Apple's /usr/bin/$tool, a shim the CLI's" >&2
    echo "sandbox can't run. Install $tool somewhere else (Homebrew's works) and rerun." >&2
    exit 2
  fi
  pin "$tool" "'$real'"
  if [ "$tool" = git ]; then git_real=$real; fi
done
pin git-upload-pack "'$git_real' upload-pack"
pin git-receive-pack "'$git_real' receive-pack"

PATH="$tools_dir:$PATH" exec claude plugin eval "$evals_dir" \
  --scaffold --allow-tools "${eval_grants[@]}" "$@"
