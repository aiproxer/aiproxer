$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location (Join-Path $Root "connectors\localstub")
$env:GOWORK = "off"
& go test -parallel=8 -timeout=10m ./... -count=1
Write-Host "OK backend-plugin-example-check"
