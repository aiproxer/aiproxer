# test-openresponses-compliance.ps1 — OpenResponses full-path compliance suite (Windows).
#
# Proves the ACTUAL pinned official OpenResponses compliance suite on the full
# independent deployment (official JS client -> OpenResponses frontend -> core ->
# generic OpenResponses backend -> independent refbackend origin), separately
# from the Go-native mirrors, plus the direct independent-emulator wire suites,
# the 45-cell FE*BE conformance matrix (Task 8.5), and the emulator boundary
# architecture gates.
#
# With `-Static` it runs only the fast Task 8.5 wiring/evidence gate used by
# `make qa`: it verifies the compliance scripts, the Makefile target wiring, and
# the docs reference exist, then runs the default-build evidence validators and
# emulator boundary gates. It deliberately does NOT re-run the huge tagged
# conformance/integration suites (which `make qa`'s qa-tests already covers) and
# does NOT require a JavaScript runtime, so the release gate is wired into qa
# without recursively duplicating test work.
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-openresponses-compliance.ps1 [-Static]
# Env:   GO             - go binary (default "go")
#        GO_TEST_FLAGS  - extra go test flags (default "-parallel=8 -timeout=10m")

$ErrorActionPreference = "Stop"

$go = if ($env:GO) { $env:GO } else { "go" }
$flags = @("-parallel=8", "-timeout=10m")
if ($env:GO_TEST_FLAGS) {
    $flags = @($env:GO_TEST_FLAGS -split "\s+")
}

function Invoke-GoTest {
    param([string[]]$TestArgs)
    & $go test @flags @TestArgs
    if ($LASTEXITCODE -ne 0) {
        throw "go test failed with exit code $LASTEXITCODE"
    }
}

$root = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
$complianceTool = Join-Path $root "tools\openresponses-compliance"

# Prepare-OfficialComplianceTooling ensures Node and the pinned tool dependencies
# are available before the ACTUAL official suite runs. `npm ci` is a SETUP step
# (exact versions + integrity from package-lock.json); no test run downloads.
function Prepare-OfficialComplianceTooling {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        throw "official compliance suite requires Node.js >= 20 in PATH (npm ci in $complianceTool is setup)"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $complianceTool "node_modules"))) {
        Write-Host "openresponses-compliance: installing pinned tool dependencies (setup, npm ci)..."
        Push-Location $complianceTool
        try {
            npm ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) {
                throw "npm ci failed with exit code $LASTEXITCODE"
            }
        }
        finally {
            Pop-Location
        }
    }
}

# Invoke-OfficialComplianceSuite runs the ACTUAL pinned official suite through
# the Go harness, which deploys the full independent path and invokes the
# vendored runner. The harness FAILS (never silently skips) when the suite is
# requested but tooling is missing, and fails when any official case fails or is
# skipped.
function Invoke-OfficialComplianceSuite {
    Prepare-OfficialComplianceTooling
    $env:AIP_RUN_OFFICIAL_COMPLIANCE = "1"
    try {
        Invoke-GoTest "-run", "TestOfficialComplianceSuite_FullDeployment", "-timeout", "15m", "./internal/integration/openresponses/"
    }
    finally {
        Remove-Item Env:\AIP_RUN_OFFICIAL_COMPLIANCE -ErrorAction SilentlyContinue
    }
}

$static = $false
foreach ($arg in $args) {
    if ($arg -eq "-Static") { $static = $true }
}

if ($static) {
    $root = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
    foreach ($script in @("scripts/test-openresponses-compliance.ps1", "scripts/test-openresponses-compliance.sh")) {
        if (-not (Test-Path -LiteralPath (Join-Path $root $script))) {
            throw "openresponses-compliance-static: missing $script"
        }
    }
    $makefile = Join-Path $root "Makefile"
    if (-not (Select-String -LiteralPath $makefile -Pattern '^test-openresponses-compliance:' -Quiet)) {
        throw "openresponses-compliance-static: Makefile compliance target missing"
    }
    if (-not (Select-String -LiteralPath $makefile -Pattern '^test-openresponses-compliance-static:' -Quiet)) {
        throw "openresponses-compliance-static: Makefile static compliance target missing"
    }
    if (-not (Select-String -LiteralPath $makefile -Pattern '^qa:.*test-openresponses-compliance-static' -Quiet)) {
        throw "openresponses-compliance-static: Makefile qa does not wire the compliance gate"
    }
    if (-not (Select-String -LiteralPath (Join-Path $root "docs\release-gates.md") -Pattern 'test-openresponses-compliance' -Quiet)) {
        throw "openresponses-compliance-static: docs do not reference the compliance gate"
    }
    # Fast Task 8.5 evidence validators (default build; the tagged matrix loops are
    # run by make qa's qa-tests and by the full standalone compliance suite).
    Invoke-GoTest "./internal/testkit/conformance/"
    Invoke-GoTest "./internal/archtest/", "-run", "OpenResponses|EmulatorBoundary"
    Write-Host "openresponses-compliance-static: Task 8.5 wiring and evidence verified"
    exit 0
}

# 1. Authoritative 5x9 = 45-cell matrix, OpenResponses frontend row, and
#    OpenResponses backend column (Task 8.5 evidence).
Invoke-GoTest "-tags=precommit,integration", "./internal/testkit/conformance/..."

# 2. Independent OpenResponses refclient <-> refbackend direct wire suites
#    (official fixtures, adversarial streams, required presence).
Invoke-GoTest "./internal/refclient/openresponses/...", "./internal/refbackend/openresponses/..."

# 3. Production OpenResponses frontend + generic backend adapter suites.
Invoke-GoTest "./internal/plugins/frontends/openresponses/...", "./internal/plugins/backends/openresponsescompat/..."

# 4. Full black-box deployment harness (client -> frontend -> core -> backend ->
#    independent provider origin) across JSON/SSE/compact/WebSocket.
Invoke-GoTest "./internal/integration/openresponses/..."

# 5. OpenResponses profile/source/license pinning and emulator boundary gates.
Invoke-GoTest "./internal/archtest/...", "-run", "OpenResponses|EmulatorBoundary"
Invoke-GoTest "./internal/plugins/protocols/openresponses/...", "-run", "Profile|Source|License|Manifest"

# 6. ACTUAL pinned official compliance suite on the full independent deployment
#    (separate from the Go-native mirrors above). FAILS when any official case
#    fails or is skipped, or when Node/tooling is unavailable.
Invoke-OfficialComplianceSuite

Write-Host "openresponses-compliance: all suites passed"
exit 0
