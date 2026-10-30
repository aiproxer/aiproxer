[CmdletBinding()]
param(
    [string]$BaselineRef = 'e719bb5f042e548296f9cf36950bb261d4354d83',
    [string]$OutputRoot = ''
)

$ErrorActionPreference = 'Stop'
$specRoot = $PSScriptRoot
$repoRoot = & git -C $specRoot rev-parse --show-toplevel
if ($LASTEXITCODE -ne 0) {
    throw "git repository root lookup failed with exit code $LASTEXITCODE"
}
if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path ([IO.Path]::GetTempPath()) ('lip-betterleaks-baseline-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
if (Test-Path -LiteralPath $OutputRoot) {
    throw "OutputRoot already exists: $OutputRoot"
}
New-Item -ItemType Directory -Path $OutputRoot | Out-Null

Write-Host "Archiving baseline $BaselineRef from $repoRoot to $OutputRoot"
$archive = Join-Path $OutputRoot '.baseline.tar'
try {
    & git -C $repoRoot archive --format=tar -o $archive $BaselineRef
    if ($LASTEXITCODE -ne 0) {
        throw "git archive failed with exit code $LASTEXITCODE"
    }
    & tar -xf $archive -C $OutputRoot
    if ($LASTEXITCODE -ne 0) {
        throw "tar extraction failed with exit code $LASTEXITCODE"
    }
} finally {
    if (Test-Path -LiteralPath $archive) {
        Remove-Item -LiteralPath $archive
    }
}

$overlay = Join-Path $specRoot 'benchmark-baseline_test.go.txt'
$destination = Join-Path $OutputRoot 'internal\plugins\features\secretguard\baseline_bench_test.go'
Copy-Item -LiteralPath $overlay -Destination $destination

Push-Location $OutputRoot
try {
    Write-Host "Baseline benchmark"
    & go test -run '^$' -bench '^BenchmarkSecretGuardBaselineExact$' -benchmem -benchtime=1x ./internal/plugins/features/secretguard
    if ($LASTEXITCODE -ne 0) {
        throw "baseline benchmark failed with exit code $LASTEXITCODE"
    }
    $binary = Join-Path $OutputRoot 'lipstd-baseline.exe'
    Write-Host "Baseline release build: $binary"
    & go build -o $binary ./cmd/lipstd
    if ($LASTEXITCODE -ne 0) {
        throw "baseline release build failed with exit code $LASTEXITCODE"
    }
    $artifact = Get-Item -LiteralPath $binary
    Write-Host ("Baseline lipstd size: {0} bytes" -f $artifact.Length)
} finally {
    Pop-Location
}

Write-Host "Baseline checkout: $OutputRoot"
