# Reasoning Output Preservation

Standard-distribution default-on, catalog-gated multi-turn reasoning replay for AIProxer ([issue #157](https://github.com/aiproxer/aiproxer-enterprise/issues/157)).

When a client later submits an assistant turn without reasoning that the proxy previously observed on the same authoritative session, this feature can restore uniquely missing reasoning onto a fresh candidate clone before backend open. Spec: [`.kiro/specs/archive/reasoning-output-preservation/`](../.kiro/specs/archive/reasoning-output-preservation/).

## Goals

- Capture bounded reasoning artifacts from final canonical assistant output after response hooks and completion gates.
- Match later assistant history by exact non-reasoning content only.
- Restore only unique `missing` reasoning at recorded positions; never overwrite client-supplied reasoning.
- Keep artifacts session-scoped, TTL/turn/byte bounded, process-local, and absent from ordinary observability.
- Hard `reasoning_replay`: unsupported dialects never silently downgrade. Policy `on_unrepresentable: reject` excludes the candidate; `log_skip` continues without restore (configured skip, not silent success).

## Non-goals (v1)

- Generating, reconstructing, summarizing, or evaluating unobserved reasoning.
- Fuzzy, semantic, embedding, or LLM-assisted matching.
- Cross-session, cross-principal, cross-plugin, or cross-replica restoration.
- Durable/distributed artifact storage.
- Making reasoning visible when a frontend/backend has no legal wire form.
- Retry/failover after first downstream content (existing runtime invariant unchanged).

## Enablement

Feature plugin id: `reasoning-output-preservation` (registered in `internal/standardplugins`).

On the standard `aipstd` / `runtimebundle.BuildHost` path, one enabled default row is injected when no matching features row is present (same composition pattern as `tool-call-repair`). Explicit opt-out: add a matching row with `enabled: false`, or omit `enabled` (plain-bool decode is false). Any matching instance id or factory kind suppresses injection and preserves the operator row. Custom/nonstandard composition roots that do not call `EnsureReasoningOutputPreservationInConfig` receive no injection. The feature is **not** a mandatory `StandardDistributionRequirements` entry.

When the feature is not constructed (explicit disabled / non-injected custom root), there is no store, attempt transform, stream observer, hashing, or feature telemetry (D12).

When `enabled: true`, YAML `action` is required: `observe` or `restore`. Capture and restore both require ResolveMatch eligibility; unmatched candidates are fully inactive for that request (no session/store reads, no event parse/buffer, no restore/mutation, no feature outcome telemetry).

### Exact standard injected defaults

When injection runs (no matching features row), the constructed row is:

```yaml
- id: reasoning-output-preservation
  enabled: true
  config:
    action: restore
    use_builtin_catalog: true
    # no rules
    on_ambiguous: log_skip
    on_unrepresentable: reject
    on_state_error: reject
    state:
      ttl: 24h
      max_turns_per_session: 16
      max_reasoning_bytes_per_turn: 65536
      max_session_bytes: 262144
```

Shipped `config/config.yaml` and root `config.yaml` document this same row explicitly so operators can set `enabled: false` cleanly.

### Operator examples (config-validation dogfood)

These YAML files prove `check-config` / `routes` / `inventory` composition with a **local-stub** backend. They do **not** exercise live reasoning capture/restore end-to-end (stub text has no reasoning stream). Behavioral proofs: `TestPhase5_*` in `internal/core/runtime` and `internal/plugins/features/reasoningpreservation`.

| File | Action | Use |
|------|--------|-----|
| [`config/examples/reasoning-preservation-observe.yaml`](../config/examples/reasoning-preservation-observe.yaml) | `observe` | Config-validation: capture-capable wiring |
| [`config/examples/reasoning-preservation-restore.yaml`](../config/examples/reasoning-preservation-restore.yaml) | `restore` | Config-validation: restore-capable wiring |

Validate:

```bash
go run ./cmd/aipstd check-config --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/aipstd routes --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/aipstd inventory --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/aipstd check-config --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/aipstd routes --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/aipstd inventory --config config/examples/reasoning-preservation-restore.yaml
```

**`aipstd inventory`** (enabled): shows the feature instance and generic stage posture — `attempt_transform` / stream-observer participant IDs occupied. It does **not** print outcome aggregates or reasoning payloads.

**Feature `BuildSafeInventory` / telemetry API** (in-process): exposes fixed aggregate outcome counters (`observed`, `restored`, …) and config posture only — never payloads, anchors, signatures, opaque JSON, or session partition keys. When the feature is disabled/absent: no reasoning participants in CLI inventory and empty/disabled safe inventory.

## Configuration

| Field | Meaning |
|-------|---------|
| `action` | `observe` (capture only) or `restore` (capture + restore missing) |
| `use_builtin_catalog` | When true, built-in `compatible-auto.v2` OpenAI-compatible prefixes + automatic family matcher apply after explicit rules |
| `rules` | Explicit backend instance rules (`id`, `backend`, optional `model_keywords`, required `enabled`) |
| `on_ambiguous` | Must be `log_skip` (non-mutating) |
| `on_unrepresentable` | `reject` (exclude candidate) or `log_skip` |
| `on_state_error` | `reject` or `log_skip` |
| `state.ttl` | Artifact TTL |
| `state.max_turns_per_session` | Max artifacts per authoritative session |
| `state.max_reasoning_bytes_per_turn` | Per-turn reasoning payload bound |
| `state.max_session_bytes` | Per-session total reasoning byte bound |

### Rule / catalog precedence

1. Explicit rule with `model_keywords` matching the candidate model (`enabled: true` / `false`). Explicit keywords keep contains-semantics and may opt into models outside the automatic set (including GPT 5.6+).
2. Explicit backend-only rule (no keywords) for that backend instance id.
3. When `use_builtin_catalog: true`, shared automatic matcher (`internal/plugins/features/reasoningpreservation/reasoningreplay`, catalog id `compatible-auto.v2`): OpenAI-compatible backend prefixes plus boundary-aware families DeepSeek, Kimi, Moonshot (`moonshot` / `moonshotai`), GLM, MiMo, Qwen, HY3, MiniMax, and GPT 5.x through GPT 5.5 (numeric version parse; GPT 5.6+ / GPT 6+ / GPT 4.x excluded). Dot, dash, and underscore numeric minors share the ceiling (`gpt-5.6`, `gpt-5-6`, `gpt-5_6` excluded); named suffixes after major remain eligible (`gpt-5-mini`). Family tokens require a left boundary (start or non-alnum) and a right boundary (end or non-letter so digit/separator glue is allowed); letter continuation is rejected (false negatives preferred over incidental hits such as `deepseeker`, `qwentest`, `minimaximum`, `kimiko`).
4. Otherwise no match → no capture/restore and no feature outcome telemetry for that candidate.

Operator rules match exact backend instance ids. Built-in automatic eligibility is shared with OpenAI-compatible backend `CapabilityReasoningReplay` / replay-support resolution so catalog and caps cannot drift.

## Dialect matrix (v1)

| Dialect ID | Typical family | Notes |
|------------|----------------|-------|
| `openai.chat.reasoning_text.v1` | OpenAI-compatible chat / OpenRouter / local OpenAI-chat | Text-style historical reasoning |
| `openai.responses.reasoning_item.v1` | OpenAI Responses | Exact reasoning-item Opaque (`id`, `type`, `summary`, optional `content`, `encrypted_content` absent/null/value, optional `status`). No summary/text fallback. Neutral schema: `internal/plugins/protocols/openairesponsesitem`. Terminal carrier: `EventReasoningPart` / `Collected.ReasoningParts`. |
| `anthropic.thinking.v1` | Anthropic Messages | Thinking + optional signature |
| `anthropic.redacted_thinking.v1` | Anthropic Messages | Redacted thinking blocks |

Hard capability: `reasoning_replay`. Every restored dialect must be explicitly supported by the candidate; unsupported dialects follow `on_unrepresentable` (`reject` / `log_skip`) with **no conversion** between Chat-shaped and Responses-shaped artifacts. Gemini generateContent is unsupported for positive replay in v1 (explicit unsupported classification).

### OpenAI Responses exact posture

- **Allowlist only:** unknown fields / non-array summary|content fail closed; incomplete drafts never commit to TurnStore.
- **Presence:** `encrypted_content` and `content` distinguish absent vs null vs value; empty summary array is legal.
- **Streaming primary:** backends always stream; frontend nonstream collects the same canonical stream (no backend nonstream path).
- **Replay:** Responses backends restore exact Opaque only — no `ToParam()` / summary synthesis fallback.
- **Combinations:** Chat↔Chat, Responses↔Responses, Chat FE/Responses BE, Responses FE/Chat BE (asymmetric cells keep dialect identity; mismatch uses reject/`log_skip` only).
- **State:** process-local TurnStore; restart/rebalance loses artifacts. Unmatched catalog/rule candidates remain fully inert.

## Failure policies

| Classification / fault | Behavior |
|------------------------|----------|
| `preserved` | Leave client reasoning unchanged |
| `missing` + unique match | Restore at recorded positions (`restore` action) |
| `conflicting` / `ambiguous` / `unmatched` | Non-mutating (`on_ambiguous` is `log_skip`) |
| Unrepresentable dialect | `reject` → `exclude_candidate` / `log_skip` → continue without restore |
| Store/state errors | `reject` → exclude candidate; `log_skip` → continue without restore |
| Observer/store append failure | Fail-open on committed output; never triggers retry after first content |

## Client-sent reasoning policy (`routing.attempt_policy`)

Agent clients (Codex-style) replay the model's earlier `reasoning` items in
Responses `input`. The frontend decodes each one into a canonical
`PartReasoning`, which requires the hard `reasoning_replay` capability — and
no catalog provider profile satisfies it, so any session whose history
contains a reasoning item is rejected on every profile backend.

One route/executor-level key decides what happens when an attempt candidate
cannot replay the dialect of client-sent reasoning items:

| Value | Behavior |
|-------|----------|
| `log_skip` (default) | Strip those reasoning parts for that attempt only, leaving every other item byte-identical and order-preserved, and record one `reasoning_replay_skipped{backend}` observation plus one content-safe `reasoning_replay_skipped` decision entry per stripped attempt. Lossless candidates still receive the exact items. |
| `reject` | Refuse that candidate exactly as before (existing failover to the next candidate). Strict operators who need full fidelity set this. |

```yaml
routing:
  # Default: strip unrepresentable client-sent reasoning per attempt.
  # Set to "reject" for strict fidelity (today's behaviour).
  attempt_policy: log_skip
```

**Explicit reversal notice.** The archived
`openai-responses-reasoning-preservation` requirement line 34 states that
hard `reasoning_replay` capability semantics "shall remain non-downgradeable".
That still holds for the legacy negotiation seam — and for proxy-restored
reasoning under `on_unrepresentable: reject` — but it is **reversed for
client-sent reasoning**: under the default `log_skip`, `reasoning_replay`
downgrades per attempt through the existing `aipapi` Negotiate seam, gated on
the resolved policy and never globally. Reasoning items are hidden model
state, not transcript, so omitting one asserts nothing false; reordering
model-authored items stays forbidden and refuses instead.

**Unified rule.** Proxy-restored and client-sent reasoning follow the same
per-candidate outcome: under `log_skip` the proxy never restores into a
candidate that cannot replay the dialect, and both provenances share a single
downgrade record per stripped attempt (no double strip, never a mutated shared
call — later lossless candidates still negotiate against the exact items).
Under `reject`, the restore path keeps its existing `on_unrepresentable`
behaviour.

## Bounds and privacy

- Artifacts are capped by TTL, turns/session, bytes/turn, and bytes/session.
- Oversize reasoning is discarded (safe `oversize` outcome).
- Feature telemetry / `BuildSafeInventory` expose only fixed outcomes and aggregate counts/bytes: `observed`, `preserved`, `missing`, `restored`, `ambiguous`, `conflicting`, `unmatched`, `unrepresentable`, `state_error`, `evicted`, `oversize`. CLI `aipstd inventory` shows participant posture only (not these aggregates).
- Never log reasoning text, signatures, opaque JSON, anchors, excerpts, or session partition keys.

## Optional semantic compression (follow-up #369, disabled by default)

Additive nested `compression` under this feature; standard injected `reasoning-output-preservation: restore` ships with `compression.enabled: false` and no compressor route. Enabling requires explicit `route` and `egress_policy_ref`; config validation rejects missing route/policy, unknown keys, non-positive bounds, unsafe maxima, and aggregate limits smaller than per-session limits.

- **Lossy and bounded, never exact.** Only canonically `openai.chat.reasoning_text.v1` plain text without signature/opaque/exact authority is eligible (`ReplaySemanticText`); OpenAI Responses exact items (`openai.responses.reasoning_item.v1`), Anthropic signed `thinking` / `redacted_thinking`, encrypted/opaque, unknown dialects, and mixed exact-bearing placements fail closed and never enter compressor input. Truncation/token dropping is not claimed as semantic equivalence; raw `max_output_bytes` is enforced before JSON decode and decoded `max_surrogate_bytes` + savings (`min_saved_bytes`, `min_savings_ratio`) are validated after decode.
- **Disabled default, no exact-compression fallback.** `compression.enabled` omitted/false produces zero compressor requests, pending state, surrogate bytes, compression telemetry, or additional billable inference — byte-equivalent to pre-369 preservation. `enabled: true` defaults to `mode: shadow`; no `max_surrogate_bytes` compression without explicit `mode: active`.
- **Shadow vs active.** `shadow` executes the full lane (classification → reservation → egress decision → detached no-tools auxiliary submit → non-blocking Poll adoption → raw-bound extraction → strict decode → CAS attach) but every `AttemptTransform` still replays the original reasoning; evidence is `shadow_ready` hypothetical only. `active` replays a surrogate only when the same canonical classifier still returns `ReplaySemanticText`, the stored surrogate correlates (artifact ID, original digest, `SemanticDigest`, `EgressPolicyHash`, policy revision), existing destination `ReasoningReplaySupport` can represent the original dialect, and all byte/savings bounds pass — every uncertainty falls back to original/`on_unrepresentable`.
- **Privacy / egress gate.** `SemanticText` does not imply sensitivity clearance. Before any remote compressor submission the selected `route`, purpose `reasoning_semantic_compression`, and originating trusted scope pass an allow / `redact_then_allow` / `deny` decision. `deny` or missing required policy skips compression; `redact_then_allow` sanitizes via the existing trusted `secretguard` authority locally before input-byte accounting and prompt construction. Control-plane role/visibility/lineage/principal scope stay in the auxiliary envelope/execution context for authorization/billing and never enter `Call.Messages`, compressor JSON payload, or content-bearing telemetry. Explicit `route` alone is never consent.
- **Budgeted optional state never evicts originals.** Pending/surrogate bytes are accounted separately from authoritative `ReasoningBytes` FIFO and bounded both per-session (`max_pending_per_session`, `max_surrogate_bytes_per_session`/`max_surrogate_bytes`) and feature-instance aggregate (`max_pending_total`, `max_surrogate_bytes_total`). Exhaustion rejects optional state and `Forget`s submitted jobs when safe; incurred provider work remains billable. Reservation precedes provider submission; late/stale/expiry/eviction results decrement counters exactly once via CAS.
- **Config example (shadow).** See `config/examples/reasoning-preservation-observe.yaml` (disabled block plus commented `compression: enabled: true, mode: shadow, route, timeout, max_*_bytes/tokens, min_*_bytes/ratio, max_pending_* , max_surrogate_* , egress_policy_ref`). Dogfood files remain config-validation only (local-stub backend has no reasoning stream); behavioral proofs: `reasoningpreservation.TestShadowFixture_DeterministicEvidence`, `TestCompression_*`, `internal/core/auxreq` Poll tests.

## Process-local / sticky / restart

- V1 state is process-local memory owned by one feature instance.
- Restoration requires the same process and authoritative secure-session resume.
- Sticky routing helps keep eligible backends, but does not replicate artifacts across replicas.
- Restart, rebalance, or another instance → state miss (no restore) until new observations accumulate.
- Client-supplied session hints cannot access another partition.

## Runtime invariants (operators)

- Restore runs on `CloneCall(baseline)` per candidate before final eligibility/Open.
- Capture commits only on `success_released` for the winning surfaced B-leg.
- Parallel losers, cancellation, EOF/close, and gate-replaced completions do not persist pending artifacts.
- No transparent retry/failover after first downstream content event.

## Migration / compatibility

- On the **standard** `aipstd` / `runtimebundle.BuildHost` path, omitting the feature row now receives the injected enabled `restore` defaults above (same injection pattern as `tool-call-repair`). Shipped `config/config.yaml` and root `config.yaml` document that row explicitly.
- Explicit opt-out: add a matching features row with `enabled: false` (or omit `enabled`; plain-bool decode is false). Any matching instance id or factory kind suppresses injection.
- Custom/nonstandard composition roots that do not call `EnsureReasoningOutputPreservationInConfig` are unchanged (no injection).
- Installed/enabled still leaves unmatched models inactive for that request — no store I/O, event buffering, restore, or feature telemetry; eligibility remains catalog/rule gated.
- Go v1 state is process-local only; do not expect cross-process or cross-replica continuity.

## Full HTTP E2E validation

Follow-up spec: [`.kiro/specs/archive/reasoning-preservation-e2e-validation/`](../.kiro/specs/archive/reasoning-preservation-e2e-validation/) (parent issue [#157](https://github.com/aiproxer/aiproxer-enterprise/issues/157); parent feature spec [`.kiro/specs/archive/reasoning-output-preservation/`](../.kiro/specs/archive/reasoning-output-preservation/)).

Proofs drive a stateful client transcript and independent backend-request oracle through `runtimebundle.BuildHost` + `stdhttp` + real Chat/Anthropic adapters + protocol emulators (`internal/refbackend`). Failures report seed/mode/turn/structural reason codes only — never reasoning text, signatures, opaque payloads, anchors, or raw bodies.

| Suite | Gate | What it covers |
|-------|------|----------------|
| Deterministic controls | Default `go test` / `make test-unit` | OpenAI Chat + OpenAI Responses FE×BE topology (stream/nonstream, presence, tools, session/anchor, cross-dialect reject/`log_skip`, gating/opt-out) |
| Default-on injection / catalog boundary | Default suite | Absent-row standard injection: Moonshot restore, GPT 5.5 restore, GPT 5.6 inert, unmatched family inert, explicit `enabled: false` opt-out (`TestReasoningPreservationHTTP_DefaultOnInjection`) |
| Anthropic cross-check | Default suite | Signed `thinking` / `redacted_thinking` observe/restore (or observe-only) through real Anthropic adapters |
| Seeded Responses presence smoke | Default suite | Deterministic `reasoninge2e.ResponsesSmokeCases` + topology subtest `responses_seeded_presence_smoke` (fixed seed, content-safe traces) |
| Seeded Chat matrix 64×20 | `//go:build precommit` | 16 `random_backend_drop_all` + 16 `always_reason_random_client` + 32 `combined`; 20 HTTP turns/seed; run via `make qa` tagged suite or targeted `go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/` (not lightweight `make test-precommit-extra`) |
| Soak 1000×100 | Env `AIP_REASONING_E2E_SOAK=1` + `make test-reasoning-e2e-soak` | Same full HTTP stack; 250/250/500 mode split; bounded workers; **not** a PR/default gate. Local smoke may override seeds/turns/workers |

```bash
# Deterministic Chat + Responses topology + Anthropic + default-on HTTP E2E (default tags)
go test -count=1 -run 'TestReasoningPreservationHTTP' ./internal/stdhttp/
# Focused default-on / catalog-boundary controls
go test -count=1 -run TestReasoningPreservationHTTP_DefaultOnInjection ./internal/stdhttp/

# Precommit 64×20 Chat matrix
go test -tags=precommit -count=1 -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/

# Opt-in soak (defaults 1000 seeds × 100 turns; override for smoke)
AIP_REASONING_E2E_SEEDS=3 AIP_REASONING_E2E_TURNS=4 AIP_REASONING_E2E_WORKERS=2 make test-reasoning-e2e-soak

# Single-seed soak replay (one command)
AIP_REASONING_E2E_SOAK=1 AIP_REASONING_E2E_MODE=combined AIP_REASONING_E2E_SEED=42 \
  go test -tags=precommit -run TestReasoningPreservationHTTP_Soak -count=1 ./internal/stdhttp/
```

Soak env: `AIP_REASONING_E2E_SOAK=1` (required), `AIP_REASONING_E2E_SEEDS` (default 1000), `AIP_REASONING_E2E_TURNS` (default 100), `AIP_REASONING_E2E_WORKERS` (default 4, max 32), replay pair `AIP_REASONING_E2E_MODE` + `AIP_REASONING_E2E_SEED`. Nightly/manual workflow: `.github/workflows/reasoning-e2e-soak-nightly.yml` (not attached to PR `qa.yml` or `race-fuzz-nightly.yml`).

**OpenAI Chat** remains the deep randomized matrix surface. **OpenAI Responses** exact preservation is covered by the default-tag FE×BE topology matrix, seeded presence smoke, refbackend/refclient harness, and package-level exact ingest/encode/restore tests. Spec: [`.kiro/specs/archive/openai-responses-reasoning-preservation/`](../.kiro/specs/archive/openai-responses-reasoning-preservation/). Release-grade claim still requires external gates listed in [release-gates.md](release-gates.md) (Linux race, fuzz, soak).

## Release evidence pointers

- Phase 5 runtime/feature proofs: `TestPhase5_*` under `internal/core/runtime` and `internal/plugins/features/reasoningpreservation`.
- Full HTTP E2E: `TestReasoningPreservationHTTP*` under `internal/stdhttp` (Chat matrix/soak require `-tags=precommit`; soak also requires `AIP_REASONING_E2E_SOAK=1`).
- Responses exact path: `internal/plugins/protocols/openairesponsesitem`, `openairesponsestream`, FE/BE openairesponses, feature exact observer/restore, topology matrix.
- Adapter/parity: dialect encode + `internal/plugins/frontends/parity` VisibleThinker reasoning tests.
- Fuzz: `FuzzComputeAnchor`, `FuzzDecodeConfig`, `FuzzCanonizeReasoningItemOpaque`, FE decode / BE stream / exact observer targets (short local smoke; 30s release run separately).
- Architecture: official features must not import `internal/core`; FE must not import BE (`internal/archtest`).
- Follow-up exact Responses productionization: [`.kiro/specs/archive/openai-responses-reasoning-preservation/`](../.kiro/specs/archive/openai-responses-reasoning-preservation/).
