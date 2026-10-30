$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

function Run-Step([string]$Label, [string[]]$Arguments, [hashtable]$Environment = @{}) {
    Write-Host "`n==> $Label"
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    $saved = @{}
    $succeeded = $false
    foreach ($key in $Environment.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, [string]$Environment[$key], 'Process')
    }
    try {
        & $Arguments[0] $Arguments[1..($Arguments.Count - 1)]
        if ($LASTEXITCODE -ne 0) { throw "$Label failed with exit code $LASTEXITCODE" }
        $succeeded = $true
    }
    finally {
        foreach ($key in $Environment.Keys) {
            [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process')
        }
        $stopwatch.Stop()
        $status = if ($succeeded) { 'PASS' } else { 'FAIL' }
        Write-Host ("<== {0}: {1} ({2})" -f $Label, $status, $stopwatch.Elapsed)
    }
}

Set-Location $root
Run-Step 'billing focused suites' @('go', 'test', './enterprise/internal/core/billing', './internal/core/runtime', './internal/core/usageauthority/...', './enterprise/internal/infra/billingstore', './enterprise/internal/infra/billingspool', './enterprise/internal/infra/billingcompose', './enterprise/internal/infra/billingadmission', './internal/infra/runtimebundle', './internal/archtest', './enterprise/internal/infra/usageauthority/...', './enterprise/internal/archtest', '-run', 'Billing|UsageAuthority|Usage|Spool|Compose|Admission|Runtime|Migration|Outbox|Wiring|Construction|Deletion|LOC|Symbol', '-count=1')
Run-Step 'predecessor billing regressions' @('go', 'test', './enterprise/internal/core/billing', './internal/core/runtime', './enterprise/internal/infra/billingstore', './internal/infra/runtimebundle', '-count=1')

if ($env:AIP_REQUIRE_POSTGRES -eq '1') {
    $dsn = if ($env:AIP_TEST_POSTGRES_DSN) { $env:AIP_TEST_POSTGRES_DSN } else { $env:AIP_TEST_POSTGRES_ADMIN_DSN }
    if (-not $dsn) { throw 'FAIL: AIP_REQUIRE_POSTGRES=1 but no PostgreSQL DSN is configured' }
    Run-Step 'PostgreSQL billingstore integration' @('go', 'test', '-tags=integration', './enterprise/internal/infra/billingstore', '-run', 'Postgres', '-count=1') @{ AIP_REQUIRE_POSTGRES = '1'; AIP_TEST_POSTGRES_DSN = $dsn }
}
else {
    Write-Host 'SKIP: PostgreSQL billingstore integration (set AIP_REQUIRE_POSTGRES=1 to require it)'
}

Run-Step 'quality checks' @('make', 'quality-checks')
# This is the repository-wide unit gate. Do not also invoke make test-unit:
# that target runs the same ./... graph and would execute the full suite twice.
Run-Step 'unit/full test suite' @('go', 'test', '-parallel=8', '-timeout=10m', './...', '-count=1')
Run-Step 'documentation checks' @('make', 'docs-check')
if ((go env GOOS) -eq 'windows') {
    Write-Host 'SKIP: race certification is unavailable on Windows in this repository'
}
else {
    Run-Step 'race tests' @('make', 'test-race')
}
Run-Step 'diff whitespace' @('git', 'diff', '--check')
$goFiles = @((git diff --name-only --diff-filter=ACM -- '*.go') + (git ls-files --others --exclude-standard -- '*.go') | Where-Object { $_ } | Sort-Object -Unique)
$gofmt = @($goFiles | ForEach-Object { gofmt -l $_ } | Where-Object { $_ })
if ($gofmt.Count -ne 0) { throw "FAIL: changed Go files are not gofmt-clean: $($gofmt -join ', ')" }
$changedGo = $goFiles.Count
$changedFiles = @((git diff --name-only) + (git ls-files --others --exclude-standard) | Where-Object { $_ } | Sort-Object -Unique).Count
if ($changedGo -gt 100 -or $changedFiles -gt 100) { throw "FAIL: changed files exceed the 100-file checkpoint limit (Go=$changedGo total=$changedFiles)" }
Write-Host 'PASS: billing convergence certification completed; PostgreSQL/race status was reported explicitly'
