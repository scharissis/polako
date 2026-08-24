#!/usr/bin/env bash
# Cut a release: one version number, two tags.
#
#   backlog-drain--v<version>  what the Claude plugin tooling creates and
#                              validates (plugin.json vs the marketplace entry)
#   v<version>                 semver, so `go install ...@v<version>` resolves,
#                              and the trigger for the binary release workflow
#
# The version is read from .claude-plugin/plugin.json. Bump it and commit that
# bump before running this.
set -euo pipefail
cd "$(dirname "$0")/.."

version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  .claude-plugin/plugin.json | head -1)
if [ -z "$version" ]; then
  echo "could not read a version from .claude-plugin/plugin.json" >&2
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "working tree is dirty - commit the version bump first" >&2
  exit 1
fi

echo "==> releasing $version"

# Both tags below are pushed before anything in CI runs, so a manifest the
# plugin tooling rejects has to be caught here or not at all. `plugin tag`
# refuses on plugin.json and the marketplace entry disagreeing; this catches
# the rest of what the validator checks, before a tag exists to retract.
if ! claude plugin validate .; then
  echo "manifest validation failed - fix it before tagging; no tag was pushed" >&2
  exit 1
fi

# Refuses if plugin.json and the marketplace entry disagree.
claude plugin tag --push --message "backlog-drain %s"

git tag -a "v$version" -m "backlog-drain $version"
git push origin "refs/tags/v$version"

echo "pushed backlog-drain--v$version and v$version"
echo "the binary release workflow runs on v$version"
