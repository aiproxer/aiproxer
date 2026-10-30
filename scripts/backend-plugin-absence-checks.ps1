# Prove root builds/tests with Phase 7 migrated connector module trees absent.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root
$env:GOWORK = "off"

$migrated = @(
  "connectors\openrouter",
  "connectors\nvidia",
  "connectors\huggingface",
  "connectors\ollama",
  "connectors\llamacpp",
  "connectors\lmstudio",
  "connectors\vllm",
  "connectors\opencode",
  "connectors\codex",
  "connector-support\openaicompat"
)

Write-Host "== root build/test with Phase 7 modules temporarily renamed =="
$renamed = @()
try {
  foreach ($rel in $migrated) {
    $path = Join-Path $Root $rel
    if (-not (Test-Path $path)) { continue }
    $tmp = $path + ".__absent_check"
    Rename-Item -Path $path -NewName (Split-Path $tmp -Leaf)
    $renamed += @{ From = $tmp; To = $path }
  }
  & go build -o NUL ./cmd/aipstd
  if ($LASTEXITCODE -ne 0) { Write-Error "aipstd build failed without Phase 7 connectors" }
  # Only absence-safe gates: modules are renamed away, so "modules present" proofs must not run.
  & go test ./internal/archtest -run 'TestDefaultWireModelOmitsOptionalKinds|TestEssentialOnly_StandardBackendBundleIsEssentialOnly|TestEssentialOnly_AllowlistExact|TestDynamic_NoOptionalKindNamesInGenericPluginreg|TestEssentialBackendBundle_ExactAllowlist|TestRootGoMod_NoConnectorModules'
  if ($LASTEXITCODE -ne 0) { Write-Error "archtest failed without Phase 7/8 connectors" }
} finally {
  foreach ($r in $renamed) {
    if (Test-Path $r.From) {
      Rename-Item -Path $r.From -NewName (Split-Path $r.To -Leaf)
    }
  }
}

Write-Host "== package-full metadata includes Phase 7 artifacts =="
$dest = Join-Path $Root ".aip-package-staging\absence-full"
if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
& go run ./tools/backendplugin/package_plugins -root $Root -profile full -dest $dest
if ($LASTEXITCODE -ne 0) { Write-Error "package full failed" }
$indexPath = Join-Path $dest "package-index.json"
if (-not (Test-Path $indexPath)) { Write-Error "missing $indexPath" }
$index = Get-Content $indexPath -Raw
foreach ($kind in @("openrouter","nvidia","huggingface","ollama","llamacpp","lmstudio","vllm","opencode","codex")) {
  if ($index -notmatch $kind) {
    Write-Error "package index missing $kind"
  }
}

Write-Host "OK backend-plugin-absence-checks"
