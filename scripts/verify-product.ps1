# verify-product.ps1 - the Windows outer supervisor for `make verify-product`.
#
# The 600-second command budget must include the `go run` compilation, so the
# bound is enforced here rather than inside devcheck. The absolute deadline is
# passed to devcheck, which uses the remaining time and reports before this
# supervisor's hard stop. Keep the 600-second value in step with the POSIX
# recipe in the Makefile and docs/testing/product-smoke.md.
[CmdletBinding()]
param(
    [string]$Go = "go",
    [string]$Evidence = ""
)

$ErrorActionPreference = "Stop"

# Test-ReparsePrefix reports whether the path or an existing ancestor is a
# symlink/junction. The supervisor cannot prove such a destination is outside
# the repository without following the link, so it leaves any summary alone and
# lets the controller's canonical validation decide. An inspection error is
# treated as "cannot prove safe" for the same reason.
function Test-ReparsePrefix {
    param([string]$Path)
    $current = $Path
    while ($true) {
        $item = $null
        try {
            $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        } catch [System.Management.Automation.ItemNotFoundException] {
            $item = $null
        } catch {
            return $true
        }
        if ($null -ne $item -and ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
            return $true
        }
        $parent = Split-Path -Parent $current
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $current) {
            return $false
        }
        $current = $parent
    }
}

# Run from the repository root so `go run ./tools/devcheck` and a relative
# evidence path match the POSIX target.
Set-Location (Split-Path -Parent $PSScriptRoot)

# Invalidate a previous verdict before compilation can consume the budget. The
# controller clears it too, but it cannot do so if the toolchain never starts
# it. Never modify a path that resolves inside the source tree: the controller
# rejects that destination, and deleting a repository file first would be a
# side effect on an invalid input. Deletion failures other than "not found" are
# fatal: proceeding could leave the previous PASS behind if compilation times
# out before the controller runs.
if ($Evidence -ne "") {
    $evidencePath = if ([System.IO.Path]::IsPathRooted($Evidence)) { $Evidence } else { Join-Path (Get-Location) $Evidence }
    $repo = [System.IO.Path]::GetFullPath((Get-Location).Path)
    $full = [System.IO.Path]::GetFullPath($evidencePath)
    $separator = [System.IO.Path]::DirectorySeparatorChar
    $inside = $full.Equals($repo, [StringComparison]::OrdinalIgnoreCase) -or
        $full.StartsWith($repo + $separator, [StringComparison]::OrdinalIgnoreCase)
    if ($inside) {
        [Console]::Error.WriteLine("verify-product: evidence directory is inside the source tree; leaving any existing summary untouched")
    } elseif (Test-ReparsePrefix $full) {
        [Console]::Error.WriteLine("verify-product: evidence path traverses a symlink or junction; leaving any existing summary untouched")
    } else {
        $summary = Join-Path $full "summary.json"
        $item = $null
        try {
            $item = Get-Item -LiteralPath $summary -Force -ErrorAction Stop
        } catch [System.Management.Automation.ItemNotFoundException] {
            # No previous verdict is the normal first-run case.
        } catch {
            [Console]::Error.WriteLine("verify-product: could not inspect the previous summary at ${summary}: $($_.Exception.Message)")
            exit 2
        }
        if ($null -ne $item) {
            if ($item.PSIsContainer) {
                [Console]::Error.WriteLine("verify-product: the previous summary path is a directory, refusing to proceed: ${summary}")
                exit 2
            }
            try {
                Remove-Item -LiteralPath $summary -Force -Confirm:$false -ErrorAction Stop
            } catch {
                [Console]::Error.WriteLine("verify-product: could not remove the previous summary at ${summary}: $($_.Exception.Message)")
                exit 2
            }
        }
    }
}

$deadline = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + 600
$env:AIP_PRODUCT_SMOKE_DEADLINE_UNIX = [string]$deadline

$arguments = @("run", "-buildvcs=false", "./tools/devcheck", "-task=product-smoke", "-evidence=`"$Evidence`"")
# Start the child through System.Diagnostics instead of Start-Process. On
# Windows PowerShell 5.1 a Start-Process object can finish WaitForExit without
# exposing ExitCode, and `exit $null` then reports success for a failed child.
# Process.Start owns the handle this supervisor waits on, so ExitCode is the
# child's real verdict. If that verdict is unavailable anyway, fail closed:
# never let a missing code turn into a pass.
$startInfo = [System.Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $Go
$startInfo.Arguments = ($arguments -join " ")
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $false
$process = [System.Diagnostics.Process]::Start($startInfo)
if ($null -eq $process) {
    [Console]::Error.WriteLine("product smoke: the supervised command could not be started")
    exit 126
}
if (-not $process.WaitForExit(600000)) {
    $killOutput = & taskkill.exe /F /T /PID $process.Id 2>&1
    if ($LASTEXITCODE -ne 0) {
        [Console]::Error.WriteLine("product smoke: the 600-second deadline expired and the process tree could not be stopped: $killOutput")
        exit 125
    }
    exit 124
}
$exitCode = $null
try {
    if ($process.HasExited) {
        $exitCode = $process.ExitCode
    }
} catch {
    [Console]::Error.WriteLine("product smoke: the supervised command exited but its exit code is unreadable: $($_.Exception.Message)")
    exit 126
}
if ($null -eq $exitCode) {
    [Console]::Error.WriteLine("product smoke: the supervised command exited without an exit code; failing closed")
    exit 126
}
exit $exitCode
