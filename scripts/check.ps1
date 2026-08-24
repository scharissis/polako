# Everything CI runs, run locally: gofmt, vet, tests.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

Write-Host '==> gofmt'
$unformatted = gofmt -l .
if ($unformatted) {
    Write-Host 'not gofmt-clean:'
    Write-Host $unformatted
    exit 1
}

Write-Host '==> go vet'
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host '==> go test'
go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host 'all checks passed'
