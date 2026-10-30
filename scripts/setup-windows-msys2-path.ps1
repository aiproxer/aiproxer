# Prepends MSYS2 MinGW64 + usr/bin to the *user* PATH so go/cgo can find gcc, cc1, ld, dlltool, etc.
# CGO and go test -race need MinGW-w64 on PATH (not only a full path to gcc.exe).
# Idempotent: skips if msys64\mingw64\bin is already in User PATH.
# Run: powershell -NoProfile -ExecutionPolicy Bypass -File scripts/setup-windows-msys2-path.ps1

$ErrorActionPreference = "Stop"

$mingwBin = 'C:\msys64\mingw64\bin'
$msysUsrBin = 'C:\msys64\usr\bin'

if (-not (Test-Path $mingwBin)) {
    Write-Host "ERROR: $mingwBin not found. Install MSYS2 to C:\msys64 (or edit this script)." -ForegroundColor Red
    exit 1
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }

function Normalize-PathSeg([string]$p) {
    return ($p -replace '\\', '/').TrimEnd('/').ToLowerInvariant()
}

$mingwNorm = Normalize-PathSeg $mingwBin
$already = $false
foreach ($seg in $userPath -split ';') {
    if (-not $seg) { continue }
    if ((Normalize-PathSeg $seg) -eq $mingwNorm) { $already = $true; break }
}

if ($already) {
    Write-Host "User PATH already includes $mingwBin - nothing to do." -ForegroundColor Green
    exit 0
}

$prefix = "${mingwBin};${msysUsrBin};"
$newUserPath = $prefix + $userPath
[Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
Write-Host 'Prepended to User PATH:' -ForegroundColor Cyan
Write-Host "  $mingwBin"
Write-Host "  $msysUsrBin"
Write-Host ''
Write-Host 'Open a new terminal (or sign out) so Go picks up the updated PATH, then run: go test -race ./...' -ForegroundColor Yellow
exit 0
