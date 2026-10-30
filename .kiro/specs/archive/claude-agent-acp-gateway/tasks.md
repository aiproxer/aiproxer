# Implementation Plan

> Status: completed on merged baseline `46b6be78ea70f38ef15fdc7757414518aa7bae88` (PRs #1244, #1290 and #1292; issue #1195 closed). Original approvals are retained. Certification boundaries and task evidence are recorded in `validation.md` and `research.md`.

- [x] 1. Source and configuration
- [x] 1.1 Add `credential_source: gateway` with validated configuration and secret refs
  - Config: `gateway.base_url` (https or loopback http, no userinfo) + `gateway.headers` name list; values only via named secret refs; reject literals, malformed lists, unknown fields; generalize the single-secret check to per-header refs without changing the three existing sources.
  - Done when red-first tables cover every accept/reject path, missing refs, and literal-value rejection.
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: backend plugin (Config, AuthEnv, secrets)_
- [x] 1.2 Compose gates and redaction
  - Gateway instances travel the same local-only/honor/identity gates; `PreparedSpawn`/logging/evidence redact header values; reject pre-exec on any unmet prerequisite.
  - Done when gate-composition and leakage-scan tests pass across all sources.
  - _Requirements: 1.3, 2.3, 4.1_
  - _Boundary: backend plugin (AuthEnv, diagnostics)_
  - _Depends: 1.1_
- [x] 2. Handshake and admission
- [x] 2.1 Advertise the gateway capability and perform `authenticate`
  - Only for the gateway source: capability advertised, `authenticate` called with `methodId: "gateway"` + `_meta.gateway {baseUrl, headers}`; never combined with `--hide-claude-auth` or Claude credential variables; client extensions cannot supply or override gateway keys.
  - Done when fake-adapter tests capture the exact payload per source, prove no-fallback on rejection, and prove other sources advertise nothing new.
  - _Requirements: 2.1, 2.2, 2.4_
  - _Boundary: backend plugin (AuthEnv, handshake)_
  - _Depends: 1.2_
- [x] 2.2 Recertified identity extension and gateway admission
  - Update the connector-owned ESM extension to observe gateway identity (kind/host match, no first-party signal); bind new bytes; `ValidatePromptAdmission` accepts only the matching tuple and refuses wrong-host/first-party/stale evidence without admitting input; uncertified bytes reject pre-exec.
  - Done when admission tables and extension-binding tests pass with synthetic responses and the new digest is recorded in the certification task.
  - _Requirements: 3.1, 3.2_
  - _Boundary: backend plugin (extension, EffectiveIdentity)_
  - _Depends: 2.1_
- [x] 3. Behavior and lifecycle
- [x] 3.1 Gateway turn integration with a stub gateway
  - Stub-gateway fixture proves a streaming turn routed through the gateway with canonical deltas; classification keeps pre-output eligibility and post-output terminality; gateway quota errors surface generically, never as Anthropic plan exhaustion; no gateway state outlives the process.
  - Done when integration and error-mapping tests pass and probes confirm the connector sent no first-party credentials.
  - _Requirements: 3.3, 4.2, 4.3_
  - _Boundary: backend plugin (service), test fixture_
  - _Depends: 2.2_
- [x] 4. Certification, disclosure and regression
- [x] 4.1 Certification run, disclosure and documentation
  - Record the stub-gateway certification rows (advertisement, authenticate, routed turn, admission pass/refuse, leakage scans) against exact bytes; update enabling digests only for the observed profile; disclose gateway posture + host (no header values); document config, provisioning, endpoint policy, no-fallback and billing honesty; disabled example.
  - Done when evidence is attached to the spec and docs/example checks pass.
  - _Requirements: 5.1, 5.2, 5.3_
  - _Boundary: certification evidence, docs, config example_
  - _Depends: 3.1_
- [x] 4.2 Regression proof for existing sources and connectors
  - Re-run suites proving api_key/subscription_token/host_login, deny/allow postures, and cursor/agy/gemini connectors are unchanged; archtests and delivery report clean.
  - _Requirements: 5.4_
  - _Boundary: regression suites_
  - _Depends: 4.1_

## Completion Status

- [x] All seven implementation subtasks are reconciled against merged code and the owning tests in `validation.md`.
- [x] Exact-profile stub-gateway certification and the existing-source/adjacent-connector regressions pass; the task-owned architecture failure was repaired in #1292 before completion.
- [x] The requirements' Deferred list is unchanged. Remote CI and remote race evidence are not claimed; the owner explicitly excluded defunct remote CI from acceptance.
