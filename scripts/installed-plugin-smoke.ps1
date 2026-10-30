# Phase 8.5: one aipstd binary; package via release.yaml; same-binary inspect/doctor/invoke.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root
$env:GOWORK = "off"
& go run ./tools/backendplugin/installed_plugin_smoke $Root
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
