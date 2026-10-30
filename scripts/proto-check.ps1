# Protobuf contract gate for api/backendplugin/v1 (see issue #715).
# Runs buf lint, buf breaking against origin/main, and generation freshness.
# Generation is hermetic: pinned `go install tool` plugins are installed to a
# scratch GOBIN prepended to PATH, and output is regenerated into a scratch
# copy of api/ and byte-compared, so the worktree is never modified and
# ambient plugin state cannot shadow the pinned toolchain.
# buf missing -> warn and skip (CI installs buf first, so the gate is real there).

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = (Resolve-Path (Join-Path $scriptDir "..")).Path

if (-not (Get-Command buf -ErrorAction SilentlyContinue)) {
    Write-Host "WARNING: buf not found on PATH; skipping protobuf contract gate (CI installs buf 1.66.0 first)." -ForegroundColor DarkYellow
    exit 0
}

Write-Host "== buf lint =="
Push-Location (Join-Path $root "api")
try {
    & buf lint
    if ($LASTEXITCODE -ne 0) { throw "buf lint failed (exit $LASTEXITCODE)" }
} finally {
    Pop-Location
}

Write-Host "== buf breaking =="
$base = $null
try {
    $base = @(git -C $root merge-base HEAD origin/main 2>$null | Where-Object { $_ }) | Select-Object -First 1
} catch {
    $base = $null
}
if ($base) {
    Push-Location $root
    try {
        & buf breaking --config api/buf.yaml --against ".git#commit=$base"
        if ($LASTEXITCODE -ne 0) { throw "buf breaking failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
} else {
    Write-Host "WARNING: origin/main not resolvable; skipping breaking-change detection." -ForegroundColor DarkYellow
}

Write-Host "== buf generate freshness =="
$scratch = Join-Path ([System.IO.Path]::GetTempPath()) ("aip-proto-check-" + [guid]::NewGuid().ToString("n"))
$scratchApi = Join-Path $scratch "api"
$pluginDir = Join-Path $scratch "plugins"
try {
    New-Item -ItemType Directory -Force -Path $scratchApi | Out-Null
    New-Item -ItemType Directory -Force -Path $pluginDir | Out-Null
    Copy-Item (Join-Path $root "api/buf.yaml") $scratchApi
    Copy-Item (Join-Path $root "api/buf.gen.yaml") $scratchApi
    $apiRoot = Join-Path $root "api"
    $apiPrefix = $apiRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    Get-ChildItem -Path $apiRoot -Filter "*.proto" -Recurse -File | ForEach-Object {
        $rel = $_.FullName.Substring($apiPrefix.Length)
        $dest = Join-Path $scratchApi $rel
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dest) | Out-Null
        Copy-Item $_.FullName $dest
    }

    $savedGoBin = $env:GOBIN
    try {
        $env:GOBIN = $pluginDir
        & go install tool
        if ($LASTEXITCODE -ne 0) { throw "go install tool failed (exit $LASTEXITCODE)" }
    } finally {
        $env:GOBIN = $savedGoBin
    }
    $savedPath = $env:PATH
    try {
        $env:PATH = "$pluginDir$([System.IO.Path]::PathSeparator)$savedPath"
        Push-Location $scratchApi
        try {
            & buf generate --template buf.gen.yaml
            if ($LASTEXITCODE -ne 0) { throw "buf generate failed (exit $LASTEXITCODE)" }
        } finally {
            Pop-Location
        }
    } finally {
        $env:PATH = $savedPath
    }

    $stale = @()
    $scratchPrefix = $scratchApi.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    Get-ChildItem -Path $scratchApi -Filter "*.pb.go" -Recurse -File | ForEach-Object {
        $rel = $_.FullName.Substring($scratchPrefix.Length)
        $treeFile = Join-Path (Join-Path $root "api") $rel
        # Byte comparison via git (no module-dependent cmdlets: make recipes
        # run under sh, where Utility-module autoload is unreliable).
        $same = (Test-Path $treeFile)
        if ($same) {
            & git diff --no-index --exit-code --quiet -- $treeFile $_.FullName 2>$null
            $same = ($LASTEXITCODE -eq 0)
        }
        if (-not $same) {
            $stale += ("api/" + ($rel -replace '\\', '/'))
        }
    }
    if ($stale.Count -gt 0) {
        Write-Host "ERROR: generated protobuf output is stale:" -ForegroundColor Red
        $stale | ForEach-Object { Write-Host "  STALE: $_" -ForegroundColor Red }
        Write-Host "Regenerate with pinned tools: go install tool && cd api && buf generate --template buf.gen.yaml" -ForegroundColor Yellow
        exit 1
    }
} finally {
    Remove-Item -Recurse -Force $scratch -ErrorAction SilentlyContinue
}

Write-Host "OK: protobuf contract gate passed" -ForegroundColor Green
exit 0
