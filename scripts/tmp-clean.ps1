# tmp-clean.ps1
# Conservative cleanup of stale project-owned temp residue left behind by
# hard-killed tests. Dry-run by default; pass -Apply to delete.
#
# Safety invariants:
#   * Only immediate children of the configured temp root are ever examined.
#   * A child is a candidate only when its name starts with an exact
#     allowlisted project prefix (ordinal match, including trailing '-').
#   * Reparse points (symlinks/junctions) and fresh directories are always
#     skipped; no symlink/junction traversal happens.
#   * Deletion uses bounded retries; a locked/in-use directory is skipped and
#     reported, never allowed to fail the whole cleanup.
#
# This script is never invoked automatically by tests or `make serve`; it is
# an explicit developer command (make tmp-clean / scripts/tmp-clean.ps1).
param(
    [switch]$Apply,
    [int]$OlderThanHours = 72,
    [string]$TempRoot,
    [switch]$SelfTest
)

$ErrorActionPreference = "Stop"

# Exact project-owned prefixes. Directory names must start with one of these
# strings verbatim (case-sensitive, trailing '-' included). No generic paths
# or shared fixed catalogs are listed: ownership must be certain.
$Script:AllowlistPrefixes = @(
    'aip-plugin-serve-'
    'aip-plugin-inspect-'
    'aip-plugin-doctor-'
    'aip-structural-catalog-'
    'aip-connector-build-'
    'aip-staged-root-'
    'aip-enterprise-module-'
    'rp-e2e-'
    'aip-tools-bin-'
    'aip-release-gates-'
    'aip-isolated-root-'
    'aip-installed-smoke-'
    'aip-xplat-'
    '.aip-pkg-staging-'
    'aip-live-bridge-'
    'securesession-storecontract-'
    'securesession-quarantine-'
    'aip-processhost-fixture-'
)

function Test-OlderThanHours {
    param([int]$Value)
    return $Value -ge 1
}

function Test-AllowlistPrefix {
    param([string]$Name)
    foreach ($prefix in $Script:AllowlistPrefixes) {
        if ($Name.StartsWith($prefix, [System.StringComparison]::Ordinal)) {
            return $true
        }
    }
    return $false
}

function Format-Bytes {
    param([long]$Bytes)
    if ($Bytes -ge 1GB) { return ("{0:N1} GB" -f ($Bytes / 1GB)) }
    if ($Bytes -ge 1MB) { return ("{0:N1} MB" -f ($Bytes / 1MB)) }
    if ($Bytes -ge 1KB) { return ("{0:N1} KB" -f ($Bytes / 1KB)) }
    return "$Bytes B"
}

function Get-DirectorySize {
    param([string]$Path)
    $bytes = [long]0
    $items = @(Get-ChildItem -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue)
    foreach ($item in $items) {
        if ($null -ne $item.Length) { $bytes += $item.Length }
    }
    return $bytes
}

function Remove-TempCleanTarget {
    param([string]$Path, [int]$Retries = 3)
    $attempt = 0
    while ($true) {
        try {
            Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction Stop
            return $true
        } catch {
            $attempt++
            if ($attempt -ge $Retries) { return $false }
            Start-Sleep -Milliseconds 200
        }
    }
}

function Invoke-TempClean {
    param(
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [int]$OlderThanHours = 72,
        [switch]$Apply
    )
    if (-not (Test-OlderThanHours $OlderThanHours)) {
        throw "OlderThanHours must be a positive number of hours (got $OlderThanHours)"
    }
    if (-not (Test-Path -LiteralPath $TempRoot -PathType Container)) {
        throw "temp root does not exist: $TempRoot"
    }
    $resolvedRoot = (Resolve-Path -LiteralPath $TempRoot).Path
    $entries = @(Get-ChildItem -LiteralPath $resolvedRoot -Force -ErrorAction Stop)
    $threshold = [TimeSpan]::FromHours($OlderThanHours)
    $now = [DateTime]::UtcNow

    $scanned = $entries.Count
    $candidateCount = 0
    $deletedCount = 0
    $skippedCount = 0
    $retainedCount = 0
    $reclaimedBytes = [long]0

    $mode = if ($Apply.IsPresent) { "APPLY" } else { "DRY-RUN" }
    Write-Host "tmp-clean [$mode] root: $resolvedRoot (older-than $OlderThanHours h)"

    foreach ($entry in $entries) {
        if (-not $entry.PSIsContainer) { continue }
        $name = $entry.Name
        if (-not (Test-AllowlistPrefix $name)) { continue }
        if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            $retainedCount++
            Write-Host "  SKIP  reparse point: $name"
            continue
        }
        $age = $now - $entry.LastWriteTimeUtc
        if ($age -lt $threshold) {
            $retainedCount++
            Write-Host ("  KEEP  fresh ({0:N1} h < {1} h): {2}" -f $age.TotalHours, $OlderThanHours, $name)
            continue
        }
        $candidateCount++
        $bytes = Get-DirectorySize $entry.FullName
        if ($Apply.IsPresent) {
            if (Remove-TempCleanTarget $entry.FullName) {
                $deletedCount++
                $reclaimedBytes += $bytes
                Write-Host ("  DELETED ({0}): {1}" -f (Format-Bytes $bytes), $name)
            } else {
                $skippedCount++
                Write-Host "  SKIP  locked/in-use: $name"
            }
        } else {
            Write-Host ("  CANDIDATE ({0}, age {1:N1} h): {2}" -f (Format-Bytes $bytes), $age.TotalHours, $name)
        }
    }

    Write-Host ("Summary: scanned=$scanned candidates=$candidateCount deleted=$deletedCount skipped=$skippedCount retained=$retainedCount reclaimedBytes=$reclaimedBytes ({0})" -f (Format-Bytes $reclaimedBytes))
}

function Invoke-TempCleanSelfTest {
    $testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("tmp-clean-selftest-" + [guid]::NewGuid().ToString("n"))
    New-Item -ItemType Directory -Force -Path $testRoot | Out-Null
    try {
        $oldMatch = Join-Path $testRoot "aip-plugin-serve-old"
        New-Item -ItemType Directory -Force -Path $oldMatch | Out-Null
        Set-Content -LiteralPath (Join-Path $oldMatch "payload.bin") -Value ("x" * 1024) -NoNewline
        (Get-Item -LiteralPath $oldMatch).LastWriteTimeUtc = [DateTime]::UtcNow.AddHours(-200)

        $fresh = Join-Path $testRoot "aip-plugin-serve-fresh"
        New-Item -ItemType Directory -Force -Path $fresh | Out-Null

        $unknown = Join-Path $testRoot "third-party-toolbox-old"
        New-Item -ItemType Directory -Force -Path $unknown | Out-Null
        (Get-Item -LiteralPath $unknown).LastWriteTimeUtc = [DateTime]::UtcNow.AddHours(-200)

        Invoke-TempClean -TempRoot $testRoot -OlderThanHours 72
        if (-not (Test-Path -LiteralPath $oldMatch)) { throw "self-test failed: dry-run must retain old matching dir" }
        if (-not (Test-Path -LiteralPath $fresh)) { throw "self-test failed: dry-run must retain fresh dir" }
        if (-not (Test-Path -LiteralPath $unknown)) { throw "self-test failed: dry-run must retain unknown-prefix dir" }

        Invoke-TempClean -TempRoot $testRoot -OlderThanHours 72 -Apply
        if (Test-Path -LiteralPath $oldMatch) { throw "self-test failed: apply must delete old matching dir" }
        if (-not (Test-Path -LiteralPath $fresh)) { throw "self-test failed: apply must retain fresh matching dir" }
        if (-not (Test-Path -LiteralPath $unknown)) { throw "self-test failed: apply must retain unknown-prefix dir" }

        $reparseTarget = Join-Path $testRoot "reparse-target-not-owned"
        New-Item -ItemType Directory -Force -Path $reparseTarget | Out-Null
        $reparseLink = Join-Path $testRoot "aip-tools-bin-link"
        try {
            New-Item -ItemType Junction -Path $reparseLink -Target $reparseTarget | Out-Null
            (Get-Item -LiteralPath $reparseLink).LastWriteTimeUtc = [DateTime]::UtcNow.AddHours(-200)
            Invoke-TempClean -TempRoot $testRoot -OlderThanHours 72 -Apply
            if (-not (Test-Path -LiteralPath $reparseLink)) { throw "self-test failed: apply must skip reparse point" }
            if (-not (Test-Path -LiteralPath $reparseTarget)) { throw "self-test failed: reparse target must be untouched" }
        } catch {
            Write-Host "reparse check skipped (junction creation unavailable): $($_.Exception.Message)"
        }

        if (Test-OlderThanHours 0) { throw "self-test failed: invalid age 0 must be rejected" }
        if (Test-OlderThanHours -3) { throw "self-test failed: invalid age -3 must be rejected" }
        if (-not (Test-OlderThanHours 1)) { throw "self-test failed: valid age 1 must be accepted" }
        if (-not (Test-OlderThanHours 72)) { throw "self-test failed: valid age 72 must be accepted" }

        if (-not (Test-AllowlistPrefix "aip-plugin-serve-abc")) { throw "self-test failed: allowlist must match exact prefix" }
        if (Test-AllowlistPrefix "aip-plugin-serve") { throw "self-test failed: allowlist matched name lacking trailing separator" }
        if (Test-AllowlistPrefix "aip-plugin-servE-abc") { throw "self-test failed: allowlist matched case variant" }
        if (Test-AllowlistPrefix "not-owned-abc") { throw "self-test failed: allowlist matched unknown prefix" }
        if (-not (Test-AllowlistPrefix ".aip-pkg-staging-abc")) { throw "self-test failed: allowlist must match dot-prefixed staging" }
        if (Test-AllowlistPrefix "aip-pkg-staging-abc") { throw "self-test failed: allowlist matched staging without leading dot" }

        Write-Host "OK tmp-clean.ps1 self-test"
    } finally {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

if ($SelfTest) {
    Invoke-TempCleanSelfTest
    exit 0
}

$effectiveRoot = $TempRoot
if ([string]::IsNullOrWhiteSpace($effectiveRoot)) {
    $effectiveRoot = [System.IO.Path]::GetTempPath()
}
Invoke-TempClean -TempRoot $effectiveRoot -OlderThanHours $OlderThanHours -Apply:$Apply
