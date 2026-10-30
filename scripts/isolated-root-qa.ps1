# Phase 8.5: copy root excluding connectors/connector-support/Node/artifacts; GOWORK=off QA.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root
$env:GOWORK = "off"
& go run ./tools/backendplugin/isolated_root_qa $Root
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
