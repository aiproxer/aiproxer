# Implementation Plan

The implementation is intentionally staged around the smallest useful v1. Every production task starts with focused RED evidence; no threat-feed/WAF/persistence framework is introduced.

- [x] 1. Establish the self-defense configuration and domain contracts
  - [x] 1.1 RED-test default-on configuration, validation, and reload classification
    - Add tests proving omitted `access.self_defense` resolves to enabled + impossible paths enabled + the documented adaptive defaults, while explicit `false` is preserved.
    - Cover duration/count/CIDR bounds, `max_quarantine >= initial_quarantine`, offline `check-config`, and invalid candidate rollback.
    - Prove policy fields are reloadable while `state_ttl` / `max_entries` are restart-required and mixed changes reject atomically.
    - _Requirements: 1.1,1.2,7.1-7.6,10.4_
    - _Boundary: config/wiring_
    - _Depends: none_
    - _Validation: `go test ./internal/core/config/... ./internal/core/configreload/...`_

  - [x] 1.2 Define the minimal `core/ingressdefense` policy/state types and architecture admission
    - Add RED compile/architecture tests before introducing the new top-level core package.
    - Define immutable policy/state-limit values, closed reason/transition values, and exact-IP-only state semantics with no HTTP/provider/public-SDK types.
    - Add the core-ownership census rationale: default provider-neutral ingress security remains required when optional features are absent.
    - Keep YAML parsing/defaulting in existing `internal/core/config`; do not add a public `pkg/*` contract.
    - _Requirements: 4.3-4.6,5.6,6.1-6.4,10.1-10.2_
    - _Boundary: core domain policy + architecture guard_
    - _Depends: 1.1_
    - _Validation: `go test ./internal/core/ingressdefense/... ./internal/archtest/...`_

- [x] 2. Implement bounded process-local adaptive state
  - [x] 2.1 Implement failure-window and exponential-quarantine transitions with fake time
    - RED-test isolated failures, threshold crossing, window reset, repeated offense escalation, maximum cap, quarantine expiry, probe offense, successful-auth clear, and inactivity TTL.
    - Use saturating duration arithmetic; reads must not refresh hostile inactivity TTL.
    - An impossible-path offense increments the same offense level used by thresholded auth failures; no generic weighted scoring engine.
    - _Requirements: 3.4-3.5,4.1-4.6,5.3,5.5,6.1,6.4_
    - _Boundary: core domain policy/state_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/core/ingressdefense/...`_

  - [x] 2.2 Prove hard capacity, eviction, exact-IP isolation, and concurrency bounds
    - RED-test unique-IP churn above capacity, lazy TTL expiry, deterministic bounded eviction/replacement, IPv4-mapped normalization at the adapter boundary, and exact address isolation.
    - Prove one source never creates `/24`, `/64`, ASN or country state.
    - Use sharded or equivalently fine-grained synchronization; add barriers/race evidence proving unrelated addresses do not wait on a long-held global request lock.
    - No per-address goroutine/timer and no persistence.
    - _Requirements: 5.6,6.1-6.5,9.4_
    - _Boundary: core domain state_
    - _Depends: 2.1_
    - _Validation: `go test ./internal/core/ingressdefense/...`; `go test -race ./internal/core/ingressdefense/...` where supported_

- [x] 3. Add the conservative early HTTP gate
  - [x] 3.1 (P) Implement and certify the fixed impossible-path matcher
    - RED-test the small audited v1 exact/prefix/traversal set (for example `.env`, `.git`, WordPress, phpMyAdmin/Adminer, PHPUnit and selected obvious CGI/file traversal probes) plus near-miss legitimate paths.
    - Match request-target path structure only; never read request body/canonical messages/tool arguments or arbitrary query values.
    - Add a regression that evaluates all current standard frontend route claims and management/recovery paths against the built-in matcher and fails on collision.
    - _Requirements: 3.1-3.3,3.6,10.3,10.5_
    - _Boundary: stdhttp driving adapter/tests_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/stdhttp/selfdefense/... ./internal/stdhttp/...`_

  - [x] 3.2 Reuse GeoIP client-IP resolution and implement early path/quarantine middleware
    - RED-test direct peer, untrusted XFF/Forwarded spoofing, trusted chains, malformed authoritative chains, parser bounds, and IPv4-mapped normalization using the existing #387 fixtures/semantics.
    - Resolve once, attach the normalized address to the cycle-neutral request context, apply impossible-path behavior, adaptive exemptions, quarantine lookup, and generic 403/404/429 responses.
    - Impossible-path hit records one offense only for non-exempt sources; adaptive exemption bypasses quarantine lookup/mutation but not deterministic 404.
    - Middleware must not read bodies, call external services, or retain raw paths.
    - _Requirements: 1.4,2.1-2.5,3.1-3.5,5.1-5.2,8.3,9.1-9.2_
    - _Boundary: stdhttp driving adapter + cycle-neutral HTTP contract_
    - _Depends: 2.1,3.1_
    - _Validation: `go test ./internal/stdhttp/selfdefense/... ./internal/stdhttp/geoip/...`_

- [x] 4. Make dynamic quarantine authentication-aware
  - [x] 4.1 (P) Add the private conservative credential-presence probe
    - RED-test local API-key requests with absent/present effective key headers, local-noop credential-free auth, future/custom/unknown providers, and mixed provider chains.
    - Implement a private optional provider capability; arbitrary providers that do not implement it must aggregate to `MayAuthenticate`.
    - Return `DefinitelyNoCredential` only when the complete active provider set can prove the request cannot authenticate as presented; never return or retain secret values.
    - Preserve existing public `httpauth.Provider` and SDK contracts.
    - _Requirements: 5.1-5.2,9.6,10.2_
    - _Boundary: stdhttp auth adapter/private contract_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/stdhttp/auth/...`_

  - [x] 4.2 Observe authoritative auth outcomes at the provider-chain chokepoint
    - RED-test pre-principal 401, 403, 5xx/provider error, principal-then-later-reject, full successful chain, and disabled self-defense.
    - Track whether any principal was accepted; count only terminal 401 before principal, and clear exact-address adaptive state only after the full provider chain succeeds immediately before route delegation.
    - Preserve existing auth renderer/status/body semantics; state observation must not replace or infer from downstream final status.
    - Keep the existing `auth.Middleware` entry point as a compatibility wrapper if an internal options variant is introduced.
    - _Requirements: 1.2,4.1-4.2,5.2-5.5,9.6,10.4_
    - _Boundary: stdhttp auth driving adapter_
    - _Depends: 2.1,3.2,4.1_
    - _Validation: `go test ./internal/stdhttp/auth/...`_

- [x] 5. Compose process state and immutable generation policy
  - [x] 5.1 Own one bounded state instance in ProcessServices
    - RED-test construction, disabled-start lightweight ownership, process close, partial startup rollback and one-instance reuse across generation reload.
    - Construct state from effective startup-fixed `max_entries` / `state_ttl`; it owns no goroutine, file, database or network resource.
    - Add the non-owning self-defense state/policy projection to the cycle-neutral HTTP security contract; generations never close/resize process state.
    - _Requirements: 1.2,6.1-6.5,7.4,7.6_
    - _Boundary: composition root / process lifecycle_
    - _Depends: 1.2,2.2_
    - _Validation: `go test ./internal/infra/runtimebundle/...`_

  - [x] 5.2 Wire the self-defense gate and auth hooks in the canonical standard stack
    - RED-test exact outer-to-inner ordering: global security/server/recovery -> existing GeoIP -> self-defense -> general tracing/metrics/request-id/access-log -> inner recovery -> auth -> routes.
    - Project the existing compiled GeoIP resolver source/trusted prefixes to self-defense even when GeoIP enforcement policy is disabled; do not require a country database.
    - Prove GeoIP hard denial wins before self-defense, self-defense impossible/quarantine denial never reaches expensive/noisy inner layers, and outer headers/recovery remain effective.
    - Disabled generation omits both ingress self-defense and auth observation structurally.
    - _Requirements: 1.1-1.4,2.1,7.3,8.1,8.4-8.5,10.4_
    - _Boundary: stdhttp + runtimebundle generation composition_
    - _Depends: 3.2,4.2,5.1_
    - _Validation: `go test ./internal/stdhttp/... ./internal/infra/runtimebundle/...`_

  - [x] 5.3 Prove reload, last-good rollback, and management recovery isolation
    - Policy-only reload must publish a new immutable handler graph while retaining adaptive state; in-flight requests remain on their admitted generation policy.
    - Invalid/mixed restart-required candidates leave last-good generation and process state active.
    - Explicitly prove the separate management/recovery listener is not wrapped by self-defense and remains usable after bad data-plane policy candidates.
    - _Requirements: 1.3,7.2-7.6,8.4,10.5_
    - _Boundary: config reload + composition/integration tests_
    - _Depends: 5.2_
    - _Validation: `go test ./internal/core/configreload/... ./internal/infra/runtimebundle/... ./internal/stdhttp/admin/configreload/...`_

- [x] 6. Add bounded observability and operator documentation
  - [x] 6.1 (P) Add the three self-defense process metrics
    - RED-test counters/gauge registration and finite label values before wiring them into the metrics bundle.
    - Provide denial count by closed reason, quarantine-transition count by closed reason, and current state entry gauge.
    - Prove source IP/CIDR/path/query/header/User-Agent/credential/principal/country cannot become labels.
    - Metrics absence must not affect decisions; no per-request hostile log is added by default.
    - _Requirements: 9.1-9.5_
    - _Boundary: observability infrastructure_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/infra/metrics/... ./internal/core/ingressdefense/...`_

  - [x] 6.2 Document the deliberately small v1 configuration and safety trade-offs
    - Update canonical config/operator documentation with default-on/explicit opt-out behavior, shared `access.geoip.client_ip` trust, adaptive exemptions, defaults, reload/restart fields and generic response semantics.
    - State that fixed IP/CIDR policy remains under `access.geoip`, successful auth resets rather than permanently trusts an IP, and credential-bearing/unknown-auth traffic may still reach auth to protect legitimate shared-IP users.
    - List external feeds, WAF/body inspection, persistent/distributed state and subnet escalation as deferred/non-goals.
    - _Requirements: 1.1-1.5,5.1-5.6,7.1-7.4,8.1-8.5,10.1-10.4_
    - _Boundary: docs/config examples_
    - _Depends: 5.2_
    - _Validation: config example parse/check-config tests where available + doc review against effective defaults_

- [x] 7. Run the focused adversarial acceptance matrix
  - [x] 7.1 Certify shared-address safety and auth classification
    - Scenario: hostile no-credential traffic at IP X triggers quarantine; a valid credential from IP X still reaches auth, succeeds, clears the entry, and reaches a legitimate frontend.
    - Cover local API key, local-noop, unknown/custom provider, invalid credential, principal-then-reject, 403 and 5xx outcomes.
    - Prove success is not permanent trust: a later impossible path is still 404 and later hostile failures can create fresh state.
    - _Requirements: 4.1-4.6,5.1-5.5,9.6_
    - _Boundary: stdhttp composed acceptance tests_
    - _Depends: 5.2_
    - _Validation: focused composed `httptest` suite through the standard handler stack_

  - [x] 7.2 Certify false-positive, spoofing and resource-exhaustion boundaries
    - Legitimate LLM routes with bodies containing SQLi/XSS/shell/malware/traversal examples must not match self-defense solely because of content.
    - Cover untrusted forwarding spoof, malformed trusted forwarding, IPv4-mapped IPv6, exact-IP-only isolation, no `/24`/`/64` escalation, adaptive exemption, existing GeoIP hard denial, route non-collision and management isolation.
    - Drive more unique hostile addresses than `max_entries` and prove bounded state/metric cardinality with no unbounded path retention.
    - _Requirements: 2.1-2.5,3.2,3.5-3.6,5.6,6.2-6.5,8.1-8.5,9.3-9.5,10.3-10.5_
    - _Boundary: security/concurrency/integration tests_
    - _Depends: 2.2,5.3,6.1,7.1_
    - _Validation: focused stdhttp/runtimebundle tests; race test for ingressdefense where supported_

- [x] 8. Run final architecture and repository certification
  - [x] 8.1 Run focused quality/architecture gates and review scope minimality
    - Run core ingress-defense, config/reload, stdhttp/auth/selfdefense, runtimebundle and metrics focused suites plus architecture guards.
    - Grep/review that no threat-feed client, persistence schema, generic WAF/rule DSL, body scanner, public SDK contract or automatic subnet escalation entered the implementation.
    - Run `make quality-checks`; run `make test-cost` if the implementation materially changes default-suite cost.
    - _Requirements: 1-10_
    - _Boundary: repository certification_
    - _Depends: 6.2,7.2_
    - _Validation: focused `go test` commands; `make quality-checks`; `make test-cost` when applicable_

  - [x] 8.2 Run wide QA and close the spec truthfully
    - Run `make qa` after focused gates pass, plus race evidence where supported for the bounded state.
    - Revalidate implementation-time middleware/auth/GeoIP assumptions from `research.md`; repair the design rather than silently diverging if main changed materially.
    - Mark implementation tasks complete/archive the SDD only after merged-main evidence satisfies all applicable acceptance criteria.
    - _Requirements: 1-10_
    - _Boundary: final release-grade certification/spec lifecycle_
    - _Depends: 8.1_
    - _Validation: `make qa`; applicable race run; merged-main focused rerun_
    - _Status: `make qa` PASSES on this branch (exit 0, all gates green, including the 116-requirement release-gate report). COMPLETE. PR #670 merged as b4297dd9 on 2026-09-26. `make qa` green; Linux CI race evidence obtained; merged-main focused rerun green; `lipstd` builds and `--help` / `check-config` exit 0. SDD archived to `.kiro/specs/archive/ingress-self-defense/`._

## Implementation Notes

- Task 1.1: any change that adds non-test lines under `internal/core` breaks the `internal/archtest` line-budget ratchet (`budgets.go` `internal/core` entry plus `phase20_budget_exactness_test.go` `phase20AuditCoreLines`, which must move together as `measured + 25`). The task-local `_Validation:` command cannot detect this; run `go test -count=1 ./internal/archtest` and re-measure whenever `internal/core` grows. Task 1.2 adds a new core package, so expect another ratchet there.
- Task 1.1: `time.ParseDuration` has no `d` unit, so the `state_ttl` upper bound must be written as `168h`; operator documentation (task 6.2) must not print `7d`.
- Task 1.2: `internal/core/ingressdefense` needs `doc.go` as well as `policy.go` — `TestCorePackagesHaveDocGo` requires it in every `internal/core/*` package, and the design File Structure Plan omits it.
- Task 1.2 gate-hardening follow-ups for 2.1: (a) `TestReasonConstantSetIsClosed` only counts const specs with an explicit `Reason` type, so an untyped or aliased `Reason*` constant evades it — widen to any const whose name begins with `Reason`; (b) the 5.6 aggregate-network guard text-bans `netip.PrefixFrom`/`ASN`/`Country` but cannot ban bare `netip.Prefix` (legitimately used by `Policy.AdaptiveExemptCIDRs`), so a future `map[netip.Prefix]*entry` in `state.go` would pass — add an AST check for prefix-typed map keys; (c) `Policy.Validate()` is only meaningful for a filled/enabled policy, while `CompiledSelfDefense.Policy()` returns a zero `Policy{}` for a nil receiver — document that on `Validate`.
- Task 1.2: `scripts/quality-checks.ps1` derives its package scope from tracked modifications only, so a brand-new UNTRACKED package is not built/vetted by `make quality-checks` (repo-wide gofmt and module-wide golangci-lint still cover it). Lint new packages explicitly before relying on `make quality-checks`.
- Task 1.2: `config.CompiledSelfDefense` still exposes scalar accessors that duplicate `Policy()`/`StateLimits()`. No production consumer reads them yet; decide in 5.1 whether to retire them so 5.1/5.2 cannot bypass the domain type.
- Tasks 2.1/2.2 — contracts the HTTP/auth adapters (3.2, 4.2, 5.2) MUST honour, documented on `State`: (a) check `Policy.Enabled` before consulting the state at all, because the process store survives a reload that disables self-defense; (b) apply `Policy.AdaptiveExempt` before `IsQuarantined`, `RecordProbe` AND `RecordAuthFailure`, because the state deliberately does not evaluate the exemption allowlist. Obligation (b)'s third part was added in group 7: both the gate and `selfDefenseAuthHooks.RecordAuthFailure` in `internal/stdhttp/middleware.go` now filter exempt sources, so an exempt address creates no bounded state and moves neither the entry gauge nor the transition counter. Only the gate filtered before that. No requirement mandated it (3.5/8.3 are scoped to the impossible-path hit and the GeoIP interaction), so the security outcome was already correct and only the observability was noisy; the filter is still the right fix because the operator doc promises exemptions suppress adaptive state. Pinned by `TestAcceptanceAdaptiveExemptionAccruesNoAuthFailureState`, which also drives a non-exempt control address to prove the filter is a real exemption and not a disabled observation path.
- Tasks 2.1/2.2 interpretation: a counted auth failure BELOW the threshold returns the zero `Transition` (empty `Reason`) so the closed four-value reason vocabulary is never widened. Callers must read `Reason` only when `QuarantineStarted` is true; the metrics adapter (6.1) must not derive a label from a zero `Reason`.
- Tasks 2.1/2.2 eviction rule: fixed-capacity insertion-order ring per shard, address→shard is a fixed FNV-1a over the 16-byte address, cursor advances unconditionally so a stale slot is overwritten without evicting a live key. It is admission-order, NOT LRU. Capacity is enforced per shard as `ceil(MaxEntries/shards)`.
- `-race` cannot run on this Windows machine: `runtime/cgo: ...\windows_amd64\cgo.exe: exit status 2` (broken cgo toolchain, reproducible with a standalone `import "C"` program). Linux CI must run `go test -race ./internal/core/ingressdefense/...` before merge; that is the only way to close the stated 2.1/2.2 race evidence.
- Review lesson worth reusing: several state-machine rules survived mutation because a test's fixture accidentally let a second code path produce the same observable outcome. Pin normative boundaries by placing the triggering event EXACTLY on the documented threshold (e.g. `lastHostileAt + StateTTL` for the `>=` in transition rule 1) and assert exact equality, not a lower bound.
- Tasks 3.1/3.2 — architecture: the new `IngressSelfDefenseOverlayMax` archtest overlay (markers `/stdhttp/selfdefense/`, `/stdhttp/contract/source_addr.go`) was needed because adding ~447 production lines broke `TestShrinkage_NetReductionMeetsRequirement115` with only 88 lines of convergence headroom left. It uses the repository's own documented, nine-times-preceded extension point and RESTORES the legacy `-800` convergence floor (to -882) rather than relaxing it. **Maintainer follow-up:** legacy convergence headroom is now down to ~82 lines and the overlays carry a large share of the surface — record the reduced headroom in the spec/ADR trail and require a real convergence-tree reduction pass before overlay #11 rather than defaulting to another overlay.
- Tasks 3.1/3.2 — budgets: `internal/stdhttp` and the self-defense overlay are now re-audited at 7607/7632 and 467/492 respectively (audit == live measurement, ceiling == measured + 25). Task 5.2 wires into `internal/stdhttp/middleware.go`, so expect another deliberate ratchet move there.
- Tasks 3.1/3.2 — seam facts for later groups: (a) `internal/stdhttp/selfdefense.CredentialProbe` is a local `func(*http.Request) bool` (true = may authenticate) because `contract.CredentialDisposition` is task 5.1's — 5.2 must REPLACE it with a one-line adapter to the contract enum, not add a second probe implementation; (b) `internal/stdhttp/contract.SourceAddr/WithSourceAddr` is the only context helper and carries just the normalized `netip.Addr`, so 4.2's auth observer must read it from there and must NOT import `internal/stdhttp/selfdefense`; (c) `selfdefense.Middleware` has no production caller until 5.2 wires it into `stackHTTPHandler` immediately inside the existing `if sec.GeoIP.Policy != nil` block.
- Tasks 3.1/3.2 — documented trade-off: a probe smuggled behind a percent-encoded `%3F` decodes to a literal `?` inside `r.URL.Path`, so `normalizeTarget` cuts there; such a probe evades the deterministic classifier and accrues no adaptive offense, but still reaches no route because the router sees the same decoded `?`. If adaptive-counter coverage for smuggled probes is ever required, that is a requirements change (3.2/9.4), not a code fix.
- Tasks 3.1/3.2 — deferred follow-ups for 4.x/5.2: prove the gate's step-4 exemption short-circuit is observably before `IsQuarantined` (currently one extra lock is undetectable); pin `internal/stdhttp/contract/source_addr.go`'s "only the address" invariant with a source-shape/archtest scan; extend the 3.6 non-collision inventory to the operator-configurable admin path prefixes once an inventory seam is reachable (`internal/stdhttp/mount_admin.go`); align the 404 body with `http.NotFound` at wiring time so the self-defense 404 is not fingerprintable against Go's default `404 page not found`.
- Tasks 4.1/4.2 — 5.1 MUST NOT reimplement the aggregate. Add a `CredentialDisposition`-returning sibling to `auth.NewCredentialPresenceProbe` (one fail-open decision point), keep `MayAuthenticate` as the ZERO enum value, and assert polarity explicitly: the existing bool is INVERTED relative to the enum name (`definitelyNoCredential` -> false, `mayAuthenticate` -> true, per `selfdefense/middleware.go` `if in.Probe != nil && !in.Probe(r)`). A naive adapter will flip it and lock out every credential-bearing user.
- Tasks 4.1/4.2 — 5.2 wiring checklist: select `auth.SelfDefenseMiddleware` with hooks when enabled and plain `auth.Middleware` when disabled (allocation-identical, satisfying Req 1.2); build hooks against the process `State` + generation `Policy` reading `transition.Reason` ONLY when `QuarantineStarted`; set `selfdefense.Input.Probe = selfdefense.CredentialProbe(auth.NewCredentialPresenceProbe(providers))` ONCE per generation from the SAME provider slice the auth chain uses; keep `State`/`Policy` non-owning in the gate; never add a second aggregate, disposition type, or source-address read.
- Tasks 4.1/4.2 — budgets: the two new `internal/stdhttp/auth` production files were routed through the self-defense overlay (markers now also `/stdhttp/auth/credential_probe.go` and `/stdhttp/auth/selfdefense_observation.go`; measured 622, cap 647). This is scope-tighter than the GeoIP precedent, which already claims the pre-existing shared `/stdhttp/middleware.go`. `internal/stdhttp` is re-audited at 7789/7814. **Convergence slack is now only 55 lines** (`-855` vs the `-800` floor) and the `internal/stdhttp` ceiling has 25 — groups 5 and 6 will likely need the same overlay routing, and `request_plane.go`'s CriticalFileBudget headroom may bite.
- Tasks 4.1/4.2 — accepted product trade-off to name for the maintainer: because the clear is keyed on the same exact address, a valid-credential success by the legitimate user behind shared NAT also lifts the quarantine accumulated by an attacker at that address. This is exactly what Requirement 5.3 and research Repair 2 mandate ("a reset, not an IP trust cache"), so it is compliant, but it is a liveness-over-security trade, not a free win. The same reasoning makes a concurrent impossible-path offense landing between chain start and delegation get wiped; spec-mandated, not a defect.
- Tasks 4.1/4.2 — archtest follow-ups: add a marker-list drift test for `pathMarkerOverlaySpecs` (none exists today, so silent broadening of a marker list is invisible; the GeoIP overlay's broad `/stdhttp/middleware.go` marker is equally unpinned), and add a forbidden-import archtest rule proving `internal/stdhttp/auth` never imports `internal/stdhttp/selfdefense` (design "Allowed Dependencies" is currently documentation-only).
- Tasks 5.1/5.2 — the generated `internal/stdhttp/contract.SelfDefenseSecurityInput` carries SEVEN members (`Policy`, `State`, `Resolver`, `ImpossiblePaths`, `Probe`, `Observer`, `Now`), two more than the design's enumerated list. Accepted: the design says "carries:" not "exactly these" (it already omits `ImpossiblePaths` and `Probe`), and the only normative prohibition is process state limits. `Now` is required so the gate and the paired auth observer cannot straddle two clocks in one generation. A reflection test pins the member set, so any future addition is a deliberate, visible contract edit.
- Tasks 5.1/5.2 — `ProcessServices.Close` deliberately registers NO closer for the adaptive state and does NOT nil the exported `ProcessServices.IngressDefense` field. Justified and review-confirmed: the field is written once pre-publication and read-only after (so it is race-free by construction), the state owns no disposable resource, `compileCandidate` refuses new generations after close, and an in-flight generation's post-close use is a benign map operation on a fully functional object. Disposal reduces to reference reachability, which GC handles.
- Tasks 5.1/5.2 — `internal/stdhttp/auth/credential_probe.go` (reviewed in group 4) was reduced so the boolean probe DELEGATES to the new `NewCredentialPresenceDispositionProbe` sibling; there is exactly one fail-open decision point. `internal/stdhttp/contract.CredentialDisposition` polarity is INVERTED relative to its name: `DefinitelyNoCredential` means the gate DENIES. Inverting it produces the shared-NAT lockout signature (`may authenticate reaches the auth chain -> status = 429, want 200`).
- Tasks 5.1/5.2 — `testdata/architecture/hexagonal_migration_baseline.json` admits `internal/core/ingressdefense` as a direct core import of `./internal/infra/runtimebundle`. The test asserts EXACT set equality both ways from `go list`, so any other new core import still fails; design "Allowed Dependencies" explicitly sanctions this import.
- Tasks 5.1/5.2 — budgets after group 5: `internal/infra/runtimebundle` 13787 (measured 13762), `internal/stdhttp` 8038 (measured 8013), self-defense overlay 894 (measured 869), convergence delta -820 vs the -800 floor (the four new overlay markers are load-bearing: without them it would be -598). `compile_generation.go` was shrunk rather than inflating its `CriticalFileBudget`. **Remaining slack is small — group 6 should expect to re-measure.**
- Tasks 5.1/5.2 — open coverage follow-ups for the final validation: (a) no shipped test pins the composition-root probe DERIVATION, only its fail-open posture (the shared `processServicesTestConfig()` normalizes to `local_noop`, so a constant-`MayAuthenticate` projection would pass — add an `auth.handler: local_api_key` generation fixture asserting `DefinitelyNoCredential` for a bare request); (b) `TestProcessServices_AdaptiveStateIsPerProcessAndDisposedWithItsOwner` overstates its name (it pins distinct-per-process ownership plus a clean close, not a state disposal); (c) `TestSelfDefenseGateHasExactlyOneDataPlaneCallSite` walks only `internal/stdhttp`.
- Task 5.3 — the reload/rollback/management certification is real: an independent reviewer ran six production mutations (gate-order swap, shared-mutable-policy, per-generation state, removal of the `configreload.Classify` publication call, downgrading `max_entries` to reloadable, in-place state reset on compile) and every one broke a substantive assertion. The in-flight pinning test is genuinely parked across publication on a channel barrier inside the admitted generation's composed handler (`GenerationDispatcher.ServeHTTP` holds `defer lease.Release()` for the whole call); no sleeps or polling.
- Task 5.3 — PRE-EXISTING, NOT ours: `go test -v ./internal/infra/runtimebundle/` is intermittently flaky on loaded machines. Reviewer evidence with the two new files moved out: 2 of the first 4 runs failed on PRE-EXISTING heavyweight `TestRefinement*` / `TestBillingHostLoop_*` / `TestBuild*` load-timeout failures, a different test name each time, then 14 clean runs. The new tests ran 50x each with zero failures. **Track this separately — do not attribute it to this spec.**
- Task 5.3 — residual: management recovery isolation is proven at the handler/loopback level with the real `adminreload.NewHandler` and a real `runtimehost.Coordinator`, but NOT through `adminreload.Server.Start`'s listener bind, so a defect in that wiring would not be caught. `make test-cost` is NOT required (new tests contribute under 3% of an already 21-52s package, variance-dominated), though the maintainer may want the ratchet recorded.
- Tasks 6.1/6.2 — metrics: `lip_self_defense_denials_total{reason}`, `lip_self_defense_quarantine_transitions_total{reason}`, `lip_self_defense_state_entries` (no label). The `reason` VALUE domain is pinned on the REAL `NewBundle(...).Registry` via `Gather()`, not just the label NAME and not just the public API — an adversarial review twice defeated a name-only/public-API-only guard by smuggling a source address into a `reason` value on an owned family with the suite fully green. The enforcing check is `selfDefenseBundleLabelFindings` (test file) and it is evasion-independent w.r.t. file, collector type, call site and label name.
- Tasks 6.1/6.2 — DOCUMENTED LIMITATION (accurate, deliberate, no follow-up ticket): both halves of the label guard are scoped by the `lip_self_defense_` name prefix, so a self-defense series RENAMED out of that prefix — or published from a file carrying no self-defense metric name — is outside its scope, and registering it on the bundle registry does NOT bring it in. Closing that needs a whole-bundle inventory ratchet (enumerate every `Register*(r)` reachable from `NewBundle` plus the std process/http collectors and require `Gather()` to produce no family outside it), which is a materially larger change and must not be smuggled into a feature task.
- Tasks 6.1/6.2 — the `SelfDefenseProm.Denial`/`QuarantineTransition` methods carry `defer func() { _ = recover() }()`, matching the repo's established rule that a non-authoritative observer must never change the caller's answer (precedent: `internal/infra/runtimehost/observability.go`, `internal/core/runtime/conversation_view.go` `safeObserver`). The invariant is load-bearing because the gate calls the observer AFTER choosing the generic status and BEFORE writing the response. Without it a metrics panic became a 500 instead of the generic 404 — reviewer-verified by removing the recover.
- Tasks 6.1/6.2 — budgets: `internal/infra/metrics` was OUTSIDE every LOC budget, so the feature's metrics collector is now ratcheted via a `/infra/metrics/self_defense_` PREFIX marker on the self-defense overlay. `internal/infra/metrics` is not one of the five Req 11.5 affected surfaces, so this is a pure subtraction (measured: raw delta identical with and without the marker) and cannot mask a regression inside them. Current state: overlay 1034 / cap 1049 (15 lines headroom, deliberately left below the usual 25 rather than inflating the cap), convergence delta -961 against the -800 floor, `internal/infra/runtimebundle` 13786 / 13787 (1 line headroom), `internal/stdhttp` 8013 / 8038.
- Tasks 7.1/7.2 - the acceptance matrix is certified by three composed suites, not by lower-layer assertions. Independent review reproduced eight mutations (four the implementer's, four of its own) and every one was caught with an on-message failure, including the two that matter most: a fail-closed credential probe (the shared-NAT lockout signature) and a /24-truncated state key (subnet escalation). The shared-NAT scenario drives a real `POST /v1/responses` round trip through the real frontend and local-stub backend and demands `status=="completed"` with a non-empty id, so it cannot be short-circuited. The churn scenario asserts the entry cap exactly (1024 of 2048 addresses), a structural metric-cardinality bound (`2*len(AllReasons())+1`, independent of address count), and a byte-identical `runtime.NumGoroutine()` measured after a 64-request warm-up in a non-parallel top-level test.
- Tasks 7.1/7.2 - cost is immaterial (~0.25s added, under 1% of the affected packages; compile-only elapsed indistinguishable with and without the 2023 new lines). `make test-cost` is NOT required before merge.
- Tasks 7.1/7.2 - residual: the malformed-forwarding fail-closed 403 is certified in `internal/stdhttp` against a mounted route, never through the real frontend in the composed distribution. "Route not reached and auth not invoked" is a strictly earlier stack position than frontend decode, so the assertion is stronger, not weaker; a real-frontend malformed-chain case would be cheap optional hardening.
- Tasks 7.1/7.2 - the new acceptance helpers use unsynchronized counters. They are race-free today because the auth and route paths contain no `go func` and `stackHTTPHandler` spawns none, but a future asynchronous dispatch would surface as a race in the TEST HELPER. Converting them to `sync/atomic` is cheap insurance.
- Task 8.1 - FINAL BUDGET STATE (measured, do not assume earlier numbers): `internal/infra/runtimebundle` 13786/13787 (1 line), `internal/stdhttp` 8021/8038 (17 lines; 8 consumed by group 7's exemption filter + acceptance wiring, recorded as a comment rather than a cap raise), self-defense overlay 1034/1049 (15 lines, deliberately 10 below convention), convergence -961 against the -800 floor (the merge base was -882, so the feature IMPROVED convergence by 79 lines while adding a 1034-line overlay). All three ratchets are live and near-binding; the next change in any of those trees must re-measure with `go run ./scripts/arch-report.go` first.
- Task 8.1 - scope-minimality certified over the real diff (75 files, +13898/-58, 70 `.go` files, merge base `d196469b`), not by token search. The 14 new production files import only `fmt`, `math`, `context`, `time`, `sync`, `sync/atomic`, `log/slog`, `net/netip`, `net/http` (server types only) plus three in-repo packages and Prometheus; `go.mod`/`go.sum` untouched in every module; `git diff --name-only -- 'pkg/*'` empty and `pkg/lipapi` 472 / `pkg/lipsdk` 73 exported symbols identical to the merge base. No `regexp` in the matcher (34 frozen literal rules, byte-folded, 0 allocs, hard-bounded at 64 by test), no `r.Body`/`RawQuery` read, no goroutine/timer, no prefix/ASN/country-keyed state. Exactly ONE client-address resolver, ONE credential aggregate, ONE adaptive state, ONE source-address context helper.
- Task 8.1 - the ONE deliberate header read in the whole feature is `internal/stdhttp/auth/credential_probe.go` `p.headers().APIKeyFrom(r.Header) == ""` — the conservative credential-presence probe required by Requirement 5, comparing emptiness only and never storing/returning/logging the value. If this code is ever extended toward content inspection, that is the line to re-check first.
- Task 8.1 - `make test-cost` was deliberately NOT run (opt-in, Windows-authoritative, needs maintainer authorization). Controlled like-for-like measurement in a throwaway base worktree instead: full `go test -count=1 ./...` 99.9s at base vs 101.7s at HEAD (+1.8s, +1.8%, inside noise); focused min-of-2 63.8s base vs 63.0s HEAD. Judged NOT material for ~10,900 lines of new test code. A human must run `make test-cost` if they want the ratchet formally recorded.
- Task 8.1 - `make test-race` on Windows is a DOCUMENTED SKIP, not the broken-cgo signature: it delegates to `scripts/race-check.ps1`, which prints `SKIP: Go race evidence is unsupported on Windows; Linux CI remains mandatory.` and exits 0. The `runtime/cgo: cgo.exe: exit status 2` failure only appears from a manual `-race` build. **Linux CI must still run `make test-race`** — this spec has no race-detector evidence from any platform.
- Task 8.2 - `make qa` PASSES on this branch (exit 0; quality-checks-fast, qa-tests, the all-module lint sweep, vuln, backend-plugin release gates, and the openresponses static compliance all green; the release-gate report enumerates 116 requirements). IMPORTANT: the FIRST `make qa` run failed with `FAILED: connectors/oci ... outcome=deadline_exceeded`. That is a per-module lint DEADLINE in `scripts/lint-all-modules.ps1` under parallel load, not a lint finding and not caused by this feature: `git diff --name-only d196469b..HEAD -- 'connectors/*' 'connector-support/*'` is empty, and `golangci-lint run` inside `connectors/oci` alone returns **0 issues in 62s**. The immediate `make qa` re-run passed. **Known flaky gate: if `make qa` reports a single module `deadline_exceeded`, re-run that module's lint standalone before treating it as a real failure.**
- Task 8.2 - DELIVERED. PR #670 merged as `b4297dd9` on 2026-09-26T23:46:40Z. Merged-main verification on the original main worktree: focused self-defense suites, `internal/archtest`, `internal/qa` all green; `go build ./cmd/lipstd` exit 0; `lipstd --help` and `check-config --config config/config.yaml` exit 0. SDD archived with `phase: completed`, `completed: true`, `ready_for_implementation: false`. **The next change in either tree must re-measure with `go run ./scripts/arch-report.go` before relying on these caps.** Note: the overlay comment's per-file figure uses the harness's own counting semantics (a raw `Get-Content | Measure-Object -Line` reports fewer lines for the same file).

- Task 8.2 - RACE EVIDENCE OBTAINED, and the platform caveat is now RESOLVED. make test-race remains a documented Windows skip, but PR #670 carried a Linux race detector check that PASSED, and a manual dispatch of the strict full-suite gate (ace-fuzz-nightly.yml, ref = the feature branch) ran the feature's own packages under -race on Linux: internal/core/ingressdefense ok 1.030s, internal/stdhttp ok 59.971s, internal/stdhttp/selfdefense ok 1.067s. So the new sharded concurrent state, the HTTP gate and the stdhttp stack are all race-detector clean. The dispatched run's overall red status is entirely PRE-EXISTING and unrelated: a WARNING: DATA RACE between parallel subtests of TestBuildCreateRequest_UnrepresentableContentRejected (internal/plugins/backends/openresponsescompat/mapping_test.go:253/256/265, a shallow struct copy so every subtest mutates one shared Items backing array) and the known load-timeout flake in TestRefinement*. Neither file is in this feature's diff. The pre-existing race is filed as #671. The FIRST nightly dispatch failed for a third, also-environmental reason (go: downloading ... [setup failed] on every module, the same signature as the two prior scheduled failures on main); a retry got past module download and produced the real results above.
- Task 8.2 - dependency-direction note for future readers: internal/plugins/backends/openresponsescompat (and therefore much of the tree) now transitively depends on internal/core/ingressdefense via internal/core/config -> CompileSelfDefense. That is the dependency design.md "Allowed Dependencies" explicitly sanctions ("internal/core/config may depend on internal/core/ingressdefense to compile typed policy/state limits"). It is benign because the package is a near-leaf importing only fmt, net/netip and time, but it does mean config-reading packages now compile it in.

## Completion Status

Completed and certified on `main` at `1956345a51b5f56ee33067c74990e89863e53af1`.

- [x] Every task in this plan is checked and its validation ran; no task is marked complete with a skipped or failing check.
- [x] The feature and its four corrective follow-ups are merged to `main`: #670 `b4297dd9`, #673 `2ec9a155`, #674 `a1825980`, #678 `99f48a46`, #679 `1956345a`. `spec.json` records the same chain under `implementation`.
- [x] `gofmt`, `go vet ./...`, `go build ./...`, the full `go test ./...`, `make quality-checks`, `make test`, `make qa`, `internal/archtest` and `internal/qa` all exit 0 on the merged baseline; `lipstd check-config --config config/config.yaml` reports the shipped example valid.
- [x] Every review finding from the three review rounds has a dedicated regression test, and each is failing-first: the admission-ring ABA on both the `Clear` and the TTL-expiry path, the effective-value reload classification, the owned-route carve, its exact-versus-subtree and case-sensitivity fidelity, the disabled-mount carve, `GET` owning `HEAD`, the hidden methodless registration, the percent-escape rule, and the operator literal-path grammar with the differential invariant in `internal/archtest`.
- [x] Approved requirements, design and task approvals are preserved verbatim. Where the implementation superseded approved text, the divergence is recorded below rather than by editing the approved requirement.

### Known divergences from approved text

Requirement 3.6 states that the built-in impossible-path set "does not overlap standard distribution frontend routes". The shipped implementation deliberately permits that overlap, because a frontend `base_path` and the diagnostics/metrics/operator mount paths are configurable to any literal path, including one inside a frozen family, and silently making such a route unreachable would be a worse defect than a probe reaching the router. The invariant actually enforced is:

> A published route is never refused: the matcher refuses a path exactly when it matches a frozen family AND the router does not own it.

Ownership and match semantics are resolved against the real `http.ServeMux` after mounting, so a disabled feature carves nothing, an exact registration carves no subtree, a trailing-slash registration carves its subtree, comparison is case-sensitive, a `GET` registration also owns `HEAD`, the method is part of ownership, and the router's fallback is never ownership. Operator-supplied paths are constrained to a literal grammar, and the carve reproduces the router's segment grammar for the wildcard claims the bundled frontends publish.

Requirement 3.6's text is left unchanged because it is approved content; superseding it is a requirements decision, not a documentation one. The normative operator-facing statement of the shipped behaviour is `docs/ingress-self-defense.md`, which is kept current.

### Evidence not held

- **Race evidence is partial.** The bounded state and the stdhttp gate were certified race-detector clean on Linux for the #670 baseline. The four corrective follow-ups are routing, config-validation and documentation changes with no concurrency, but no race workflow runs on their pull requests and `go test -race` is a documented Windows skip, so they are UNVERIFIED rather than passing.
- The nightly full-suite race gate was red for a pre-existing, unrelated reason: `WARNING: DATA RACE` between parallel subtests of `TestBuildCreateRequest_UnrepresentableContentRejected`, a shallow struct copy so all subtests mutated one shared `Items` backing array. That defect was filed as #671 and is fixed on `main` by #680, which gives each parallel subtest its own call instead of copying a shared one. Neither file is in this feature's diff. The nightly gate itself has not been re-dispatched here, so its current status is unverified.
- `make test-cost` has never been run for this feature; it is opt-in and Windows-authoritative. The controlled like-for-like measurement recorded above is not a substitute for the ratchet.
- Convergence headroom is narrowing: the delta is -891 against a -800 floor. The next change touching `internal/stdhttp`, `internal/infra/runtimebundle` or `internal/core` must re-measure with `go run ./scripts/arch-report.go` before relying on any cap recorded here.
