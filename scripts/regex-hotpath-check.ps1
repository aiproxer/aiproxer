# Fail if regexp.MustCompile / regexp.Compile appears in hot-path packages without
# an entry in regex-hotpath-allowlist.txt. Run from repository root.

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $scriptDir "..")
$allowlistFile = Join-Path $scriptDir "regex-hotpath-allowlist.txt"

if (-not (Get-Command rg -ErrorAction SilentlyContinue)) {
    Write-Host "regex-hotpath-check: ripgrep (rg) not found; skipping." -ForegroundColor DarkYellow
    exit 0
}

$allowedPaths = @()
if (Test-Path $allowlistFile) {
    Get-Content $allowlistFile | ForEach-Object {
        $line = ($_ -split '#')[0].Trim()
        if ([string]::IsNullOrWhiteSpace($line)) { return }
        $line = $line -replace '\\', '/'
        $allowedPaths += $line
    }
}

function Test-AllowedFile {
    param([string]$RelPath)
    foreach ($a in $allowedPaths) {
        # Case-sensitive exact match, like the bash check and the Go QA check:
        # an allowlist entry with the wrong case must not silently exempt a file.
        if ($RelPath -ceq $a) { return $true }
    }
    return $false
}

$rootPath = $root.Path
Push-Location $rootPath
try {
    $pattern = 'regexp\.(MustCompile|Compile)\('
    $rgArgs = @(
        '-n', '--glob', '*.go', '--glob', '!*_test.go',
        $pattern,
        'internal/plugins/frontends', 'internal/core/runtime'
    )
    $raw = @(& rg @rgArgs 2>$null)
} finally {
    Pop-Location
}

$violations = @()
$rootNorm = $rootPath.TrimEnd('\', '/')
$rootPrefix = $rootNorm + [IO.Path]::DirectorySeparatorChar
foreach ($row in $raw) {
    if ([string]::IsNullOrWhiteSpace($row)) { continue }
    $file = (($row -split ':', 3)[0]).Trim()
    $joined = Join-Path $rootPath $file
    $fileNorm = if ([System.IO.Path]::IsPathRooted($file)) {
        [System.IO.Path]::GetFullPath($file)
    } else {
        [System.IO.Path]::GetFullPath($joined)
    }
    $rel = $file -replace '\\', '/'
    if ($fileNorm.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        $rel = $fileNorm.Substring($rootPrefix.Length) -replace '\\', '/'
    }
    if (Test-AllowedFile $rel) { continue }
    $violations += $row
}

if ($violations.Count -gt 0) {
    Write-Host "ERROR: regexp.MustCompile / regexp.Compile in hot paths (internal/plugins/frontends, internal/core/runtime)." -ForegroundColor Red
    Write-Host "Hoist fixed patterns to package-level vars, cache config-driven patterns, or add the file to scripts/regex-hotpath-allowlist.txt with justification:" -ForegroundColor Yellow
    $violations | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    exit 1
}

Write-Host "OK: regex hot-path check passed" -ForegroundColor Green
exit 0
