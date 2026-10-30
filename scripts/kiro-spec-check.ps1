param(
    [Parameter(Mandatory = $true)]
    [string]$Spec
)

$ErrorActionPreference = "Stop"
if (-not (Select-String -Path tools/kiro/speccheck/*_test.go -SimpleMatch -Pattern "{name: `"$Spec`"" -Quiet)) {
    Write-Error "kiro-spec-check: $Spec is not registered in tools/kiro/speccheck; run 'go run ./tools/kiro/kirocheck' for lifecycle and budget checks"
    exit 2
}
$env:KIRO_SPEC = $Spec
& go test -parallel=8 -timeout=10m ./tools/kiro/speccheck/ -run "^TestKiroSpec$" -count=1
exit $LASTEXITCODE
