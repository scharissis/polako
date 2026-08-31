# Print the shape of cmd/polako: longest files, longest functions, comment
# density, totals. Reports nothing and fails on nothing — see scripts/health/.
#
# Not part of check.ps1 and not in CI: this script gates on nothing. The gate
# that does is cmd/polako/sizebudget_test.go, which measures the same way.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

go run ./scripts/health @args
exit $LASTEXITCODE
