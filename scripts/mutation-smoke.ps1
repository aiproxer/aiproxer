# Optional mutation smoke (Windows). Requires gremlins on PATH.
# Optional mutation smoke (Windows). Requires gremlins on PATH.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$gremlins = Get-Command gremlins -ErrorAction SilentlyContinue
if (-not $gremlins) {
  Write-Host "mutation-smoke: gremlins not on PATH; install with: go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0"
  Write-Host "mutation-smoke: skipping"
  exit 0
}
& gremlins unleash --path "./pkg/aipapi" --timeout=5m
& gremlins unleash --path "./internal/core/routing" --timeout=5m
