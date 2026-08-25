#!/usr/bin/env bash
# Smoke-test a tagged release, before the publish PR moves anybody onto it.
#
# This is step 2.5 of three. The gap between tagging and publishing exists so
# the built artefacts can be checked while nobody is exposed to them yet, and
# this is what fills it. Separate from check.sh because every check here needs
# the network, `gh` and a real `claude` — the three things the Go suite is
# hermetic against — and because none of it can run before the tags exist.
#
# What it covers is what CI structurally cannot: the -ldflags version stamp,
# which only release-workflow builds carry; the published release and its
# assets; `go install ...@vX.Y.Z` resolving against the remote; the marketplace
# ref naming a tag that exists; the plugin installing from the tag about to be
# published; and the binary and the plugin agreeing on a version. It cannot
# cover the skill actually taking an issue to a PR — see "Cutting a release" in
# the README for that half.
#
# Every check runs and the failures are summarised at the end, rather than the
# first one aborting: deciding whether to publish is easier with the whole
# picture than with one failure at a time.
#
# Writes nothing outside a temporary directory it removes on exit — not to
# ~/.claude, ~/go/bin or ~/.backlog-drain. The plugin half installs into a
# throwaway CLAUDE_CONFIG_DIR, so the release under test never becomes the
# release this machine is running. (`go install` still populates the shared
# module cache; that is a cache, not configuration.)
#
# GitHub is asked through `gh` rather than `git ls-remote`, so the whole script
# needs one working credential instead of two — an ssh-agent that has forgotten
# its key should not read as a broken release.
#
# Matches are tested with here-strings rather than `... | grep -q`, because
# under pipefail a grep that matches early leaves the producer with SIGPIPE and
# the pipeline reporting 141 - a passing check read as a failing one.
set -uo pipefail
cd "$(dirname "$0")/.."

name=backlog-drain
skill=implement-issue

version=${1:-}
if [ -z "$version" ]; then
  version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    .claude-plugin/plugin.json | head -1)
fi
version=${version#v}
if [ -z "$version" ]; then
  echo "could not read a version from .claude-plugin/plugin.json" >&2
  exit 1
fi

for bin in git gh claude go; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "\`$bin\` not found on PATH - the smoke test needs all of git, gh, claude and go" >&2
    exit 1
  fi
done

repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null)
if [ -z "$repo" ]; then
  echo "could not read the repository from gh - is it authenticated? (gh auth login)" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

passed=0
failed=0
skipped=0
failures=""

ok() {
  printf '  ok    %s\n' "$1"
  passed=$((passed + 1))
}
# Every failure names what to do about it, not just what went wrong: this runs
# once per release, and the operator reading it has forgotten the details.
bad() {
  printf '  FAIL  %s\n' "$1"
  [ $# -gt 1 ] && printf '        %s\n' "$2"
  failures="$failures  - $1"$'\n'
  failed=$((failed + 1))
}
skip() {
  printf '  skip  %s\n' "$1"
  [ $# -gt 1 ] && printf '        %s\n' "$2"
  skipped=$((skipped + 1))
}
# Tools that fail with a trailing blank line would otherwise quote nothing back
# at the operator, which reads as the check failing for no reason.
lastLine() {
  grep -v '^[[:space:]]*$' "$1" 2>/dev/null | tail -1
}

semverTag="v$version"
pluginTag="$name--v$version"

# Resolves a tag to the commit it names, empty if there is no such tag. The API
# dereferences annotated and lightweight tags alike, so how a tag was created
# never becomes a false alarm. Keyed on the exit status rather than the output,
# because `gh api --jq` prints the error envelope to stdout on a 404 or 422 —
# an emptiness test reads that as a resolved tag.
tagCommit() {
  local sha
  sha=$(gh api "repos/$repo/commits/$1" --jq .sha 2>/dev/null) || return 1
  printf '%s' "$sha"
}

echo "==> smoke-testing $name $version in $repo"
echo

# ---------------------------------------------------------------- 1. the tags

echo "tags"
semverCommit=$(tagCommit "$semverTag")
pluginCommit=$(tagCommit "$pluginTag")

if [ -n "$semverCommit" ]; then
  ok "$semverTag is on origin"
else
  bad "$semverTag is not on origin" "run ./scripts/release.sh - nothing else here can pass without it"
fi
if [ -n "$pluginCommit" ]; then
  ok "$pluginTag is on origin"
else
  bad "$pluginTag is not on origin" "run ./scripts/release.sh - the marketplace ref has nothing to point at"
fi

if [ -n "$semverCommit" ] && [ -n "$pluginCommit" ]; then
  if [ "$semverCommit" = "$pluginCommit" ]; then
    ok "both tags name the same commit"
  else
    bad "the two tags name different commits" \
      "the plugin and the binary would ship from different trees; delete both tags and re-run release.sh"
  fi
fi

if [ -n "$semverCommit" ]; then
  # Asked of GitHub rather than of the local clone, which would need a fetch
  # first - and a smoke test should not mutate refs to answer a question.
  # "behind" means main contains the tagged commit; "identical" means it is
  # main's tip.
  #
  # Keyed on the exit status for the same reason tagCommit is: a failed call
  # prints its error envelope to stdout, and reading that as a comparison
  # result accuses the operator of tagging off main when the truth is that the
  # question was never answered.
  if ! status=$(gh api "repos/$repo/compare/main...$semverCommit" --jq .status 2>/dev/null); then
    bad "could not compare the tagged commit against main" \
      "gh api repos/$repo/compare failed - is main the default branch, and does the token reach it?"
  else
    case "$status" in
    behind | identical) ok "the tagged commit is on main" ;;
    *) bad "the tagged commit is not on main (compare says \"$status\")" \
      "a release tagged off main is a release nobody can reproduce from main" ;;
    esac
  fi
fi

# The hermetic suite holds the ref to never being *ahead* of plugin.json, but
# nothing offline can tell whether it names a tag that exists - and a ref
# naming a deleted or never-pushed tag makes every install fail, silently,
# until somebody tries one. This is the only place that question can be asked.
currentRef=$(sed -n 's/.*"ref"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  .claude-plugin/marketplace.json | head -1)
if [ -z "$currentRef" ]; then
  bad "marketplace.json declares no ref" "installs would track a branch instead of a release"
elif [ -n "$(tagCommit "$currentRef")" ]; then
  ok "the marketplace ref ($currentRef) names a tag that exists"
else
  bad "the marketplace ref names $currentRef, which is not on origin" \
    "every install fails on this today; moving the ref to $pluginTag is the fix"
fi

# ------------------------------------------------------------- 2. the release

echo
echo "release"
if ! gh release view "$semverTag" --json tagName >/dev/null 2>&1; then
  bad "no GitHub release for $semverTag" \
    "the release workflow runs on the v* tag - check its run before going further"
else
  draft=$(gh release view "$semverTag" --json isDraft --jq .isDraft)
  pre=$(gh release view "$semverTag" --json isPrerelease --jq .isPrerelease)
  if [ "$draft" = "false" ] && [ "$pre" = "false" ]; then
    ok "the release is published, not a draft or prerelease"
  else
    bad "the release is a draft or prerelease (draft=$draft prerelease=$pre)" \
      "gh release edit $semverTag --draft=false --prerelease=false"
  fi

  gh release view "$semverTag" --json assets \
    --jq '.assets[] | "\(.name) \(.size)"' >"$tmp/assets.txt" 2>/dev/null

  missing=""
  small=""
  for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64.exe; do
    asset="${name}_${semverTag}_${target}"
    size=$(awk -v a="$asset" '$1 == a { print $2 }' "$tmp/assets.txt")
    if [ -z "$size" ]; then
      missing="$missing $asset"
    elif [ "$size" -lt 1000000 ]; then
      # A stdlib-only Go binary lands around 2.5MB on every target. Anything
      # under a megabyte is a truncated upload, not a smaller build.
      small="$small $asset($size)"
    fi
  done
  if [ -z "$missing" ]; then
    ok "all five platform binaries are attached"
  else
    bad "assets missing:$missing" "the build step of the release workflow did not finish"
  fi
  [ -n "$small" ] && bad "assets implausibly small:$small" "re-upload them; a truncated binary will not run"

  # --------------------------------------------------------- 3. release notes

  gh release view "$semverTag" --json body --jq .body >"$tmp/body.md" 2>/dev/null
  # The same range the release workflow extracts, so a mismatch means the
  # workflow fell back to --generate-notes rather than that the two disagree
  # about how to read a changelog.
  sed -n "/^## \[*${version}\]*/,/^## /p" CHANGELOG.md \
    | sed -e '1{/^## /d;}' -e '${/^## /d;}' >"$tmp/notes.md"

  # Trailing whitespace and blank lines at either end survive a round trip
  # through the API inconsistently, and none of them are a release problem.
  norm() {
    tr -d '\r' <"$1" | sed 's/[[:space:]]*$//' \
      | awk 'NF { while (held-- > 0) print ""; held = 0; print; seen = 1; next } seen { held++ }'
  }
  if [ ! -s "$tmp/notes.md" ]; then
    bad "CHANGELOG.md has no section for $version" \
      "the release body will be generated commit subjects; write the section and edit the release"
  elif diff -q <(norm "$tmp/notes.md") <(norm "$tmp/body.md") >/dev/null 2>&1; then
    ok "the release body is the changelog section for $version"
  else
    bad "the release body is not the changelog section for $version" \
      "the workflow fell back to --generate-notes; gh release edit $semverTag --notes-file CHANGELOG-section"
  fi
fi

# -------------------------------------------------------- 4. the built binary

echo
echo "binary"
case "$(uname -s)" in
Darwin) goos=darwin ;;
Linux) goos=linux ;;
*) goos="" ;;
esac
case "$(uname -m)" in
arm64 | aarch64) goarch=arm64 ;;
x86_64 | amd64) goarch=amd64 ;;
*) goarch="" ;;
esac

# What a released binary must print. The stamp is `v0.6.0`, not `0.6.0`:
# GITHUB_REF_NAME carries the prefix and drainVersion returns it untouched.
want="$name $semverTag"
drain=""

if [ -z "$goos" ] || [ -z "$goarch" ]; then
  skip "running the downloaded binary" "no release asset for $(uname -s)/$(uname -m)"
else
  asset="${name}_${semverTag}_${goos}_${goarch}"
  if gh release download "$semverTag" --pattern "$asset" --dir "$tmp/dl" >/dev/null 2>&1; then
    chmod +x "$tmp/dl/$asset"
    got=$("$tmp/dl/$asset" -version 2>&1)
    if [ "$got" = "$want" ]; then
      drain="$tmp/dl/$asset"
      ok "the downloaded $goos/$goarch binary reports \"$want\""
    else
      bad "the downloaded binary reports \"$got\", not \"$want\"" \
        "the -ldflags stamp in the release workflow is wrong; every recorded run would be misattributed"
    fi
  else
    bad "could not download $asset" "gh release download failed"
  fi
fi

if [ -n "$drain" ]; then
  mkdir -p "$tmp/metrics"
  if out=$("$drain" stats -metrics "$tmp/metrics" 2>&1) && grep -q "no run data" <<<"$out"; then
    ok "\`stats\` reads an empty run-data directory"
  else
    bad "\`stats\` failed on an empty run-data directory" "$(head -1 <<<"$out")"
  fi
fi

# ------------------------------------------------------------- 5. go install

echo
echo "go install"
# Go fetches a module over https, which on a private repo needs a credential
# the operator may only have configured for ssh - and whether *this machine*
# can clone over https is not what this check is about. Borrowing gh's token
# for the one command, through GIT_CONFIG_* so nothing is written to any
# gitconfig, keeps the check about the tag resolving.
ghToken=$(gh auth token 2>/dev/null)
if [ -n "$ghToken" ]; then
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.https://x-access-token:${ghToken}@github.com/.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/"
fi
if GOPRIVATE="github.com/${repo%%/*}/*" GOBIN="$tmp/gobin" \
  go install "github.com/$repo/cmd/$name@$semverTag" >"$tmp/goinstall.log" 2>&1; then
  got=$("$tmp/gobin/$name" -version 2>&1)
  if [ "$got" = "$want" ]; then
    ok "\`go install ...@$semverTag\` resolves and reports \"$want\""
  else
    bad "\`go install ...@$semverTag\` reports \"$got\", not \"$want\"" \
      "the module version is not the tag; check that $semverTag is a plain semver tag on the module root"
  fi
elif grep -qE "unknown revision|invalid version" "$tmp/goinstall.log"; then
  bad "\`go install ...@$semverTag\` cannot resolve that version" \
    "$(lastLine "$tmp/goinstall.log")"
else
  bad "\`go install ...@$semverTag\` failed" \
    "$(lastLine "$tmp/goinstall.log")"
fi
unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 ghToken

# ------------------------------------------------------------- 6. the plugin

echo
echo "plugin"
# Everything below runs against a throwaway config directory, so installing the
# release under test does not move this machine onto it, and does not disturb
# the marketplace already registered at user scope.
export CLAUDE_CONFIG_DIR="$tmp/claude"
mkdir -p "$CLAUDE_CONFIG_DIR"

# The marketplace is added from a local copy with the ref already moved,
# because that - not the tag - is the configuration under test. Adding the
# remote marketplace pinned at $pluginTag would read the marketplace.json *in*
# that tag, whose ref still names the previous release: no tag ever contains
# its own ref, since the publish commit lands after the tag is cut. So this
# rehearses exactly what the publish PR is about to make true, one step early.
mkdir -p "$tmp/mkt/.claude-plugin"
sed "s|\"ref\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"ref\": \"$pluginTag\"|" \
  .claude-plugin/marketplace.json >"$tmp/mkt/.claude-plugin/marketplace.json"

marketplace=$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  .claude-plugin/marketplace.json | head -1)
pluginInstalled=false

if ! claude plugin marketplace add "$tmp/mkt" >"$tmp/marketplace.log" 2>&1; then
  bad "could not add a marketplace pinning $pluginTag" "$(lastLine "$tmp/marketplace.log")"
elif ! claude plugin install "$name@$marketplace" --yes >"$tmp/install.log" 2>&1; then
  bad "could not install $name@$marketplace from $pluginTag" "$(lastLine "$tmp/install.log")"
else
  pluginInstalled=true
  ok "the plugin installs with the ref moved to $pluginTag"

  # Keyed on this plugin's id rather than on being first in the array: the
  # manifest is a list, and reading whichever version came out on top would
  # quietly vouch for a different plugin than the one under test.
  claude plugin list --json >"$tmp/list.json" 2>/dev/null
  got=$(awk -v id="\"$name@$marketplace\"" '
    index($0, "\"id\"") && index($0, id) { found = 1 }
    found && index($0, "\"version\"") {
      sub(/.*"version"[[:space:]]*:[[:space:]]*"/, "")
      sub(/".*/, "")
      print
      exit
    }' "$tmp/list.json")
  if [ "$got" = "$version" ]; then
    ok "the installed plugin reports $version"
  else
    bad "the installed plugin reports \"$got\", not \"$version\"" \
      "plugin.json and the tag disagree; Claude Code caches by version, so this is what users would be pinned to"
  fi

  # Only the inventory counts. The plugin's own description names the skill
  # too - "/implement-issue takes a single issue from plan to PR" - so a search
  # of the whole output would report a shipped skill for a plugin that ships
  # none, which is the one thing this check exists to notice.
  claude plugin details "$name" >"$tmp/details.txt" 2>/dev/null
  inventory=$(sed -n '/^Component inventory/,$p' "$tmp/details.txt")
  if grep -qF "$skill" <<<"$inventory"; then
    ok "its component inventory lists $skill"
  else
    bad "its component inventory does not list $skill" \
      "the skill did not ship in the tagged tree; check skills/$skill/SKILL.md at $pluginTag"
  fi
fi

# ---------------------------------------------- 7. the two halves, together

echo
echo "the pair"
if [ -z "$drain" ]; then
  skip "version-skew check" "no downloaded binary to run"
elif [ "$pluginInstalled" != true ]; then
  skip "version-skew check" "the plugin did not install"
else
  # A label no issue carries makes this a preflight-only run: preflight does
  # the PATH, git, gh and version-skew checks, lowestOpenIssue then finds
  # nothing and the process exits 0 without starting a single claude run.
  # -metrics off keeps smoke runs out of the real run data.
  if out=$("$drain" -dir . -label "__${name}-smoke__" -metrics off 2>&1); then
    if grep -q "version skew" <<<"$out"; then
      bad "the binary and the plugin disagree on a version" \
        "$(grep -m1 'version skew' <<<"$out")"
    else
      ok "binary $version and plugin $version agree - no skew warning"
    fi
    # -F because a repository name may carry a dot, which as a pattern would
    # match a name this drain never reached.
    if grep -qF "$repo" <<<"$out"; then
      ok "preflight reaches $repo"
    else
      bad "preflight did not name the repository" "$(head -1 <<<"$out")"
    fi
  else
    bad "preflight failed" "$(tail -1 <<<"$out")"
  fi
fi

# A skill missing from the session inventory is what execClaude's lacksCommand
# exists to catch: the run that burns a turn achieving nothing. The init event
# is the only place a session says what it has.
#
# Judged on whether that event arrived, not on the exit status: a fresh
# CLAUDE_CONFIG_DIR has no credentials, so the process still emits its
# inventory and then fails the turn. The inventory is all this needs, and
# skipping over an unauthenticated config dir would skip the check everywhere.
if [ "$pluginInstalled" != true ]; then
  skip "session lists /$name:$skill" "the plugin did not install"
else
  claude -p "hi" --output-format stream-json --verbose >"$tmp/init.jsonl" 2>"$tmp/init.err"
  init=$(grep -m1 '"slash_commands"' "$tmp/init.jsonl" 2>/dev/null)
  if [ -z "$init" ]; then
    skip "session lists /$name:$skill" \
      "claude emitted no init event - check by hand with CLAUDE_CONFIG_DIR set"
  elif grep -q "\"$name:$skill\"" <<<"$init"; then
    ok "a session lists /$name:$skill"
  else
    bad "a session does not list /$name:$skill" \
      "an unattended run would burn a turn doing nothing; check the skill's frontmatter at $pluginTag"
  fi
fi

# ------------------------------------------------------------------ summary

echo
echo "==> $passed passed, $failed failed, $skipped skipped"
if [ "$failed" -gt 0 ]; then
  echo
  echo "failed:"
  printf '%s' "$failures"
  echo
  echo "Nobody is on $version yet, which is the point of checking here: fix what is"
  echo "listed above before the publish PR moves the marketplace ref onto it."
  exit 1
fi

echo
echo "The skill half is not covered by any of this. Before opening the publish PR,"
echo "drive one real issue through $version - see \"Cutting a release\" in the README."
echo
echo "Then move the marketplace entry's ref to $pluginTag."
