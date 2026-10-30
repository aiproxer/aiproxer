# Shared Windows child-process boundary for developer-facing scripts.
$ErrorActionPreference = "Stop"
$TaskRunnerRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$script:TaskRunnerBinary = $null

function Test-TaskRunnerBuildRequired {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path $Path)) {
        return $true
    }
    $binTime = (Get-Item $Path).LastWriteTimeUtc
    $srcDir = Join-Path $TaskRunnerRoot "tools/taskrunner"
    $newestSrc = Get-ChildItem -Path $srcDir -Recurse -File | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
    return -not ($newestSrc -and $binTime -gt $newestSrc.LastWriteTimeUtc)
}

function Get-TaskRunnerBinary {
    if ($script:TaskRunnerBinary -and (Test-Path $script:TaskRunnerBinary)) {
        return $script:TaskRunnerBinary
    }

    $cacheDir = Join-Path ([System.IO.Path]::GetTempPath()) "aip-taskrunner"
    if (-not (Test-Path $cacheDir)) {
        New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
    }
    $path = Join-Path $cacheDir "aip-taskrunner.exe"

    if (Test-TaskRunnerBuildRequired $path) {
        $hash = [System.Security.Cryptography.SHA256]::Create()
        try {
            $nameBytes = [System.Text.Encoding]::UTF8.GetBytes($path.ToLowerInvariant())
            $mutexSuffix = ([System.BitConverter]::ToString($hash.ComputeHash($nameBytes))).Replace("-", "")
        } finally {
            $hash.Dispose()
        }
        $mutex = New-Object System.Threading.Mutex($false, ("Local\AIPTaskRunnerBuild-" + $mutexSuffix))
        $lockTaken = $false
        try {
            try {
                $lockTaken = $mutex.WaitOne([TimeSpan]::FromMinutes(2))
            } catch [System.Threading.AbandonedMutexException] {
                $lockTaken = $true
            }
            if (-not $lockTaken) {
                throw "timed out waiting to build temporary aip-taskrunner"
            }

            # Another process may have completed the cold build while this one
            # waited. Recheck under the cross-process lock before compiling.
            if (Test-TaskRunnerBuildRequired $path) {
                $buildPath = "$path.$PID.$([Guid]::NewGuid().ToString('N')).tmp.exe"
                Push-Location $TaskRunnerRoot
                try {
                    # The helper is a local diagnostic executable; do not require repository
                    # VCS metadata merely to compile the process-tree boundary.
                    & go build -buildvcs=false -o $buildPath ./tools/taskrunner/cmd/aip-taskrunner
                    if ($LASTEXITCODE -ne 0) {
                        throw "failed to build temporary aip-taskrunner (exit $LASTEXITCODE)"
                    }
                    Move-Item -LiteralPath $buildPath -Destination $path -Force
                } finally {
                    Pop-Location
                    Remove-Item -LiteralPath $buildPath -Force -ErrorAction SilentlyContinue
                }
            }
        } finally {
            if ($lockTaken) {
                $mutex.ReleaseMutex()
            }
            $mutex.Dispose()
        }
    }
    $script:TaskRunnerBinary = $path
    return $path
}

function Invoke-TaskRunner {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$Cwd,
        [Parameter(Mandatory = $true)][string]$Timeout,
        [Parameter(Mandatory = $true)][string[]]$Command,
        [string[]]$Env = @(),
        [ValidateSet("stream", "capture")][string]$Output = "stream"
    )

    $runnerArgs = @(
        "--label", $Label,
        "--cwd", $Cwd,
        "--timeout", $Timeout,
        "--output", $Output
    )
    # Keep VCS stamping intact by default, including local release checks.
    # Worktrees with incomplete metadata opt into the diagnostic override at
    # their local boundary with AIP_DISABLE_VCS_STAMPING=1.
    $effectiveEnv = @($Env)
    $hasGoFlags = @($effectiveEnv | Where-Object { $_ -like "GOFLAGS=*" }).Count -gt 0
    if (-not $hasGoFlags -and $env:AIP_DISABLE_VCS_STAMPING -eq "1") {
        $effectiveEnv += "GOFLAGS=-buildvcs=false"
    }
    foreach ($entry in $effectiveEnv) {
        $runnerArgs += @("--env", $entry)
    }
    $runnerArgs += "--"
    $runnerArgs += $Command

    $binary = Get-TaskRunnerBinary
    $result = @()
    $exitCode = 0
    # The capture below merges the child's stderr into its output. Windows
    # PowerShell turns those merged native records into terminating errors under
    # this script's Stop preference, so a child that merely logs progress (for
    # example "go: downloading ...") aborts the task even though the child
    # succeeded. The exit code is the contract here, so the native call runs
    # under Continue and the caller's preference is restored afterwards.
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        if ($Output -eq "capture") {
            $result = @(& $binary @runnerArgs 2>&1)
        } else {
            & $binary @runnerArgs
        }
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($exitCode -ne 0) {
        if ($Output -eq "capture" -and $result) { $result | ForEach-Object { Write-Error $_ -ErrorAction Continue } }
        throw "taskrunner failed for $Label (exit $exitCode)"
    }
    return $result
}
