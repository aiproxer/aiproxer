param(
    [Parameter(Mandatory = $true)][string]$Target
)

. "$PSScriptRoot/taskrunner.ps1"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Test parallelism defaults to the machine's logical core count so t.Parallel-
# heavy suites are not capped below available cores; override AIP_TEST_PARALLEL=<n>
# (mirrors GO_TEST_FLAGS in the Makefile).
function Get-TestParallel {
    $value = 0
    if ([int]::TryParse([string]$env:AIP_TEST_PARALLEL, [ref]$value) -and $value -ge 1) { return $value }
    if ([int]::TryParse([string]$env:NUMBER_OF_PROCESSORS, [ref]$value) -and $value -ge 1) { return $value }
    return 8
}
$testParallel = Get-TestParallel
$goTestFlags = @("-parallel=$testParallel", "-timeout=10m")
if ($env:AIP_TEST_PACKAGES) {
    if ($env:AIP_TEST_PACKAGES -notmatch '^[1-9][0-9]*$') { throw "AIP_TEST_PACKAGES must be a positive integer" }
    $goTestFlags += "-p=$($env:AIP_TEST_PACKAGES)"
}

# qa-tests admits the exhaustive billing certification sweep, which measures at
# ~90% of the default 10m budget on an otherwise idle host. CI already isolates
# that sweep behind BILLING_SCHEMA_TIMEOUT via `make test-billing-schema`, so the
# wide gate must carry the same budget or it fails on cost rather than on a
# defect. This mirrors QA_TESTS_GO_TEST_FLAGS in the Makefile exactly: only the
# -timeout value is replaced, and every other flag, package list, and test
# selection stays unchanged.
# Without it the same suite would pass on POSIX and fail on Windows purely on
# host speed, which is not a portability signal.
function Get-QaTestsGoTestFlags {
    param([string[]]$Flags, [string]$BillingSchemaTimeout)
    $replaced = @()
    foreach ($flag in $Flags) {
        if ($flag -match '^-timeout=') { continue }
        $replaced += $flag
    }
    $replaced += "-timeout=$BillingSchemaTimeout"
    return $replaced
}

# The taskrunner process timeout must stay strictly larger than the inner Go
# test timeout. If both are equal, Go's per-package deadline and the supervisor
# kill race: a suite can be terminated before Go reports which test exceeded its
# budget. The five-minute margin lets Go fail first and flush its output, while
# still bounding a hung child.
function Get-QaTestsTaskTimeout {
    param([string]$BillingSchemaTimeout)
    $durationPattern = '(\d+(?:\.\d+)?|\.\d+)(ns|us|ms|s|m|h)'
    $matches = [regex]::Matches($BillingSchemaTimeout, $durationPattern)
    $matchedLength = 0
    $innerMinutes = 0
    foreach ($match in $matches) {
        $matchedLength += $match.Length
        $amount = [double]$match.Groups[1].Value
        switch ($match.Groups[2].Value) {
            'ns' { $innerMinutes += $amount / 60000000000 }
            'us' { $innerMinutes += $amount / 60000000 }
            'ms' { $innerMinutes += $amount / 60000 }
            's' { $innerMinutes += $amount / 60 }
            'm' { $innerMinutes += $amount }
            'h' { $innerMinutes += $amount * 60 }
        }
    }
    if ($matches.Count -eq 0 -or $matchedLength -ne $BillingSchemaTimeout.Length) {
        throw "BILLING_SCHEMA_TIMEOUT must be a positive Go duration, such as 30m (got '$BillingSchemaTimeout')"
    }
    if ($innerMinutes -le 0) {
        throw "BILLING_SCHEMA_TIMEOUT must be a positive Go duration, such as 30m (got '$BillingSchemaTimeout')"
    }
    $outerMinutes = [math]::Ceiling($innerMinutes + 5)
    return "$outerMinutes" + "m"
}
$localGoEnv = if ($env:AIP_DISABLE_VCS_STAMPING -eq "1" -and -not $env:GOFLAGS) { @("GOFLAGS=-buildvcs=false") } else { @() }

function Run-RootGoTest {
    param([string]$Label, [string[]]$TestArgs, [string[]]$Env = @(), [string]$Timeout = "15m")
    Invoke-TaskRunner -Label $Label -Cwd $root -Timeout $Timeout -Env (@($localGoEnv) + @($Env)) -Command (@("go", "test") + $TestArgs) | Out-Host
}

function Run-NestedGoTest {
    param([string]$Label, [string]$Directory, [string[]]$TestArgs, [string]$Timeout = "15m")
    Invoke-TaskRunner -Label $Label -Cwd (Join-Path $root $Directory) -Timeout $Timeout -Env (@($localGoEnv) + @("GOWORK=off")) -Command (@("go", "test") + $TestArgs + @("./...")) | Out-Host
}

function Run-RootGoTestWithMatches {
    param([string]$Label, [string]$Package, [string]$Pattern, [string[]]$TestArgs, [string[]]$Env = @(), [string]$Timeout = "15m")
    $listResult = Invoke-TaskRunner -Label "${Label}:list" -Cwd $root -Timeout $Timeout -Env (@($localGoEnv) + @($Env)) -Output capture -Command (@("go", "test", "-tags=integration", "-list", $Pattern, $Package))
    $matches = @($listResult | Where-Object { $_ -match '^Test[A-Za-z0-9_]+$' })
    if ($matches.Count -eq 0) {
        throw "$Label selector $Pattern matched zero tests in $Package"
    }
    Run-RootGoTest $Label $TestArgs $Env $Timeout
}

# Bounded-concurrency wrapper around Invoke-TaskRunner for independent batches
# (nested modules, fuzz targets). Tasks are hashtables with Label/Cwd/Timeout/
# Env/Command keys. All tasks run even when some fail; collected failures throw
# at the end. Uses a runspace pool (lighter than Start-Job child processes and
# Windows PowerShell 5.1 compatible) and one prebuilt aip-taskrunner binary
# shared by every pooled invocation.
function Invoke-PooledTaskRunner {
    param(
        [Parameter(Mandatory = $true)][object[]]$Tasks,
        [Parameter(Mandatory = $true)][int]$MaxJobs
    )

    if ($Tasks.Count -eq 0) { return }

    $runnerBinary = Get-TaskRunnerBinary
    $sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
    $pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, ([Math]::Max(1, $MaxJobs)), $sessionState, $Host)
    $pool.Open()

    $invocationScript = {
        param($binaryPath, $task)
        $runnerArgs = @(
            "--label", $task.Label,
            "--cwd", $task.Cwd,
            "--timeout", $task.Timeout,
            "--output", "capture"
        )
        foreach ($e in $task.Env) { $runnerArgs += @("--env", $e) }
        $runnerArgs += "--"
        $runnerArgs += $task.Command

        $output = @(& $binaryPath @runnerArgs 2>&1)
        return @{ ExitCode = $LASTEXITCODE; Output = $output }
    }

    $pending = [System.Collections.Generic.Queue[object]]::new()
    foreach ($task in $Tasks) { $pending.Enqueue($task) }

    $inFlight = [System.Collections.Generic.List[object]]::new()
    $failedLabels = [System.Collections.Generic.List[string]]::new()

    try {
        while ($pending.Count -gt 0 -or $inFlight.Count -gt 0) {
            while ($pending.Count -gt 0 -and $inFlight.Count -lt $MaxJobs) {
                $task = $pending.Dequeue()
                Write-Host "  started $($task.Label)"
                $ps = [System.Management.Automation.PowerShell]::Create()
                $ps.RunspacePool = $pool
                [void]$ps.AddScript($invocationScript).AddArgument($runnerBinary).AddArgument($task)
                $inFlight.Add([PSCustomObject]@{ PS = $ps; Label = $task.Label; Result = $ps.BeginInvoke() })
            }

            if ($inFlight.Count -eq 0) { break }

            # Incremental drain: report each batch as it finishes instead of
            # buffering everything until the whole pool completes.
            $done = @($inFlight | Where-Object { $_.Result.IsCompleted })
            if ($done.Count -eq 0) {
                Start-Sleep -Milliseconds 200
                continue
            }
            foreach ($entry in $done) {
                $inFlight.Remove($entry) | Out-Null
                $exitCode = 1
                $output = @()
                try {
                    $res = $entry.PS.EndInvoke($entry.Result)
                    if ($res -and $res.Count -gt 0) {
                        $exitCode = [int]$res[0].ExitCode
                        $output = $res[0].Output
                    }
                } catch {
                    Write-Host ("ERROR {0}: {1}" -f $entry.Label, ($_ | Out-String).Trim()) -ForegroundColor Red
                } finally {
                    $entry.PS.Dispose()
                }
                Write-Host "--- $($entry.Label) ---"
                foreach ($line in $output) { Write-Host $_ }
                if ($exitCode -ne 0) {
                    Write-Host "FAILED: $($entry.Label)" -ForegroundColor Red
                    $failedLabels.Add($entry.Label)
                }
            }
        }
    } finally {
        foreach ($entry in $inFlight) { try { $entry.PS.Dispose() } catch { } }
        $pool.Close()
        $pool.Dispose()
    }

    if ($failedLabels.Count -gt 0) {
        throw ("pooled tasks failed ({0}): {1}" -f $failedLabels.Count, ($failedLabels -join ", "))
    }
}

function Run-Parity {
    switch ($Target) {
        "parity-acp-plugin" {
            Run-NestedGoTest "parity-acp-plugin:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted"))
            Run-NestedGoTest "parity-acp-plugin:connectors/acp:test" "connectors/acp" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_"))
        }
        "parity-cursorcliacp-plugin" {
            Run-NestedGoTest "parity-cursorcliacp-plugin:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile"))
            Run-NestedGoTest "parity-cursorcliacp-plugin:connectors/cursorcliacp:test" "connectors/cursorcliacp" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_"))
        }
        "parity-cli-acp-plugins" {
            foreach ($module in @("connectors/geminicliacp", "connectors/agycliacp", "connectors/cursorcliacp", "connectors/claudeagentacp")) {
                Run-NestedGoTest "parity-cli-acp-plugins:$module:test" $module (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_"))
            }
        }
        "parity-openrouter-plugin" {
            Run-NestedGoTest "parity-openrouter-plugin:connector-support/openaicompat:test" "connector-support/openaicompat" $goTestFlags
            Run-NestedGoTest "parity-openrouter-plugin:connectors/openrouter:test" "connectors/openrouter" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestBilling_"))
            Run-RootGoTest "parity-openrouter-plugin:internal/archtest:test" (@($goTestFlags) + @("-run", "OpenRouter|Phase7_"))
            Run-RootGoTest "parity-openrouter-plugin:internal/infra/runtimebundle:test" (@($goTestFlags) + @("-run", "TestPhase7_OpenRouter"))
        }
        "parity-hosted-compatible-plugins" {
            foreach ($module in @("connectors/nvidia", "connectors/huggingface")) {
                Run-NestedGoTest "parity-hosted-compatible-plugins:$module:test" $module (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_"))
            }
            Run-RootGoTest "parity-hosted-compatible-plugins:internal/archtest:test" (@($goTestFlags) + @("-run", "Phase7_"))
        }
        "parity-ollama-plugins" { Run-NestedGoTest "parity-ollama-plugins:connectors/ollama:test" "connectors/ollama" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
        "parity-opencode-plugins" {
            Run-NestedGoTest "parity-opencode-plugins:connectors/opencode:test" "connectors/opencode" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_"))
            Run-RootGoTest "parity-opencode-plugins:internal/archtest:test" (@($goTestFlags) + @("-run", "OpenCode|Phase8_"))
        }
        "parity-codex-plugins" {
            Run-NestedGoTest "parity-codex-plugins:connectors/codex:test" "connectors/codex" $goTestFlags
            Run-RootGoTest "parity-codex-plugins:internal/archtest:test" (@($goTestFlags) + @("./internal/archtest", "-run", "Codex|Phase8_.*Codex|TestCodex_"))
            Run-RootGoTest "parity-codex-plugins:internal/infra/runtimebundle:test" (@($goTestFlags) + @("./internal/infra/runtimebundle", "-run", "TestPhase8_Codex"))
        }
        "test-local-compatible-plugin-modules" { Run-LocalCompatibleModules }
        "parity-local-compatible-plugins" {
            Run-LocalCompatibleModules
            Run-RootGoTest "parity-local-compatible-plugins:internal/archtest:test" (@($goTestFlags) + @("./internal/archtest", "-run", "Phase7_"))
        }
    }
}

function Run-NestedGoTestsInParallel {
    param(
        [Parameter(Mandatory = $true)][array]$Items,
        [string]$Timeout = "15m"
    )
    $tasks = @()
    foreach ($item in $Items) {
        $tasks += @{
            Label   = $item.Label
            Cwd     = (Join-Path $root ($item.Directory -replace '/', '\'))
            Timeout = $Timeout
            Env     = (@($localGoEnv) + @("GOWORK=off"))
            Command = (@("go", "test") + @($item.TestArgs) + @("./..."))
        }
    }
    # Cap concurrent module batches: each batch itself fans out to
    # -parallel=$testParallel goroutines internally, so a larger pool would
    # oversubscribe the machine.
    $maxJobs = [Math]::Min(4, [Math]::Max(2, $Items.Count))
    Invoke-PooledTaskRunner -Tasks $tasks -MaxJobs $maxJobs
}

function Run-LocalCompatibleModules {
    $modules = @(
        @{ Label = "test-local-compatible-plugin-modules:connectors/llamacpp:test"; Directory = "connectors/llamacpp"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
        @{ Label = "test-local-compatible-plugin-modules:connectors/lmstudio:test"; Directory = "connectors/lmstudio"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
        @{ Label = "test-local-compatible-plugin-modules:connectors/vllm:test"; Directory = "connectors/vllm"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
    )
    Run-NestedGoTestsInParallel $modules
}

function Run-Fuzz {
    # Canonical target list shared with the POSIX runner (scripts/fuzz-smoke.sh).
    $targetsPath = Join-Path $PSScriptRoot "fuzz-targets.tsv"
    if (-not (Test-Path -LiteralPath $targetsPath)) { throw "fuzz target list not found: $targetsPath" }
    $targets = @(Import-Csv -LiteralPath $targetsPath -Delimiter "`t" | Where-Object { $_.name })
    if ($targets.Count -eq 0) { throw "fuzz target list is empty: $targetsPath" }

    $fuzzTime = if ($env:FUZZTIME) { $env:FUZZTIME } else { "500ms" }
    $jobsAllowed = [math]::Floor($testParallel / 2)
    if ($jobsAllowed -lt 2) { $jobsAllowed = 2 }
    if ($jobsAllowed -gt 8) { $jobsAllowed = 8 }
    if ($env:AIP_FUZZ_JOBS -match '^\d+$' -and [int]$env:AIP_FUZZ_JOBS -ge 1) { $jobsAllowed = [int]$env:AIP_FUZZ_JOBS }

    # The pool already occupies the machine with concurrent targets, so each
    # fuzz process gets a small worker slice instead of the GOMAXPROCS default.
    $workerParallel = 2

    Write-Host "Fuzz smoke (FUZZTIME=$fuzzTime): $jobsAllowed concurrent targets x $workerParallel workers each"

    $tasks = @()
    foreach ($item in $targets) {
        $cwd = $root
        $taskEnv = @()
        if ($item.module) {
            $cwd = Join-Path $root ($item.module -replace '/', '\')
            $taskEnv = @("GOWORK=off")
        }
        $tasks += @{
            Label   = "test-fuzz:$($item.name)"
            Cwd     = $cwd
            Timeout = "10m"
            Env     = (@($localGoEnv) + @($taskEnv))
            Command = @("go", "test", "-fuzz=^$($item.name)$", "-fuzztime=$fuzzTime", "-run=^$", "-parallel=$workerParallel", $item.package)
        }
    }

    Invoke-PooledTaskRunner -Tasks $tasks -MaxJobs $jobsAllowed
}

switch -Regex ($Target) {
    "^test-unit$" { Run-RootGoTest "test-unit:root" (@($goTestFlags) + @("./...")); break }
    "^qa-tests$" {
        $envOverride = @("AIP_TEST_POSTGRES_DSN=", "AIP_TEST_POSTGRES_ADMIN_DSN=", "AIP_MANAGED_POSTGRES_DSN=", "AIP_MIGRATION_POSTGRES_DSN=")
        $billingSchemaTimeout = if ($env:BILLING_SCHEMA_TIMEOUT) { $env:BILLING_SCHEMA_TIMEOUT } else { "30m" }
        $qaTestFlags = Get-QaTestsGoTestFlags -Flags $goTestFlags -BillingSchemaTimeout $billingSchemaTimeout
        $qaTestTimeout = Get-QaTestsTaskTimeout -BillingSchemaTimeout $billingSchemaTimeout
        if ($env:AIP_SKIP_QA_TESTS -match '^(?i:1|true|yes|on)$') {
            Write-Host "Skipping duplicate root tests pass (AIP_SKIP_QA_TESTS=1); running tagged precommit/integration delta packages only..." -ForegroundColor DarkGray
            Run-RootGoTest "qa-tests:precommit-delta" (@($qaTestFlags) + @("-tags=precommit,integration", "./internal/qa/...", "./internal/core/runtime/...", "./internal/stdhttp/...", "./internal/testkit/conformance/...", "./tools/backendplugin/...", "./enterprise/internal/core/billing/...")) $envOverride $qaTestTimeout
            break
        }
        Run-RootGoTest "qa-tests:root" (@($qaTestFlags) + @("-tags=precommit,integration", "./...")) $envOverride $qaTestTimeout
        break
    }
    "^test-fuzz$" { Run-Fuzz; break }
    "^parity-checks$" {
        # The three identically-flagged root contract batches run as one go
        # test invocation: single tool startup plus cross-package pipelining.
        Run-RootGoTest "parity-checks:contract-tcks" (@($goTestFlags) + @(
                "./internal/testkit/contract/...",
                "./internal/providerprofiles/...",
                "./pkg/aipsdk/backendplugin/contracttest/..."
            ))
        Run-RootGoTest "parity-checks:conformance" (@($goTestFlags) + @("-tags=precommit,integration", "./internal/testkit/conformance/..."))
        Run-RootGoTest "parity-checks:compatible" (@($goTestFlags) + @("./internal/testkit/compatibleparity/...", "-run", "CompatibleParity"))

        $nestedSuites = @(
            @{ Label = "parity-checks:connector-support/acp"; Directory = "connector-support/acp"; TestArgs = (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted")) },
            @{ Label = "parity-checks:connectors/acp"; Directory = "connectors/acp"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_")) },
            @{ Label = "parity-checks:connector-support/openaicompat"; Directory = "connector-support/openaicompat"; TestArgs = $goTestFlags },
            @{ Label = "parity-checks:connectors/openrouter"; Directory = "connectors/openrouter"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestBilling_")) },
            @{ Label = "parity-checks:connectors/nvidia"; Directory = "connectors/nvidia"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
            @{ Label = "parity-checks:connectors/huggingface"; Directory = "connectors/huggingface"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
        )
        Run-NestedGoTestsInParallel $nestedSuites

        Run-RootGoTest "parity-checks:sentinel" (@($goTestFlags) + @("-tags=integration", "./internal/testkit/conformance", "-run", "^TestBoundedSentinel")); break
    }
    "^parity-|^test-local-compatible-plugin-modules$" {
        Run-Parity
        break
    }
    "^backend-plugin-security-checks$" {
        foreach ($pkg in @("./internal/infra/backendplugins/...", "./pkg/aipsdk/backendplugin/...", "./internal/infra/diagredact/...")) { Run-RootGoTest "backend-plugin-security-checks:$pkg" (@($goTestFlags) + @($pkg)) }
        Run-RootGoTest "backend-plugin-security-checks:runtimebundle" (@($goTestFlags) + @("./internal/infra/runtimebundle/", "-run", "TestBuild_localOnly|TestBuild_unknownBackendCredential|TestBuild_oauthUser|TestBuild_unsupportedBackend|TestBuild_staticBackend|TestBuild_noneBackend|TestBuild_strictAuthoritative"))
        Run-RootGoTest "backend-plugin-security-checks:docs" (@($goTestFlags) + @("./docs/backend-plugins/", "-run", "TestThreat|TestOperator_|TestDocs_"))
        break
    }
    "^backend-plugin-cross-platform-qa$" {
        $selectArgs = @()
        if ($env:CROSS_PLATFORM_SKIP_COMPILE -eq "1") { $selectArgs += "-skip-compile" }
        if ($env:CROSS_PLATFORM_SELECT) { $selectArgs += @("-select", $env:CROSS_PLATFORM_SELECT) }
        # Do not pass -skip-native: the tool owns the mandatory process-host
        # selection (flooding budget, capture, termination and reap). Skipping
        # it makes a green Windows run prove nothing about those guarantees.
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "20m" -Env $localGoEnv -Command (@("go", "run", "./tools/backendplugin/crossplatform_qa", "-root", ".", "-out", ".aip-crossplatform-matrix.json") + $selectArgs) | Out-Host
        Run-RootGoTest "backend-plugin-cross-platform-qa:backendplugins" (@($goTestFlags) + @("./internal/infra/backendplugins/...", "-run", "TestAdversarial_|TestActivate_|TestStream_|TestDigest|TestManifest|TestDiscover|TestShutdown|TestReap|TestPeer|TestChannel|TestExact|TestUpgrade|TestRollback|TestUninstall|TestConfig|TestSecrecy|TestUnauthorized|TestProtected|TestLaunch|TestKill|TestCancel"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:backendplugin-sdk" (@($goTestFlags) + @("./pkg/aipsdk/backendplugin/...", "-run", "Test"))
        Run-NestedGoTest "backend-plugin-cross-platform-qa:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|Cancel"))
        Run-RootGoTestWithMatches "backend-plugin-cross-platform-qa:tools" "./tools/backendplugin/" "TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_" (@($goTestFlags) + @("-tags=integration", "./tools/backendplugin/", "-run", "TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:archtest" (@($goTestFlags) + @("./internal/archtest/", "-run", "TestBackendPluginCrossPlatform_"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:processhost" (@($goTestFlags) + @("./internal/infra/backendplugins/processhost/", "-run", "TestHostSecureProfiles_"))
        break
    }
    "^backend-plugin-release-gates-static$" {
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "15m" -Command @("go", "run", "./tools/backendplugin/release_gates", "-root", ".", "-out", ".aip-release-gates-report.json", "-mode=static") | Out-Host
        Run-RootGoTestWithMatches "backend-plugin-release-gates-static:tools" "./tools/backendplugin/" "TestReleaseGates_" (@($goTestFlags) + @("-tags=integration", "./tools/backendplugin/", "-run", "TestReleaseGates_"))
        Run-RootGoTest "backend-plugin-release-gates-static:release-gates" (@($goTestFlags) + @("./tools/backendplugin/release_gates/", "-run", "TestParseRequirementIDs_|TestListMatchingTests_|TestValidateSelectors_"))
        Run-RootGoTest "backend-plugin-release-gates-static:archtest" (@($goTestFlags) + @("./internal/archtest/", "-run", "TestBackendPluginReleaseGates_"))
        break
    }
    "^backend-plugin-release-gates$" {
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "120m" -Command @("go", "run", "./tools/backendplugin/release_gates", "-root", ".", "-out", ".aip-release-gates-report.json", "-mode=full") | Out-Host
        break
    }
    "^lint$" {
        & "$PSScriptRoot/lint-all-modules.ps1"
        break
    }
    "^vuln$" { Invoke-TaskRunner -Label "vuln:govulncheck" -Cwd $root -Timeout "10m" -Command @("go", "tool", "govulncheck", "./...") | Out-Host; break }
    default { throw "unsupported Windows task target: $Target" }
}
