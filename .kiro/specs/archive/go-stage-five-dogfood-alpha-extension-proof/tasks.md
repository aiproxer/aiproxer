# Implementation Plan

- [x] 1. Establish safe standard-server command foundation
- [x] 1.1 Add protected diagnostics posture validation
  - Treat attempts, inventory, route trace, metrics, profiling, model diagnostics, and secure-session summaries as protected surfaces distinct from health.
  - Reject non-local protected diagnostics exposure without `diagnostics.shared_secret`, while preserving local-only empty-secret operation.
  - Done when validation tests cover local-only allowed, non-local rejected, and secret-protected allowed cases for each protected surface group.
  - _Requirements: 2.4, 6.1, 6.2, 6.4, 6.6, 6.7_
  - _Boundary: DiagnosticsSecurityValidator_
  - _Validation: go test ./internal/core/config/... ./internal/stdhttp/...

- [x] 1.2 Add shared bootstrap modes for inspection and serving
  - Centralize config load, diagnostics-safe validation, standard registry installation, mandatory factory validation, model alias validation, feature-surface merge, and optional runtime build behind inspection and serve modes.
  - Ensure inspection mode avoids backend request execution and does not start request serving.
  - Done when `check-config` succeeds/fails using the shared bootstrap path and serve still builds the runtime used by `stdhttp`.
  - _Requirements: 1.1, 1.2, 1.5, 6.4_
  - _Boundary: BootstrapPlan_
  - _Depends: 1.1_
  - _Validation: go test ./cmd/lipstd ./internal/infra/runtimebundle/...

- [x] 1.3 Add route inspection and live-mode visibility
  - Build a route read model showing effective default route, enabled backend IDs/kinds, configured model aliases, and whether the config is using local-stub or live-provider backend rows without contacting providers.
  - Produce deterministic command output suitable for operator inspection and tests without exposing credential values.
  - Done when `lipstd routes --config <path>` prints expected route targets and tests distinguish local-stub configs from live-provider configs without secrets.
  - _Requirements: 1.3, 6.3, 8.2_
  - _Boundary: RoutesReadModel_
  - _Validation: go test ./cmd/lipstd ./internal/core/diag/...

- [x] 1.4 Add shared inventory snapshot output for CLI and HTTP
  - Expose one inventory snapshot builder used by both HTTP diagnostics and CLI inventory.
  - Keep plugin config payloads, API keys, local keys, diagnostic secrets, resume tokens, and raw captures out of serialized output.
  - Done when CLI inventory and HTTP inventory produce matching active plugin and extension information for the same config.
  - _Requirements: 1.4, 4.5, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6_
  - _Boundary: InventoryReadModel_
  - _Validation: go test ./cmd/lipstd ./internal/core/diag/...

- [x] 1.5 Add command dispatch while preserving legacy serve invocation
  - Add explicit command parsing for `serve`, `check-config`, `routes`, and `inventory` using the existing standard binary entrypoint.
  - Keep `lipstd --config <path>` equivalent to the current serve behavior, and return non-zero exits for invalid commands or validation failures.
  - Done when command tests show legacy invocation and explicit `serve` both reach the serving path, while read-only commands use real route and inventory read models without starting a listener.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_
  - _Boundary: LipstdCommand_
  - _Depends: 1.2, 1.3, 1.4_
  - _Validation: go test ./cmd/lipstd_

- [x] 1.6 Add secure-session authority regression coverage
  - Verify standard startup and request handling preserve proxy-owned secure-session authority.
  - Cover client-provided session hints and ensure they are not treated as authoritative session identity.
  - Done when regression tests prove unsafe diagnostics fail before serving and proxy-owned session IDs remain authoritative.
  - _Requirements: 6.4, 6.5_
  - _Boundary: DiagnosticsSecurityValidator, SecureSession_
  - _Validation: go test ./internal/core/runtime/... ./internal/stdhttp/...

- [x] 2. Add no-key local dogfood backend and examples
- [x] 2.1 Implement optional deterministic local stub backend
  - Add an optional `local-stub` backend factory that emits canonical streaming text, usage, and optional configured tool-call events.
  - Keep it disabled unless configured and outside mandatory standard-distribution requirements.
  - Done when the backend can satisfy a canonical text call through the standard executor without provider credentials.
  - _Requirements: 2.1, 2.2, 2.5, 3.6_
  - _Boundary: LocalStubBackend_
  - _Validation: go test ./internal/plugins/backends/localstub/... ./internal/pluginreg/...

- [x] 2.2 Add local stub backend config validation and capability contract
  - Validate local stub config defaults, non-negative token counts, model/default text behavior, and optional tool event configuration.
  - Advertise streaming, non-streaming text, usage, and configured tool-call capability behavior consistently with examples.
  - Done when invalid stub config fails clearly and valid config exposes the expected route/model/capability behavior.
  - _Requirements: 2.1, 2.2, 2.3, 3.5_
  - _Boundary: LocalStubBackend_
  - _Validation: go test ./internal/plugins/backends/localstub/... ./internal/pluginreg/...

- [x] 2.3 Add no-key example configurations
  - Add local stub examples for the general dogfood path and each frontend-specific smoke surface.
  - Keep examples loopback-safe and clearly distinguish deterministic local stub use from live-provider examples.
  - Done when every example config validates without provider credentials and identifies its protocol surface and default route.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 8.1_
  - _Boundary: ExampleConfigs_
  - _Depends: 2.1, 2.2_
  - _Validation: go test ./internal/core/config/... ./cmd/lipstd_

- [x] 2.4 Add architecture guardrails for test-only reference support
  - Extend boundary tests so production standard wiring cannot import `internal/refbackend` or `internal/refclient` outside `_test.go` support paths.
  - Preserve existing provider SDK and core/plugin import constraints.
  - Done when architecture tests fail on production imports of test-only reference support and pass for current test packages.
  - _Requirements: 2.6, 7.1, 7.2, 7.3_
  - _Boundary: ArchitectureGates_
  - _Validation: go test ./internal/archtest/...

- [x] 3. Add default-suite dogfood smoke coverage
- [x] 3.1 Build reusable standard-server smoke harness
  - Create an in-process smoke harness that preserves the same standard handler path as serving, including trace/request ID, access logging wrapper, panic recovery, transport auth, frontend mounts, default route selection, and startup security validation.
  - Avoid a real listener where practical, but do not bypass `stackHTTPHandler` or equivalent standard-server middleware.
  - Done when a harness test can send a request through the standard server path to a stub backend without live credentials.
  - _Requirements: 3.6, 3.7, 6.1, 6.2_
  - _Boundary: DogfoodSmokeHarness_
  - _Depends: 2.3_
  - _Validation: go test ./internal/stdhttp/...

- [x] 3.2 Add OpenAI Responses smoke path
  - Send a minimal OpenAI Responses-compatible request through the standard server harness to the stub backend.
  - Assert valid protocol response shape, deterministic assistant output, and stream completion.
  - Done when the smoke fails with a protocol-specific diagnostic if routing, capability, or stream completion breaks.
  - _Requirements: 3.1, 3.5, 3.6, 3.7_
  - _Boundary: DogfoodSmokeHarness_
  - _Depends: 3.1_
  - _Validation: go test ./internal/stdhttp/...

- [x] 3.3 Add OpenAI legacy chat smoke path
  - Send a minimal legacy OpenAI chat-compatible request through the standard server harness to the stub backend.
  - Assert valid legacy response shape, deterministic assistant output, and stream completion.
  - Done when the smoke identifies legacy chat path failures separately from other frontend paths.
  - _Requirements: 3.2, 3.5, 3.6, 3.7_
  - _Boundary: DogfoodSmokeHarness_
  - _Depends: 3.1_
  - _Validation: go test ./internal/stdhttp/...

- [x] 3.4 Add Anthropic smoke path
  - Send a minimal Anthropic-compatible request through the standard server harness to the stub backend.
  - Assert valid Anthropic response shape, deterministic assistant output, and stream completion.
  - Done when the smoke identifies Anthropic path failures separately from other frontend paths.
  - _Requirements: 3.3, 3.5, 3.6, 3.7_
  - _Boundary: DogfoodSmokeHarness_
  - _Depends: 3.1_
  - _Validation: go test ./internal/stdhttp/...

- [x] 3.5 Add Gemini smoke path and no-retry-after-output regression
  - Send a minimal Gemini-compatible request through the standard server harness to the stub backend.
  - Add a standard-server regression proving failover/retry does not occur after client-visible output has started.
  - Done when Gemini smoke passes and a post-output failure case does not silently replace the committed attempt.
  - _Requirements: 3.4, 3.5, 3.6, 3.7, 7.5_
  - _Boundary: DogfoodSmokeHarness, StreamingInvariantSmoke_
  - _Depends: 3.1_
  - _Validation: go test ./internal/stdhttp/... ./internal/core/runtime/...

- [x] 4. Harden proof plugins and extension evidence
- [x] 4.1 Harden auto-append proof behavior
  - Ensure the first-prompt proof applies only to the first logical session request and does not repeat across retries or failovers.
  - Keep the behavior disabled unless configured and visible in inventory when enabled.
  - Done when tests prove first-request-only behavior across a logical session and disabled configs have no request effect.
  - _Requirements: 4.1, 4.5, 5.1, 5.2_
  - _Boundary: ProofPluginSet.refautoappend_
  - _Validation: go test ./internal/plugins/features/refautoappend/... ./internal/core/diag/...

- [x] 4.2 Harden tool policy proof behavior
  - Ensure the proof filters disallowed advertised tools and blocks disallowed emitted tool calls deterministically.
  - Use the tool catalog and tool-call policy seams without adding frontend-specific tool logic to core.
  - Done when tests show catalog filtering, emitted-call denial, inventory visibility, and disabled no-op behavior.
  - _Requirements: 4.2, 4.5, 5.1, 5.2, 7.4_
  - _Boundary: ProofPluginSet.reftoolpolicy_
  - _Validation: go test ./internal/plugins/features/reftoolpolicy/... ./internal/core/extensions/... ./internal/core/diag/...

- [x] 4.3 Harden traffic accounting and redacted capture proof behavior
  - Record client-proxy and proxy-backend observations separately enough to distinguish logical requests and backend attempts.
  - Ensure transcript/capture output is redacted before persistence or exposure and never appears raw in inventory.
  - Done when tests show usage/traffic/capture records include attempt context and redaction is applied before output.
  - _Requirements: 4.3, 4.4, 4.5, 5.3, 5.5_
  - _Boundary: ProofPluginSet.reftraffictranscript_
  - _Validation: go test ./internal/plugins/features/reftraffictranscript/... ./internal/core/diag/...

- [x] 4.4 Add canonical revalidation regressions for proof mutations
  - Verify request and event mutations used by proof plugins are validated after mutation.
  - Cover invalid mutation failures without weakening existing fail-open/fail-closed semantics.
  - Done when mutation tests fail on invalid canonical calls/events and pass on valid proof-plugin behavior.
  - _Requirements: 7.4_
  - _Boundary: ExtensionRegressionTests_
  - _Depends: 4.1, 4.2, 4.3_
  - _Validation: go test ./internal/core/extensions/... ./internal/core/hooks/... ./internal/plugins/features/...

- [x] 5. Complete operator artifacts and final integration validation
- [x] 5.1 Add executable dogfood operator artifacts and consistency checks
  - Add checked repository artifacts that document local stub validation, serving, route inspection, inventory inspection, proof-plugin boundaries, deferred Python-era features, and optional live smoke gates.
  - Prefer examples and snippets that are exercised by config validation or link/check tests rather than free-floating prose.
  - This documentation-oriented task is intentionally included because Requirement 8 is an explicit spec deliverable; it must remain tied to executable examples or checks.
  - Done when README and dogfood documentation point to the same local workflow, and validation checks catch missing example configs or broken doc links where practical.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5_
  - _Boundary: DogfoodDocs_
  - _Validation: go test ./internal/core/config/... ./internal/archtest/...

- [x] 5.2 Run full stage integration and quality validation
  - Run focused tests for CLI, config, diagnostics, local stub backend, proof plugins, stdhttp smoke, runtime invariants, pluginreg, and architecture guardrails.
  - Run repository quality checks after focused tests pass.
  - Done when the stage validation commands pass or any environment-specific skips are documented in the final implementation notes.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 4.1, 4.2, 4.3, 4.4, 4.5, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 7.1, 7.2, 7.3, 7.4, 7.5, 8.1, 8.2, 8.3, 8.4, 8.5_
  - _Boundary: Integration Validation_
  - _Depends: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.2, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 3.5, 4.1, 4.2, 4.3, 4.4, 5.1_
  - _Validation: make quality-checks && go test ./cmd/lipstd ./internal/core/config/... ./internal/core/diag/... ./internal/plugins/backends/localstub/... ./internal/plugins/features/... ./internal/stdhttp/... ./internal/core/runtime ./internal/pluginreg/... ./internal/archtest/...
