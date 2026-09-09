# WorkBuddy AI ACP Backend (`workbuddy-acp`)

The `workbuddy-acp` connector enables routing chat completions and agent requests through your locally installed **WorkBuddy AI** application using your active account quota.

## Overview

WorkBuddy AI is an AI-assisted coding environment powered by Tencent. Under the hood, the WorkBuddy application bundles a Node.js runtime and the `codebuddy` CLI, which communicates natively using the **Agent Client Protocol (ACP)** over standard input/output (`stdio`).

The proxy hooks into this engine via `codebuddy --acp --acp-transport stdio`, managing session lifecycles, JSON-RPC handshakes, permission bypass, and full two-way streaming.

## Model Picking & Tier Mapping

Upstream Tencent APIs enforce strict account-level model tier routing rather than accepting arbitrary model strings. Sending raw model IDs (such as `hy4` or `hy4 preview`) directly to the upstream server results in a `400 model [...] service info not found` rejection.

WorkBuddy accounts map available models into **4 abstract tiers**:

| Tier Identifier | Underlying Engine | Context Window | Credit Weight |
|---|---|---|---|
| **`primary-model`** | **Hunyuan 4 (hy4 preview flagship)** | **272k tokens** | **1.70×** |
| **`deep-model`** | Deep reasoning / extended thinking | 176k tokens | 2.20× |
| **`balanced-model`** | Daily balanced coding model | 256k tokens | 0.59× |
| **`fast-model`** | MiniMax (fast lightweight completion) | 200k tokens | 0.34× |

### Model Vendor Prefix (`workbuddy/`)

To access these models via the proxy, prepend the `workbuddy/` vendor prefix. The connector automatically resolves common aliases to the appropriate upstream tier:

| Requested Model | Target Tier | Description |
|---|---|---|
| `workbuddy/hy4` | `primary-model` | **Recommended** for Tencent Hunyuan 4 flagship |
| `workbuddy/hy4-preview` | `primary-model` | Alias for Hunyuan 4 |
| `workbuddy/hunyuan` | `primary-model` | Alias for Hunyuan 4 |
| `workbuddy/auto` | `primary-model` | Default model alias |
| `workbuddy/primary-model` | `primary-model` | Direct native tier identifier |
| `workbuddy/deep` | `deep-model` | Deep reasoning engine |
| `workbuddy/deep-model` | `deep-model` | Direct native tier identifier |
| `workbuddy/balanced` | `balanced-model` | Daily balanced model |
| `workbuddy/balanced-model` | `balanced-model` | Direct native tier identifier |
| `workbuddy/fast` | `fast-model` | Fast lightweight model (MiniMax) |
| `workbuddy/fast-model` | `fast-model` | Direct native tier identifier |

*(Note: Bare names like `hy4` and `primary-model` are also registered as global aliases in the proxy's model capability index).*

## Runtime & CLI Resolution

The connector automatically discovers:
1. **Node.js**:
   - Bundled with WorkBuddy: `~/.workbuddy-ai/binaries/node/versions/*/node.exe`
   - Host `PATH` (`node` / `node.exe`)
   - Override via config `node_executable` or env `WORKBUDDY_NODE_BIN`
2. **WorkBuddy CLI (`codebuddy`)**:
   - Windows Default: `%LOCALAPPDATA%\Programs\WorkBuddyAI\resources\app.asar.unpacked\cli\bin\codebuddy`
   - Program Files: `%PROGRAMFILES%\WorkBuddyAI\resources\app.asar.unpacked\cli\bin\codebuddy`
   - Host `PATH` (`codebuddy`)
   - Override via config `cli_path` or env `WORKBUDDY_CLI_PATH`

## Configuration

### `config/config.yaml`

```yaml
backends:
  workbuddy-acp:
    connector: "workbuddy-acp"
    timeout: 300
    models:
      - "workbuddy/primary-model"
      - "workbuddy/deep-model"
      - "workbuddy/balanced-model"
      - "workbuddy/fast-model"
      - "workbuddy/hy4"
      - "workbuddy/hy4-preview"
      - "workbuddy/hunyuan"
      - "workbuddy/auto"
    extra:
      permission_mode: "bypassPermissions"
      thought_level: "enabled"
      process_timeout: 300
      idle_timeout: 60
```

### Backend Instance YAML (`config/backends/backend-instances/workbuddy-acp.default.yaml`)

```yaml
connector: workbuddy-acp
models:
  - "workbuddy/primary-model"
  - "workbuddy/deep-model"
  - "workbuddy/balanced-model"
  - "workbuddy/fast-model"
  - "workbuddy/hy4"
  - "workbuddy/hy4-preview"
  - "workbuddy/hunyuan"
  - "workbuddy/auto"
extra:
  model: "workbuddy/primary-model"
  permission_mode: "bypassPermissions"
  thought_level: "enabled"
  process_timeout: 300
  idle_timeout: 60
  mcp_servers: []
```

## Usage Examples

### 1. Standard Chat Completion

```bash
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <your-proxy-key>" \
  -d '{
    "model": "workbuddy/hy4",
    "messages": [
      {"role": "user", "content": "Write a Python function to compute fibonacci numbers."}
    ],
    "stream": false
  }'
```

### 2. Streaming (Server-Sent Events)

```bash
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <your-proxy-key>" \
  -d '{
    "model": "workbuddy/hy4",
    "messages": [
      {"role": "user", "content": "Explain asynchronous programming in Python in 3 bullet points."}
    ],
    "stream": true
  }'
```
