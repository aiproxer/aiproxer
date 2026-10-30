param(
  [string]$OpenAIModel = "moonshotai/kimi-k2.7-code",
  [string]$AnthropicModel = "minimax/minimax-m3",
  [string]$VisionModel = "moonshotai/kimi-k2.7-code",
  [string]$BackendID = "opencode-go",
  [string]$BackendBaseURL = "https://opencode.ai/zen/go/v1",
  [string]$ProxyAddress = "",
  [int]$StartupTimeoutSeconds = 45,
  [int]$RequestTimeoutSeconds = 180,
  [switch]$SkipAnthropic,
  [switch]$SkipVision,
  [switch]$KeepArtifacts
)

$ErrorActionPreference = "Stop"

if ($BackendID -eq "opencode-go" -and [string]::IsNullOrWhiteSpace($env:OPENCODE_GO_API_KEY)) {
  throw "OPENCODE_GO_API_KEY is required for live OpenCode Go E2E"
}
if ($BackendID -eq "opencode-zen" -and [string]::IsNullOrWhiteSpace($env:OPENCODE_API_KEY) -and [string]::IsNullOrWhiteSpace($env:OPENCODE_ZEN_API_KEY)) {
  throw "OPENCODE_API_KEY or OPENCODE_ZEN_API_KEY is required for live OpenCode Zen E2E"
}
if ($BackendID -ne "opencode-go" -and $BackendID -ne "opencode-zen") {
  throw "BackendID must be opencode-go or opencode-zen"
}

$root = Split-Path -Parent $PSScriptRoot
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("aip-opencode-go-e2e-" + [System.Guid]::NewGuid().ToString("N"))
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

function Assert-ContainsAny {
  param(
    [string]$Text,
    [string[]]$Needles,
    [string]$Label
  )
  foreach ($needle in $Needles) {
    if ($Text -match $needle) {
      return
    }
  }
  throw "$Label response did not contain any expected marker. Response: $Text"
}

function Invoke-OpenAIChatNonStreaming {
  param([string]$Model)
  $body = @{
    model = $Model
    stream = $false
    max_tokens = 256
    messages = @(
      @{ role = "user"; content = "Reply with exactly these three words: AIProxer OPENCODE OK" }
    )
  } | ConvertTo-Json -Depth 10
  $headers = @{
    Authorization = "Bearer aip-e2e"
    "X-AIP-Route" = "${BackendID}:$Model"
  }
  Invoke-RestMethod -Method Post -Uri "http://$ProxyAddress/v1/chat/completions" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds
}

function Invoke-OpenAIChatStreaming {
  param([string]$Model)
  $body = @{
    model = $Model
    stream = $true
    max_tokens = 256
    messages = @(
      @{ role = "user"; content = "Reply with exactly these three words: AIProxer STREAM OK" }
    )
  } | ConvertTo-Json -Depth 10
  $headers = @{
    Authorization = "Bearer aip-e2e"
    "X-AIP-Route" = "${BackendID}:$Model"
  }
  Invoke-WebRequest -Method Post -Uri "http://$ProxyAddress/v1/chat/completions" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds -UseBasicParsing
}

function Invoke-AnthropicMessages {
  param([string]$Model)
  $body = @{
    model = $Model
    max_tokens = 256
    stream = $false
    messages = @(
      @{ role = "user"; content = "Reply with exactly these three words: AIProxer ANTHROPIC OK" }
    )
  } | ConvertTo-Json -Depth 10
  $headers = @{
    "x-api-key" = "aip-e2e"
    "anthropic-version" = "2023-06-01"
    "X-AIP-Route" = "${BackendID}:$Model"
  }
  Invoke-RestMethod -Method Post -Uri "http://$ProxyAddress/v1/messages" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds
}

function Invoke-OpenAIVision {
  param([string]$Model)
  Add-Type -AssemblyName System.Drawing
  $bmp = [System.Drawing.Bitmap]::new(32, 32)
  $graphics = [System.Drawing.Graphics]::FromImage($bmp)
  $stream = [System.IO.MemoryStream]::new()
  try {
    $graphics.Clear([System.Drawing.Color]::Red)
    $bmp.Save($stream, [System.Drawing.Imaging.ImageFormat]::Jpeg)
    $redJPEG = "data:image/jpeg;base64," + [Convert]::ToBase64String($stream.ToArray())
  } finally {
    $stream.Dispose()
    $graphics.Dispose()
    $bmp.Dispose()
  }
  $body = @{
    model = $Model
    stream = $false
    max_tokens = 256
    messages = @(
      @{
        role = "user"
        content = @(
          @{ type = "text"; text = "What is the dominant color in this image? Reply with one word." },
          @{ type = "image_url"; image_url = @{ url = $redJPEG } }
        )
      }
    )
  } | ConvertTo-Json -Depth 14
  $headers = @{
    Authorization = "Bearer aip-e2e"
    "X-AIP-Route" = "${BackendID}:$Model"
  }
  Invoke-RestMethod -Method Post -Uri "http://$ProxyAddress/v1/chat/completions" -Headers $headers -ContentType "application/json" -Body $body -TimeoutSec $RequestTimeoutSeconds
}

function Wait-ProxyReady {
  $deadline = (Get-Date).AddSeconds($StartupTimeoutSeconds)
  $healthURL = "http://$ProxyAddress/healthz"
  while ((Get-Date) -lt $deadline) {
    if ($script:proc -and $script:proc.HasExited) {
      throw "proxy exited before becoming healthy with code $($script:proc.ExitCode)"
    }
    try {
      $resp = Invoke-WebRequest -Method Get -Uri $healthURL -TimeoutSec 2 -UseBasicParsing
      if ($resp.StatusCode -eq 200) {
        return
      }
    } catch {
      Start-Sleep -Milliseconds 250
    }
  }
  throw "proxy did not become healthy at $healthURL within ${StartupTimeoutSeconds}s"
}

function Stop-ProxyProcess {
  if (-not $script:proc -or $script:proc.HasExited) {
    return
  }
  try {
    & taskkill.exe /PID $script:proc.Id /T /F | Out-Null
  } catch {
    Stop-Process -Id $script:proc.Id -Force
  }
  $script:proc.WaitForExit(5000) | Out-Null
}

function Remove-TempDirWithRetry {
  for ($i = 0; $i -lt 10; $i++) {
    try {
      Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction Stop
      return
    } catch {
      if ($i -eq 9) {
        Write-Warning "failed to remove temporary directory ${tmpDir}: $($_.Exception.Message)"
        return
      }
      Start-Sleep -Milliseconds 250
    }
  }
}

if ([string]::IsNullOrWhiteSpace($ProxyAddress)) {
  $ProxyAddress = New-FreeLoopbackAddress
}

$opencodeGoEnabled = if ($BackendID -eq "opencode-go") { "true" } else { "false" }
$opencodeZenEnabled = if ($BackendID -eq "opencode-zen") { "true" } else { "false" }

$config = @"
server:
  address: "$ProxyAddress"

routing:
  max_attempts: 1
  default_route: "${BackendID}:$OpenAIModel"

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
    - id: opencode-go
      enabled: $opencodeGoEnabled
      config:
        base_url: "$BackendBaseURL"
    - id: opencode-zen
      enabled: $opencodeZenEnabled
      config:
        base_url: "$BackendBaseURL"
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
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
  $script:proc = Start-Process -FilePath "go" -ArgumentList @("run", "./cmd/aipstd", "serve", "--config", $configPath) -WorkingDirectory $root -NoNewWindow -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
  Wait-ProxyReady

  $chat = Invoke-OpenAIChatNonStreaming -Model $OpenAIModel
  $chatText = [string]$chat.choices[0].message.content
  if ([string]::IsNullOrWhiteSpace($chatText)) {
    throw "OpenAI-compatible non-streaming response was empty"
  }
  Assert-ContainsAny -Text $chatText -Needles @("(?i)AIProxer", "(?i)OPENCODE", "(?i)OK") -Label "OpenAI-compatible non-streaming"
  Write-Host "$BackendID openai-compatible non-streaming: PASS"
  Write-Host "  model: $OpenAIModel"
  Write-Host "  response: $chatText"

  $stream = Invoke-OpenAIChatStreaming -Model $OpenAIModel
  $streamText = [string]$stream.Content
  if ($streamText -notmatch "data:") {
    throw "OpenAI-compatible streaming response did not look like SSE: $streamText"
  }
  Assert-ContainsAny -Text $streamText -Needles @("(?i)AIProxer", "(?i)STREAM", "(?i)OK") -Label "OpenAI-compatible streaming"
  Write-Host "$BackendID openai-compatible streaming: PASS"

  if (-not $SkipAnthropic) {
    $anthropic = Invoke-AnthropicMessages -Model $AnthropicModel
    $anthropicText = [string]($anthropic.content | ForEach-Object { $_.text })
    if ([string]::IsNullOrWhiteSpace($anthropicText)) {
      throw "Anthropic-compatible response was empty"
    }
    Assert-ContainsAny -Text $anthropicText -Needles @("(?i)AIProxer", "(?i)ANTHROPIC", "(?i)OK") -Label "Anthropic-compatible"
    Write-Host "$BackendID anthropic-compatible non-streaming: PASS"
    Write-Host "  model: $AnthropicModel"
    Write-Host "  response: $anthropicText"
  }

  if (-not $SkipVision) {
    $vision = Invoke-OpenAIVision -Model $VisionModel
    $visionText = [string]$vision.choices[0].message.content
    if ([string]::IsNullOrWhiteSpace($visionText)) {
      throw "Vision response was empty"
    }
    if ($visionText -notmatch "(?i)red") {
      throw "Vision response should prove image visibility by mentioning red. Response: $visionText"
    }
    Write-Host "$BackendID vision via openai-compatible chat: PASS"
    Write-Host "  model: $VisionModel"
    Write-Host "  response: $visionText"
  }

  $ok = $true
} finally {
  Stop-ProxyProcess
  if (-not $ok -or $KeepArtifacts) {
    Write-Host "temporary config: $configPath"
    Write-Host "aipstd stdout: $stdoutPath"
    Write-Host "aipstd stderr: $stderrPath"
  } else {
    Remove-TempDirWithRetry
  }
}
