#!/usr/bin/env bash
# Cut a release: one version number, two tags.
#
#   backlog-drain--v<version>  what the Claude plugin tooling creates and
#                              validates (plugin.json vs the marketplace entry),
#                              and what the marketplace entry's `ref` pins to
#   v<version>                 semver, so `go install ...@v<version>` resolves,
#                              and the trigger for the binary release workflow
#
# The version is read from .claude-plugin/plugin.json. Bump it and commit that
# bump before running this.
#
# Tagging is step 2 of three. Step 1 is the release commit; step 3 is moving the
# marketplace entry's `ref` to the tag this creates, which is the act of
# publishing — nobody is exposed to a release until that lands. Keeping them
# apart is what stops main from ever advertising a tag that does not exist yet.
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
if ! grep -q "^## \[*$version\]*" CHANGELOG.md; then
  echo "CHANGELOG.md has no section for $version - write the release notes first;" >&2
  echo "the release workflow publishes that section as the GitHub release body" >&2
  exit 1
fi

echo "==> releasing $version"

# `claude plugin tag` arrived well after the plugin system itself, so a CLI old
# enough to lack it fails with a bare `unknown command 'tag'` that says nothing
# about the fix. Rehearse the command instead of asking the help text, which
# exits 0 and prints the parent help on a CLI that has no `tag` at all - so the
# only reliable question is what the command itself does. A dry run also
# refuses early on everything the real one would: a version the manifests
# disagree on, or a tag that already exists.
if ! dryRun=$(claude plugin tag --dry-run 2>&1); then
  if printf '%s' "$dryRun" | grep -qi "unknown command"; then
    echo "this Claude Code has no \`claude plugin tag\` (found $(claude --version 2>/dev/null))." >&2
    echo "update it - npm install -g @anthropic-ai/claude-code@latest - and rerun; no tag was pushed" >&2
    exit 1
  fi
  echo "\`claude plugin tag --dry-run\` refused; no tag was pushed:" >&2
  printf '%s\n' "$dryRun" >&2
  exit 1
fi

# Both tags below are pushed before anything in CI runs, so a manifest the
# plugin tooling rejects has to be caught here or not at all. `plugin tag`
# refuses on plugin.json and the marketplace entry disagreeing; this catches
# the rest of what the validator checks, before a tag exists to retract.
if ! claude plugin validate .; then
  echo "manifest validation failed - fix it before tagging; no tag was pushed" >&2
  exit 1
fi

# Refuses if plugin.json and the marketplace entry disagree.
claude plugin tag --push

git tag -a "v$version" -m "backlog-drain $version"
git push origin "refs/tags/v$version"

echo "pushed backlog-drain--v$version and v$version"
echo "the binary release workflow runs on v$version"

# Whether step 3 is still owed. The full rule - the pinned ref must never be
# ahead of plugin.json - is enforced by TestMarketplaceRefIsNotAheadOfTheVersion
# on every PR, so this only has to say what to do next.
ref=$(sed -n 's/.*"ref"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  .claude-plugin/marketplace.json | head -1)
if [ "$ref" = "backlog-drain--v$version" ]; then
  echo "the marketplace entry already pins backlog-drain--v$version: $version is published"
else
  echo
  echo "STEP 3 - nobody is on $version yet. The marketplace entry still pins $ref."
  echo "Smoke-test the release, then open a PR moving that ref to backlog-drain--v$version."
fi
