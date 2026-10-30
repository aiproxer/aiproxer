# Research & Design Decisions

## Summary
- **Feature:** cursor-sdk-standalone
- **Discovery Scope:** Complex integration / repository extraction; full discovery focused on existing contracts, release prerequisites, packaging, and host verification coupling.
- **Key Findings:**
  - The connector is already an independent executable Go module and imports public host contracts, not host `internal/` packages. Extraction does not require a new ABI.
  - Its root and ACP dependencies use `v0.0.0` plus sibling-directory replacements. No versioned releases were returned by the local tag inventory or GitHub release list. Independent module publication is a prerequisite, not an assumed existing capability.
  - Host coupling includes Cursor-specific workflows, QA workflow contracts, release/family inventories, scripts, cache lanes, examples, and architecture tests that require the connector directory to exist.
  - The connector contains 125 tracked Go files. One deletion PR would exceed the 100-Go-file change gate; cutover must be delivered in bounded steps.
  - The manifest claims Linux and Windows, including amd64/arm64. macOS fake-bridge smoke does not establish production macOS plugin support; the authoring guide explicitly describes Darwin production profiles as fail-closed.

## Research Log

### Public ABI and existing ownership
- **Sources:** `docs/adr/0008-hybrid-backend-connector-plugins.md`, `docs/backend-plugins/authoring.md`, `pkg/lipsdk/backendplugin/interfaces.go`, `connectors/cursorsdk/cmd/lip-backend-cursorsdk/main.go`, `connectors/cursorsdk/internal/service/service.go`.
- **Findings:** `Service.Describe/Configure` and `ConfiguredInstance.Resolve/ListModels/Execute/Close` are already public. The connector uses `NewGRPCServer` and `ForwardExecute`; bootstrap uses host-provided pipe or file-descriptor IPC. The host owns trust, outer process supervision, immutable generations, and deployment access authority; the connector owns bridge/agent processes and SDK semantics.
- **Implications:** retain those owners and interfaces. Do not move Cursor-specific diagnostics into core or introduce a service locator, generic effect runtime, new transport, or direct host-to-Node path.

### Independent module resolution
- **Sources:** `connectors/cursorsdk/go.mod`, `connector-support/acp/go.mod`, authoring guide module publication section, `git tag --list`, `gh release list --limit 10`.
- **Findings:** root contracts and ACP are separate module dependencies. ACP also relies on a root replacement. Existing publication guidance expects tagged dependencies, but currently observed tags are rescue snapshots rather than semver releases.
- **Implications:** establish a tagged root baseline first, then publish a standalone-resolvable ACP nested-module tag, then pin both in the extracted repository. Do not arbitrarily name an already-existent version or turn all support libraries into new projects.

### Repository and verification coupling
- **Sources:** `.github/workflows/cursor-sdk-platform.yml`, `.github/dependabot.yml`, `.github/actions/go-cache/policy.json`, `Makefile`, `scripts/makefile-scope.sh`, `pkg/lipsdk/backendplugin/contracttest/coverage.go`, `internal/qa/*iteration*test.go`, `internal/archtest/cursor_sdk_external_gates_test.go`, `internal/archtest/cursorsdk_boundaries_test.go`.
- **Findings:** Cursor is absent from root production imports and standard required bundles, but tests and jobs explicitly census the in-tree connector. The root Dependabot file contains the Go-module directory, while the observed npm security PR demonstrates a JavaScript security-maintenance surface as well.
- **Implications:** update active inventories and tests to the external boundary; preserve generic deny-by-default and architecture guarantees. Remove the dedicated workflow only after its required-check configuration is replaced or unrequired. Do not just delete checks to obtain green CI.

### Packaging and platform limits
- **Sources:** `connectors/cursorsdk/release.yaml`, `connectors/cursorsdk/manifest/template.backendplugin.json`, `tools/backendplugin/package_plugins/main.go:packageOne`, [Node SEA documentation](https://nodejs.org/api/single-executable-applications.html).
- **Findings:** the current package tool builds the Go executable and copies private companions, but that does not prove a prebuilt/installable JavaScript application. SEA module loading, asset handling, native addons, signatures, and platform support require validation. Latest Node documentation contains features newer than the bridge's CI baseline; these cannot be assumed available in Node 22.22.3.
- **Implications:** choose a private pinned Node runtime plus production JS tree as the initial packaging baseline, subject to redistribution/license verification and SDK execution tests. Evaluate SEA only as an alternative; do not upgrade SDK/runtime or claim single-file support during extraction. Keep runtime files in protected plugin-private storage, with checksums and plugin-local validation; the host manifest continues to bind only the outer executable.

### Change gate and phased removal
- **Sources:** `git ls-files "connectors/cursorsdk/*.go" "connectors/cursorsdk/**/*.go" | Measure-Object -Line` (125), root `AGENTS.md`.
- **Findings:** wholesale connector removal violates the 100 modified Go files per PR rule.
- **Implications:** certify the external replacement before deleting anything. The baseline contains 59 test Go files and 66 non-test Go files. Transfer the first batch's test ownership to certified external CI while retaining a completely buildable fallback; then delete remaining implementation and update host guards within the second batch's remaining 34-Go-file budget. Rebalance further if generic contract tests must stay until final removal. No retirement exceptions, gate overrides, or partially buildable module.

## Architecture Pattern Evaluation
| Option | Strength | Limitation | Decision |
| --- | --- | --- | --- |
| No change | Preserves existing process isolation | npm security and Cursor CI remain coupled to host | Reject |
| Move only the Node bridge | Smaller move | Go connector/tests/releases remain coupled | Reject |
| Extract existing executable connector | Preserves ABI and ownership; independent release | Requires module publication and release packaging | Select |
| Rewrite SDK use in Go | Could remove JS runtime | No verified equivalent API; semantic drift | Out of scope |
| Generic plugin/DI/effect runtime | None needed for extraction | Duplicates existing authorities | Reject |

## Design Decisions

### Decision: Separate source and release ownership, retain the ABI
- Adopt existing public contracts and trusted executable discovery. Move complete Cursor-specific integration together.
- Generalization is already supplied by the existing ABI; no new framework is needed.
- Destination is the standalone public repository `aiproxer/aiproxer-cursor-sdk` (MIT), confirmed by the maintainer. Its provisioning status and publication permissions are re-verified before implementation mutates external infrastructure.

### Decision: Publish only the required reusable modules
- Publish a root semver contract baseline and an ACP nested-module tag with normal module resolution; keep ACP in its existing repository.
- ACP tag format must follow Go nested-module rules (`connector-support/acp/vX.Y.Z`). Its tagged content must use the published root version and omit sibling replacements. Other connectors' development replacements need not change in this spec.
- Pin exact released versions in the extracted connector. No private host APIs or copied generated ABI files.

### Decision: Package a private runtime, evaluate SEA with evidence
- A multi-file private runtime avoids making system Node a host prerequisite and preserves conventional package metadata resolution.
- Runtime redistribution/license review and real SDK import/readiness are blocking release gates. If private-runtime packaging fails on a platform, document a tested external-Node variant honestly; requirements permit this and host extraction remains independent.
- SDK upgrades and new production platform claims require separate review.

### Decision: Preserve host policy rather than erase provider references indiscriminately
- Provider-name strings used to deny unsafe composition or demonstrate unapproved external backends are not dependency coupling.
- Update first-party inventories to exclude the extracted source; keep relevant security regressions, using generic external-plugin fixtures when practical.
- Historical archived specs remain in Go-LIP; active docs link to the standalone project.

## Risks & Mitigations
- **No published contract baseline:** block standalone release and in-tree deletion until dependency resolution succeeds without replacements.
- **Required bridge check remains configured:** inspect GitHub required status checks/rulesets before retiring the workflow, and replace its host-relevant invariant with a generic Node-independence check.
- **Companion trust confused with host executable trust:** retain host digest checks, restrict install permissions, and validate private-file checksums locally; do not claim host authentication validates every JS file.
- **Deletion gate:** two bounded source-removal PRs after external certification; first transfer tests while retaining a buildable fallback, then remove implementation and update active inventories.
- **False macOS support:** preserve production manifest support only; retain macOS fake-bridge smoke as development evidence, not production certification.
- **Cross-repository permissions or runtime redistribution unavailable:** report a blocker before destructive cutover; no hidden provisional release.

## References
- `docs/adr/0008-hybrid-backend-connector-plugins.md`
- `docs/backend-plugins/authoring.md` and `docs/backend-plugins/operator.md`
- `.kiro/steering/{product,tech,structure,testing}.md`
- [Go module version numbering](https://go.dev/doc/modules/version-numbers)
- [Node single executable applications](https://nodejs.org/api/single-executable-applications.html)

### Decision: do not redistribute the SDK (maintainer decision, 2026-10-02)
- **Context:** packaging surfaced the release blocker recorded above — `@cursor/sdk` is proprietary ("use is subject to Cursor's Terms of Service") and its platform package bundles `rg` and `cursorsandbox` binaries whose license texts it does not redistribute. No redistribution right could be verified.
- **Alternatives considered:** (a) redistribute the SDK and its dependency closure in the archive; (b) ship a build-time opt-in "bundled" variant gated on license acceptance; (c) do not redistribute — ship the lockfile and let the operator provision.
- **Selected:** (c). Option (a) asserts an unverified legal right to every operator. Option (b) adds a variant matrix to a trust surface that had already needed three review rounds to stabilize, which is the wrong place to grow.
- **Rationale and consequences:** the runtime stays free of any global Node/npm requirement because provisioning uses the shipped private runtime's own bundled npm; reproducibility is preserved by the shipped lockfile; npm is required rather than any package manager because `overrides` semantics differ and the Undici security baseline depends on them. The provenance split is explicit — the plugin authenticates what it ships, the operator authenticates what they provisioned — and the shipped checksum record therefore covers shipped files only.
- **Spec impact:** requirements 3.4, 3.5 (amended), new 3.6; requirement 2.4; Boundary Context gained a redistribution-posture line; design "Private runtime package" and "Plugin certification" components amended.

---

# Gap Analysis: cursor-sdk-standalone (2026-10-01)

## 1. Current State Investigation

### Domain assets
- `connectors/cursorsdk/` is already an independent Go module (`go 1.26.6`) with `cmd/lip-backend-cursorsdk`, `internal/service`, `internal/product` plus `protocol/`, `fakebridge/`, `comparison/`, `manifest/`, and `bridge-node/`. Measured: 125 tracked Go files (59 `*_test.go`), 170 files total under the connector, 10 bridge `src/*.test.ts`.
- `release.yaml`: `plugin_id io.golip.backend.cursorsdk`, `factory_kind cursorsdk`, `command ./cmd/lip-backend-cursorsdk`, `version 0.1.0`, `profiles [full]`, `replace_policy development-replace-to-monorepo-root`, `private_companions [bridge-node]`.
- Manifest template: closed `golip.backendplugin.manifest/v1`, digest/build placeholders, protocol major 1 / minor 0, Linux+Windows × amd64/arm64 only, export `static` / `local_only` / `per_instance` / `agent_runtime`.
- Bridge (`bridge-node/package.json`, verified on disk): private ESM package, `engines.node >=22.13`, `@cursor/sdk 1.0.23` exact, `overrides.undici 6.28.1`, scripts `build` / `typecheck` / `test` / `live-probe` / `live-scenarios`.

### Reusable contracts and patterns
- Public ABI already exists and is consumed correctly: `pkg/lipsdk/backendplugin.Service` (`Describe`/`Configure`), `ConfiguredInstance` (`Resolve`/`ListModels`/`Execute`/`Close`), `NewGRPCServer` + `ForwardExecute`, `api/backendplugin/v1` registration, `pkg/lipapi` calls/events, `pkg/lipsdk/modelinventory`, and `connector-support/acp` (`ExecutableCache`, `ModelIndex`, tracking inventory, workspace hints).
- Transport uses host-provided `LIP_PLUGIN_CHANNEL_PIPE` / `LIP_PLUGIN_CHANNEL_FD` with loopback `-listen` fallback; no new transport is needed.
- Verified: zero `go-llm-interactive-proxy/internal/` imports from `connectors/cursorsdk`; root production graph does not depend on Cursor.
- Existing behavior evidence is portable: SDK fixtures, bridge hermetic suite, plugin conformance helpers, lifecycle/cancellation/generation-fencing tests, redaction/diagnostics checks, and platform smoke scripts.

### Host coupling surfaces (the actual extraction work)
- Contract/family census: `pkg/lipsdk/backendplugin/contracttest/coverage.go:31` (`connectors/cursorsdk`, `acp-sdk`).
- Architecture guards: `internal/archtest/cursor_sdk_external_gates_test.go`, `cursorsdk_boundaries_test.go`, `backend_multi_user_policy_test.go:122`, plus `standardplugins` posture/multi-user/prefix tests and `providerprofiles` inventory tests.
- QA workflow-lane contracts: `internal/qa` iteration-speed, development-iteration, push-lane, and remote-CI tests pin `cursor-sdk-platform.yml` / `bridge-node-tests` behavior.
- CI/release tooling: `cursor-sdk-platform.yml` (3-OS smoke + required bridge checks on Node 22.22.3), root `dependabot.yml` (`/connectors/cursorsdk` plus `/connector-support/acp`), go-cache `cursorsdk` lane + prune script, Makefile `test-cursor-sdk-*` targets, 8 `scripts/test-cursor-sdk-*` files, `makefile-scope.sh`, `fuzz-targets.tsv`, adhoc-goroutine allowlists, and `.golangci.yml` exclusion.
- Operator surfaces: `docs/cursor-sdk-backend.md`, root `README.md`, `config/config.yaml` Cursor section, and `config/examples/cursor-sdk-experimental.yaml`.

## 2. Requirement-to-Asset Map

| Requirement | Existing asset | Gap tag |
| --- | --- | --- |
| 1.1–1.3 Node-free build/verify/run | Root Go build is already Cursor-free; no-Node lane script/test do not exist | Missing |
| 1.4 Remove active Cursor source/jobs | In-tree module + workflow/Dependabot/cache/Makefile/script coupling all present | Missing + Constraint (100-Go-file gate) |
| 2.1 Standalone repository | No external repo provisioned in evidence; in-tree source is complete and movable | Missing |
| 2.2 Versioned host contracts, no sibling checkout | `v0.0.0` + sibling `replace` only; rescue tags only, `git ls-remote --tags origin` empty; ACP carries the same replace with an explicit publish-time removal comment | Missing (blocking) |
| 2.3 Independent installable release | `release.yaml` is dev-postured (`localdev`, development replace); no standalone release workflow/artifacts | Missing |
| 2.4 Release metadata + checksums | Manifest placeholders + packaging index exist; `compatibility.json` does not | Missing |
| 2.5 Independent SDK/JS security maintenance | Root Dependabot covers the module path; plugin-owned JS audit/release workflow does not | Missing |
| 3.1–3.3 Trusted compatible install/inspect/reject | Existing manifest/discovery/negotiation supports this; external plugin must preserve IDs and posture | Reusable (preserve exact identity) |
| 3.4 Explicit missing-prerequisite errors | Blocked-state diagnostics exist; plugin-local launcher path does not | Missing (launcher) |
| 3.5 Tested packaging decision per OS | Private-companion mechanism exists; per-OS private-runtime vs external-Node evidence does not | Missing + Research Needed (redistribution) |
| 4.1–4.4 Provider/stream semantics | Fixtures, bridge tests, canonical execution, terminal-ordering, reuse/isolation tests exist | Reusable (relocate without drift) |
| 4.5 No replay after downstream content | Core + `DisableTransportRetries` posture exists | Reusable (no new mechanism) |
| 5.1–5.2 Bounded cancellation/shutdown, generation fencing | Existing deadlines, bridge process owner, stale-generation guards exist | Reusable; launcher descendant adds one owned child (Constraint) |
| 5.3–5.5 Redaction, health, sandbox denial | Existing redaction/bounded diagnostics/readiness/sandbox checks exist | Reusable |
| 6.1 Buildable standalone before removal | No standalone build yet | Missing (blocking sequence) |
| 6.2 Full platform/smoke certification | Scripts exist in-tree; external CI + native per-arch evidence do not | Missing |
| 6.3 Post-extraction independence proof | No-Node certification does not exist | Missing |
| 6.4–6.5 Migration/rollback docs | Active in-tree docs exist; external migration/rollback docs do not | Missing |
| 6.6 Provenance retention | Archived Cursor spec exists; active-doc pointer cutover not done | Missing (small) |

## 3. Implementation Approach Options

### Option A: Extend existing components (decouple CI, keep source in-tree)
- Keep `connectors/cursorsdk` in place; remove or gate Node invocations from default host checks and document Cursor as opt-in.
- Rationale: smallest diff, preserves all current tests and release metadata.
- Trade-offs: ✅ fast; ✅ no module publication needed. ❌ Fails 1.4/2.1–2.5 directly: npm manifests, SDK security surface, and Cursor CI ownership stay in the host repo. ❌ Leaves the exact problem (Node as a host-repo dependency) unsolved.

### Option B: Create new components (full standalone repository in one cutover)
- Provision `aiproxer/aiproxer-cursor-sdk`, move all 125 Go files plus bridge/release tooling at once, publish modules, delete in-tree source in the same change.
- Rationale: cleanest end state, one migration event.
- Trade-offs: ✅ single ownership transfer. ❌ Violates the 100-modified-Go-file gate. ❌ Couples module publication, packaging validation, required-check migration, and deletion into one high-risk change with no certified fallback.

### Option C: Hybrid phased extraction (recommended shape)
- Phase 1: publish root semver baseline → publish ACP nested-module tag (`connector-support/acp/vX.Y.Z`) → provision standalone repo skeleton → relocate source without behavior change.
- Phase 2: add plugin-local launcher + native archives + `compatibility.json` → certify standalone source, installed package, and migration path against a versioned host artifact → publish first compatible plugin release.
- Phase 3: replace obsolete required checks with a generic no-Node guard → bounded in-tree removal (59 test files first with buildable fallback, then 66 implementation files + guard updates) → final host + migration/rollback certification.
- Trade-offs: ✅ Respects the change gate. ✅ Blocks deletion on certified replacement. ✅ Keeps every intermediate state verifiable. ❌ Requires disciplined sequencing and exact inventory bookkeeping.

## 4. Effort and Risk
- Effort: **L (1–2 weeks)** — 125-file relocation is mechanical, but module publication, private-runtime packaging, per-arch validation, CI/required-check migration, and bounded two-PR deletion add multiple integration surfaces.
- Risk: **Medium-High** — proprietary SDK/runtime redistribution is unverified, required-check/ruleset state lives outside the repo, and Windows/macOS packaging has platform-specific signing/support limits. (Module tags no longer block: root and ACP now resolve at `v0.1.0-rc.1`.)

## 5. Recommendations for Design Phase
- Keep the approved design direction (Option C / extract existing executable connector, retain ABI); this gap analysis found no reason to invent a new plugin mechanism or rewrite the SDK in Go.
- Treat these as blocking prerequisites, not assumptions: real root semver tag → real ACP nested-module tag → clean `GOWORK=off` resolution with no sibling checkout → certified standalone release → only then in-tree deletion.
- Carry forward as explicit Research Needed: destination repo provisioning/permissions; actual tag versions; GitHub required checks/rulesets governing `bridge-node-tests`; Node redistribution notices; Cursor SDK + bundled `rg` redistribution rights; SEA-vs-private-runtime validation per OS/arch; macOS production support boundary; go-cache lane removal impact.
- Preserve exactly: plugin ID, factory/route prefix, credential/access/sharing/execution posture, SDK 1.0.23 + Undici 6.28.1 baseline, protocol minor compatibility, default-deny multi-user behavior, and historical spec provenance.
