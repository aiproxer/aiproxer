# Fail when a commit or branch range changes more than 100 Go files.
# Admin override: AIP_ALLOW_LARGE_CHANGE=1 or `git config aip.allowLargeChange true`.
$ErrorActionPreference = "Stop"
$root = (git rev-parse --show-toplevel).Trim()
Set-Location $root
$toolArgs = @($args)
if ($toolArgs.Count -eq 0) {
    $toolArgs = @("--staged")
}
& go run ./tools/changesize @toolArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
