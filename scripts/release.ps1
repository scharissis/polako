# Cut a release: one version number, two tags. See release.sh for the why.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$version = (Get-Content .claude-plugin\plugin.json -Raw | ConvertFrom-Json).version
if (-not $version) { throw 'could not read a version from .claude-plugin/plugin.json' }
if (git status --porcelain) { throw 'working tree is dirty - commit the version bump first' }

Write-Host "==> releasing $version"

# Both tags below are pushed before anything in CI runs, so a manifest the
# plugin tooling rejects has to be caught here or not at all. See release.sh.
claude plugin validate .
if ($LASTEXITCODE -ne 0) {
  Write-Error 'manifest validation failed - fix it before tagging; no tag was pushed'
  exit $LASTEXITCODE
}

# Refuses if plugin.json and the marketplace entry disagree.
claude plugin tag --push --message 'backlog-drain %s'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

git tag -a "v$version" -m "backlog-drain $version"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
git push origin "refs/tags/v$version"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "pushed backlog-drain--v$version and v$version"
Write-Host "the binary release workflow runs on v$version"
