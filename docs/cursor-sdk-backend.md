# Cursor SDK backend (experimental)

The `cursorsdk` backend is an **experimental**, separately routable Cursor connector delivered as the standalone executable backend plugin [`aiproxer/aiproxer-cursor-sdk`](https://github.com/aiproxer/aiproxer-cursor-sdk) (manifest-discovered executable plugin + private bridge companion over exact `@cursor/sdk` **1.0.23**). It does **not** replace `cursorcliacp`. ACP remains independently selectable. There is no connector-local fallback between the two.

Operator sample: [`config/examples/cursor-sdk-experimental.yaml`](../config/examples/cursor-sdk-experimental.yaml). Standalone installation and SDK provisioning: [`aiproxer-cursor-sdk`](https://github.com/aiproxer/aiproxer-cursor-sdk).

## Status

- Experimental and **non-default**. Not part of `aipsdk.StandardDistributionRequirements` or `EssentialBackendBundle`.
- **Standalone certified release, and the only delivery form.** The integration's maintained home is the standalone executable backend plugin [`aiproxer/aiproxer-cursor-sdk`](https://github.com/aiproxer/aiproxer-cursor-sdk), which owns the Go connector, the SDK bridge, the private JavaScript runtime, packaging, and the SDK/Undici pins. The in-tree `connectors/cursorsdk` module was removed from this repository by the host cutover, so this document describes the plugin's install path only.
- **Release closure is owned externally.** The certified release and its native platform evidence are recorded in the plugin project and in the cutover spec's `release-certification.json`; the host keeps the connector contract, not the connector source. Install the certified [`aiproxer-cursor-sdk`](https://github.com/aiproxer/aiproxer-cursor-sdk) release and provision its operator-owned SDK per that repository's documented provisioning command; live and platform verification runs there, not in this repository.
- Delivered only as the standalone plugin (closed manifest + digest-bound executable). Root-static registration is forbidden.
- Local-only (`BackendAccessLocalOnly` / `access_scope: local_only`): single-user loopback only. Rejected under `access.mode: multi_user`, and additionally not present in the host-owned multi-user approval registry, so absence of the approval is a second, independent denial.
- **Why local-only is the current contract, not a permanent SDK law:** Cursor's SDK supports both user API keys and service-account API keys, and service accounts are intended for team automation. A deliberately designed, separately reviewed Cursor **service-account / cloud-agent** backend could therefore be multi-user-safe in future. Today's `cursorsdk` factory is `local_only` because its current security/runtime contract does not establish the shared-service isolation and credential binding such a mode requires. Do not obtain that capability by changing this factory's `access_scope`; it requires a distinct factory plus an explicit host approval-registry change.
- Future default switch or ACP deprecation requires a separate reviewed migration; this feature retains both connectors.

## Install the plugin (manual)

AIProxer **never** runs `npm install`, `npm ci`, or other package lifecycle scripts during config validation, startup, model discovery, or request handling. Install the certified standalone plugin release and provision its operator-owned SDK with the plugin's documented provisioning command (see the plugin repository); the plugin never downloads dependencies itself.

Prerequisites:

| Requirement | Value |
| --- | --- |
| Node | `>=22.13` (the plugin ships its own private runtime; no system Node is required) |
| `@cursor/sdk` | exact `1.0.23` (pinned in the plugin's bridge `package.json` / lockfile) |
| Platforms | The plugin ships `linux/amd64` and `windows/amd64` archives, each assembled and verified natively. `windows/arm64`, `linux/arm64` and `darwin/*` are not declared. |

The plugin repository documents the exact download, digest check, unpack, install and provisioning steps. The host then discovers the plugin through `plugins.backend_discovery.paths`, and the plugin's private bridge launcher is addressed through `bridge_executable`: on `windows/amd64` the explicit path is required, on `linux/amd64` the packaged default resolves on its own. Lookup is direct PATH/absolute resolution only — no shell, no `npm` launcher names.

The plugin's own `doctor` command answers the version and provisioning questions without the Go tooling (`<plugin-root>/private/bridge/aip-cursor-sdk-bridge[.exe] doctor`).

## API key and billing

SDK auth is a **static Cursor SDK API key**, separate from Cursor CLI login, Cursor Desktop login, and `cursorcliacp` auth state. Do not treat those as substitutes. Billing for the SDK key is Cursor’s SDK/API billing surface, not the CLI subscription login path.

The key is delivered exclusively as a host secret. Bind the instance to the host environment root `CURSOR_API_KEY` through `backend_discovery.instances`:

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/AIProxer/plugins/cursorsdk
    instances:
      cursor-sdk:
        kind: cursorsdk
        secret_refs:
          api_key: {env: CURSOR_API_KEY}
```

Rules the connector enforces:

1. Composition-root env `CURSOR_API_KEY` only (bare name; **no** `CURSOR_API_KEY_2` / `_N` numbering; exactly one slot admitted per session).
2. A literal YAML `api_key` field is rejected at config load with a field-path error naming the approved environment root (fail closed, never a fallback). Connector YAML never carries key material, and the connector never reads process environment or files for credentials on its own.
3. Missing key → actionable config error naming `CURSOR_API_KEY`; secret values are redacted from errors.

The key is sent only in a private bridge request frame after handshake — never on argv or in the child process environment.

## Registration and route selection

- Kind / route prefix: `cursorsdk`.
- Canonical model IDs: `cursor/<native-id>` (same vendor namespace as ACP).
- Registry provenance keeps backend instance and kind distinct from `cursorcliacp`.
- When both connectors are configured, routes must identify the intended backend. A bare model name alone must not silently pick SDK vs ACP.
- Example selectors: `cursorsdk:cursor/<model>`, `cursorcliacp:cursor/<model>`.
- Pre-output failures follow the operator-configured core routing plan only; the adapter never falls back to the other Cursor connector.

## Configuration reference

| Key | Default | Notes |
| --- | --- | --- |
| `bridge_executable` | packaged default (omit the field) | Direct executable; shell/npm names rejected. Omit on Linux to use the private companion next to the installed plugin; on Windows set the absolute installed launcher path (required) |
| `model` | inventory | Optional default model hint |
| `default_workspace` / `workspace_path` / `project_dir` | none | Explicit workspace required per agent identity |
| `mcp_servers` | empty | YAML/JSON object, normalized, max 256 KiB |
| `setting_sources` | `[]` | Empty = none. Allowed: `project`, `user`, `team`, `mdm`, `plugins`, `all` |
| `sandbox_mode` | `required` | `required` or explicit `off` |
| `auto_review` | `false` | Independent of sandbox/trust |
| `bridge_env_allowlist` | platform minimum | Extra names only; credential env names rejected |
| `max_agents` | `32` | 1–32 |
| `max_concurrent_runs` | `8` | 1–8 and ≤ `max_agents` |
| `bridge_start_timeout_seconds` | `30` | 1–120 |
| `cancel_timeout_seconds` | `5` | 0.1–30 |
| `shutdown_timeout_seconds` | `10` | 1–120 |
| `agent_idle_timeout_seconds` | `900` | `0` disables idle eviction; else 1s–24h |
| `models` | live discovery | Optional static inventory override (existing shape) |

### Settings and sandbox

- Default `setting_sources: []` — no ambient Cursor project/user/team settings until explicitly trusted.
- Default `sandbox_mode: required` — fails closed if the SDK reports sandbox unavailable.
- Windows x64 may need explicit local-only `sandbox_mode: off` when sandbox support is unavailable; never silently downgraded.
- `auto_review` defaults off and is not a substitute for sandbox or workspace trust.
- `enableAgentRetries` is forced false inside the bridge; custom tools / `local.force` are not exposed.

### MCP

- Pass configured MCP servers as a YAML/JSON object under `mcp_servers`.
- Values are normalized to deterministic JSON; non-objects and oversize configs fail closed.
- First delivery does **not** bridge AIProxer tools into the SDK via custom tools, local callbacks, or an implicit AIProxer↔MCP bridge.
- SDK-native tools and configured MCP run inside the Cursor agent; they are **not** replayed as canonical frontend tool calls.

## Capabilities and limits

Omitted (fail negotiation / reject per core policy) unless later proven:

- canonical tools and parallel tool calls
- vision, documents, structured outputs

Also:

- `EnforcesMaxOutputTokens: false` — max-output requests fail closed rather than silently truncating.
- Reasoning effort maps only through exact catalog-driven SDK equivalents (`reasoning` values, or `effort` via variants that enable thinking). No `xhigh` ↔ `extra-high` aliasing. Boolean-thinking-only models do not advertise canonical reasoning effort.

Frozen bounds: bridge frame 16 MiB; prompt 8 MiB; MCP 256 KiB; retained stderr 8 KiB.

## Streaming, cancel, restart, continuity

- Streaming is primary; canonical order is response start → message start → text/reasoning deltas → conservative per-turn usage → one terminal → EOF.
- Cancel: provider `run.cancel` first; timeout/unresponsive bridge escalates to current-generation process-tree termination.
- Bridge crash/restart invalidates all bridge-local agent/run handles; later work rebuilds from the **canonical** transcript.
- Continuity is **process-local**. SDK `Agent.resume` is not used across AIProxer or bridge restarts.
- Post-output failures surface on the committed B-leg; no silent retry/switch after first client-visible content.

## Platform matrix and support

| Lane | What it proves |
| --- | --- |
| Standalone plugin Go tests | Fake bridge / fixtures; no Node, npm, account, or network |
| Standalone plugin verification | Current-OS fake-bridge lifecycle (start/stream/cancel-terminal/crash/restart/rebootstrap/shutdown); no API key |
| Standalone plugin CI | Linux / macOS / Windows matrix (fake bridge) |
| Standalone plugin live scenarios | Opt-in real `@cursor/sdk` Node scenarios only (no Go process lifecycle hooks) |
| Standalone plugin live bridge | Opt-in Go→Node lifecycle via production `Open`/`RunStream`; pin/discovery/content/cancel/shutdown |

Missing Node, old Node, or unpinned bridge → probe reports **blocked**, not a silent pass. Experimental rollout evidence is accepted; measured live/comparative dogfood remains opt-in blocked without credentials.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Bridge not found | Install the standalone plugin release; set `bridge_executable` to its private launcher (required on Windows); confirm no shell/`npm` wrapper |
| Wrong SDK version | The plugin's `aip-cursor-sdk-bridge --version` / `doctor` must show `@cursor/sdk` 1.0.23 |
| Node too old | The plugin ships its own runtime; a system Node is not required |
| Missing API key | Bind `api_key: {env: CURSOR_API_KEY}` under `backend_discovery.instances` for the instance; never put the key in connector YAML (not CLI login either) |
| Sandbox unavailable | Prefer fixing sandbox; local-only explicit `sandbox_mode: off` on Windows when required |
| Multi-user rejection | Expected for local-only `cursorsdk`; `access.mode: multi_user` also requires a host-owned approval-registry entry this factory does not have |
| Model-only route ambiguity | Use `cursorsdk:…` or `cursorcliacp:…` explicitly |
| Auth / capability / config errors | Non-recoverable; not treated as transient process faults |

Safe diagnostics expose backend kind/instance, bridge/SDK versions, discovery state, and bounded counts — not keys, prompts, paths, or raw SDK IDs.

## Live tests (opt-in)

Live and platform verification now runs in the standalone plugin repository. See its documented commands there; host `make test-cursor-sdk-*` targets and the former host platform workflow have been retired.

Rules:

- Without `CURSOR_SDK_LIVE=1` or without `CURSOR_API_KEY`, plugin scripts exit **0** with `BLOCKED` (safe skip; not a green live proof).
- Opted-in Node live prints a JSON summary with `status`: `complete` | `blocked` | `failed` and `ok` true only for `complete`. Incomplete/blocked required scenarios (e.g. missing hard-restart hooks, sandbox unavailable) set `ok: false`; CLI exits nonzero (`3` blocked, `1` failed).
- The plugin live-bridge harness emits one parent JSON summary on stdout. Parent `status` may be `blocked` with process exit **0** when required live proofs are honestly unavailable (not a silent pass). It verifies `@cursor/sdk` **1.0.23** via `--version`, launches the private runtime directly (no shell/npm), keeps the API key out of argv/child env, and drives production `backendRuntime.Open` → `RunStream`.
- Observed credentialed Windows live (sanitized aggregates only): Node probe passed pin/count/deltas/dispose/cancel; Node core discovery/text/safety_off/configured_mcp/cancel/shutdown passed; reasoning skipped without thinking-delta; sandbox-required blocked; Node hard-restart/rebootstrap hooks blocked. Reuse last credentialed observation (before text-only reuse prompt update) timed out and **has not been revalidated** afterward — do **not** claim Node reuse live-passed. Go live-bridge (`go test -v` JSON): parent may be `status=blocked` exit 0; pin/discovery/canonical single terminal/cancelled terminal/no later content/shutdown passed; hard restart blocked because active text-only peers could not be held; rebootstrap/MCP instrumentation blocked; sandbox-required blocked. Do **not** treat hard restart, rebootstrap, or Node reuse as live-passed.
- Canonical full-prompt rebootstrap is **passed** only with create-count/prompt-capture instrumentation (fake/plugin platform lane). Fast provider finish before a cancellable in-flight state is **blocked**, not a pass.
- Per-scenario temp workspaces, strict timeouts, isolated state.
- Do **not** collect prompts, tool content, raw workspace paths, keys, or SDK agent/run IDs in artifacts.
- Live suites are separate from default `make test` / CI unit lanes (`cursorsdk_live_bridge` tag is required for the Go harness entrypoint).

## ACP vs SDK comparison

Comparison reporting now runs in the standalone plugin repository.

Offline runs label cells `synthetic` or `blocked` and keep `replacement_status: retain_both_connectors`. This does **not** change defaults or deprecate `cursorcliacp`.

## Minimal enable sketch

```yaml
routing:
  default_route: "cursorsdk:cursor/<model-from-inventory>"

plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/AIProxer/plugins/cursorsdk
    instances:
      cursor-sdk:
        kind: cursorsdk
        secret_refs:
          api_key: {env: CURSOR_API_KEY}
  backends:
    - kind: cursorsdk
      id: cursor-sdk
      enabled: true
      config:
        # No api_key field here: the key arrives only through the instances
        # secret_refs binding above.
        # bridge_executable omitted on Linux (packaged default companion); on
        # Windows set the absolute installed launcher path, e.g.
        # bridge_executable: "C:/AIProxer/plugins/cursorsdk/private/bridge/aip-cursor-sdk-bridge.exe"
        default_workspace: /absolute/path/to/workspace
        setting_sources: []          # default: none
        sandbox_mode: required       # Windows may need explicit off (local-only)
        auto_review: false
        mcp_servers: {}              # optional configured MCP object
```

The named route is optional. The bridge discovers its models at runtime, so
without `routing.default_route` the default route is the default model the
enabled instance designated — the model the bridge reports as its default, or
its only published model. `aipstd routes` shows the result as
`effective_default_route`, with `effective_default_route_model` and
`effective_default_route_source: discovered_inventory`. Naming the route pins
one model and always wins over the discovered one. The two instructions above
are about different connectors: the derived default route belongs to the
enabled `cursorsdk` instance, and `cursorcliacp` is never reached by it.

Keep `cursorcliacp` enabled only when you intentionally route to it with an explicit `cursorcliacp:…` selector.
