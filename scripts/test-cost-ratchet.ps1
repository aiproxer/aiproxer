# Windows test-cost ratchet orchestrator.
#
# The current checkout is the head under test.  Only the detached anchor
# worktree is created and it is removed in the finally block.  Measurements,
# reports, and child-process logs stay under OutputRoot for CI upload or local
# inspection.  All external command arguments are arrays so paths containing
# spaces remain single arguments.

[CmdletBinding()]
param(
    [string]$BaseSHA,
    [string]$OutputRoot,
    [int]$Parallel = 0,
    [switch]$Preflight
)

$ErrorActionPreference = "Stop"

Set-StrictMode -Version Latest

$PolicyRelativePath = "scripts/test-cost-budget.json"
$Targets = @("test-unit", "quality-checks", "qa-tagged-hotspots")

function Test-IsWindows {
    return $env:OS -eq "Windows_NT" -or [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
}

function Test-Truthy {
    param([AllowNull()][string]$Value)
    return $Value -match "^(?i:1|true|yes|on)$"
}

function Get-RepositoryRoot {
    param([Parameter(Mandatory = $true)][string]$ScriptRoot)

    $candidate = Join-Path $ScriptRoot ".."
    return (Resolve-Path -LiteralPath $candidate).Path
}

function Convert-ToAbsolutePath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$RelativeBase
    )

    if ([IO.Path]::IsPathRooted($Path)) {
        return [IO.Path]::GetFullPath($Path)
    }
    return [IO.Path]::GetFullPath((Join-Path $RelativeBase $Path))
}

function Get-GitText {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    $output = @(& git @Arguments 2>$null)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed (exit $exitCode)"
    }
    return ($output -join "`n").Trim()
}

function Invoke-GitChecked {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $output = @(& git @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    foreach ($line in $output) {
        Write-Host $line
    }
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed (exit $exitCode)"
    }
}

function Test-CleanCheckout {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot)

    $status = @(Get-GitText @("-C", $RepositoryRoot, "status", "--porcelain=v1", "--untracked-files=all"))
    if ($status.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace(($status -join ""))) {
        Write-Host "Head checkout is not clean:" -ForegroundColor Red
        foreach ($line in $status) {
            Write-Host "  $line" -ForegroundColor Red
        }
        throw "head checkout must be clean before running the test-cost ratchet (CI checkouts must remain clean too)"
    }
}

function Resolve-Commit {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$Revision,
        [Parameter(Mandatory = $true)][string]$Description
    )

    $resolved = Get-GitText @("-C", $RepositoryRoot, "rev-parse", "--verify", "${Revision}^{commit}")
    if ([string]::IsNullOrWhiteSpace($resolved)) {
        throw "$Description does not resolve to a commit: $Revision"
    }
    return $resolved
}

function Test-PathAtCommit {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$RelativePath
    )

    $object = "${Commit}:$RelativePath"
    $null = @(& git -C $RepositoryRoot cat-file -e $object 2>$null)
    return $LASTEXITCODE -eq 0
}

function Test-PolicyProtection {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$HeadCommit,
        [AllowEmptyString()][string]$BaseRevision,
        [Parameter(Mandatory = $true)][bool]$AllowGrowth,
        [bool]$AllowPolicyUpdate = $false
    )

    if ([string]::IsNullOrWhiteSpace($BaseRevision)) {
        Write-Host "Policy protection: skipped (no -BaseSHA supplied)" -ForegroundColor DarkGray
        return
    }

    $baseCommit = Resolve-Commit $RepositoryRoot $BaseRevision "base revision"
    $policyAtBase = Test-PathAtCommit $RepositoryRoot $baseCommit $PolicyRelativePath
    if (-not $policyAtBase) {
        Write-Host "Policy protection: bootstrap allowed (budget file is absent at base $baseCommit)" -ForegroundColor Yellow
        return
    }

    $null = @(& git -C $RepositoryRoot diff --quiet $baseCommit $HeadCommit -- $PolicyRelativePath 2>$null)
    $diffExitCode = $LASTEXITCODE
    if ($diffExitCode -eq 0) {
        Write-Host "Policy protection: budget unchanged from base $baseCommit" -ForegroundColor Green
        return
    }
    if ($diffExitCode -ne 1) {
        throw "unable to compare $PolicyRelativePath with base $baseCommit (git diff exit $diffExitCode)"
    }

    if (-not $AllowGrowth -and -not $AllowPolicyUpdate) {
        throw "budget policy changed relative to -BaseSHA $baseCommit; set AIP_ALLOW_TEST_COST_POLICY_UPDATE=1 for an authorized policy update, or AIP_ALLOW_TEST_COST_GROWTH=1 to also authorize measurement overrides"
    }
    Write-Host "Policy protection: authorized budget update (measurement overrides=$AllowGrowth)" -ForegroundColor Yellow
}

function Get-TestParallel {
    param([int]$Requested)

    if ($Requested -lt 0) {
        throw "-Parallel must be zero (auto) or a positive integer"
    }
    if ($Requested -ge 1) {
        return $Requested
    }

    $fromEnvironment = 0
    if ([int]::TryParse([string]$env:AIP_TEST_PARALLEL, [ref]$fromEnvironment) -and $fromEnvironment -ge 1) {
        return $fromEnvironment
    }
    return [Math]::Max(1, [Environment]::ProcessorCount)
}

function Get-EffectiveOutputRoot {
    param(
        [AllowEmptyString()][string]$Requested,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot
    )

    if ([string]::IsNullOrWhiteSpace($Requested)) {
        $name = "ltc-" + [Guid]::NewGuid().ToString("N").Substring(0, 8)
        return [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) $name))
    }
    return Convert-ToAbsolutePath $Requested $RepositoryRoot
}

function Get-AnchorWorktreeRoot {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$AnchorCommit
    )

    # Go compiler entries include source paths. Reuse the pinned anchor's short
    # path across hosted runs; per-run TEMP/TMP and artifacts remain isolated.
    return Join-Path (Split-Path -Parent $RepositoryRoot) ("aip-testcost-anchor-" + $AnchorCommit.Substring(0, 8))
}

function Assert-OutputRoot {
    param(
        [Parameter(Mandatory = $true)][string]$OutputRoot,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot
    )

    $outputNormalized = [IO.Path]::GetFullPath($OutputRoot).TrimEnd([char[]]@("\", "/"))
    $repositoryNormalized = [IO.Path]::GetFullPath($RepositoryRoot).TrimEnd([char[]]@("\", "/"))
    if ([StringComparer]::OrdinalIgnoreCase.Equals($outputNormalized, $repositoryNormalized)) {
        throw "-OutputRoot must not be the repository root"
    }
    $repositoryPrefix = $repositoryNormalized + [IO.Path]::DirectorySeparatorChar
    if ($outputNormalized.StartsWith($repositoryPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "-OutputRoot must be outside the repository so the head checkout remains clean: $OutputRoot"
    }
    if (Test-Path -LiteralPath $OutputRoot -PathType Leaf) {
        throw "-OutputRoot points to a file: $OutputRoot"
    }
}

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    # Set TEMP/TMP only for this synchronous child invocation.  Restoring both
    # values and the location in finally prevents process-wide leakage while
    # ensuring taskrunner helpers built by each tree cannot cross-use a cache.
    $previousTemp = [Environment]::GetEnvironmentVariable("TEMP", "Process")
    $previousTmp = [Environment]::GetEnvironmentVariable("TMP", "Process")
    $gitConfigVariables = @(
        "GIT_CONFIG_COUNT",
        "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
        "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1"
    )
    $previousGitConfig = @{}
    foreach ($name in $gitConfigVariables) {
        $previousGitConfig[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    }
    $exitCode = 1
    try {
        $env:TEMP = $TempRoot
        $env:TMP = $TempRoot
        [Environment]::SetEnvironmentVariable("GIT_CONFIG_COUNT", "2", "Process")
        [Environment]::SetEnvironmentVariable("GIT_CONFIG_KEY_0", "core.autocrlf", "Process")
        [Environment]::SetEnvironmentVariable("GIT_CONFIG_VALUE_0", "false", "Process")
        [Environment]::SetEnvironmentVariable("GIT_CONFIG_KEY_1", "core.eol", "Process")
        [Environment]::SetEnvironmentVariable("GIT_CONFIG_VALUE_1", "lf", "Process")
        Push-Location -LiteralPath $WorkingDirectory
        try {
            $previousPreference = $ErrorActionPreference
            try {
                $ErrorActionPreference = "Continue"
                $output = @(& $FilePath @Arguments 2>&1)
                $exitCode = $LASTEXITCODE
            } finally {
                $ErrorActionPreference = $previousPreference
            }
            foreach ($line in $output) {
                Write-Host $line
            }
        } finally {
            Pop-Location
        }
    } finally {
        if ($null -eq $previousTemp) {
            Remove-Item Env:TEMP -ErrorAction SilentlyContinue
        } else {
            $env:TEMP = $previousTemp
        }
        if ($null -eq $previousTmp) {
            Remove-Item Env:TMP -ErrorAction SilentlyContinue
        } else {
            $env:TMP = $previousTmp
        }
        foreach ($name in $gitConfigVariables) {
            [Environment]::SetEnvironmentVariable($name, $previousGitConfig[$name], "Process")
        }
    }
    return [int]$exitCode
}

function Invoke-RequiredExternal {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    Write-Host "[$Label] $FilePath $($Arguments -join ' ')" -ForegroundColor DarkGray
    $exitCode = Invoke-External $FilePath $Arguments $WorkingDirectory $TempRoot
    if ($exitCode -ne 0) {
        throw "$Label failed (exit $exitCode)"
    }
}

function Warm-Tree {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][int]$TestParallel
    )

    # Module and compile warmups may add otherwise unnecessary go.sum entries.
    # Preserve the clean tree while retaining their populated shared caches.
    $moduleSnapshots = @{}
    foreach ($moduleFile in @("go.mod", "go.sum")) {
        $modulePath = Join-Path $TreeRoot $moduleFile
        $moduleSnapshots[$modulePath] = [IO.File]::ReadAllBytes($modulePath)
    }
    try {
        # Quality checks run go mod tidy inside the restricted taskrunner token.
        # Populate every module needed by that command before measurement so
        # network/module download time cannot enter the resource comparison.
        Invoke-RequiredExternal "$Label-modules" "go" @("mod", "download", "all") $TreeRoot $TempRoot

        # Keep this exact no-test warm-up separate from measured runs so the
        # anchor/head pair starts with the same compile cache shape.
        Invoke-RequiredExternal $Label "go" @("test", "-run", '^$', "-count=1", "-parallel=$TestParallel", "-timeout=10m", "./...") $TreeRoot $TempRoot

        # Warm exactly the tagged QA hotspot packages without running tests.
        Invoke-RequiredExternal "$Label-hotspots" "go" @("test", "-run", '^$', "-count=1", "-parallel=$TestParallel", "-timeout=10m", "-tags=precommit,integration", "./internal/archtest", "./internal/infra/runtimebundle", "./tools/backendplugin/...") $TreeRoot $TempRoot
    } finally {
        foreach ($modulePath in $moduleSnapshots.Keys) {
            [IO.File]::WriteAllBytes($modulePath, $moduleSnapshots[$modulePath])
        }
    }

    # internal/qa executes the real aipstd check-config command. Warm its
    # reusable executable link action for both trees, just as the package's
    # process-level cache does after the first developer iteration.
    $warmBinary = Join-Path $TempRoot "aipstd-warm.exe"
    Invoke-RequiredExternal "$Label-aipstd" "go" @("build", "-buildvcs=false", "-o", $warmBinary, "./cmd/aipstd") $TreeRoot $TempRoot
}

function Apply-AnchorCompatibilityPatch {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$AnchorRoot,
        [Parameter(Mandatory = $true)][string]$AnchorCommit,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    # The current frozen anchor has the same observability fixture's 50 ms
    # stall guard. Under Windows load it expired before an immediate fake
    # cancellation ran. Match the head's scheduler-safe test guard without
    # changing assertions, production sources, workloads or cost budgets.
    if ($AnchorCommit -eq "bb1ef9620ee6e8d9199950161e46fc51914945f2") {
        $relativePath = "internal/core/runtime/cancellation_observability_phase6_test.go"
        $path = Join-Path $AnchorRoot $relativePath
        $content = [IO.File]::ReadAllText($path)
        $old = "CancelTimeout: 50 * time.Millisecond"
        if ([regex]::Matches($content, [regex]::Escape($old)).Count -ne 5) {
            throw "frozen observability fixture changed: expected five known cancellation guards"
        }
        $content = $content.Replace($old, "CancelTimeout: time.Second")
        [IO.File]::WriteAllText($path, $content, [Text.UTF8Encoding]::new($false))
        Invoke-GitChecked @("-C", $AnchorRoot, "add", "--", $relativePath)
        Invoke-GitChecked @(
            "-C", $AnchorRoot, "-c", "user.name=AIProxer test-cost ratchet",
            "-c", "user.email=test-cost-ratchet@invalid.local", "-c", "commit.gpgsign=false",
            "commit", "-m", "test: stabilize frozen observability cancellation guards"
        )
        Test-CleanCheckout $AnchorRoot
        return
    }

    $bootstrapAnchor = "a7a00cedddc4e49d7f96502ee28a6ea1d9603315"
    if ($AnchorCommit -ne $bootstrapAnchor) {
        return
    }

    Write-Host "Anchor compatibility: applying performance and test compatibility patches to pinned PR #553 anchor" -ForegroundColor Yellow
    $correctionCommit = "1f69c577983cd60b03120ae855bc215e8e5138af"
    $null = Resolve-Commit $RepositoryRoot $correctionCommit "anchor compatibility correction"
    $patchText = Get-GitText @(
        "-C", $RepositoryRoot, "diff", $bootstrapAnchor, $correctionCommit, "--",
        "internal/testkit/dbparity/cmd/main_test.go",
        "internal/testkit/postgres_makefile_gate_test.go"
    )
    if (-not [string]::IsNullOrWhiteSpace($patchText)) {
        $patchPath = Join-Path ([IO.Path]::GetTempPath()) ("aip-testcost-anchor-compat-" + [Guid]::NewGuid().ToString("N") + ".patch")
        try {
            [IO.File]::WriteAllText($patchPath, $patchText + "`n", [Text.UTF8Encoding]::new($false))
            Invoke-GitChecked @("-C", $AnchorRoot, "apply", $patchPath)
        } finally {
            Remove-Item -LiteralPath $patchPath -Force -ErrorAction SilentlyContinue
        }
    }

    # The pinned anchor predates Windows load-stability and read-only QA fixes
    # needed when its full suite runs under the ratchet. Keep this compatibility
    # surface test/QA-only and explicit; production sources still come from the anchor.
    $loadCompatibilityPaths = @(
        "internal/stdhttp/request_plane_generation_test.go",
        "internal/infra/runtimebundle/publish_pinned_characterization_test.go",
        "internal/infra/runtimehost/observability_test.go",
        "internal/qa/dual_plane_migration_smoke_test.go",
        "internal/plugins/frontends/openresponses/websocket_upgrade_test.go",
        "scripts/quality-checks.ps1",
        "tools/changesize/main_test.go"
    )
    foreach ($relativePath in $loadCompatibilityPaths) {
        Copy-Item -LiteralPath (Join-Path $RepositoryRoot $relativePath) -Destination (Join-Path $AnchorRoot $relativePath) -Force
    }

    # Stage only the files intentionally changed by this compatibility patch.
    # The anchor is created with autocrlf disabled below; keeping this list
    # explicit also prevents a future checkout conversion from entering the
    # synthetic anchor commit.
    $compatibilityPaths = @(
        "internal/testkit/dbparity/cmd/main_test.go",
        "internal/testkit/postgres_makefile_gate_test.go",
        "go.sum"
    )
    $compatibilityPaths += $loadCompatibilityPaths
    foreach ($relativePath in ($compatibilityPaths | Where-Object { $_ -ne "go.sum" })) {
        $absolutePath = Join-Path $AnchorRoot $relativePath
        $content = [IO.File]::ReadAllText($absolutePath)
        [IO.File]::WriteAllText($absolutePath, $content.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
    }

    # Current Go may need checksums that the historical anchor never wrote.
    # Generate them outside measurement and commit only go.sum; a go.mod
    # change would alter the dependency graph and therefore fails closed.
    Invoke-RequiredExternal "anchor-compatibility-tidy" "go" @("mod", "tidy") $AnchorRoot $TempRoot
    $null = @(& git -C $AnchorRoot diff --quiet -- go.mod 2>$null)
    if ($LASTEXITCODE -ne 0) {
        throw "anchor compatibility go mod tidy unexpectedly changed go.mod"
    }
    $goSumPath = Join-Path $AnchorRoot "go.sum"
    $goSum = [IO.File]::ReadAllText($goSumPath)
    [IO.File]::WriteAllText($goSumPath, $goSum.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
    Invoke-GitChecked (@("-C", $AnchorRoot, "add", "--") + $compatibilityPaths)
    Invoke-GitChecked @(
        "-C", $AnchorRoot,
        "-c", "user.name=AIProxer test-cost ratchet",
        "-c", "user.email=test-cost-ratchet@invalid.local",
        "-c", "commit.gpgsign=false",
        "commit", "-m", "test: make pinned performance anchor executable on Windows"
    )
    Test-CleanCheckout $AnchorRoot
}

function Apply-CurrentAnchorTestCompatibilityPatch {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$AnchorRoot,
        [Parameter(Mandatory = $true)][string]$AnchorCommit
    )

    # Test-only compatibility copied from head for the active pinned anchor.
    # The anchor's late-arm assertion must wait for asynchronous terminalization
    # when the full suite saturates a Windows runner, and the anchor's
    # changesize negative test must stay hermetic under an inherited PR-wide
    # AIP_ALLOW_LARGE_CHANGE override. The frontendpipe saturation tests hold
    # their admission permit until a competing request is provably rejected, so
    # the anchor does not depend on overlapping goroutine scheduling under load.
    # The wire-counter timeout fake waits for cancellation instead of racing
    # two timers that can both be ready when a loaded Windows runner resumes.
    # The EOF fixture explicitly releases its source and permits keepalives
    # before EOF instead of assuming its reader always beats the idle timer.
    # Production sources still come from the anchor.
    $testCompatibilityPathsByAnchor = @{
        "6dbb831885341516117034923f0c3203373aded0" = @(
            "internal/core/runtime/parallel_race_late_arm_race_test.go"
        )
        "bb1ef9620ee6e8d9199950161e46fc51914945f2" = @(
            "tools/changesize/main_test.go",
            "internal/plugins/frontends/frontendpipe/candidate_proof_saturation_race_test.go",
            "internal/plugins/frontends/frontendpipe/candidate_assessment_saturation_race_test.go",
            "internal/core/runtime/wire_metering_composition_test.go",
            "internal/core/stream/keepalive_test.go"
        )
    }
    if (-not $testCompatibilityPathsByAnchor.ContainsKey($AnchorCommit)) {
        return
    }

    $relativePaths = $testCompatibilityPathsByAnchor[$AnchorCommit]
    foreach ($relativePath in $relativePaths) {
        Copy-Item -LiteralPath (Join-Path $RepositoryRoot $relativePath) -Destination (Join-Path $AnchorRoot $relativePath) -Force
        $absolutePath = Join-Path $AnchorRoot $relativePath
        $content = [IO.File]::ReadAllText($absolutePath)
        [IO.File]::WriteAllText($absolutePath, $content.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
    }
    Invoke-GitChecked (@("-C", $AnchorRoot, "add", "--") + $relativePaths)
    Invoke-GitChecked @(
        "-C", $AnchorRoot,
        "-c", "user.name=AIProxer test-cost ratchet",
        "-c", "user.email=test-cost-ratchet@invalid.local",
        "-c", "commit.gpgsign=false",
        "commit", "-m", "test: keep pinned anchor tests hermetic under the ratchet"
    )
    Test-CleanCheckout $AnchorRoot
}

function Build-TestCostBinary {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    Invoke-RequiredExternal "build-$Label-aip-testcost" "go" @("build", "-o", $BinaryPath, "./cmd/aip-testcost") $TreeRoot $TempRoot
}

function Measure-Tree {
    param(
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$Target,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$Revision,
        [Parameter(Mandatory = $true)][string]$MeasurementPath,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][int]$TestParallel
    )

    $arguments = @(
        "measure", "--target", $Target,
        "--root", $TreeRoot,
        "--revision", $Revision,
        "--out", $MeasurementPath,
        "--temp-root", $TempRoot,
        "--parallel", [string]$TestParallel
    )
    $previousQABinary = [Environment]::GetEnvironmentVariable("AIP_QA_AIPSTD_BINARY", "Process")
    try {
        $env:AIP_QA_AIPSTD_BINARY = Join-Path $TempRoot "aipstd-warm.exe"
        Invoke-RequiredExternal "measure-$Target-$Revision" $BinaryPath $arguments $TreeRoot $TempRoot
    } finally {
        [Environment]::SetEnvironmentVariable("AIP_QA_AIPSTD_BINARY", $previousQABinary, "Process")
    }
    if (-not (Test-Path -LiteralPath $MeasurementPath -PathType Leaf)) {
        throw "aip-testcost did not write measurement: $MeasurementPath"
    }
}

function Compare-TreePair {
    param(
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$Target,
        [Parameter(Mandatory = $true)][string]$BaselinePath,
        [Parameter(Mandatory = $true)][string]$CurrentPath,
        [Parameter(Mandatory = $true)][string]$PolicyPath,
        [Parameter(Mandatory = $true)][string]$ReportPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][bool]$AllowOverride
    )

    $arguments = @(
        "compare", "--target", $Target,
        "--baseline", $BaselinePath,
        "--current", $CurrentPath,
        "--policy", $PolicyPath,
        "--out", $ReportPath
    )
    if ($AllowOverride) {
        $arguments += "--allow-override"
    }

    Write-Host "[compare-$Target] validating budget with aip-testcost" -ForegroundColor DarkGray
    $exitCode = Invoke-External $BinaryPath $arguments $WorkingDirectory $TempRoot
    if ($exitCode -ne 0 -and $exitCode -ne 1) {
        throw "compare-$Target failed operationally (exit $exitCode)"
    }
    if (-not (Test-Path -LiteralPath $ReportPath -PathType Leaf)) {
        throw "aip-testcost did not write comparison report: $ReportPath"
    }

    $report = Get-Content -LiteralPath $ReportPath -Raw | ConvertFrom-Json
    return [PSCustomObject]@{
        Target = $Target
        Passed = [bool]$report.passed
        Overridden = [bool]$report.overridden
        Violations = @($report.violations).Count
        Warnings = @($report.warnings).Count
        ExitCode = [int]$exitCode
        Report = $ReportPath
    }
}

function Write-FinalTables {
    param(
        [Parameter(Mandatory = $true)][string]$OutputRoot,
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Measurements,
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Comparisons
    )

    Write-Host ""
    Write-Host "=== Test-cost ratchet artifacts ===" -ForegroundColor Cyan
    Write-Host ("{0,-12} {1,-8} {2}" -f "tree", "target", "measurement")
    foreach ($measurement in $Measurements) {
        Write-Host ("{0,-12} {1,-8} {2}" -f $measurement.Tree, $measurement.Target, $measurement.Path)
    }

    Write-Host ""
    Write-Host "=== Test-cost ratchet comparisons ===" -ForegroundColor Cyan
    Write-Host ("{0,-16} {1,-7} {2,-10} {3,-8} {4,-8} {5}" -f "target", "passed", "overridden", "violations", "warnings", "report")
    foreach ($comparison in $Comparisons) {
        Write-Host ("{0,-16} {1,-7} {2,-10} {3,-8} {4,-8} {5}" -f $comparison.Target, $comparison.Passed, $comparison.Overridden, $comparison.Violations, $comparison.Warnings, $comparison.Report)
    }
    Write-Host "OutputRoot: $OutputRoot" -ForegroundColor Green
}

if (-not (Test-IsWindows)) {
    throw "test-cost ratchet is Windows-only"
}

$RepositoryRoot = Get-RepositoryRoot $PSScriptRoot
$PolicyPath = Join-Path $RepositoryRoot $PolicyRelativePath
if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf)) {
    throw "budget policy is missing: $PolicyPath"
}

Test-CleanCheckout $RepositoryRoot
$HeadCommit = Resolve-Commit $RepositoryRoot "HEAD" "head revision"
$PolicyDocument = Get-Content -LiteralPath $PolicyPath -Raw | ConvertFrom-Json
$AnchorRevision = [string]$PolicyDocument.anchor_ref
if ([string]::IsNullOrWhiteSpace($AnchorRevision)) {
    throw "budget policy anchor_ref is empty: $PolicyPath"
}
$AnchorCommit = Resolve-Commit $RepositoryRoot $AnchorRevision "anchor revision"
$allowOverride = Test-Truthy $env:AIP_ALLOW_TEST_COST_GROWTH
$allowPolicyUpdate = Test-Truthy $env:AIP_ALLOW_TEST_COST_POLICY_UPDATE
Test-PolicyProtection $RepositoryRoot $HeadCommit $BaseSHA $allowOverride $allowPolicyUpdate
$TestParallel = Get-TestParallel $Parallel
$EffectiveOutputRoot = Get-EffectiveOutputRoot $OutputRoot $RepositoryRoot
Assert-OutputRoot $EffectiveOutputRoot $RepositoryRoot

if ($Preflight) {
    Write-Host "OK: test-cost ratchet preflight" -ForegroundColor Green
    Write-Host "  head:     $HeadCommit"
    Write-Host "  anchor:   $AnchorCommit ($AnchorRevision)"
    Write-Host "  targets:  $($Targets -join ', ')"
    Write-Host "  parallel: $TestParallel"
    Write-Host "  output:   $EffectiveOutputRoot"
    exit 0
}

New-Item -ItemType Directory -Path $EffectiveOutputRoot -Force | Out-Null
$measurementRoot = Join-Path $EffectiveOutputRoot "measurements"
$reportRoot = Join-Path $EffectiveOutputRoot "reports"
$binaryRoot = Join-Path $EffectiveOutputRoot "binaries"
New-Item -ItemType Directory -Path $measurementRoot, $reportRoot, $binaryRoot -Force | Out-Null

$runID = [Guid]::NewGuid().ToString("N")
# Keep the checkout path short: the pinned anchor contains tracked paths that
# exceed legacy Win32 MAX_PATH when nested under a long runner TEMP directory.
$anchorRoot = Get-AnchorWorktreeRoot $RepositoryRoot $AnchorCommit
$anchorTempRoot = Join-Path $EffectiveOutputRoot ("a-" + $runID.Substring(0, 8))
$headTempRoot = Join-Path $EffectiveOutputRoot ("h-" + $runID.Substring(0, 8))
if (Test-Path -LiteralPath $anchorRoot) {
    throw "refusing to use existing anchor worktree path: $anchorRoot"
}
New-Item -ItemType Directory -Path $anchorTempRoot, $headTempRoot -Force | Out-Null

$testCostBinary = Join-Path $binaryRoot "aip-testcost.exe"
$anchorCreated = $false
$ratchetFailed = $false
$fatalFailure = $null
$measurements = [System.Collections.Generic.List[object]]::new()
$comparisons = [System.Collections.Generic.List[object]]::new()

try {
    # Create exactly one worktree: the detached anchor.  The current checkout
    # remains the head, so its caller-owned worktree is never removed.
    Invoke-GitChecked @(
        "-C", $RepositoryRoot,
        "-c", "core.autocrlf=false",
        "-c", "core.eol=lf",
        "worktree", "add", "--detach", $anchorRoot, $AnchorCommit
    )
    $anchorCreated = $true
    Apply-AnchorCompatibilityPatch $RepositoryRoot $anchorRoot $AnchorCommit $anchorTempRoot
    Apply-CurrentAnchorTestCompatibilityPatch $RepositoryRoot $anchorRoot $AnchorCommit

    # The committed anchor predates the ratchet tool itself. Build the neutral
    # measurement wrapper once from head, then point it at each source tree.
    # The commands being measured still come from their respective trees; in
    # particular, isolated TEMP/TMP roots keep each quality-checks.ps1 copy's
    # cached aip-taskrunner.exe source-correct.
    Build-TestCostBinary "head" $RepositoryRoot $testCostBinary $headTempRoot

    # Warm in the required order before any measured target: anchor, then head.
    Warm-Tree "warm-anchor" $anchorRoot $anchorTempRoot $TestParallel
    Warm-Tree "warm-head" $RepositoryRoot $headTempRoot $TestParallel

    foreach ($target in $Targets) {
        # Each target is a complete anchor/head pair before moving to the next
        # target: test-unit pair first, then quality-checks pair.
        $anchorMeasurement = Join-Path $measurementRoot ("anchor-$target.json")
        $headMeasurement = Join-Path $measurementRoot ("head-$target.json")
        Measure-Tree $testCostBinary $target $anchorRoot $AnchorCommit $anchorMeasurement $anchorTempRoot $TestParallel
        $measurements.Add([PSCustomObject]@{ Tree = "anchor"; Target = $target; Path = $anchorMeasurement })
        Measure-Tree $testCostBinary $target $RepositoryRoot $HeadCommit $headMeasurement $headTempRoot $TestParallel
        $measurements.Add([PSCustomObject]@{ Tree = "head"; Target = $target; Path = $headMeasurement })

        $reportPath = Join-Path $reportRoot ("$target.json")
        $comparison = Compare-TreePair $testCostBinary $target $anchorMeasurement $headMeasurement $PolicyPath $reportPath $RepositoryRoot $headTempRoot $allowOverride
        $comparisons.Add($comparison)
        if (-not $comparison.Passed) {
            $ratchetFailed = $true
        }
    }

    # The orchestrator itself must not leave the caller's head dirty.  This
    # also catches quality scripts that unexpectedly mutate tracked files.
    Test-CleanCheckout $RepositoryRoot
} catch {
    $fatalFailure = $_
} finally {
    Remove-Item -LiteralPath (Join-Path $anchorTempRoot "aipstd-warm.exe") -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $headTempRoot "aipstd-warm.exe") -Force -ErrorAction SilentlyContinue
    if ($anchorCreated) {
        try {
            # This is the only cleanup mutation: remove the worktree created by
            # this invocation.  OutputRoot and all logs/reports are retained.
            Invoke-GitChecked @("-C", $RepositoryRoot, "worktree", "remove", "--force", $anchorRoot)
        } catch {
            Write-Host "ERROR: failed to remove created anchor worktree $anchorRoot : $($_.Exception.Message)" -ForegroundColor Red
            if ($null -eq $fatalFailure) {
                $fatalFailure = $_
            }
        }
    }
}

Write-FinalTables $EffectiveOutputRoot $measurements $comparisons

if ($null -ne $fatalFailure) {
    Write-Host "ERROR: test-cost ratchet did not complete: $($fatalFailure.Exception.Message)" -ForegroundColor Red
    Write-Host "Artifacts retained at: $EffectiveOutputRoot" -ForegroundColor Yellow
    exit 1
}
if ($ratchetFailed) {
    Write-Host "ERROR: one or more test-cost budgets exceeded; reports and logs were retained" -ForegroundColor Red
    Write-Host "Artifacts retained at: $EffectiveOutputRoot" -ForegroundColor Yellow
    exit 1
}

Write-Host "OK: test-cost ratchet passed" -ForegroundColor Green
exit 0
