# Spec-to-code validation review: go-core-reimplementation-v1

**Date:** 2026-04-21  
**Scope:** Cross-check of approved spec corpus (`requirements.md`, `design.md`, `tasks.md`, `research.md`, ref matrices, steering) against implementation under `pkg/lipapi`, `pkg/lipsdk`, `internal/core`, `cmd/lipstd`, `internal/plugins`, `internal/testkit`, `internal/refclient`, `internal/refbackend`, `testdata/migration`.  
**Assumption:** Task checkboxes and automated tests are green per operator (this review does not re-run CI).

---

## Executive summary

The Go codebase delivers a **strong canonical core**, **explicit plugin boundaries** (verified by `go list` dependency tests), **routing / first-request / B2BUA / executor** behavior aligned with Requirements 5–8 and steering, and a **conformance harness** that satisfies most of Requirement 15 for matrix structure, documented subset limits (notably ACP), and migration fixtures.

The following **previously identified gaps were remediated** (2026-04-21 follow-up):

1. **Standard distribution HTTP (Req 3.x, 13.4):** `cmd/lipstd` now calls [`internal/stdhttp.Run`](../../../internal/stdhttp/server.go), which builds the executor from config, mounts all bundled frontends via [`MountBundledFrontends`](../../../internal/stdhttp/mount.go), and serves diagnostics paths from `config.yaml`. `runtime.App.Start` remains a lightweight bootstrap log; the blocking HTTP lifecycle lives in `stdhttp` to avoid `internal/core/runtime` importing bundled plugins.

2. **Tool reactor on wire (Req 11.2):** [`lipapi.MergeToolEventInto`](../../../pkg/lipapi/tool_event_merge.go) maps reactor output onto stream events; [`retryRecvStream.Recv`](../../../internal/core/runtime/executor.go) applies it when `res.Event.Kind != ""`.

3. **Keepalive (Req 5.5):** Streaming encoders wrap the canonical stream with [`stream.WrapRecoveryKeepalive`](../../../internal/core/stream/recovery_keepalive.go) and emit SSE comment lines (`: keepalive`) for `stream.KeepaliveEventCode`. Recv-phase failover uses incremental `tryReplacementIteration` with `IsRetryPath` and yields canonical keepalive events between attempts.

Additional items remain **documented v1 subsets** (aligned with `requirements.md` implementation notes and `research.md`) or **doc/layout drift** (design.md path names vs `internal/` + `pkg/`).

---

## Layout and documentation drift

| Item | Spec / design | Implementation | Severity |
|------|----------------|------------------|----------|
| Package paths | `lipcore/`, top-level `lipapi/` in design tree | `internal/core/`, `pkg/lipapi/`, `pkg/lipsdk/` | Low — intentional Go idiom; ownership matches design intent |
| `research.md` vs `requirements.md` | Anthropic `anthropic-version` “not validated or stored” | Research says “read but not modeled” | Low — wording only |
| `cmd/lipstd` log line | — | (Resolved) main no longer logs “runtime behavior is not implemented yet” | — |

---

## Findings table

| ID | Severity | Evidence | Notes |
|----|----------|----------|-------|
| **3.1–3.4**, **13.4** | **Resolved** | [`cmd/lipstd/main.go`](../../../cmd/lipstd/main.go), [`internal/stdhttp/server.go`](../../../internal/stdhttp/server.go), [`internal/stdhttp/mount.go`](../../../internal/stdhttp/mount.go) | `stdhttp.Run` serves bundled frontends + `diag` health/attempts when enabled in config. |
| **11.2** | **Resolved** | [`pkg/lipapi/tool_event_merge.go`](../../../pkg/lipapi/tool_event_merge.go), [`executor.go`](../../../internal/core/runtime/executor.go) | Reactor `ToolEvent` merged back onto stream `Event` before response-part hooks. |
| **5.5** | **Resolved** | [`stream/recovery_keepalive.go`](../../../internal/core/stream/recovery_keepalive.go), frontend `WriteStreamSSE`, `tryReplacementIteration` | Idle upstream wrapped with keepalive; recv-phase failover yields keepalives between attempts. |
| **1.1–1.2**, **1.5** | OK | [`internal/core/runtime/boundaries_test.go`](../../../internal/core/runtime/boundaries_test.go); no vendor imports under `internal/core` | Matches small-core boundary. |
| **2.1–2.2**, **2.5–2.6** | OK / bounded | [`pkg/lipapi/call.go`](../../../pkg/lipapi/call.go), [`events.go`](../../../pkg/lipapi/events.go), [`parts.go`](../../../pkg/lipapi/parts.go); executor decode path per frontend packages | Canonical-only core; translation only via plugins. Multimodal request parts are explicit in `Call`, but `Event` has no first-class assistant image/file delta family in v1, so response-side multimodal parity is bounded to direct protocol evidence where documented. |
| **2.4–2.7** | OK | [`pkg/lipapi/capabilities.go`](../../../pkg/lipapi/capabilities.go), `Negotiate` in executor loop | Reject/downgrade before `Open`. |
| **4.1–4.8** (subset) | OK / documented | Backend plugins + `research.md` / `requirements.md` implementation notes | Official SDKs behind plugin boundary per **4.7**. Request-side multimodal mapping is covered across bundled backends; response-side multimodal output is only claimed where the current v1 stream/canonical boundary can represent or directly prove it (Gemini ref emulator/client). ACP tools rejected: [`acp/invoke.go`](../../../internal/plugins/backends/acp/invoke.go) `validateACPCall`. |
| **5.1–5.4** | OK | [`executor.go`](../../../internal/core/runtime/executor.go) `OutputCommitted`, `committed` gate, `UpstreamFailure` post-output | Post-output recoverable errors forced non-recoverable surface path ~329–337. |
| **5.2** | OK | Frontends use collector over `EventStream` (e.g. [`openairesponses/handler.go`](../../../internal/plugins/frontends/openairesponses/handler.go) non-stream path) | Non-stream is collection over same stream type. |
| **5.3** | OK | `retryRecvStream` propagates cancel; `RecordAttempt` uses `context.WithoutCancel` for store | Matches cancellation + lineage durability. |
| **6.x**, **7.x** | OK | [`internal/core/routing/parser.go`](../../../internal/core/routing/parser.go), [`weighted.go`](../../../internal/core/routing/weighted.go), [`planner.go`](../../../internal/core/routing/planner.go) | `[first]` parse error for two first branches; weighted deterministic with `Rand`; retry path `IsRetryPath` per planner. |
| **8.x** | OK | [`internal/core/b2bua/store.go`](../../../internal/core/b2bua/store.go), executor `resolveALeg`, `NextBLeg`, `recordAttempt` | A-leg/B-leg and swallow/surface recording. |
| **9.x**, **10.x** (bus) | OK | [`internal/core/hooks/bus.go`](../../../internal/core/hooks/bus.go), executor `RunSubmit` / `RunRequestPartHooks` / `RunResponsePartHooks` order | Submit before plan; part hooks at correct phases. |
| **10.5**, **11.5** | OK | No-op feature plugins + tests under `internal/plugins/features/*` | Reference plugins exist. |
| **12.x** | OK | [`pkg/lipsdk/registration.go`](../../../pkg/lipsdk/registration.go), [`cmd/lipstd/hooks_compose.go`](../../../cmd/lipstd/hooks_compose.go), `runtime.New` validation | Constructor wiring; `ValidateRegistrations`. |
| **13.1–13.3** | OK (in core path) | `diag.WithTraceID`, `LogDecision`, attempt records | Trace + structured decisions on negotiate / swallow / open. |
| **14.1**, **14.3** | OK | `context` threading; executor `rng` mutex wrapper | No obvious package-level request globals in reviewed paths. |
| **15.3**, **15.10**, **15.12** | OK | [`internal/testkit/conformance/matrix.go`](../../../internal/testkit/conformance/matrix.go), [`matrix_test.go`](../../../internal/testkit/conformance/matrix_test.go), [`conformance_text_test.go`](../../../internal/testkit/conformance/conformance_text_test.go) | Full FE×BE Cartesian product; cells run text, stream+non-stream parity, upstream error shape. |
| **15.9** | OK (justified skips) | `matrix.go` `SubsetJustification` for `acp` column (tools + multimodal deferred) | Explicit justification string; satisfies “listed and justified” for empty multimodal overlap. |
| **15.11** | OK / documented | [`conformance_tools_test.go`](../../../internal/testkit/conformance/conformance_tools_test.go) (reviewed via matrix meta + package layout) | Tool rows gated by `ToolsViable`; ACP documents tool exclusion. |
| **15.13** | Partial / OK | [`testdata/migration/README.md`](../../../testdata/migration/README.md) | OpenAI stream + non-stream + Anthropic JSON; provenance documented. “Additional protocol pair” satisfied by Anthropic fixture. |
| **15.7–15.8** | OK / bounded for v1 | `refclient-spec-matrix.md`, `refbackend-spec-matrix.md` + `internal/refclient/*`, `internal/refbackend/*` | Multimodal request paths are covered across reference clients/backends. Response-side image/document evidence now exists directly for Gemini refclient/refbackend; other protocols remain text-output-only in claimed v1 evidence and must not be described as full assistant-output parity. |

### Implementation notes (Req 3 / 4) vs code — spot-check

| Note | Verified |
|------|----------|
| OpenAI Responses: `message`-only input; `function_call_output` rejected | `decode` paths + tests referenced in `research.md` |
| `tool_choice` `required` → canonical `any` | Documented in requirements + `lipapi` validation alignment |
| Reasoning deltas not on Responses / Anthropic SSE wire | [`openairesponses/encode.go`](../../../internal/plugins/frontends/openairesponses/encode.go) skips `EventReasoningDelta`; [`encode_test.go`](../../../internal/plugins/frontends/openairesponses/encode_test.go) `TestWriteStreamSSE_reasoningDeltaDoesNotBreakCompletion` |
| Legacy chat: assistant `tool_calls` rejected | [`openailegacy/decode.go`](../../../internal/plugins/frontends/openailegacy/decode.go) ~150–151 |
| Gemini: non-stream omits `usageMetadata` despite usage on canonical stream | Subset contract per requirements note — encode path must match tests in `frontends/gemini` |
| Multimodal assistant outputs on proxy wire | Bounded by v1 canonical stream: `lipapi.Event` has text/reasoning/tool/usage families only, so proxy conformance currently proves request-side multimodal preservation, while direct Gemini emulator/client tests cover wire-level inline image/PDF outputs |

---

## Residual risks (not automatic spec violations)

- **ACP / emulator “TBD”**: `refbackend-spec-matrix.md` notes full stdio interleave TBD; acceptable if treated as roadmap, not v1 contract.
- **Executor concurrency**: `lockedRng` serializes `Rand`; store mutations use `WithoutCancel` — race invariants rely on tests (**14.6**): assumed green per operator.
- **OAuth / sandboxing / DI**: No evidence in reviewed trees of out-of-scope features in core.

---

## Conclusion

| Area | Assessment |
|------|------------|
| Canonical model, routing, B2BUA, capability negotiation, streaming-first semantics (including keepalive wiring), post-output commit | **Aligned** with Requirements 1–2, 4–8, 12–14 and design core rules |
| Conformance matrix + migration goldens | **Largely aligned** with Requirement 15 |
| Standard distribution HTTP, recovery keepalive, tool-reactor merge | **Addressed** in code (see Resolved rows). |

**Follow-ups (optional):**

- Tune `DefaultRecoveryKeepaliveInterval` or make it configurable via `config.yaml` if operators need different idle thresholds.
- Consider merging `MountFrontend` (conformance) with `stdhttp.MountBundledFrontends` to a single helper if drift appears.

---

## Verification commands (post-review)

- `go test -timeout=10m ./...` — **pass** (2026-04-21, post-fix).
- `go run ./cmd/lipstd -config ./config/config.yaml` — blocks serving HTTP until SIGINT/SIGTERM; use Ctrl+C locally. At least one **enabled** backend in `config.yaml` is required for successful upstream calls (API keys via env or YAML as documented in [`internal/stdhttp/wire.go`](../../../internal/stdhttp/wire.go)).

---

_End of review._
