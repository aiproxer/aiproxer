# Dynamic multi-module checks for connectors/* and connector-support/*.
param(
    [switch]$SelfTest
)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
function Get-DiscoverBinary {
    $cacheDir = Join-Path ([System.IO.Path]::GetTempPath()) "aip-discover-modules"
    if (-not (Test-Path $cacheDir)) {
        New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
    }
    $path = Join-Path $cacheDir "discover_modules.exe"
    $needsBuild = $true
    if (Test-Path $path) {
        $binTime = (Get-Item $path).LastWriteTimeUtc
        $srcDir = Join-Path $Root "tools/backendplugin/discover_modules"
        $newestSrc = (Get-ChildItem -Path $srcDir -Recurse -File | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1)
        if ($newestSrc -and $binTime -gt $newestSrc.LastWriteTimeUtc) {
            $needsBuild = $false
        }
    }
    if ($needsBuild) {
        Invoke-Go "backend-plugin-module-checks:discovery-build" $Root @("build", "-buildvcs=false", "-o", $path, "./tools/backendplugin/discover_modules") | Out-Host
    }
    return $path
}

# Robocopy has its own exit-code protocol: 0-7 are success (1 means files were
# copied) and >=8 is a failure. The generic taskrunner rejects every non-zero
# child exit, so the copy is invoked directly and classified with this helper.
function Test-RobocopySucceeded {
    param([Parameter(Mandatory = $true)][int]$ExitCode)
    return $ExitCode -ge 0 -and $ExitCode -le 7
}

function Invoke-Robocopy {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    & robocopy $Source $Destination @Arguments
    $exit = $LASTEXITCODE
    if (-not (Test-RobocopySucceeded $exit)) {
        throw "robocopy failed copying '$Source' to '$Destination' (robocopy exit $exit; 0-7 is success, >=8 is failure)"
    }
}

if ($SelfTest) {
    foreach ($ok in @(0, 1, 7)) {
        if (-not (Test-RobocopySucceeded $ok)) { throw "robocopy classifier rejected successful exit $ok" }
    }
    foreach ($bad in @(8, 16, 32)) {
        if (Test-RobocopySucceeded $bad) { throw "robocopy classifier accepted failure exit $bad" }
    }
    Write-Host "OK backend-plugin-module-checks robocopy exit-code classifier self-test"
    exit 0
}

function Invoke-Go {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$Cwd,
        [Parameter(Mandatory = $true)][string[]]$Args,
        [string]$Timeout = "8m",
        [string[]]$Env = @("GOWORK=off"),
        [ValidateSet("stream", "capture")][string]$Output = "stream"
    )
    Invoke-TaskRunner -Label $Label -Cwd $Cwd -Timeout $Timeout -Env $Env -Output $Output -Command (@("go") + $Args)
}

try {
    $discoverBinary = Get-DiscoverBinary

    Write-Host "== root go list/build/module graph =="
Invoke-Go "backend-plugin-module-checks:root:list" $Root @("list", "./...") | Out-Host
Invoke-Go "backend-plugin-module-checks:root:build" $Root @("build", "-o", "NUL", "./cmd/aipstd") | Out-Host
$modsAll = @(Invoke-Go "backend-plugin-module-checks:root:module-graph" $Root @("list", "-m", "all") -Output capture)
if ($modsAll | Select-String -Pattern "connectors/|connector-support/") {
    throw "root go list -m all must not contain connector modules"
}

Write-Host "== root go test ./... =="
Invoke-Go "backend-plugin-module-checks:root:test" $Root @("test", "./...") | Out-Host

Write-Host "== discover modules =="
$discovered = @(Invoke-TaskRunner -Label "backend-plugin-module-checks:discovery" -Cwd $Root -Timeout "8m" -Env @("GOWORK=off") -Output capture -Command @($discoverBinary, "-root", $Root) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
Write-Host ("discovered: " + ($discovered -join " "))

foreach ($mod in $discovered) {
    $module = $mod.Trim()
    if (-not $module) { continue }
    $moduleDir = Join-Path $Root $module
    Write-Host "== module $module =="
    Invoke-Go "backend-plugin-module-checks:$module:list" $moduleDir @("list", "./...") | Out-Host
    Invoke-Go "backend-plugin-module-checks:$module:test" $moduleDir @("test", "./...") | Out-Host
    if (Test-Path (Join-Path $moduleDir "cmd")) {
        Get-ChildItem (Join-Path $moduleDir "cmd") -Directory | ForEach-Object {
            Invoke-Go "backend-plugin-module-checks:$module:build:$($_.Name)" $moduleDir @("build", "-o", "NUL", ("./cmd/" + $_.Name)) | Out-Host
        }
    }
    $imports = @(Invoke-Go "backend-plugin-module-checks:$module:imports" $moduleDir @("list", "-f", "{{.ImportPath}} {{.Imports}} {{.TestImports}} {{.XTestImports}}", "./...") -Output capture)
    if ($imports | Select-String -Pattern "aiproxer/internal/") {
        throw "$module imports root internal/"
    }
}

Write-Host "== synthetic connector discovery =="
$syn = Join-Path $Root "connectors\_synthetic_ci_probe"
New-Item -ItemType Directory -Force -Path $syn | Out-Null
Set-Content -Path (Join-Path $syn "go.mod") -Value "module github.com/aiproxer/aiproxer/connectors/_synthetic_ci_probe`n`ngo 1.26.5`n"
try {
    $found = @(Invoke-TaskRunner -Label "backend-plugin-module-checks:synthetic-discovery" -Cwd $Root -Timeout "8m" -Env @("GOWORK=off") -Output capture -Command @($discoverBinary, "-root", $Root))
    if (-not ($found | Where-Object { $_ -eq "connectors/_synthetic_ci_probe" })) {
        throw "synthetic connector not discovered"
    }
} finally {
    Remove-Item -Recurse -Force $syn -ErrorAction SilentlyContinue
}

Write-Host "== root build with connectors/ absent (temp copy) =="
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("aip-root-no-connectors-" + [guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    Invoke-Robocopy -Source $Root -Destination $tmp -Arguments @("/E", "/NFL", "/NDL", "/NJH", "/NJS", "/nc", "/ns", "/np", "/XD", ".git", "connectors", "connector-support", ".aip-package-staging", ".aip-plugins", "node_modules", "/XF", "*.exe")
    Invoke-Go "backend-plugin-module-checks:root-without-connectors:build" $tmp @("build", "-o", "NUL", "./cmd/aipstd") | Out-Host
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

    Write-Host "OK backend-plugin-module-checks"
}
finally {
}
