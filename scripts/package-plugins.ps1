param(
  [Parameter(Mandatory = $true)][ValidateSet("minimal", "full")][string]$Profile,
  [Parameter(Mandatory = $true)][string]$Dest
)
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
& go run ./tools/backendplugin/package_plugins -root $Root -profile $Profile -dest $Dest
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "OK package-$Profile -> $Dest"
