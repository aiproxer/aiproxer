param(
  [string]$Model = "",
  [string]$OllamaBaseUrl = "http://localhost:11434/v1",
  [string]$ProxyAddress = "",
  [switch]$Cloud,
  [switch]$Image,
  [string]$ImageModel = "",
  [switch]$SkipDirectOllamaPreflight,
  [int]$StartupTimeoutSeconds = 30,
  [int]$RequestTimeoutSeconds = 120
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("aip-ollama-smoke-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null
$configPath = Join-Path $tmpDir "config.yaml"
$stdoutPath = Join-Path $tmpDir "aipstd.stdout.log"
$stderrPath = Join-Path $tmpDir "aipstd.stderr.log"

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

function Read-FirstOllamaModel {
  $modelsURL = ($OllamaBaseUrl.TrimEnd("/")) + "/models"
  $models = Invoke-RestMethod -Method Get -Uri $modelsURL -TimeoutSec 10
  if (-not $models.data -or $models.data.Count -eq 0) {
    throw "Ollama returned no models from $modelsURL"
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
      @{ role = "user"; content = "Reply with exactly: AIProxer OLLAMA TEXT SMOKE OK" }
    )
  } | ConvertTo-Json -Depth 8

  return Invoke-RestMethod -Method Post -Uri $Uri -Headers $Headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds
}

function Invoke-ImageChat {
  param(
    [string]$Uri,
    [string]$RequestModel,
    [hashtable]$Headers = @{}
  )
  $redPixelPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADUlEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
  $body = @{
    model = $RequestModel
    stream = $false
    max_tokens = 32
    messages = @(
      @{
        role = "user"
        content = @(
          @{ type = "text"; text = "What is the dominant color in this image? Reply with one word." },
          @{ type = "image_url"; image_url = @{ url = $redPixelPNG } }
        )
      }
    )
  } | ConvertTo-Json -Depth 12

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

if ([string]::IsNullOrWhiteSpace($Model)) {
  $Model = Read-FirstOllamaModel
}
if ($Cloud -and -not $Model.EndsWith("-cloud")) {
  $Model = "$Model-cloud"
}
if ($Image -and [string]::IsNullOrWhiteSpace($ImageModel)) {
  $ImageModel = $Model
}
if ([string]::IsNullOrWhiteSpace($ProxyAddress)) {
  $ProxyAddress = New-FreeLoopbackAddress
}

if (-not $SkipDirectOllamaPreflight) {
  try {
    $direct = Invoke-TextChat -Uri (($OllamaBaseUrl.TrimEnd("/")) + "/chat/completions") -RequestModel $Model
    $directText = [string]$direct.choices[0].message.content
    if ([string]::IsNullOrWhiteSpace($directText)) {
      throw "direct Ollama returned an empty assistant message"
    }
    Write-Host "ollama direct text preflight: PASS"
  } catch {
    throw "direct Ollama text preflight failed for model '$Model': $($_.Exception.Message)"
  }
  if ($Image) {
    try {
      $directImage = Invoke-ImageChat -Uri (($OllamaBaseUrl.TrimEnd("/")) + "/chat/completions") -RequestModel $ImageModel
      $directImageText = [string]$directImage.choices[0].message.content
      if ($directImageText -notmatch "(?i)red") {
        throw "expected image response to mention red, got: $directImageText"
      }
      Write-Host "ollama direct image preflight: PASS"
    } catch {
      throw "direct Ollama image preflight failed for model '$ImageModel': $($_.Exception.Message)"
    }
  }
}

$defaultRoute = if ($Cloud) { "ollama-cloud:$Model" } else { "ollama:$Model" }
$proxyRequestModel = if ($Cloud) { "ollama-cloud:$Model" } else { "ollama:$Model" }

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
      enabled: $(if ($Cloud) { "false" } else { "true" })
      config:
        base_url: "$OllamaBaseUrl"
        responses_api: disabled
        discovery:
          enabled: true
          local_models: true
          cloud_models: false
          capabilities: true
          timeout: 10s
    - id: ollama-cloud
      enabled: $(if ($Cloud) { "true" } else { "false" })
      config:
        base_url: "$OllamaBaseUrl"
        responses_api: disabled
        discovery:
          enabled: true
          cloud_models: true
          capabilities: true
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
  if ($text -notmatch "(?i)AIProxer|OLLAMA|SMOKE|OK") {
    throw "unexpected assistant response: $text"
  }

  Write-Host "ollama text smoke: PASS"
  Write-Host "model: $Model"
  Write-Host "response: $text"
  if ($Image) {
    $imageResp = Invoke-ImageChat -Uri "http://$ProxyAddress/v1/chat/completions" -RequestModel $proxyRequestModel -Headers $headers
    $imageText = [string]$imageResp.choices[0].message.content
    if ($imageText -notmatch "(?i)red") {
      throw "expected proxy image response to mention red, got: $imageText"
    }
    Write-Host "ollama image smoke: PASS"
    Write-Host "image model: $ImageModel"
    Write-Host "image response: $imageText"
  }
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
}
