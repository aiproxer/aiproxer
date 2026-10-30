# race-check.ps1 — Windows shim for `make test-race` and the pre-commit quality gate.
#
# Go race detector runs (`go test -race`) are disabled on Windows in this repository
# (ThreadSanitizer / toolchain friction). Use `bash scripts/race-check.sh` on Linux or
# macOS, or rely on CI (`.github/workflows/qa.yml` runs `race-check.sh --strict`).
#
# Parameters: `-Staged` and `-Strict` are reserved for call-site compatibility.

param(
    [switch]$Staged = $false,
    [switch]$Strict = $false
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    Write-Host "ERROR: race-check.ps1 is for Windows only; use: bash scripts/race-check.sh [--staged|--strict]" -ForegroundColor Red
    exit 2
}

Write-Host "SKIP: Go race evidence is unsupported on Windows; Linux CI remains mandatory." -ForegroundColor Yellow
exit 0
