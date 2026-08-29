# Everything CI runs, run locally: gofmt, vet, tests.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

Write-Host '==> gofmt'
# Same scoping as check.sh: gofmt does not skip dot-directories the way go
# does, so "." would format-check every nested worktree under
# .claude/worktrees too. List tracked files instead.
$goFiles = git ls-files '*.go'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$unformatted = $null
if ($goFiles) {
    $unformatted = gofmt -l $goFiles
}
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
