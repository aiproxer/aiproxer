# Implementation Plan

- [x] 1. Foundation: shared seam and connector scaffold
- [x] 1.1 Add the optional negotiated session-option seam to the shared ACP support
  - Decode session/new and set_config_option advertised options, grouped values and current defaults; retain private ACP state on the per-process stdio transport across fresh BindSession handles.
  - When a connector opts in, apply its initial session options after `session/new` and use its effort option ID for per-request reasoning effort. Use refreshed options after exact model selection, restore the negotiated effort default when effort is omitted, and invalidate ambiguous state on failures before any prompt.
  - Connectors that do not opt in keep today's reasoning-effort option and ignore advertised options.
  - Done when new table and fake-transport tests cover advertised and missing values and model-dependent effort, omitted-effort reset, pooled reuse/replacement, independent concurrent sessions and option order, and the existing ACP support, Cursor, Gemini, and AGY tests pass unchanged.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5_
  - _Boundary: connector-support (SessionOptionsSpec)_
  - _Validation: go test ./connector-support/acp/..._

- [x] 1.2 Reconcile the Claude Agent connector posture and canonical namespace
  - Create the independent connector module with its plugin entrypoint, release metadata, and manifest template declaring kind `claudeagentacp`, an agent runtime, local-only access, per-instance processes, and a static credential mode.
  - Preserve the distinct factory kind while advertising canonical model namespace `anthropic/`; configured instance `claude-acp` remains the backend route identity, not a provider-specific core rule.
  - Done when the module builds and the copied manifest-posture parity and service conformance tests pass for the new kind.
  - _Requirements: 1.3, 1.4_
  - _Boundary: backend plugin (Manifest and release, Service)_
  - _Validation: go test ./... in connectors/claudeagentacp_

- [x] 2. Core connector behaviour
- [x] 2.1 Resolve the adapter executable and certify the amended exact runtime profile
  - _Acceptance baseline: independent reviewer-r5 APPROVED for the earlier identity sidecar; preserve that record. Changed model-sidecar bytes require new certification and independent acceptance, not a hash-only authorization update._
  - Resolve from configured path, then `CLAUDE_AGENT_ACP_BIN`, then `claude-agent-acp` on PATH, through the instance-owned executable cache. Never install or download anything.
  - Resolve the connector sidecar deterministically, require Node >=22, and run bounded version probes only after explicit consent. Reject missing sidecar, unsupported Node, unknown/unparsable bootstrap/imported module closure/SDK/CLI identities (version and SHA-256), missing bundled CLI and external overrides; never enable from a minimum version alone.
  - Certify adapter 0.89.1 / SDK 0.3.293 / bundled CLI 2.1.293 against the trust model in design.md, with no administrator source present: owned-root and allowlist effects; each session-setting and host-policy (`managedSettings`) control, recording which are honoured; hostile user/project settings (env, helpers, hooks, MCP, plugins, auto-allow, bypass) and changes between turns; default tools-off and the foreground allow subset. Then with administrator sources present: the gate refuses before any adapter exec, and `honor` mode plus the identity gate reject administrator provider routing. Record identities and results in research.md.
  - Administrator policy taking effect under `honor`, or root/same-user tampering, is expected behaviour and does not fail certification. Certification fails only when an effect the design assigns to connector-owned controls is not excluded by at least one honoured control; then shrink the profile (for example drop the allow policy) rather than enable it.
  - Done when table tests cover found, missing, unknown, unparsable and timed-out adapters with the expected diagnostics, and the certification matrix result is recorded for the enabled profile.
  - Bind every shipped model-extension file and exercise exact discovered model selection, policy-denied resolutions, hostile watcher mutation and existing effective-identity guards with synthetic credentials/provider fixtures. Keep Linux/amd64 deny-only limits and exact Node/upstream identities; never bypass executable certification.
  - _Requirements: 1.1, 1.2, 2.2, 2.6, 3.1, 3.2, 3.4, 3.5_
  - _Boundary: backend plugin (AdapterResolver)_
  - _Depends: 2.7, 2.4_

- [x] 2.2 Build the credential-source environment, configuration roots and session settings
  - Support exactly `api_key` (default), `subscription_token` and `host_login`; reject a missing required secret; never fall back between sources. Add `--hide-claude-auth` for `api_key`.
  - For `api_key` and `subscription_token`, create a fresh 0700 owned root per spawned process (config, home, XDG and Anthropic config directories inside it) and delete it on reap, replacement and Close. For `host_login`, pass the host home and configuration directory unchanged and never touch credential files.
  - Start from an explicit environment allowlist, scrub Anthropic, Claude Code, provider, federation and secret-shaped names, reject credential-like operator additions, and construct connector-owned SessionNewMeta with `settingSources: []`, isolated SDK settings, strict empty MCP, no plugins, host policy via `managedSettings`, tools-off by default and the certified foreground allow subset; exclude background/subagent facilities. Secrets never appear in argv, logs, or error text.
  - Done when table tests assert the exact environment, argv and session metadata for each source, owned-root creation and deletion, missing-secret rejection, rejected allowlist names and redacted string forms; clients cannot override SDK metadata, credential source, environment or MCP profile.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 3.4, 3.5_
  - _Boundary: backend plugin (AuthEnv)_

- [x] 2.3 Answer permission and unknown requests in every lifecycle phase
  - Add the narrow optional transport-options seam before the stdio read loop starts; outside active prompts always refuse permission and error unknown methods. During prompts select an offered decline option by kind by default. Under the allow policy, validate a certified foreground tool/input and select only a one-time allow option; refuse detached/background input or unknown forms. Never select a persistent allow option.
  - Reply with a cancelled outcome when no offered option matches, and with a method-not-found error for any unrecognised agent request.
  - Done when table tests over recorded permission option sets (tool, edit, plan-exit, empty) and unknown methods produce the expected replies, plus fake-process requests before handshake, during options RPCs, between turns and after terminal never receive empty success. Existing connectors retain their defaults.
  - _Requirements: 3.1, 3.2, 3.3_
  - _Boundary: backend plugin (PermissionHandler) and connector-support (opt-in transport policy)_

- [x] 2.4 Integrate runtime-discovered exact model identities and session options
  - Replace static aliases and operator model lists with one connector-local model seam shared by inventory and turn-session options. Extract only selectable policy-filtered runtime rows, expose exact native identities, preserve capabilities and existing identity admission, and derive canonical IDs without family/model/version tables.
  - Keep the shared negotiated-options seam: model selection precedes refreshed model-dependent effort, omitted effort resets, and unknown/unresolved/colliding/managed-denied models reject without fuzzy matching or fallback. Do not publish raw unfiltered initialization models or revive aliases after settings watcher mutation.
  - Done when red-first Node/Go tests with arbitrary new family/version fixtures prove discovery, version/date/context formatting, duplicate/collision handling, malformed/unresolved/denied rows and watcher refusal, while fake transports prove exact model/effort and reset. This task introduces no service discovery process or transport owner.
  - _Requirements: 1.4, 4.1, 4.2, 4.3, 4.4_
  - _Boundary: backend plugin (connector-local model-seam integration: adapter extension, models, ClaudeSpec)_
  - _Depends: 1.1, 2.2, 2.7_

- [x] 2.5 (P) Classify authentication and option failures
  - Map authentication-required before output to a failover-eligible failure and after output to a terminal failure; messages name the remediation for the selected source (`claude setup-token`, `claude auth login` or the `api_key` secret).
  - Map unadvertised model or effort values to an explicit capability rejection. Errors never contain secret values.
  - Done when tests cover the authentication-required, invalid-option, and generic RPC error cases with the expected error kinds and messages.
  - _Requirements: 2.7, 4.2, 4.4_
  - _Boundary: backend plugin (ErrorClassifier)_
  - _Depends: 1.1_

- [x] 2.6 Enforce the administrator-policy and identity gates
  - Fingerprint the platform's administrator and user-writable policy locations and the effective configuration directory's cached server policy before spawn and before each turn; under `refuse` (default) reject naming the source and the `honor` remediation, under `honor` launch and record the disposition; a changed fingerprint replaces the runtime.
  - Refuse known or potential server-policy authority before any runtime exec without explicit honor. Run bounded CLI `auth status --json` with the exact prepared environment/root as diagnostic/source validation only, preserving first-party/source rejection and known organisation disclosure. Effective query admission belongs to task 2.7; this separate probe cannot certify it.
  - Emit one startup log line and resolved-profile fields with credential source, runtime identity and policy disposition, without secrets or account e-mail. Never mask, disable or override administrator policy.
  - Done when table tests over fake filesystems and recorded `auth status` outputs cover absent, present, unreadable and changed policy sources, each credential source, provider mismatch, organisation credentials, timeout and the disclosure fields.
  - _Requirements: 2.5, 2.6_
  - _Boundary: backend plugin (PolicyGate, IdentityGate)_
  - _Depends: 2.2_

- [x] 2.7 Add the connector-local effective identity extension
  - Ship a project-authored ESM bootstrap reusing the pinned adapter library and existing session query; expose fresh current-applied identity through the private versioned RPC, generation-bound one-use ticket and synchronous prompt guard described in design.md. Never authorize from cached CLI/auth notifications or claim remote-policy refresh.
  - Add the small opt-in pre-prompt ACP seam under the existing runtime claim; retain transport-local generation/sequence state, carry the exact nonce-as-ticket tuple in connector-owned metadata and kill/reset ambiguous runtimes before release. Guard the final upstream Pushable append, refuse internal respawn during admission and disable steer/load/resume/fork and auth/provider mutation. Test replay, duplicate RPC, intervening options, delayed probe respawn, cancellation and late timeout completion with zero SDK input. Preserve other connectors and existing process/root owners.
  - Bind the extension and loaded module closure to runtime certification; keep upstream installation and administrator enforcement intact. Test actual all-source identities and remote provider rejection with zero SDK input, ticket/replacement/privacy/timeout/concurrency regressions and unchanged ACP consumers.
  - _Requirements: 2.5, 2.6, 3.3, 5.1, 5.4_
  - _Boundary: backend plugin (adapter-extension, EffectiveIdentity) and connector-support (optional pre-prompt admission)_
  - _Depends: 1.1, 2.2, 2.3, 2.6_

- [x] 3. Integration
- [x] 3.1 Integrate bounded discovery into the engine and service configure path
  - Wire the resolver, authentication environment, permission handler, Claude spec, and error classifier into the shared subprocess backend with an explicit workspace requirement.
  - Configure parses and validates YAML (rejecting literal secrets), reads opaque secrets, enforces the certified runtime/profile gate, and wires the gates so spawn and each turn pass through them. Resolve, model listing, Execute, and Close follow the existing ACP connector shape.
  - Discover synchronously on inventory load or initial execution, using the same certified/gated starter and an options-aware transport with refusal installed before its reader. Use caller cancellation and a 60-second cap, tools-off owned metadata and the configured default or an empty owned discovery workspace; send no prompt. Remove manual model configuration and publish complete validated snapshots with actual fetch timestamps.
  - Hold the existing lifecycle lease from discovery spawn through close/kill/wait/root removal; Close waits for bounded discovery. No second pool/ticker, fabricated defaults, or discovery process per compatible turn. Done when service regressions prove failure/cancel/timeout cleanup, snapshot consistency, unknown-route rejection and no identity-policy bypass.
  - Done when a fake-adapter service test configures every credential source and streams a two-turn conversation whose text and thinking arrive as ordered canonical deltas in one agent session.
  - _Requirements: 1.1, 2.1, 2.6, 5.1_
  - _Boundary: backend plugin (Service, ClaudeSpec)_
  - _Depends: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_

- [x] 3.2 Complete the turn lifecycle: cancellation, failures, and cleanup
  - _Prerequisite: consume the shared failure-terminal and canonical rejection repairs (#1041, #1047, #1052, #1053) and the independently approved `http-workspace-hint` carrier (#1048). Required delivery/CI evidence remains separate from local live proof; none of these prerequisites authorizes merging this feature._
  - _Earlier local implementation and published remote evidence remain a baseline; the amended exact head requires applicable verification and independent acceptance. Portable ACP evidence does not certify Claude profiles on new platforms._
  - Use the session-cancel profile so a client cancel ends the agent turn with exactly one terminal outcome.
  - Surface pre-output process or handshake failures, including authentication-required, as failover-eligible errors. Post-output failures end the stream with an error and no retry.
  - Done when fake-adapter tests cover cancel, crash before output, crash after output, authentication-required before and after output, history divergence, idle reap, and engine Close terminating the process tree. Obtain remote race/native evidence for the modified shared paths.
  - Prove the packaged proxy with host login, instance `claude-acp`, alternate backends disabled and one attempt: `/v1/models`, `claude-acp:anthropic/claude-haiku-5.5`, `claude-acp:anthropic/claude-sonnet-5.5`, `claude-acp:anthropic/claude-opus-5.5`, and explicit `?reasoning_effort=high`. Bind exact model/effort to SDK/provider observations, not model self-description. Missing usable login/provider access leaves acceptance open.
  - Record PID/start-time/cwd for session-discovered workspace, compatible repeats, two projects and return. Use a stable client-session hint with the provider-neutral `X-LIP-Workspace` carrier, and a distinct configured default so the live proof cannot pass through fallback. Successful disable/removal with provisioning rows retained retires only the removed instance after drain; invalid reload preserves current instances; proxy shutdown leaves no owned live descendants. Fix only demonstrated defects at existing owners, delivering new shared-host defects separately.
  - _Requirements: 2.7, 3.4, 4.1, 4.3, 5.1, 5.2, 5.3, 5.4_
  - _Boundary: backend plugin (ClaudeSpec, Service)_
  - _Depends: 3.1, 3.3_

- [x] 3.3 Integrate amended discovery packaging and operator configuration
  - Repair stock discovery provisioning through strict instance/factory-scoped opaque env references and explicitly captured non-secret execution context, as defined in design.md. Preserve empty unrelated-plugin defaults, verified posture/access gates, peer-authenticated secret delivery, resource identity/cloning and restart-required reload behavior; prove built stock lipstd Configure for all three sources without direct child environment injection.
  - Prove value-free strict-schema failures, requested-name-only capture, zero Lookup/launch for denied/disabled/wrong targets, deep-copy isolation, default-empty/loader-control rejection, no secrets after failed peer/negotiation, identity separation, restart-required rebinding and capture-failure cleanup.
  - Add the new connector wherever the ACP connector family is enumerated: contract-test coverage, lifecycle and ACP architecture tests, the multi-user policy test (as denied), cross-platform selection, and make-scope scripts.
  - Add a disabled config example for each credential source, a short operator section on installation, certified runtime identities, credential sources (including `claude setup-token` provisioning and the host-login trust disclosure), administrator-policy handling, foreground permissions, external-effect limits and neutral subscription terms, and the clarifying note in the unsupported-bridges document.
  - Package the project-authored bootstrap sidecar with the connector at a deterministic artifact location; deployment tests reject missing sidecar and unsupported Node and verify closure identity binding. Document Node >=22 and operator-installed upstream dependencies, applicable notices/licenses and no upstream-runtime redistribution.
  - Package the model extension and bind its bytes, reconcile descriptors/example/docs with auto-discovered `anthropic/` models and instance `claude-acp`, remove static inventory guidance and retain stock-host Node independence. Done when package/deployment/descriptor parity tests pass for the exact amended profile without adding Node to ordinary host dependencies.
  - Done when the repository's architecture, multi-user policy, cross-platform selection, and connector contract checks pass with the new connector included and the host rejects it under multi-user access.
  - _Requirements: 1.3, 1.4, 2.1, 2.2, 2.3, 2.4_
  - _Boundary: config/wiring, generic runtimebundle/processhost/private resource identity/reload provisioning_
  - _Validation: go test ./internal/archtest/... ./internal/standardplugins/... ./internal/qa/... ./pkg/lipsdk/backendplugin/..._

Amendment execution order overrides historical numbering: 1.2 -> 2.4 -> 2.1 -> 3.1 -> 3.3 -> 3.2. Completed leaves are accepted prerequisites, not repeated work; this explicit graph avoids an implicit numbering cycle. Runtime profile certification (2.1) gates enabling changed bytes and release claims; administrator authority remains governed by 2.6. Task 3.3 depends on 1.2 and 3.1, not on 3.2; packaged live acceptance in 3.2 depends on 3.3. All 12 historical leaf IDs remain stable.

## Implementation Notes
These notes preserve task-time acceptance and authorization boundaries; the completion record below supersedes historical delivery-pending statements, not runtime limitations.
- Feature integration validation passed with all 25 criteria mapped. Full six-lane race/fuzz evidence also passed against the accepted executable source `82402a7c`: https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/38497672004. Subsequent acceptance/checklist commits change documentation only. All leaves and groups are reconciled; delivery/merge and specification archival remain separate from implementation validation.
- Task 3.2 independently accepted against `82402a7c`: built stock proxy and packaged connector from one source proved exact Haiku/Sonnet/Opus 5.5/high bindings, omitted-effort reset, explicit HTTP 400 capability rejection with zero SDK input, distinct-default workspace isolation/compatible return, rejected-reload preservation, completed-drain selective retirement and clean owned-process shutdown (14 HTTP turns, 11 SDK inputs; billing not observed). Active post-publication drain overlap is not claimed. Exact-head dedicated native/race evidence: https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/38497650858. Feature-level validation and full-race closeout remain separate; no feature merge authorization.
- Task 2.7 includes independently reviewed resolver/hash primitives needed to build the extension. This does not complete task 2.1 certification or enable a runtime profile; actual profile certification, service assembly and sidecar packaging remain required.
- Human authorized the adapter-side contract previously deferred. V1 now includes a connector-local library bootstrap with fresh current-applied query identity and guarded user-input admission; remote policy polling remains runtime-owned, not a latest-policy refresh guarantee.
- Task 2.6 reopened after runtime certification exposed uncached server-policy authority. For the pinned runtime, auth status does not certify remote-policy absence for any credential source: conservatively refuse before runtime execution unless `admin_policy: honor` explicitly consents to potential server policy. Preserve all three sources under honor and all provider/source identity checks; do not disclose potential authority as absent.
- Human-authorized feature-specific delivery budget, with the ceiling delegated to the controller: up to 3,500 added non-test Go lines and 100 total changed files for this complete connector slice, including the authorized identity extension, service assembly, packaging and required stock-host provisioning. The separate 100-Go-file gate, test proportionality, approved credential sources and safety requirements remain unchanged.
- Task 2.6 gate primitives support Linux/WSL only; other platforms fail closed. Task 2.1 certification and task 3.1 assembly must preserve this restriction and wire pre-exec policy checks, per-turn fingerprint replacement and prepared-spawn identity checks.
- Task 3.1 must preserve structured RPC codes before the shared stream mapper drops them, so the Claude classifier can distinguish authentication and option failures; never retain raw credential-bearing causes.
- Task 2.3 accepted for the deny-only profile: allow remains rejected, with no certified foreground allow-input validation implemented; task 2.1 must certify and record this profile restriction before enablement.

## Completion Status
- [x] All 12 leaf tasks and three groups accepted; integration validation mapped 25/25 acceptance criteria.
- [x] Original implementation merged in #1006 (`275d990d145537479f0aa6f9719aac0d03c678e2`); discovery amendment merged in #1039 (`06acc57c5398cdd2a676677e6ccb674e762ab520`).
- [x] Full six-lane race/fuzz run 38497672004 completed successfully; canonical workspace P1 repair included in #1039, and diagnostic P3 follow-up merged in #1078 (`5cfc8ef9db0bbb35edbe8b9fdf0b3bcbcb6e8700`).
- [x] Completion metadata reconciled and full bundle archived. See validation.md for evidence attribution and retained Deferred scope.
