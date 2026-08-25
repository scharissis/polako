# Cut a release: one version number, two tags. See release.sh for the why,
# including why moving the marketplace `ref` is a separate step 3.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$version = (Get-Content .claude-plugin\plugin.json -Raw | ConvertFrom-Json).version
if (-not $version) { throw 'could not read a version from .claude-plugin/plugin.json' }
if (git status --porcelain) { throw 'working tree is dirty - commit the version bump first' }
if (-not (Select-String -Path CHANGELOG.md -Pattern "^## \[?$([regex]::Escape($version))\]?" -Quiet)) {
  throw "CHANGELOG.md has no section for $version - write the release notes first; the release workflow publishes that section as the GitHub release body"
}

Write-Host "==> releasing $version"

# A CLI too old for `claude plugin tag` fails with a bare `unknown command
# 'tag'`. Rehearse the command rather than asking the help text, which exits 0
# on a CLI that has no `tag` at all. See release.sh.
$dryRun = (claude plugin tag --dry-run 2>&1 | Out-String)
if ($LASTEXITCODE -ne 0) {
  if ($dryRun -match 'unknown command') {
    $have = (claude --version 2>&1 | Out-String).Trim()
    Write-Error "this Claude Code has no ``claude plugin tag`` (found $have). Update it - npm install -g @anthropic-ai/claude-code@latest - and rerun; no tag was pushed"
  } else {
    Write-Error "``claude plugin tag --dry-run`` refused; no tag was pushed:`n$dryRun"
  }
  exit 1
}

# Both tags below are pushed before anything in CI runs, so a manifest the
# plugin tooling rejects has to be caught here or not at all. See release.sh.
claude plugin validate .
if ($LASTEXITCODE -ne 0) {
  Write-Error 'manifest validation failed - fix it before tagging; no tag was pushed'
  exit $LASTEXITCODE
}

# Refuses if plugin.json and the marketplace entry disagree. --message takes
# %s for the version; see release.sh for why it matches the tag below.
claude plugin tag --push --message 'backlog-drain %s'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

git tag -a "v$version" -m "backlog-drain $version"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
git push origin "refs/tags/v$version"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "pushed backlog-drain--v$version and v$version"
Write-Host "the binary release workflow runs on v$version"

# Whether step 3 is still owed. The full rule is enforced by a test on every PR.
$ref = ((Get-Content .claude-plugin\marketplace.json -Raw | ConvertFrom-Json).plugins[0].source).ref
if ($ref -eq "backlog-drain--v$version") {
  Write-Host "the marketplace entry already pins backlog-drain--v$version: $version is published"
} else {
  Write-Host ''
  Write-Host "STEP 3 - nobody is on $version yet. The marketplace entry still pins $ref."
  Write-Host 'Smoke-test the release:'
  Write-Host ''
  Write-Host '  .\scripts\smoke.ps1'
  Write-Host ''
  Write-Host "then open a PR moving that ref to backlog-drain--v$version."
}
