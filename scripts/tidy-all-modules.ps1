param(
    [switch]$Check
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$localEnv = if ($env:AIP_DISABLE_VCS_STAMPING -eq "1" -and -not $env:GOFLAGS) { @("GOFLAGS=-buildvcs=false") } else { @() }
$jobs = if ($env:AIP_MODULE_CHECK_JOBS) { [int]$env:AIP_MODULE_CHECK_JOBS } elseif ($env:CI_NODE_TOTAL) { [int]$env:CI_NODE_TOTAL } else { [Math]::Max(2, [Math]::Min(8, [Environment]::ProcessorCount)) }
if ($jobs -lt 1) { throw "AIP_MODULE_CHECK_JOBS must be a positive integer" }

try {
    $runnerBinary = Get-TaskRunnerBinary
    $modules = @(".", "testdata/enterprise_module", "testdata/external_billing_binding", "testdata/external_connector", "testdata/external_feature_sdk")
    $discovered = @(Invoke-TaskRunner -Label "tidy-all-modules:discovery" -Cwd $Root -Timeout "8m" -Env $localEnv -Output capture -Command @("go", "run", "./tools/backendplugin/discover_modules", "-root", "."))
    $modules += $discovered | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $modules = @($modules | ForEach-Object { $_.Trim() } | Where-Object { $_ } | Select-Object -Unique | Where-Object {
        Test-Path (Join-Path $Root (Join-Path $_ "go.mod"))
    })

    Write-Host ("Tidying {0} modules with up to {1} workers" -f $modules.Count, $jobs)

    $commandArgs = if ($Check) { @("go", "mod", "tidy", "-diff") } else { @("go", "mod", "tidy") }
    $envArgs = @($localEnv) + @("GOWORK=off")

    $sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
    $pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, $jobs, $sessionState, $Host)
    $pool.Open()

    $tasks = [System.Collections.Generic.List[PSObject]]::new()
    try {
        foreach ($module in $modules) {
            $dir = if ($module -eq ".") { $Root } else { Join-Path $Root $module }
            $label = "tidy-all-modules:$module"

            $ps = [System.Management.Automation.PowerShell]::Create()
            $ps.RunspacePool = $pool
            [void]$ps.AddScript({
                param($runnerBinary, $label, $cwd, $envList, $cmdArgs)
                $runnerArgs = @(
                    "--label", $label,
                    "--cwd", $cwd,
                    "--timeout", "8m",
                    "--output", "capture"
                )
                foreach ($e in $envList) {
                    $runnerArgs += @("--env", $e)
                }
                $runnerArgs += "--"
                $runnerArgs += $cmdArgs

                # Merged native stderr would terminate the run under the script's
                # Stop preference in Windows PowerShell; the exit code below is
                # the contract, so relax the preference for the duration of the
                # call.
                $previousErrorActionPreference = $ErrorActionPreference
                try {
                    $ErrorActionPreference = "Continue"
                    $output = @(& $runnerBinary @runnerArgs 2>&1)
                    $exitCode = $LASTEXITCODE
                } finally {
                    $ErrorActionPreference = $previousErrorActionPreference
                }
                return @{
                    Module = $cwd
                    Label = $label
                    ExitCode = $exitCode
                    Output = $output
                }
            }).AddArgument($runnerBinary).AddArgument($label).AddArgument($dir).AddArgument($envArgs).AddArgument($commandArgs)

            $asyncResult = $ps.BeginInvoke()
            $tasks.Add([PSCustomObject]@{
                PowerShell = $ps
                AsyncResult = $asyncResult
                Module = $module
                Label = $label
            })
        }

        $failed = @()
        foreach ($task in $tasks) {
            $res = $task.PowerShell.EndInvoke($task.AsyncResult)
            $task.PowerShell.Dispose()
            if ($res) {
                $exitCode = $res[0].ExitCode
                $output = $res[0].Output
                if ($output) { $output | ForEach-Object { Write-Host $_ } }
                if ($exitCode -ne 0) {
                    $failed += ("{0}: exit {1}; output: {2}" -f $task.Module, $exitCode, (($output | Out-String).Trim()))
                }
            }
        }

        if ($failed.Count -gt 0) {
            throw ("module tidy failed: " + ($failed -join "; "))
        }
        if ($Check) { Write-Host "OK: all discovered Go modules are tidy" }
        else { Write-Host "OK: all discovered Go modules synchronized" }
    }
    finally {
        $pool.Close()
        $pool.Dispose()
    }
}
finally {
}
