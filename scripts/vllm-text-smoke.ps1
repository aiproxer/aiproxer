# End-to-end text smoke against a running vLLM OpenAI-compatible server and aipstd.
# Default base URL is localhost:8000/v1; override -VllmBaseUrl for other hosts/ports
# (for example WSL CPU vLLM on http://127.0.0.1:18000/v1; see docs/dogfood-local.md).
param(
  [string]$Model = "",
  [string]$VllmBaseUrl = "http://localhost:8000/v1",
  [string]$VllmApiKey = "vllm",
  [string]$ProxyAddress = "",
  [string]$ExpectedResponsePattern = "",
  [switch]$SkipDirectVllmPreflight,
  [int]$StartupTimeoutSeconds = 30,
  [int]$RequestTimeoutSeconds = 120
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("aip-vllm-smoke-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null
$configPath = Join-Path $tmpDir "config.yaml"
$stdoutPath = Join-Path $tmpDir "aipstd.stdout.log"
$stderrPath = Join-Path $tmpDir "aipstd.stderr.log"

function Get-VllmServerRoot {
  $serverRoot = $VllmBaseUrl.TrimEnd("/")
  if ($serverRoot.EndsWith("/v1")) {
    $serverRoot = $serverRoot.Substring(0, $serverRoot.Length - 3)
  }
  return $serverRoot.TrimEnd("/")
}

function Test-VllmHealth {
  $healthURL = (Get-VllmServerRoot) + "/health"
  $resp = Invoke-WebRequest -Method Get -Uri $healthURL -TimeoutSec 10
  if ($resp.StatusCode -ne 200) {
    throw "vLLM health check failed at $healthURL with status $($resp.StatusCode)"
  }
}

function New-FreeLoopbackAddress {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Parse("127.0.0.1"), 0)
  $listener.Start()
  try {
    $port = $listener.LocalEndpoint.Port
  } finally {
    $listener.Stop()
  }
  return "127.0.0.1:$port"
}

function Read-FirstVllmModel {
  $modelsURL = ($VllmBaseUrl.TrimEnd("/")) + "/models"
  $models = Invoke-RestMethod -Method Get -Uri $modelsURL -TimeoutSec 10
  if (-not $models.data -or $models.data.Count -eq 0) {
    throw "vLLM returned no models from $modelsURL"
  }
  return [string]$models.data[0].id
}

function Invoke-TextChat {
  param(
    [string]$Uri,
    [string]$RequestModel,
    [hashtable]$Headers = @{}
  )
  $body = @{
    model = $RequestModel
    stream = $false
    max_tokens = 64
    messages = @(
      @{ role = "user"; content = "Respond with exactly: AIProxer VLLM TEXT SMOKE OK" }
    )
  } | ConvertTo-Json -Depth 8

  return Invoke-RestMethod -Method Post -Uri $Uri -Headers $Headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds
}

function Wait-ProxyReady {
  $deadline = (Get-Date).AddSeconds($StartupTimeoutSeconds)
  $healthURL = "http://$ProxyAddress/healthz"
  while ((Get-Date) -lt $deadline) {
    if ($script:proc -and $script:proc.HasExited) {
      throw "proxy exited before becoming healthy with code $($script:proc.ExitCode)"
    }
    try {
      $resp = Invoke-WebRequest -Method Get -Uri $healthURL -TimeoutSec 2
      if ($resp.StatusCode -eq 200) {
        return
      }
    } catch {
      Start-Sleep -Milliseconds 250
    }
  }
  throw "proxy did not become healthy at $healthURL within ${StartupTimeoutSeconds}s"
}

if ([string]::IsNullOrWhiteSpace($ProxyAddress)) {
  $ProxyAddress = New-FreeLoopbackAddress
}

Write-Host "vllm base url: $VllmBaseUrl"

$vllmHeaders = @{
  "Authorization" = "Bearer $VllmApiKey"
}

if (-not $SkipDirectVllmPreflight) {
  try {
    Test-VllmHealth
    Write-Host "vllm health preflight: PASS"
  } catch {
    throw "vLLM health preflight failed: $($_.Exception.Message)"
  }
}

if ([string]::IsNullOrWhiteSpace($Model)) {
  $Model = Read-FirstVllmModel
}

if (-not $SkipDirectVllmPreflight) {
  try {
    $direct = Invoke-TextChat -Uri (($VllmBaseUrl.TrimEnd("/")) + "/chat/completions") -RequestModel $Model -Headers $vllmHeaders
    $directText = [string]$direct.choices[0].message.content
    if ([string]::IsNullOrWhiteSpace($directText)) {
      throw "direct vLLM returned an empty assistant message"
    }
    Write-Host "vllm direct text preflight: PASS"
  } catch {
    throw "direct vLLM text preflight failed for model '$Model': $($_.Exception.Message)"
  }
}

$defaultRoute = "vllm:$Model"
$proxyRequestModel = "vllm:$Model"

$config = @"
server:
  address: "$ProxyAddress"
  read_header_timeout: 10s
  read_timeout: 60s
  write_timeout: 180s
  idle_timeout: 60s

routing:
  max_attempts: 1
  default_route: "$defaultRoute"

continuity:
  in_memory: true
  store: memory

logging:
  level: info
  format: text

diagnostics:
  enabled: true
  health_path: "/healthz"

hooks:
  tool_reactor_error_policy: fail_open

plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: true
      config:
        base_url: "$VllmBaseUrl"
        api_key: "$VllmApiKey"
        discovery:
          catalog: true
          timeout: 10s
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
"@

Set-Content -LiteralPath $configPath -Value $config -Encoding UTF8

$script:proc = $null
$ok = $false
try {
  $script:proc = Start-Process -FilePath "go" -ArgumentList @("run", "./cmd/aipstd", "--config", $configPath, "serve") -WorkingDirectory $root -NoNewWindow -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
  Wait-ProxyReady

  $headers = @{
    "Authorization" = "Bearer smoke"
  }
  $resp = Invoke-TextChat -Uri "http://$ProxyAddress/v1/chat/completions" -RequestModel $proxyRequestModel -Headers $headers
  $text = [string]$resp.choices[0].message.content
  if ([string]::IsNullOrWhiteSpace($text)) {
    throw "proxy returned an empty assistant message"
  }
  if (-not [string]::IsNullOrWhiteSpace($ExpectedResponsePattern) -and $text -notmatch $ExpectedResponsePattern) {
    throw "unexpected assistant response: $text"
  }

  Write-Host "vllm-text-smoke: PASS"
  Write-Host "model: $Model"
  Write-Host "response: $text"
  $ok = $true
} finally {
  if ($script:proc -and -not $script:proc.HasExited) {
    Stop-Process -Id $script:proc.Id -Force
    $script:proc.WaitForExit(5000) | Out-Null
  }
  if (-not $ok -and $script:proc -and $script:proc.ExitCode -ne 0 -and $null -ne $script:proc.ExitCode) {
    Write-Host "aipstd stdout: $stdoutPath"
    Write-Host "aipstd stderr: $stderrPath"
  }
  if ($ok -and (Test-Path -LiteralPath $tmpDir)) {
    Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
