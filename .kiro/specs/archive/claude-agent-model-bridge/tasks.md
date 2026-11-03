# Implementation Plan

- [x] 1. Connector module, closed manifest and conformance scaffold
  - Create the independent `connectors/claudeagentmodel` module, plugin entrypoint, `Describe` posture and closed `release.yaml`/manifest export; S1 advertises streaming with tools still false.
  - Done when `GOWORK=off go test ./...` passes in the connector, descriptor/manifest posture tests match, and the root module/essential tables are unchanged.
  - _Requirements: 1.1, 2.1, 5.3_
  - _Boundary: backend plugin, packaging_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./...)`; `go test ./pkg/aipsdk/backendplugin/...`_

- [x] 2. Strict configuration, consent and credential isolation
  - Implement typed YAML parsing, defaults, unknown-field/range rejects, local-only/admin/credential gates, secret handling and the isolated environment allowlist.
  - Done when table tests cover every accepted default and reject path, a secret canary never reaches errors/argv/logs, and invalid config spawns no process.
  - _Requirements: 1.2, 1.3, 1.4, 1.5, 5.2_
  - _Boundary: backend plugin (config/auth)_
  - _Depends: 1_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./internal/service/... -run 'Config|Credential|Gate')`_

- [x] 3. Sidecar supervisor and bounded NDJSON transport
  - Implement the private child process, one ordered reader/writer, versioned bounded messages, stderr redaction, cancellation/drain, TTL/idle retirement and the owned process tree.
  - Done when a fake sidecar proves valid round trips plus malformed/oversized/out-of-order rejection, no orphan survives close or crash, and no TCP listener or client-controlled script/env is accepted.
  - _Requirements: 1.4, 4.5, 5.2_
  - _Boundary: backend plugin (bridge/process)_
  - _Depends: 2_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./internal/bridge/... -run 'Process|NDJSON|Lifecycle')`_

- [x] 4. Pinned SDK sidecar: discovery and text streaming
  - Add the locked official SDK sidecar, exact model/effort discovery, isolated SDK query settings, text deltas, usage presence and effective-model/fallback disclosure.
  - Done when the pinned dependency lock and node tests pass, discovery failure is explicit rather than a static list, and a request for an undiscovered model rejects before upstream I/O.
  - _Requirements: 2.1, 2.2, 2.3, 2.4_
  - _Boundary: backend plugin (sidecar, engine)_
  - _Depends: 3_
  - _Validation: `(cd connectors/claudeagentmodel/sidecar && npm ci && node --test *.test.mjs)`; `(cd connectors/claudeagentmodel && GOWORK=off go test ./... -run 'Discovery|Model')`_

- [x] 5. Canonical text events and Chat/Anthropic integration
  - Map sidecar observations to response/message/delta/usage/terminal events, preserve instruction roles and stable prefixes, and collect non-streaming through the same events.
  - Done when streaming and non-streaming Chat and Anthropic text requests pass full gRPC/HTTP integration, usage distinguishes unknown from zero, and instruction-role loss produces `instruction_role_degraded` or a strict reject.
  - _Requirements: 2.3, 5.1_
  - _Boundary: backend plugin (events/engine), integration tests_
  - _Depends: 4_
  - _Validation: focused `internal/service` and frontend integration tests_

- [x] 6. Dynamic tool validation and reversible native names
  - Validate per-request catalogues, preserve schemas/descriptions, implement the `mcp__aiproxer__<short>` mapping with hash suffix, collision rejection and ToolChoice:none/forced handling.
  - Done when table tests cover valid tools, duplicate/invalid/oversized/unicode/over-64-byte names, hash/native collisions and forced-choice degradation, with no upstream call on rejection.
  - _Requirements: 3.1, 3.2, 3.6_
  - _Boundary: backend plugin (bridge/tools)_
  - _Depends: 5_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./internal/bridge/... -run 'Tool')`_

- [x] 7. Transient MCP server and deferred PreToolUse correlation
  - Expose only the admitted catalogue through one in-process SDK MCP server, create one pending entry from PreToolUse before the handler waits, and prove native builtins are disabled with no filesystem/network effect.
  - Done when a fake SDK-shaped sidecar shows the handler only awaits, a second outstanding call fails explicitly, and a native `Read`/shell attempt is denied without side effects.
  - _Requirements: 3.3, 3.4, 3.5_
  - _Boundary: backend plugin (sidecar/tool-server, bridge/session)_
  - _Depends: 6_
  - _Validation: `(cd connectors/claudeagentmodel/sidecar && node --test *.test.mjs)`; focused `internal/bridge` tests_

- [x] 8. Canonical tool-call events and parked HTTP boundary
  - Emit complete validated tool-call start/args/finished events, end the HTTP response with a tool-use stop, and decouple HTTP reader close from native turn retirement.
  - Done when streaming and collected Chat/Anthropic responses carry correct IDs/names/arguments, `Close()` after the boundary leaves the turn parked, and cancel-before/after-call differs deterministically with no retry.
  - _Requirements: 3.5, 4.5, 5.1_
  - _Boundary: backend plugin (events/session)_
  - _Depends: 7_
  - _Validation: focused bridge/service integration tests_

- [x] 9. Exact-ID continuation and result release
  - Implement authoritative-session lookup, stateless exact-ID plus ordered-prefix proof, model/route/tool/generation binding, single-release semantics and typed mismatch/ambiguity/expiry errors.
  - Done when tests cover foreign/swapped/reordered/missing/duplicate/expired results, two concurrent sessions with the same tool name, a changed catalogue or route while parked, and exactly one MCP release with the caller's exact content (error status is not representable in this slice).
  - _Requirements: 3.7, 4.1, 4.2, 4.3, 4.4_
  - _Boundary: backend plugin (bridge/continuation)_
  - _Depends: 8_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./internal/bridge/... -run 'Continuation|Pending')`_

- [x] 10. End-to-end tool-loop acceptance and capability flip
  - Run two different harness catalogues through Chat and Anthropic, streaming and collected, including text/JSON results and error-looking content preserved as content; enable `Tools:true` only after this proof.
  - Done when the full gRPC/HTTP loop passes with no duplicate release, no connector-side execution, bounded processes/history, and the advertised `Tools` capability matches the proven envelope.
  - _Requirements: 3.5, 3.7, 4.5, 5.1, 5.2_
  - _Boundary: backend plugin and integration tests_
  - _Depends: 9_
  - _Validation: connector `GOWORK=off go test ./...` plus focused Chat/Anthropic integration suites_

- [x] 11. Operator documentation, disabled example and regression proof
  - S4 delivered the operator surface, enabled/disabled examples, certification harness, packaging proof and focused regressions; #1698 added the real two-harness acceptance, and #1715 recorded, exact-bundle verified and approved the fresh artifact oracle. The maintainer legal/policy sign-off is recorded in `docs/claude-agent-model.md` for the narrow experimental, opt-in, local-only, single-user envelope. The final-head live-acceptance gap is documented, not claimed away.
  - Write the operator/security/rollback documentation and disabled example; verify ACP/hybrid/root isolation and packaging remain unchanged.
  - Done when the example spawns nothing while disabled, docs state the experimental/legal envelope, and regression/conformance plus `make dev-test-changed` pass on the final head.
  - _Requirements: 1.1, 5.3, 5.4_
  - _Boundary: docs, config example, regression tests_
  - _Depends: 10_
  - _Validation: `make dev-test-changed`; connector conformance and packaging checks_

- [x] 12. Cross-client continuation: Responses ordered items and Gemini name-only
  - Add `FeatureOrderedItems` plus the static ordered-item capability, item-authority prefix/trailing-result handling, item-reference and previous-response `history_unavailable` refusal, name-only matching with `PrefixByName` and argument digest, identity-keyed name tombstones that do not shadow unrelated conversations, typed mismatch/ambiguity/expiry/consumption, omitted-tool snapshot retention and model/catalogue/generation binding.
  - Done when the connector tests cover item match/reorder/omit/duplicate/item-reference, name-only 0/1/2 candidates and identical prefixes across sessions, a projected Responses continuation, Gemini duplicate-name ambiguity, two serial same-name loops, omitted tools, ordered capability and changed-history refusal followed by a correct retry.
  - _Requirements: 3.7, 4.1, 4.2, 4.3, 4.4_
  - _Boundary: backend plugin (bridge continuation/manager/session, service capability)_
  - _Depends: 10_
  - _Validation: `(cd connectors/claudeagentmodel && GOWORK=off go test ./...)`; focused `internal/bridge` and `internal/service` S3 suites_

## Completion Status

- [x] S0 bounded spec and WIP slot merged via #1690.
- [x] S1 text streaming merged via #1691.
- [x] S2 deferred dynamic tool bridge merged via #1693.
- [x] S3 cross-client continuation merged via #1694.
- [x] S4 release acceptance and operator surface merged via #1695.
- [x] Real two-harness acceptance repair merged via #1698 (`83a1039ca38c4b4939f284a15c2aa6104ee5be37`).
- [x] Fresh artifact oracle recorded, exact-bundle verified and approved via #1715, merged on `main` as `f9a42bf9f638a937207a4a09752bcd8e137b1168`.
- [x] Maintainer legal/policy sign-off recorded in `docs/claude-agent-model.md` for the narrow experimental, opt-in, local-only, single-user envelope.
- [x] Documented limitation: the full 10-case live matrix and Codex profile were not rerun on the final head; the earlier matrix is historical evidence, not a Cartesian certification.

Verified on the merged baseline `f9a42bf9f638a937207a4a09752bcd8e137b1168`.
