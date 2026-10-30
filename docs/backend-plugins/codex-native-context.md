# Codex Native Context (Default-On)

The direct `openai-codex` connector has native context support for exact
encrypted reasoning continuity and Responses Compaction V2. It is default-on
when `native_context` is omitted. The shipped OpenCode/Codex example below is
an explicit opt-out example, not the active default configuration.

## Companion configuration

Native full mode is the default for direct Codex. Use this explicit block to
override individual controls or to opt out:

```yaml
plugins:
  backends:
    - id: openai-codex
      enabled: true
      config:
        native_context:
          enabled: false
          request_encrypted_reasoning: false
          reasoning_continuity: disabled
          compaction:
            enabled: false
    # The feature rule targets this instance ID, not the factory kind.
  features:
    - id: reasoning-output-preservation
      enabled: true
      config:
        action: restore
        use_builtin_catalog: true
        rules:
          - id: codex-native-context-openai-codex
            backend: openai-codex
            enabled: true
        on_ambiguous: log_skip
        on_unrepresentable: reject
        on_state_error: reject
```

The standard runtime installs one bounded backend-only Codex rule per enabled
direct instance when no matching reasoning feature row is present. The rule's
`backend` is the exact configured instance ID (for example, `openai-codex` in
the minimal example), and it has no model keywords or GPT minor-version ceiling.
An explicitly enabled feature row retains its policies and receives only missing
companion rules. A matching disabled feature row is the explicit global
companion-feature opt-out; an exact disabled backend rule wins for that instance.
The backend `native_context.enabled: false` is a complete local opt-out;
`compaction.enabled: false` disables only compaction and does not suppress the
reasoning companion.
The feature observer remains the only owner of surfaced-winner reasoning state;
native compaction stores only bounded, connector-private process-local
checkpoints.

Reasoning-only and evaluation-only compaction modes can be selected by setting
`compaction.enabled: false` or `reasoning_continuity: best_effort` as appropriate.
Required mode skips compaction when the trusted continuity marker is absent and
falls back to full history.

## Context budget policy

The planner uses the named `CodexHarnessHeadroomV1` policy. Exact catalog/model
metadata wins for headline hard context and `auto_compact_token_limit`, but the
trigger is still below a usable ceiling after reserving headroom for the Codex
harness system prompt/tooling, other agent tools, cross-harness glue, and output
margin.

- Exact catalog profile: use its hard context and auto-compact threshold when
  safe, then enforce retained-window and policy headroom.
- `gpt-5.3-codex-spark` fallback: headline `128000`, usable ceiling `96000`,
  safe trigger `80000`, reserved headroom `32000`.
- Other GPT-5.x fallback: usable ceiling `250000`, safe trigger `220000`, and
  reserved planning headroom `30000`. This is not a guessed headline limit.

An explicit `trigger_tokens` override remains supported but must be positive and
below the usable ceiling and hard-limit/retained-window/headroom checks. Unsafe
overrides fail validation instead of silently consuming harness headroom.

## Live automatic-compaction proof

The production-path proof is opt-in and billable. It uses the connector's normal
`Engine.Open` HTTPS path, the local Codex `auth.json` resolver by default, and a
small synthetic history with `gpt-5.3-codex-spark` unless overridden. It records
only booleans/counts/model/status in memory; it does not persist request bodies,
headers, prompts, reasoning, or ciphertext.

Prerequisites are a usable Codex CLI login in `~/.codex/auth.json` (or an
explicit token override) and access to Responses Compaction V2 for the selected
model. The command incurs provider usage and must not be used in the default
test suite:

```powershell
$env:AIP_CODEX_NATIVE_CONTEXT_LIVE = "1"
# Optional: $env:AIP_CODEX_NATIVE_CONTEXT_MODEL = "gpt-5.3-codex-spark"
# Optional: $env:AIP_CODEX_NATIVE_CONTEXT_TOKEN = "..."
# Optional: $env:AIP_CODEX_NATIVE_CONTEXT_AUTH_JSON = "C:\path\to\auth.json"
# Optional: $env:AIP_CODEX_NATIVE_CONTEXT_BASE_URL = "https://chatgpt.com/backend-api/codex"
Push-Location connectors/codex
go test -run '^TestNativeContextAutomaticCompactionLive$' ./internal/codex
Pop-Location
```

The mandatory automatic-compaction proof requires four successful upstream
requests in this sequence: normal, exactly one dedicated `POST /responses/compact`
request, a rewritten normal request with a committed checkpoint and untouched
latest live user tail, then a normal request reusing that checkpoint without a
second compaction request. The dedicated request uses the unary JSON compact
contract (`model`, `input`, and `instructions`/supported controls) and does not
append a streamed `{"type":"compaction"}` trigger. It also
requires connector telemetry showing one compaction attempt and success plus a
checkpoint hit, a committed checkpoint scoped to the authoritative session,
account, and model with a non-empty replacement, no client-visible compaction
item, and no internal continuity-marker leakage. The recorder and same-package
test store inspection retain only bounded shape metadata; they do not report
checkpoint contents.

The compact response's `usage` field is optional compatibility evidence, not a
parse prerequisite. The recorder logs `provider_usage_present=true|false` for
the field, while the coordinator must still surface one charged scoped usage
event classified as `provider` or deterministic `estimated` fallback. A separate
unit contract covers estimated usage emission; omission of provider usage is
therefore not treated as a failed behavioral proof or a false provider-usage
claim.

## Validate and evaluate

Run deterministic configuration and connector checks before operating the default-on path or an explicit opt-out:

```powershell
go run ./cmd/aipstd check-config --config config/examples/opencode-codex.yaml
go test ./connectors/codex/...
go test ./internal/plugins/features/reasoningpreservation/...
```

Do not place tokens, opaque reasoning, or prompts in logs, issue reports, or
evaluation artifacts. Neither live test stores opaque payload evidence.

## State and rollback

Checkpoint and reasoning state is process-local, bounded, and not migrated.
Set `native_context.enabled: false` (and, when desired, add a matching disabled
reasoning-preservation feature row), then restart or reload the runtime. Set
`compaction.enabled: false` for a compaction-only opt-out. Existing exact
client-supplied reasoning replay remains available.

Quality, break-even, and failure evidence must be reported accurately and must
not be presented as proof of coding-quality improvement without measurement.
They do not change the approved default semantics; operators can opt out without
state migration.
