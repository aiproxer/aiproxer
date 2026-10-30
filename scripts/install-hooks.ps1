# Use the same canonical installer as Bash and make hooks-install.
$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root
& bash (Join-Path $PSScriptRoot "setup-hooks.sh")
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
