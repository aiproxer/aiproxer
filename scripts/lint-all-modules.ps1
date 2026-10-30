# scripts/lint-all-modules.ps1
# Runs golangci-lint across all or scoped Go modules in parallel.

[CmdletBinding()]
param(
    [switch]$Staged,
    [switch]$Changed,
    [switch]$Advisory,
    [switch]$Ratchet,
    [string]$Base = "",
    [string]$RatchetBase = "",
    [string[]]$Modules = @()
)

# Linters that are advisory for the canonical gate: they stay enabled in
# .golangci.yml for the full report (`-Advisory`, `make lint-advisory`) but are
# disabled by default so repository-wide style and complexity debt cannot block
# a release.
$AdvisoryLinters = @("modernize", "paralleltest", "thelper", "gocognit")

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Bound the module fan-out and each analyzer independently, matching POSIX.
$LintJobs = 2
$LintConcurrency = 2
foreach ($setting in @("AIP_LINT_JOBS", "AIP_LINT_CONCURRENCY")) {
    $value = [Environment]::GetEnvironmentVariable($setting, "Process")
    if ($value) {
        if ($value -notmatch '^[1-9][0-9]*$') { throw "$setting must be a positive integer" }
        if ($setting -eq "AIP_LINT_JOBS") { $LintJobs = [int]$value }
        else { $LintConcurrency = [int]$value }
    }
}

function Get-DiscoveredModules {
    $modules = [System.Collections.Generic.List[string]]::new()
    $modules.Add(".")
    $modules.Add("testdata/enterprise_module")
    $modules.Add("testdata/external_billing_binding")
    $modules.Add("testdata/external_connector")
    $modules.Add("testdata/external_feature_sdk")

    foreach ($base in @("connectors", "connector-support")) {
        $baseDir = Join-Path $RepositoryRoot $base
        if (Test-Path $baseDir) {
            Get-ChildItem -Path $baseDir -Directory | ForEach-Object {
                $modFile = Join-Path $_.FullName "go.mod"
                if (Test-Path $modFile) {
                    $rel = "$base/$($_.Name)" -replace '\\', '/'
                    $modules.Add($rel)
                }
            }
        }
    }
    return @($modules | Sort-Object -Unique)
}

function Get-TargetModules {
    if ($Modules -and $Modules.Count -gt 0) { return $Modules }
    return @(Get-DiscoveredModules)
}

$modulePackages = @{}
if (($Changed -or $Staged -or $Base) -and -not $Modules) {
    $scopeMode = if ($Base) { "base" } elseif ($Staged) { "staged" } else { "changed" }
    $scopeArgs = @("-mode", $scopeMode)
    if ($Base) { $scopeArgs += @("-base", $Base) }
    $scopeJSON = & go -C $RepositoryRoot run -buildvcs=false ./tools/lintscope @scopeArgs
    if ($LASTEXITCODE -ne 0) { throw "Local lint scope discovery failed; refusing to omit evidence." }
    $scopePlan = ($scopeJSON -join "`n") | ConvertFrom-Json
    if ($scopePlan.full) {
        $targetModules = @(Get-DiscoveredModules)
    } else {
        $targetModules = @($scopePlan.modules | ForEach-Object { $_.directory })
        foreach ($module in $scopePlan.modules) {
            $modulePackages[$module.directory] = @($module.packages)
        }
    }
} else {
    $targetModules = @(Get-TargetModules)
}
if ($targetModules.Count -eq 0) {
    Write-Host "No modules to lint." -ForegroundColor DarkGray
    exit 0
}

# This gate is mandatory: `make lint`, the staged pre-commit lint and CI all
# run it. Skipping when the analyzer is absent turns the gate into a silent
# no-op, and substituting staticcheck hides most of the analyzer set CI
# enforces. tools/devcheck applies the same rule for `make dev-lint`.
if (-not (Get-Command golangci-lint -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: golangci-lint is required for the repository lint gate but is not on PATH." -ForegroundColor Red
    Write-Host "Install the version pinned in .github/golangci-lint.version." -ForegroundColor Red
    exit 3
}
if ($Ratchet) {
    if (-not $Advisory) { throw "-Ratchet requires -Advisory." }
    if (-not $RatchetBase) { $RatchetBase = $Base }
    if (-not $RatchetBase) { throw "-Ratchet requires -RatchetBase <commit> (or -Base)." }
}
$linter = "golangci-lint"
$linterArgs = @("run", "--allow-parallel-runners", "--concurrency=$LintConcurrency")
if ($Advisory) {
    # Match POSIX: the report is uncapped and includes integration/precommit files.
    $linterArgs += @("--build-tags=integration,precommit", "--max-issues-per-linter=0", "--max-same-issues=0")
    if ($Ratchet) {
        $linterArgs += "--new-from-rev=$RatchetBase"
    } else {
        $linterArgs += "--issues-exit-code=0"
    }
} else {
    $linterArgs += "--disable=$($AdvisoryLinters -join ',')"
}

Write-Host "Linting $($targetModules.Count) module(s) with $linter in parallel..." -ForegroundColor Cyan
Write-Host "Lint budget: modules=$LintJobs analyzers/module=$LintConcurrency" -ForegroundColor DarkGray
if ($Advisory) {
    if ($Ratchet) {
        Write-Host "Mode: ADVISORY RATCHET (full set incl. $($AdvisoryLinters -join ', '); integration+precommit tags; only new findings since $RatchetBase; fails on new findings)." -ForegroundColor Yellow
    } else {
        Write-Host "Mode: ADVISORY (full set incl. $($AdvisoryLinters -join ', '); integration+precommit tags; uncapped; findings do not fail this report)." -ForegroundColor Yellow
    }
} else {
    Write-Host "Mode: MANDATORY correctness gate (--disable=$($AdvisoryLinters -join ',')); style and complexity debt via 'make lint-advisory'." -ForegroundColor Cyan
}

$runnerBinary = Get-TaskRunnerBinary
$sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
$pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, [Math]::Min($targetModules.Count, $LintJobs), $sessionState, $Host)
$pool.Open()

$tasks = [System.Collections.Generic.List[PSObject]]::new()
try {
    foreach ($mod in $targetModules) {
        $modDir = Join-Path $RepositoryRoot $mod
        $packages = if ($modulePackages.ContainsKey($mod)) { $modulePackages[$mod] } else { @("./...") }
        $moduleLintArgs = @($linterArgs) + @($packages)
        Write-Host "Scope ${mod}: $($packages -join ' ')"
        $ps = [System.Management.Automation.PowerShell]::Create()
        $ps.RunspacePool = $pool
        [void]$ps.AddScript({
            param($runnerBinary, $repoRoot, $mod, $modDir, $linter, $linterArgs, $lintConcurrency)
            $runnerArgs = @(
                "--label", "lint:$mod",
                "--cwd", $modDir,
                "--timeout", "5m",
                # The runner's capture mode retains only bounded head/tail
                # diagnostics. Stream instead so advisory evidence is complete.
                "--output", "stream"
            )
            $runnerArgs += "--"
            $runnerArgs += @($linter) + $linterArgs

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
                Module = $mod
                ExitCode = $exitCode
                Output = $output
            }
        }).AddArgument($runnerBinary).AddArgument($RepositoryRoot).AddArgument($mod).AddArgument($modDir).AddArgument($linter).AddArgument($moduleLintArgs).AddArgument($LintConcurrency)

        $asyncResult = $ps.BeginInvoke()
        $tasks.Add([PSCustomObject]@{
            PowerShell = $ps
            AsyncResult = $asyncResult
            Module = $mod
        })
    }

    $failed = $false
    foreach ($task in $tasks) {
        $res = $task.PowerShell.EndInvoke($task.AsyncResult)
        $task.PowerShell.Dispose()
        if ($res) {
            $exitCode = $res[0].ExitCode
            $output = $res[0].Output
            $mod = $res[0].Module
            if ($exitCode -ne 0) {
                $failed = $true
                Write-Host "FAILED: $mod" -ForegroundColor Red
                if ($output) { $output | ForEach-Object { Write-Host "  $_" -ForegroundColor Red } }
            } else {
                Write-Host "PASS: $mod" -ForegroundColor Green
                if ($output) { $output | ForEach-Object { Write-Host "  $_" } }
            }
        }
    }
    if ($failed) {
        throw "Linter found issues in one or more Go modules."
    }
} finally {
    $pool.Close()
    $pool.Dispose()
}

if ($Advisory) {
    if ($Ratchet) {
        Write-Host "OK: no advisory findings on lines changed since $RatchetBase." -ForegroundColor Green
    } else {
        Write-Host "OK: advisory report complete (uncapped; findings do not fail this report)." -ForegroundColor Green
    }
} else {
    Write-Host "OK: All checked Go modules passed linting." -ForegroundColor Green
}
exit 0
