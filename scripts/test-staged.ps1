# test-staged.ps1
# Run the complete root-module test graph through Go's native build/test cache.
# The full graph is intentional: staged-package filtering can miss reverse
# dependencies affected by a changed package.

param(
    [switch]$Verbose = $false
)

$ErrorActionPreference = "Stop"

# When set (e.g. by quality-gate), include the precommit-only regression and
# hygiene tests in the same cached root-graph invocation.
$preFlags = @()
if ($env:AIP_TEST_PRECOMMIT -match '^(?i:1|true|yes|on)$') {
    $preFlags = @("-tags=precommit")
}

Write-Host "Testing complete root-module package graph (Go build/test cache enabled)" -ForegroundColor Green
if ($preFlags.Count -gt 0) {
    Write-Host "Test tags: $($preFlags -join ' ')" -ForegroundColor Cyan
}
Write-Host ""

# Deliberately omit -count=1. Go can reuse package builds and successful test
# results, while ./... catches downstream packages that staged-only selection
# would miss after a shared-package change.
$testParallel = 0
if (-not [int]::TryParse([string]$env:AIP_TEST_PARALLEL, [ref]$testParallel) -or $testParallel -lt 1) {
    if (-not [int]::TryParse([string]$env:NUMBER_OF_PROCESSORS, [ref]$testParallel) -or $testParallel -lt 1) {
        $testParallel = 8
    }
}
go test "-parallel=$testParallel" @preFlags ./...
exit $LASTEXITCODE
