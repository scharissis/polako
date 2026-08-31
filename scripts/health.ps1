# Print the shape of cmd/polako: longest files, longest functions, comment
# density, totals. Reports nothing and fails on nothing — see scripts/health/.
#
# Not part of check.ps1 and not in CI: nothing gates on these numbers yet. A
# sibling change adds the budget that does.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

go run ./scripts/health @args
exit $LASTEXITCODE
