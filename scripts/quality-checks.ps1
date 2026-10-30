# quality-checks.ps1
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Test parallelism defaults to the machine's logical core count; override with
# AIP_TEST_PARALLEL=<n> (mirrors GO_TEST_FLAGS in the Makefile and windows-task.ps1).
$script:TestParallel = 0
if (-not [int]::TryParse([string]$env:AIP_TEST_PARALLEL, [ref]$script:TestParallel) -or $script:TestParallel -lt 1) {
    if (-not [int]::TryParse([string]$env:NUMBER_OF_PROCESSORS, [ref]$script:TestParallel) -or $script:TestParallel -lt 1) {
        $script:TestParallel = 8
    }
}

function Invoke-QualityChild {
    param([string]$Label, [string[]]$Command, [string[]]$Env = @(), [string]$Timeout = "2m")
    Invoke-TaskRunner -Label "quality-checks:$Label" -Cwd $RepositoryRoot -Timeout $Timeout -Env $Env -Command $Command | Out-Host
}

function Test-UnderNestedGoModule {
    param([string]$NormalizedPath)

    $dir = Split-Path -Parent $NormalizedPath
    if ([string]::IsNullOrWhiteSpace($dir) -or $dir -eq '.') {
        return $false
    }

    while (-not [string]::IsNullOrWhiteSpace($dir) -and $dir -ne '.') {
        if (Test-Path -LiteralPath (Join-Path $dir 'go.mod')) {
            return $true
        }
        $parent = Split-Path -Parent $dir
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $dir) {
            break
        }
        $dir = $parent -replace '\\', '/'
    }

    return $false
}

function Test-AgentSkillPath {
    param([string]$NormalizedPath)

    return $NormalizedPath -match '^\.(agents|codex|cursor|kiro|opencode|pi)/skills/'
}

function Get-QualityPackages {
    $json = & go -C $RepositoryRoot run -buildvcs=false ./tools/localscope -mode changed -metadata=false
    if ($LASTEXITCODE -ne 0) { throw "Shared quality scope planning failed" }
    $plan = ($json -join "`n") | ConvertFrom-Json
    $packages = @($plan.modules | Where-Object { $_.directory -eq "." } | ForEach-Object { $_.packages })
    if ($packages.Count -eq 0 -or $packages -contains ".") { return @("./...") }
    return @($packages | ForEach-Object { "$_/..." } | Sort-Object -Unique)
}

$qualityPackages = @(Get-QualityPackages)
$testPackageFlags = @()
if ($env:AIP_TEST_PACKAGES) {
    if ($env:AIP_TEST_PACKAGES -notmatch '^[1-9][0-9]*$') { throw "AIP_TEST_PACKAGES must be a positive integer" }
    $testPackageFlags += "-p=$($env:AIP_TEST_PACKAGES)"
}

Write-Host "=== Quality Checks ===" -ForegroundColor Cyan
Write-Host ""
Write-Host "Quality scope: $($qualityPackages -join ' ')" -ForegroundColor DarkGray
Write-Host ""

Write-Host "[1/8] Checking generated feature planes..." -ForegroundColor Yellow
$null = Invoke-QualityChild "generate-feature-planes" @("go", "run", "./scripts/generate-feature-planes.go", "-check") -Timeout "2m"
Write-Host "OK: Generated feature planes check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[2/8] Checking Go formatting..." -ForegroundColor Yellow
$unformatted = @(Invoke-TaskRunner -Label "quality-checks:gofmt" -Cwd $RepositoryRoot -Timeout "2m" -Output capture -Command @("gofmt", "-l", ".") 2>$null |
    Where-Object { $_ -and -not (Test-AgentSkillPath ($_ -replace '\\', '/')) })
if ($unformatted.Count -gt 0) {
    Write-Host "Unformatted files:" -ForegroundColor Red
    $unformatted | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    Write-Host "Run: gofmt -w <files> or go fmt ./..." -ForegroundColor Yellow
    exit 1
}
Write-Host "OK: Format check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[3/8] Checking Go modules..." -ForegroundColor Yellow
$null = Invoke-QualityChild "go-mod-tidy" @("go", "mod", "tidy", "-diff") -Timeout "3m"
$shouldVerifyModuleCache = $false
if ($env:AIP_VERIFY_MODULE_CACHE -match '^(?i:1|true|yes|on)$') {
    $shouldVerifyModuleCache = $true
} elseif ($env:CI -match '^(?i:1|true|yes|on)$') {
    $shouldVerifyModuleCache = $true
}

if ($shouldVerifyModuleCache) {
    Write-Host "Verifying module checksums..." -ForegroundColor DarkGray
    go mod verify
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: go mod verify failed (checksum mismatch or corrupt module cache)" -ForegroundColor Red
        exit $LASTEXITCODE
    }
} else {
    Write-Host "Skipping module cache verification locally (set AIP_VERIFY_MODULE_CACHE=1 to enable)." -ForegroundColor DarkGray
}
Write-Host "OK: Module check passed" -ForegroundColor Green
Write-Host ""

if ($env:AIP_SKIP_GO_COMPILE_CHECKS -eq "1") {
    Write-Host "Skipping standalone build/vet: the following go test target owns compilation and curated vet checks." -ForegroundColor DarkGray
} else {
    Write-Host "[4/8] Checking build..." -ForegroundColor Yellow
    $null = Invoke-QualityChild "build" (@("go", "build") + $qualityPackages)
    Write-Host "OK: Build check passed" -ForegroundColor Green
    Write-Host ""

    Write-Host "[5/8] Running go vet..." -ForegroundColor Yellow
    $null = Invoke-QualityChild "vet" (@("go", "vet") + $qualityPackages)
    Write-Host "OK: Vet check passed" -ForegroundColor Green
    Write-Host ""
}

Write-Host "[6-8/8] Running independent guardrails in parallel..." -ForegroundColor Yellow
$guardJobs = @(
    @{ Label = "adhoc-goroutines"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/check-adhoc-goroutines.ps1") },
    @{ Label = "regex-hotpath"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/regex-hotpath-check.ps1") },
    @{ Label = "protobuf"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/proto-check.ps1") }
)
if ($env:AIP_SKIP_LINT -ne "1") {
    $guardJobs += @{ Label = "lint"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/lint-all-modules.ps1", "-Changed") }
}
if ($env:AIP_SKIP_ARCHTEST -ne "1") {
    # Match the test-unit flags (make GO_TEST_FLAGS) so the standalone
    # quality-checks archtest run shares Go's build/test cache with
    # subsequent `make test`/`make qa` executions (see #291).
    $guardJobs += @{ Label = "archtest"; Command = @("go", "test", "-parallel=$script:TestParallel", "-timeout=10m") + $testPackageFlags + @("./internal/archtest/...") }
}
$runnerBinary = Get-TaskRunnerBinary
$sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
$pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, [Math]::Max(1, $guardJobs.Count), $sessionState, $Host)
$pool.Open()

$tasks = [System.Collections.Generic.List[PSObject]]::new()
try {
    foreach ($guard in $guardJobs) {
        $ps = [System.Management.Automation.PowerShell]::Create()
        $ps.RunspacePool = $pool
        [void]$ps.AddScript({
            param($runnerBinary, $repoRoot, $label, $cmdArgs)
            $runnerArgs = @(
                "--label", "quality-checks:$label",
                "--cwd", $repoRoot,
                "--timeout", "5m",
                "--output", "capture"
            )
            $runnerArgs += "--"
            $runnerArgs += $cmdArgs

            # Merged native stderr would terminate the run under the script's
            # Stop preference in Windows PowerShell; the exit code below is the
            # contract, so relax the preference for the duration of the call.
            $previousErrorActionPreference = $ErrorActionPreference
            try {
                $ErrorActionPreference = "Continue"
                $output = @(& $runnerBinary @runnerArgs 2>&1)
                $exitCode = $LASTEXITCODE
            } finally {
                $ErrorActionPreference = $previousErrorActionPreference
            }
            return @{
                Label = $label
                ExitCode = $exitCode
                Output = $output
            }
        }).AddArgument($runnerBinary).AddArgument($RepositoryRoot).AddArgument($guard.Label).AddArgument($guard.Command)

        $asyncResult = $ps.BeginInvoke()
        $tasks.Add([PSCustomObject]@{
            PowerShell = $ps
            AsyncResult = $asyncResult
            Label = $guard.Label
        })
    }

    $guardFailure = $false
    foreach ($task in $tasks) {
        $res = $task.PowerShell.EndInvoke($task.AsyncResult)
        $task.PowerShell.Dispose()
        if ($res) {
            $exitCode = $res[0].ExitCode
            $output = $res[0].Output
            if ($output) { $output | ForEach-Object { Write-Host $_ } }
            if ($exitCode -ne 0) {
                $guardFailure = $true
            }
        }
    }
    if ($guardFailure) {
        throw "one or more parallel quality guardrails failed"
    }
}
finally {
    $pool.Close()
    $pool.Dispose()
}
Write-Host ""

Write-Host "=== All Quality Checks Passed ===" -ForegroundColor Green
exit 0
