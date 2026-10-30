# precommit-full.ps1
# Windows runner for `make precommit-full`: the optional full local lint +
# vulnerability scan gate (mirrors scripts/quality-gate.sh for POSIX).
#
# Extracted from an inline Makefile recipe because make executes recipes via
# its recipe shell (sh on MSYS/Git Bash, not PowerShell), which mangles
# PowerShell-specific syntax such as `$env:VAR='1'` and `exit $LASTEXITCODE`.

$ErrorActionPreference = "Stop"
$ScriptDir = $PSScriptRoot

function Invoke-PrecommitStep {
    param(
        [Parameter(Mandatory)][string]$Label,
        [Parameter(Mandatory)][scriptblock]$Body
    )
    Write-Host ""
    Write-Host "=== $Label ===" -ForegroundColor Cyan
    & $Body
    if ($LASTEXITCODE -ne 0) {
        throw ("{0} failed with exit code {1}" -f $Label, $LASTEXITCODE)
    }
}

Invoke-PrecommitStep "Quality Checks" { & make quality-checks }
Invoke-PrecommitStep "Module Tidy Check" {
    & powershell -NoProfile -ExecutionPolicy Bypass -File "$ScriptDir/tidy-all-modules.ps1" -Check
}
$env:AIP_TEST_PRECOMMIT = "1"
Invoke-PrecommitStep "Staged Tests (precommit tags)" {
    & powershell -NoProfile -ExecutionPolicy Bypass -File "$ScriptDir/test-staged.ps1"
}
Invoke-PrecommitStep "Race Check" {
    & powershell -NoProfile -ExecutionPolicy Bypass -File "$ScriptDir/race-check.ps1" -Staged
}
Invoke-PrecommitStep "Lint" { & make lint }
Invoke-PrecommitStep "Vulnerability Scan" { & make vuln }

Write-Host ""
Write-Host "=== Pre-Commit Full Gate Passed ===" -ForegroundColor Green
exit 0
