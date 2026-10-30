# Implementation Plan

## Execution Rules

- Execute against implementation-time current main, not the specification baseline blindly. Revalidate the session-preparation order, feature-plane census, large-body proof shape, and featurehost ownership before Task 1.
- Use TDD: create/refresh RED characterization tests before production behavior in each workstream.
- Keep this feature behavior-neutral for existing coding-oriented consumers. Do not gate #637/#467/#468/#469/#475 in this implementation.
- Do not add coding-client branches to generic core/runtime.
- Do not turn the new MetadataOnly plane into a hidden full-Call/content dependency.
- Remote TypeSafe/Jev tests are hermetic by default; live external tests are explicit opt-in only.
- Tasks marked (P) may proceed in parallel after their listed dependencies.

- [x] 1. Rebaseline current-main invariants and freeze the acceptance matrix
  - [x] 1.1 Characterize session-authority and stage ordering before production changes
    - Add/refresh tests proving the current order through BeginTurn, A-leg fetch, secret guard, frontend ingress, submit, tool catalog, request shaping, pre-request, and route hint.
    - Characterize SessionView construction/cloning and prove ClientSessionHint is not proxy authority.
    - Record implementation-time paths/symbols in test comments only where useful; preserve semantic order if files moved.
    - _Requirements: 2.1-2.3,4.1-4.7,11.4-11.8_
    - _Boundary: existing runtime/session/execctx tests; no production behavior_
    - _Depends: none_
    - _Validation: focused runtime preparation/order tests remain GREEN before feature edits_

  - [x] 1.2 (P) Freeze positive and adversarial client/tool evidence fixtures
    - Reuse current codexclientcompat, tool-call-classification, compaction-event-detection, and explicit-completion research fixtures.
    - Add a table describing which harness identities are high-confidence, ambiguous, or unsupported by UA alone at implementation time.
    - Include Codex/Roo stable identity positives, Cline generic-SDK-UA negative, OpenCode/Pi/Droid/Hermes existing root matcher cases, distinctive coding-tool clusters, weaker tool+project-marker cases, and technical-chat negatives.
    - Do not make every named harness positive merely because it was researched; require actual supported evidence.
    - _Requirements: 3.1-3.10,12.1-12.3_
    - _Boundary: feature/shared-facts test fixtures only_
    - _Depends: none_
    - _Validation: one data-driven fixture matrix with explicit expected evidence source/reason_

  - [x] 1.3 (P) Rebaseline feature-plane and large-body wire eligibility
    - Pin the implementation-time standard plane count/order, request-access classes, generated-plane currency, and static disposition for an occupied MetadataOnly plane.
    - Characterize canonical versus wire proof inputs available for ClientUserAgent/tool names without introducing new behavior yet.
    - Prove the pre-change wire path has no classification evidence and no hidden shadow Call.
    - _Requirements: 5.1-5.7,11.4-11.5,12.7,12.11_
    - _Boundary: pkg/lipsdk/feature, internal/core/largebody, frontendpipe tests_
    - _Depends: none_
    - _Validation: plane census + large-body static/differential characterization tests_

- [x] 2. Add the bounded SDK classification contract and legal extension plane
  - [x] 2.1 Define scalar SessionView classification and bounded evidence types
    - RED-test zero-value unknown semantics, positive validation, IsCodingAgent, source/confidence/evidence/revision bounds, and absence of variable content collections.
    - Add session Classification to SessionView and verify all SDK/core view clone/projection helpers preserve it without new aliasing.
    - Add sessionclassification Evidence and fixed ToolCategorySet plus pure tool-name accumulation using lipapi.ClassifyToolName.
    - Keep Call, ToolDef slices, raw header maps, provider DTOs, SQL types, and vendor response types out of the SDK contract.
    - _Requirements: 1.1-1.8,3.9,5.2-5.5,7.3-7.4,11.1-11.5_
    - _Boundary: pkg/lipsdk/session, pkg/lipsdk/sessionclassification, affected view-copy tests_
    - _Depends: 1.1,1.2_
    - _Validation: public SDK unit tests + external compile/architecture guards_

  - [x] 2.2 Add StageIDSessionClassification and exclusive PlaneSessionClassifier
    - RED-test legal stage order after secret_guard and before submit_request.
    - Declare one exclusive sessionclassification.Classifier plane with RequestBodyMetadataOnly access and bounded diagnostics.
    - Update/generate the closed plane surface, snapshot accessors, descriptors, inventory and generator fixtures; do not hand-edit generated outputs outside the repository generator workflow.
    - Duplicate effective classifier contributors must fail generation composition.
    - _Requirements: 4.1-4.7,5.1,5.6,8.5-8.6,11.4-11.7,12.11_
    - _Boundary: pkg/lipsdk/feature, plane generator/generated outputs, architecture tests_
    - _Depends: 1.3,2.1_
    - _Validation: generator currency + plane diagnostics + stage-order + duplicate-exclusive tests_

  - [x] 2.3 Add the generic fail-open classification stage runner
    - RED-test no-plane no-op, valid positive projection, invalid classifier output, classifier error, context cancellation, and preservation of an already-positive SessionView value.
    - Implement feature-neutral execution that calls only the SDK Classifier and never interprets client families, modes, persistence, or Jev.
    - Ensure runtime classifier failure cannot reject an otherwise valid inference request.
    - _Requirements: 1.2-1.7,4.3-4.6,7.6,11.4-11.8_
    - _Boundary: internal/core/extensions narrow stage runner + focused tests_
    - _Depends: 2.1,2.2_
    - _Validation: stage unit tests; core package has zero concrete sessionclassification feature imports_

- [x] 3. Implement shared client identity facts and the deterministic local heuristic
  - [x] 3.1 (P) Extract a bounded root client-family matcher and migrate compatible brownfield use
    - Create internal/agentfacts as a pure trim/case-fold/exact-prefix/token matcher over stable client identity strings.
    - RED-test high-confidence families and ambiguous generic SDK values; no regex/fuzzy/prompt matching.
    - Reuse it from codexclientcompat agent-string matching where semantics are identical while leaving feature-specific prompt signatures in codexclientcompat.
    - Do not couple the external Codex connector module to root-internal matcher code if that breaks connector isolation; document/characterize that boundary instead.
    - _Requirements: 3.1-3.2,3.10,11.3-11.5,12.1-12.3_
    - _Boundary: new internal/agentfacts + internal/plugins/features/codexclientcompat agent-string paths_
    - _Depends: 1.2_
    - _Validation: shared matcher matrix + unchanged codexclientcompat behavior fixtures_

  - [x] 3.2 Implement feature-owned config and local heuristic rules
    - Decode canonical plugins.features payload for session-classification; outer Registration.Enabled remains authoritative.
    - Support modes heuristic, jev, hybrid with heuristic as omitted-mode default; define bounded ignored_user_agent_prefixes and remote config shape.
    - RED-test Rule A known client identity; Rule B read/search + edit/remove + OS-command cluster; Rule C weaker read/search + mutation plus recognized project marker.
    - RED-test filename/code-fence/language/model/one-tool/web/generic-SDK-UA negatives and exclusions.
    - Keep recognized project/build markers bounded and ignore generic .git as decisive corroboration.
    - _Requirements: 3.1-3.8,6.1-6.4,8.1-8.4,11.1-11.3_
    - _Boundary: internal/plugins/features/sessionclassification config/heuristic package_
    - _Depends: 2.1,3.1_
    - _Validation: config table tests + local policy acceptance matrix_

  - [x] 3.3 Harden evidence bounds and prove zero transcript/path retention
    - Add property/fuzz tests for oversized UA/exclusion/marker values, odd casing, unicode/control input already rejected by identity capture, tool-name explosions, and unknown tool aliases.
    - Assert feature state/evaluation structs contain no messages, prompt strings, tool arguments, raw paths, or raw header maps.
    - Prove weak evidence never accumulates into an unbounded per-session history.
    - _Requirements: 3.5-3.10,7.1-7.5,10.2,10.5,11.5_
    - _Boundary: sessionclassification/agentfacts tests and architecture reflection guards_
    - _Depends: 3.2_
    - _Validation: fuzz/property tests + no-content architecture assertions_

- [x] 4. Build monotonic feature state, durable persistence, and remote leases
  - [x] 4.1 (P) Define the authoritative key/state contract and bounded memory store
    - RED-test secure SessionID preference, A-leg fallback, rejection of empty authority, and refusal to use ClientSessionHint as a key.
    - Implement Record, Promote, ClaimRemote, CompleteRemote semantics with unknown -> coding_agent as the only V1 classification transition.
    - First accepted positive wins; repeats are idempotent; later weak/negative proposals cannot rewrite it.
    - Implement memory store with bounded entry capacity/idle cleanup and no unrelated-session long-held lock.
    - _Requirements: 2.1-2.6,2.9,6.5-6.11,7.3-7.4,10.3-10.7_
    - _Boundary: internal/plugins/features/sessionclassification state contract + featurehost sessionclassification memory adapter_
    - _Depends: 2.1_
    - _Validation: deterministic fake-clock concurrency/store contract tests_

  - [x] 4.2 Implement Bun Load/Promote persistence and restart restoration
    - RED-test SQLite reopen before production SQL.
    - Add the feature-owned logical table with proxy-authority key, scalar classification, remote-control metadata and timestamps only.
    - Implement indexed Load and atomic compare-and-promote; no raw UA/path/prompt/vendor payload columns.
    - Use featurehost-borrowed Bun DB ownership and never close the shared DB from the feature store.
    - _Requirements: 2.1-2.10,7.3-7.4,8.7-8.8,10.6,12.6,12.10_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification Bun store/schema_
    - _Depends: 4.1_
    - _Validation: SQLite store contract + reopen/restart tests_

  - [x] 4.3 Implement atomic remote claim/lease parity on SQLite and PostgreSQL
    - RED-test two concurrent claimers, abandoned lease expiry, finite attempts, backoff, positive classification racing remote completion, and stale lease token rejection.
    - Keep HTTP/network work outside transactions.
    - Register the store in dbparity if required by the current repository persistence catalog and prove equivalent logical schema/behavior on direct PostgreSQL.
    - _Requirements: 2.5,6.5-6.11,10.3-10.6,12.5,12.8,12.10_
    - _Boundary: featurehost sessionclassification Bun store + dbparity contract_
    - _Depends: 4.2_
    - _Validation: memory/SQLite/PostgreSQL shared store contract; race-safe claim tests_

  - [x] 4.4 Add the process cache/coordinator
    - Cache stable positive classifications so later turns require no DB/network work.
    - Coalesce concurrent first loads/promotions for one key while allowing unrelated keys to proceed independently.
    - For durable unknown sessions, preserve authoritative visibility across replicas rather than hiding a remote promotion behind an unbounded negative cache.
    - Enforce capacity/idle eviction; no per-session goroutine/timer.
    - _Requirements: 2.5-2.7,6.5-6.11,10.1-10.7_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification coordinator/cache_
    - _Depends: 4.1,4.3_
    - _Validation: fake-clock cache capacity/eviction + concurrent load/promotion/claim tests + race suite_

- [x] 5. Compose the standard feature across process and immutable generations
  - [x] 5.1 Wire the lightweight process holder and enabled-generation store lifecycle
    - Construct only a lightweight StateHolder/coordinator shell at process featurehost startup; do not ensure a classification table or contact a remote classifier while the feature has never been enabled.
    - For enabled generations, add an overlap-safe feature lifecycle whose Start initializes/ensures the memory or Bun store once through the shared holder before publication; Stop must not destroy shared state used by overlapping generations.
    - Ensure candidate failure publishes no plane/classification records and final process Close owns holder/cache disposal exactly once.
    - Register feature metrics collector through the generic metrics registry without exposing concrete feature types to runtimebundle.
    - _Requirements: 1.1,2.7-2.10,8.5-8.8,9.1-9.6,10.5-10.8,11.4-11.7_
    - _Boundary: internal/standardplugins/featurehost process ownership + sessionclassification child + ordinary feature lifecycle_
    - _Depends: 4.4_
    - _Validation: never-enabled no-schema/no-network test; lifecycle overlap/rollback/constructor-count/close ownership tests; memory-vs-Bun composition tests_

  - [x] 5.2 Bind one generation classifier from feature registration/config
    - Register the session-classification standard feature factory.
    - Decode/validate mode-specific config at candidate generation, build the concrete feature classifier with the process coordinator and optional remote adapter, and contribute it through PlaneSessionClassifier.
    - Prove absent/disabled feature publishes no classifier plane.
    - Prove invalid candidate or duplicate classifier leaves last-good generation/process state untouched.
    - _Requirements: 1.1,6.1-6.4,8.1-8.8,11.7_
    - _Boundary: internal/plugins/features/sessionclassification bundle/config + internal/standardplugins feature registration/featurehost generation_
    - _Depends: 2.2,3.2,5.1_
    - _Validation: standard bundle/config/reload/candidate rollback tests_

  - [x] 5.3 Add bounded feature observability
    - Implement evaluation/transition/remote/store metrics with closed enum labels only.
    - Emit one transition observation for first positive promotion and bounded outcome diagnostics for unknown/excluded/remote failures.
    - Prove no session ID, A-leg, UA, path, prompt or arbitrary remote value can become a metric label/log payload.
    - _Requirements: 7.5,9.1-9.6,10.8_
    - _Boundary: sessionclassification featurehost collector/observer + metrics registration tests_
    - _Depends: 4.4,5.1_
    - _Validation: collector gather tests + label-cardinality/redaction assertions_

- [x] 6. Integrate same-turn classification into canonical execution
  - [x] 6.1 Build canonical Evidence and run classification after secret guard
    - RED-test exact ordering: BeginTurn/A-leg -> secret guard -> session classification -> frontend ingress/request authority -> submit.
    - Derive Evidence from accepted Invocation.ClientUserAgent, Operation and tool-name category bits only; do not scan Messages/Items/Instructions.
    - Pass bound SessionView and WorkspaceView; update ibt.preSession only with validated classification result.
    - Runtime/store/remote classifier errors preserve the request and prior positive state.
    - _Requirements: 1.1-1.7,4.1-4.7,7.1-7.7,11.1-11.8_
    - _Boundary: internal/core/runtime secure preparation + generic extension runner_
    - _Depends: 2.3,3.2,5.2_
    - _Validation: focused canonical preparation tests with spy classifier/order barriers_

  - [x] 6.2 Propagate classification through all later same-turn SessionView consumers
    - Prove submit/tool-catalog/request/pre-request/route-hint and later execctx views receive the classification without independent store/classifier calls.
    - Update view-copy/context/policy projection helpers only where required by the new scalar field.
    - Add a fake opt-in consumer test proving first-turn positive gating is possible while leaving real existing consumers behavior-neutral.
    - _Requirements: 1.6,1.8,4.2,9.5,11.7,12.6_
    - _Boundary: runtime meta construction + pkg/lipsdk/core view projection tests_
    - _Depends: 6.1_
    - _Validation: same-turn metadata matrix; classifier invocation count exactly once per admitted client turn_

  - [x] 6.3 Prove auxiliary and failure isolation
    - Ensure detached proxy-owned auxiliary calls do not create classification rows under their private child A-legs.
    - Cover canceled classification context, state failure, remote failure and invalid result without changing routing/retry/output commitment semantics.
    - Prove no post-commit replay/failover rule changes.
    - _Requirements: 4.4-4.6,7.6-7.7,11.7-11.8_
    - _Boundary: detached/aux/runtime failure tests_
    - _Depends: 6.1_
    - _Validation: detached child state-count assertions + recovery/commit regression suite_

- [x] 7. Preserve classification semantics on the large-payload wire path
  - [x] 7.1 Add bounded classification evidence to large-body proof/session facts
    - RED-test proof validation/aggregate-fact accounting before adding fields.
    - Carry sessionclassification.Evidence only; no raw header map/tool list/Call.
    - Extend no-smuggling/reflection tests and semantic-fact bounds.
    - _Requirements: 5.2-5.5,7.1-7.4,12.7_
    - _Boundary: internal/core/largebody Proof/WireSessionFacts + SDK evidence type_
    - _Depends: 1.3,2.1_
    - _Validation: proof Validate/AggregateFactBytes + no-shadow-Call/no-unbounded-field tests_

  - [x] 7.2 (P) Compile canonical-equivalent evidence in certified frontend profiles
    - Reuse the same User-Agent acceptance policy through the frontend identity helper.
    - While profiles already scan/validate tool definitions, feed names into the shared fixed-bit accumulator without retaining ToolDef lists in proof.
    - Cover OpenAI Responses, OpenAI Chat and OpenResponses certified lanes that expose tools/UA.
    - RED-test field order, absent UA, invalid UA, unknown tools and large streamed bodies.
    - _Requirements: 3.9,5.2-5.5,11.1-11.2,12.7_
    - _Boundary: certified frontend profile proof compilers + frontendpipe differential fixtures_
    - _Depends: 7.1_
    - _Validation: canonical Evidence versus wire Proof Evidence bit-for-bit differential tests_

  - [x] 7.3 Execute the metadata-only classifier after wire authority binding
    - Insert the same generic classification stage into ExecuteLargeBody after secure-session/A-leg/workspace binding and before route/request consumers.
    - Use proof Evidence, never reconstruct lipapi.Call.
    - Unknown/error continues the one committed wire execution; no post-commit canonical fallback.
    - Extend the fixed plane census/count/order for PlaneSessionClassifier and prove occupied MetadataOnly classifier does not set a canonical static blocker.
    - _Requirements: 4.1-4.7,5.1-5.7,10.1-10.2,12.7,12.11_
    - _Boundary: internal/core/runtime ExecuteLargeBody + internal/core/largebody eligibility/authority gates_
    - _Depends: 2.2,2.3,5.2,7.2_
    - _Validation: real wire executor classification tests + static disposition + canonical/wire outcome parity_

- [x] 8. Implement the optional Jev adapter behind the remote-decision contract
  - [x] 8.1 Define and test the provider-neutral RemoteDecider boundary
    - Keep RemoteInput derived/content-free: normalized client family/ambiguity, tool bits, workspace class, operation and local evidence outcome.
    - Define bounded RemoteDecision probability/confidence fields used by feature threshold policy.
    - Validate remote mode requires explicit provider, credential reference, timeout, attempt/lease/backoff values and positive threshold within safe finite bounds.
    - _Requirements: 6.1-6.4,6.7-6.10,7.1-7.7,8.3-8.4_
    - _Boundary: internal/plugins/features/sessionclassification remote contract/config_
    - _Depends: 3.2,4.4_
    - _Validation: config/DTO bounds tests; architecture assertion that vendor DTOs cannot cross this boundary_

  - [x] 8.2 Implement the current TypeSafe/Jev HTTP adapter without widening SDK contracts
    - Reconfirm current early-access TypeSafe documentation immediately before coding; map current endpoint/auth/request/response into RemoteDecider.
    - Use strict request/response size bounds, timeout/cancellation, safe redirect/origin behavior and credential redaction.
    - Never send raw UA, headers, prompt/transcript, reasoning, tool args, paths, resume tokens, provider credentials or secret findings.
    - Keep vendor-specific types under the featurehost-owned adapter package.
    - _Requirements: 6.6-6.11,7.1-7.5,11.5,12.8-12.9_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification Jev adapter_
    - _Depends: 8.1_
    - _Validation: httptest success/429/5xx/timeout/redirect/malformed/oversize/cancel/redaction matrix_

  - [x] 8.3 Integrate heuristic/jev/hybrid decision flow with durable leases
    - heuristic never constructs/calls RemoteDecider.
    - jev constructs local evidence but requires remote positive result to promote.
    - hybrid promotes on decisive local evidence first and claims remote only while still unknown.
    - Claim before network, finish after network, never hold store transaction/lock across HTTP.
    - Prove positive cached sessions issue zero remote calls and concurrent ambiguous turns have one active lease in the store scope.
    - _Requirements: 6.1-6.11,10.1,10.3-10.7,12.5,12.8_
    - _Boundary: concrete sessionclassification classifier + state coordinator/remote adapter_
    - _Depends: 5.2,8.2_
    - _Validation: fake decider call-count + concurrent/multi-store lease acceptance suite_

- [x] 9. Certify persistence, reload, concurrency, and performance behavior
  - [x] 9.1 Prove durable resume and generation reload semantics
    - New session -> promote -> close/reopen durable store -> resume authoritative SessionID -> classification available before downstream consumer.
    - Reload heuristic/remote rules without erasing process state; disable withdraws plane without deleting durable row; re-enable restores.
    - Verify in-flight request remains bound to old generation policy.
    - _Requirements: 2.4-2.10,8.5-8.8,12.6_
    - _Boundary: featurehost/runtimebundle composed tests with SQLite and generation reload_
    - _Depends: 5.2,6.2,7.3_
    - _Validation: restart/reload composed acceptance tests_

  - [x] 9.2 (P) Prove authority isolation and concurrent promotion
    - Same ClientSessionHint under different authoritative sessions/A-legs must never share rows/cache.
    - Concurrent local and remote positives converge to one stored coding_agent revision without source/evidence flapping.
    - Route/failover/compaction/backend switches preserve the same authoritative classification.
    - _Requirements: 2.1-2.6,2.9,12.4-12.6_
    - _Boundary: store/coordinator + runtime lineage tests_
    - _Depends: 4.3,6.1_
    - _Validation: concurrency barriers + race detector where supported_

  - [x] 9.3 (P) Add hot-path allocation/latency and store-call ratchets
    - Warm coding_agent benchmark asserts zero remote calls, zero DB writes and zero transcript scan; count durable store calls explicitly.
    - Unknown local path benchmark covers bounded UA/tool/marker evaluation.
    - Remote benchmark/fake latency proves no lock/transaction held during decision I/O and unrelated session progress.
    - Verify classifier-disabled path performs zero classifier/store/remote work.
    - _Requirements: 10.1-10.8,5.1,11.7_
    - _Boundary: feature/runtime benchmarks and instrumented store/decider fakes_
    - _Depends: 4.4,6.1,7.3,8.3_
    - _Validation: focused benchmarks + allocation/store-call assertions + make test-cost if harness cost changes_

- [x] 10. Run the cross-harness and false-positive acceptance matrix
  - [x] 10.1 Certify supported coding-agent positives
    - Run the evidence matrix from Task 1.2 through canonical and, where applicable, wire paths.
    - Cover stable identity positives and tool-structure positives independently so generic/absent UA coding harnesses remain classifiable.
    - Record evidence source per fixture; do not invent identity for indistinguishable harnesses.
    - _Requirements: 3.1-3.4,12.1,12.3,12.7_
    - _Boundary: feature/runtime/testkit acceptance fixtures_
    - _Depends: 6.2,7.3,8.3_
    - _Validation: data-driven cross-harness acceptance suite_

  - [x] 10.2 Certify technical-chat and ambiguous-client negatives
    - Cover programming prose, source filenames, code fences, one shell/web/read tool, model names, generic browser/API SDK UAs, ignored identity prefixes and weak tool clusters without project marker.
    - Prove unknown stays generic behavior and creates no durable negative classification.
    - _Requirements: 1.2,3.2,3.4-3.8,6.8-6.9,12.2-12.3_
    - _Boundary: feature/runtime negative fixtures_
    - _Depends: 6.2,7.3,8.3_
    - _Validation: false-positive matrix; persisted kind remains unknown/absent_

  - [x] 10.3 Prove consumer neutrality and opt-in capability
    - Use a test-only downstream consumer to gate on SessionView.Classification and prove first-turn visibility.
    - Assert existing bundled coding-oriented features have no new dependency/gate and preserve pre-feature behavior when #645 lands.
    - _Requirements: 1.6-1.8,4.2,11.7_
    - _Boundary: integration/architecture tests only; no production consumer migrations_
    - _Depends: 6.2_
    - _Validation: bundled feature behavior characterization + test-only opt-in consumer_

- [x] 11. Add operator documentation and safe diagnostics guidance
  - [x] 11.1 Document classification semantics, evidence, configuration and privacy
    - Add operator docs for unknown/coding_agent monotonic semantics, authoritative keying, local rules, Jev modes, fail-open behavior, durable/non-durable posture, reload behavior and large-body compatibility.
    - Provide canonical plugins.features examples for heuristic and explicit remote modes; do not publish real credentials.
    - State clearly that classification is not authorization and that existing features do not become gated automatically.
    - _Requirements: 1.1-1.8,6.1-6.11,7.1-7.7,8.1-8.8,11.7_
    - _Boundary: docs/session-classification.md plus config/example documentation_
    - _Depends: 5.2,8.3_
    - _Validation: documentation/config example parsing tests where supported_

  - [x] 11.2 Document observability and future consumer contract
    - Describe bounded metrics/diagnostics and evidence-code semantics without raw identity/path examples that encourage high-cardinality logging.
    - Document how a future feature checks SessionView.Classification.IsCodingAgent rather than reimplementing detection.
    - Link #456 as a future explanation consumer without making it a prerequisite.
    - _Requirements: 1.6,9.1-9.6,11.3-11.7_
    - _Boundary: operator/plugin authoring docs_
    - _Depends: 5.3,6.2_
    - _Validation: doc review against actual metric/config names_

- [x] 12. Run final architecture, parity, and release-grade certification
  - [x] 12.1 Run focused feature/platform gates and generated-surface checks
    - Run feature/session/runtime/frontends/largebody focused suites, generator checks, architecture guards, fuzz/property targets and parity checks.
    - Prove the plane count/order/access manifest and generated outputs are current.
    - Re-run grep/arch assertions for no concrete coding-client or TypeSafe branching in generic core.
    - _Requirements: 5.6,11.4-11.8,12.1-12.9,12.11_
    - _Boundary: repository-wide certification only_
    - _Depends: 9.1,9.2,9.3,10.1,10.2,10.3,11.1,11.2_
    - _Validation: make quality-checks; make parity-checks; focused fuzz targets_

  - [x] 12.2 Run persistence/concurrency/test-cost gates
    - Run make test-db-parity including direct PostgreSQL mandatory semantics when the gate requires it.
    - Run applicable race tests for store/coordinator/remote lease paths.
    - Run make test-cost before accepting any material default-suite cost increase; do not raise budgets as a normal escape hatch.
    - Record environment-gated failures truthfully and reproduce baseline attribution where necessary.
    - _Requirements: 10.3-10.8,12.5,12.8-12.10,12.12_
    - _Boundary: repository QA/persistence/concurrency gates_
    - _Depends: 12.1_
    - _Validation: make test-db-parity; make test-race where supported; make test-cost_

  - [x] 12.3 Run wide QA and close out the SDD truthfully
    - Run make qa and any domain-specific Windows/large-body checks required by implementation-time main.
    - Verify no implementation task added consumer gating outside #645 scope.
    - Update spec metadata/tasks only after all implementation acceptance evidence passes; archive the SDD according to repository convention after merged-main closeout.
    - _Requirements: 1-12_
    - _Boundary: final merged-main certification/spec lifecycle_
    - _Depends: 12.2_
    - _Validation: make qa plus merged-main focused rerun; no unchecked applicable tasks before archive_

## Implementation Notes
- Task 4.2 requires immediate DB-parity catalog registration, a real feature-owned baseline migration, and SQLite/direct-PostgreSQL Load/Promote schema contracts: the mandatory architecture gate discovers the new dialect-sensitive package before Task 4.3. Task 4.3 retains Bun remote claim/completion and full remote-contract parity; no contract or gate is relaxed.

- General architecture LOC ceilings use maintainer-authorized fixed measurements with substantial headroom; file and tree audit tests retain exact ceiling and excess checks.

- Classification keying must use AuthoritativeSessionID or proxy-owned ALegID explicitly; generic SessionView.PartitionKey still permits ClientSessionHint fallback.

- The shared evidence matrix validates local source paths; update its active-spec references when Task 12.3 archives this SDD (and preserve explicit-completion source references if that spec relocates).

- Task 1.3 baseline absence assertions must evolve in Tasks 7.1/7.2 when bounded wire evidence lands. OpenResponses canonical User-Agent capture currently uses TrimSpace directly; use the shared acceptance helper for canonical/wire parity in Task 7.2.

- Task 4.3 uses one conservative precision rule across memory, SQLite, and PostgreSQL: lease and completion-based backoff deadlines round upward to the next microsecond, preserving aligned deadlines. Strict before/exact-deadline checks use the returned deadline; no adapter-specific tolerance or early expiry is permitted.

- Task 4.4 coalesced waiters preserve their own cancellation, then consult an available valid positive before propagating an owner storage error. Owner cancellation is tracked separately because Bun adapters return bounded store errors for mid-I/O cancellation.

- Task 5.1 initializes classification state only in enabled-generation lifecycle Start. The process owns two closers (terminal policy and the lightweight classification holder); generation Stop retains shared state. Initialization tracks owner cancellation separately so live waiters retry even when Bun returns a bounded schema error.

- Task 5.2 publishes the generation classifier through the exclusive PlaneSessionClassifier against the process-owned StateHolder; the remote decider is deliberately unwired so jev/hybrid fail open to unknown with zero egress until 8.3. TestClassifierHoldsNoNetworkCapableDependency pins the classifier to three fields and must be relaxed deliberately when 8.3 adds a decider. Add "sessionclassification" to archForbiddenFeatureTokens in 12.1 before any guarded aggregate gains an SDK-classifier field. Task 5.2 adds the classifier plane; evolve the lifecycle-only candidate assertions while preserving rollback and publication isolation.

- Task 5.3 gates the transition observation on first-positive OWNERSHIP (the store's promoted bool), not record positivity: a coalesced-flight waiter receives the winner's positive record with promoted=false and must record restored with no transition. TestClassifierHoldsNoNetworkCapableDependency permits 4 fields (cfg, state, now, observer) and TestPrometheusCollectorEmitsOneTransitionForConcurrentFirstPositive must stay non-flaky via the load-count handshake, not sleeps. Add a family-to-evidence-code totality test so ExcludedIdentity cannot misattribute for a future agentfacts family.

- Task 6.1 inserts a void runSessionClassificationStage call at executor_prepare_secure.go:548, after the runSecretGuardStage block and before captureFrontendIngressBeforeSubmit. Generic core reaches the plane only through the thin RequestRuntimeSnapshot.SessionClassifier delegate registered in plane_rules_tables AllowedStageConsumers. The evidence builder re-applies identity.AcceptClientUserAgent because internal/plugins/frontends/openresponses/decode.go:204,577 assign ClientUserAgent with a bare strings.TrimSpace and bypass canonical acceptance; openresponses needs its own repair ticket. The AST no-content-traversal guard is file-scoped, so keep the behavioral sentinel test as the load-beari

- Task 6.2 real gap was only session_start_view: emitSessionStartIfNeeded runs inside prepareSubmitAndALegSecure before projectContext re-derives views from preSession, so execctx/views.go mirrored an unclassified view into the SDK session key. Fix belongs in ViewsFromSecureSubmit, not the emit site. projectContext does a whole-struct views.Session = t.preSession and needs no change. TestSessionClassificationFieldHasNoExistingConsumer scans only runtime/execctx/pkg-lipsdk; internal/core/extensions/session_classification.go is the sanctioned generic-core seam that legitimately reads the field and invokes the classifier, and a repo-wide guard belongs to 12.1.

- Task 6.3 is tests-only. TestSameTurnOptInConsumerGatesOnFirstTurnPositive is a consumer-side proof of 4.2 that 6.1 already satisfied, not coverage of the 6.2 plumbing.

- Task 7.1 carries exactly one sessionclassification.Evidence field on Proof and WireSessionFacts; the largebody no-smuggling guard must reject unbounded storage at ANY depth (a []string nested one struct level inside Proof passes a top-level-only rule), and its container-exception ratchet must cover BOTH carriers. Every piece of 7.1 production code needs a test that fails when it is removed: aggregate-fact charging, both Proof.Validate branches, the WireSessionFacts.Validate branch, the WireTurnFacts operation check, and proof-to-wire propagation. Do not assert propagation by comparing two zero carriers before 7.2 populates them. Detached isolation must be proven by a state-count census that counts ACCEPTED rows (and deliberately over-counts no-op promotions), not a snapshot; injecting the stage into executor_prepare_detached.go fails all five detached tests. A canceled classification context cannot be driven through prepareRequest (secure-session resume fails first), so drive runSessionClassificationStage directly and discriminate with a live-context control. An established positive short-circuits before the cancellation guard, so a test combining both proves preservation, not cancellation. Each failing classifier in the persisted-positive matrix gets its OWN empty probe, so turn 2 always projects unknown; the load-bearing invariants are the byte-identical durable row and a weak-evidence healthy turn restoring it.

- Task 7.2 shares one acceptance policy via frontendpipe.CompileClassificationEvidence, which calls identitywire.CaptureClientUserAgent (itself delegating to identity.AcceptClientUserAgent) plus the shared ToolCategorySet.AddToolName accumulator; profiles read only tool.Name and never retain the ToolDef slice or header map. The differential fixture cannot import the unexported canonical builder (import cycle: frontendpipe -> stdhttp/contract -> core/runtime), so it mirrors it and pins the derivation inputs in source; canonical OUTPUT semantics are owned by internal/core/runtime's executor_session_classification tests. Always include a whitespace-padded User-Agent row: that is the only input class where openresponses' bare strings.TrimSpace capture is not trivially identical, and it pins the AcceptClientUserAgent idempotence the openresponses decoder deferral rests on.

- Task 7.3 inserts a second void stage runWireSessionClassificationStage in ExecuteLargeBody after prep.BindSession and before the route/ingress/authority consumers, calling the SAME extensions.RunSessionClassificationStage as the canonical lane. It imports no lipapi at all, which is the structural proof it cannot reconstruct a Call. The wire lane DOES resolve workspace (PrepareSecureSession runs snap.Workspace().Resolve for both lanes), so pass prep.Workspace(), never a zero view. The validated projection is deliberately discarded: every requirement-4.2 consumer is a canonical-required plane that statically blocks the wire lane, so boundSession.Classification has no reader. Its wireClassificationExecutor test helper MUST install a RequestCoordinator, or admitRequestAuthorityOnce never sets requestAuthorityFrom and the afterAuthority ordering assertion is a permanent tautology.
- Task 8.1 freezes the port as Decide(ctx, RemoteInput) (RemoteDecision, error) with RemoteDecision{CodingProbability, Confidence float64} and Positive(threshold) as the entire threshold policy; RemoteInput fields must be CLOSED named types, not strings, so an out-of-vocabulary value has no representation in the port. RemoteDecision cannot represent a negative (only the zero value is non-positive), which is what makes 6.9 monotonicity structural.
- Task 8.2 pins the LIVE vendor schema (fetched from https://docs.typesafe.ai/api at coding time, per the task's reconfirm-first rule): POST https://api.typesafe.ai/v1/systemone, Bearer auth, body {state, model:"jev-latest", questions:{<id>:{type:"noul",instructions}}}, response {model, answers, usage}. The adapter sends ONE noul question with an adapter-owned id the model never sees, and state carries exactly the six derived RemoteInput members as a structured object (the vendor docs explicitly support structured state).
- Task 8.3 wires the adapter by DEPENDENCY INJECTION, not a delegate: ClassifierDeps.Remote is the frozen 8.1 RemoteDecider port, filled by bindSessionClassifier from hostclassification.NewRemoteDecider(cfg), which returns (nil, nil) for heuristic BEFORE touching the adapter. The feature package never imports the adapter package, so there is no import cycle and generic core stays untouched.
- Task 9.1 certification tests live in the INTERNAL featurehost package (not featurehost_test) because the reload-must-not-erase-process-state claim is only observable as coordinator pointer identity through the unexported rt.sessionClassification field; process_lifecycle_internal_test.go sets the precedent. The reload test must stop EVERY live generation (baseline, heuristic, remote) before asserting, so the owner count actually reaches zero - otherwise a coordinator disposed only on the LAST Release slips past the composed layer entirely and is caught only by the lower-level holder unit test.
- classification_revision is NOT a per-promotion counter: both stores unconditionally rewrite proposal.Revision = 1 and only the FIRST accepted proposal becomes the stored positive (design.md:557 'normally moves from revision 0 to revision 1 once'). Requirement 2.6 requires replay NOT to advance it. Do not 'fix' this into a monotonic counter.
- Allocation ratchets MUST use fixtures whose normalization actually allocates. strings.ToLower has an allocation-free fast path for already-lowercase ASCII, so an all-lowercase padded sentinel let a size-proportional transcript scan allocate LESS than the small fixture and the size-invariance ratchet passed on a scanning implementation. Use mixed-case fixtures (uppercase on even indices) everywhere an allocation bound is the instrument, and say why in a comment.
- A row census alone CANNOT see a fixture whose declared evidence was never delivered. Task 10.1 shipped 28 rows where technical_prose/source_filename/code_fence/programming_language_name/model_name/prompt_compaction_marker all ran with the SAME generic 'summarize the diff' prompt, so 28 rows carried only 20 distinct signatures and 6 negatives certified nothing while logging PASS. Deliver fixture.WeakPrompt/ModelName on the turn, and add an evidence-aware census that rejects two distinct matrix rows delivered byte-identical evidence (build the signature from the ACTUAL delivered prompt via a single shared selector, never from the matrix declaration, or a delivery regression is invisible).
- EXCLUSION IS SCOPED TO RULE A ONLY (design.md:495 'exclusion is checked before Rule A'; requirements.md:3.7 says it 'may prevent a prospective local match'). EvaluateLocal guards only the identity branch with isIgnoredUserAgent; Rules B/C are deliberately unguarded. So an ignored-prefixed UA PLUS a distinctive tool cluster is POSITIVE on tooling.distinct_coding_cluster - do not 'fix' this. It is pinned in both directions: removing the identity guard fails 4 exclusion rows, widening it to the whole heuristic fails TestIgnoredIdentityPrefixWithdrawsOnlyTheIdentityRule.
- The consumer-neutrality allowlist is directory-scoped, NOT prefix-scoped: a directory role matches only that directory's OWN files, so a NEW subpackage (e.g. internal/standardplugins/featurehost/sessionclassification/billing) earns NO role until enumerated. A prefix match would silently sanction exactly where a classification-reading authorization/billing path would be added, and the 1.7 advisory sweep (keyed on its own 20 authority dirs) would not cover it. Pinned by TestNeutralityRoleAllowlistDoesNotSanctionNewSubpackages, which also asserts the own-directory files keep their roles.
- Operator docs must be machine-pinned to source, and two independent Contains calls are NOT enough: a bounds assertion must require the value ATTACHED to its constant (regex `CONST`[^|]*\|[^|]*VALUE), or `MaxWorkspaceMarkers | 128` passes while the real 32 appears elsewhere. Likewise document the closed RemoteOutcome vocabulary (positive, below_threshold, timeout, rate_limited, server_error, network_error, malformed, lease_busy, budget_exhausted, skipped) and NOT the adapter-internal JevFailure kinds (server_unavailable, malformed_response, transport_failure) - the adapter maps those away before any observation is emitted, so an operator matching them against metrics searches for values that never exist.
- Metric label vocabularies must be DERIVED FROM THE SHIPPED SOURCE, never hand-written in the test. Hand-writing `source`/`confidence` made a widened SDK vocabulary (adding ConfidenceMedium) pass every doc test while the shipped operator guide went false - a silent pass in the exact guard meant to catch stale docs. Derive them by enumerating the constants of the named string type in pkg/lipsdk/session via go/packages; exclude the all-zero sentinel (ConfidenceUnknown = "") because an empty string is the absence of a band, never an observable label value.
- 12.1 discharges BOTH obligations 10.3 deferred: archForbiddenFeatureTokens now contains "sessionclassification" (precondition re-verified: none of the 9 guarded aggregates carries an SDK-classifier field, so the token is INERT today) and a repo-wide generic-core branch sweep exists. Prove both directions: TestGenericAggregatesContainNoPerFeatureFields must still pass, and TestSessionClassificationTokenRejectsGuardedAggregateFields must fail if the token is removed - otherwise it is dead text.
- 12.2 GATE RECORD (measured, not asserted): make test-db-parity -> `dbparity/cmd all` exit 0, 22 packages ok, with LIP_REQUIRE_POSTGRES=1 and DSN postgres://lip@127.0.0.1:55432/lip_test?sslmode=disable; direct PostgreSQL genuinely RAN (`dbparity/cmd postgres-direct` exit 0, incl. featurehost/sessionclassification 0.158s). Race: -race -count=3 on featurehost/sessionclassification ok; 40 lease/concurrency/no-lock subtests pass under -race (requirement 10.3). bash scripts/race-check.sh ran 356 packages ok with ZERO failures before reaching the SEPARATE billing race scan, which exceeded its 60m package timeout - that is the known pre-existing main defect (internal/core/billing consumes ~90% of its budget even idle; the repo has BILLING_SCHEMA_TIMEOUT ?= 30m at Makefile:199 but qa-tests does not use it). NOT caused by #645 and NOT fixed here; the stashed Makefile fix addresses it and stays stashed.
- 12.3 GATE RECORD: make qa components all exit 0 - quality-checks-fast (All Quality Checks Passed), lint (0 issues, all modules), vuln, backend-plugin-release-gates-static, test-openresponses-compliance-static. The tagged delta (`-tags=precommit,integration` over internal/qa, internal/core/runtime, internal/stdhttp, internal/testkit/conformance, tools/backendplugin) exit 0, 21 packages ok. internal/core/billing was EXCLUDED from that delta run and is the known pre-existing timeout (12.2 record). Requirement 1.8 re-verified at closeout: 40 production files can observe classification, all 8 sanctioned roles live, 0 rejected; 20 authority-owning packages scanned, none references it; 0 bundled coding-oriented features reference it.
- SDD closed per repo convention: spec.json -> phase "completed", completed true, ready_for_implementation false, and the directory is MOVED to .kiro/specs/archive/. I first assumed the archive move belonged after merged-main and left the spec active - the repo's own internal/qa TestQAFastPreflight_KiroLifecycle rejects that outright ("completed or superseded specs must be archived"), so the lifecycle convention couples completed with archived and both land together.
- Archiving moved 10 local source references in the frozen evidence matrix (testdata/evidence_matrix.json) from .kiro/specs/coding-agent-session-classification/ to .kiro/specs/archive/coding-agent-session-classification/, exactly as task 1.2's note instructed ("update its active-spec references when Task 12.3 archives this SDD"). TestEvidenceMatrixIntegrity enforces those references resolve, so leaving them would have broken the suite at the archive commit. NO fixture expectation, evidence row or source ID changed - only the path prefix.
- test-cost measurement (make test-cost is Windows-only, fails closed on POSIX by design): archtest is the only materially-affected package. Measured warm - #645 branch 44.3/39.6/22.0s (mean 35.3s); anchor bb1ef962 39.8/48.1/28.3/30.2s (mean 36.6s). Ranges overlap almost entirely and the means differ by ~1s in the branch's favour, so #645 is NOT a measurable default-suite cost increase; run-to-run variance on the shared 8-CPU box (22-48s for identical code) dominates. Budget scripts/test-cost-budget.json allows existing_delta_seconds 3 with a 15s floor, and archtest has its own override (ratio 1.5, delta 5s). NO budget was raised. Other touched packages: featurehost 1.7s, featurehost/sessionclassification 1.1s, features/sessionclassification 0.3s, frontendpipe 13.1s, largebody 2.9s.
- Requirement 11.4/11.5 needed a BRANCH detector, not a name grep: sweep internal/core/** + pkg/** and report a concrete family or the TypeSafe/Jev vendor only in a branching position (case clause, ==/!= operand, composite-literal key, or a string arg to strings.Contains/HasPrefix/HasSuffix/EqualFold). A plain literal is NOT a branch - requirement 11.1 requires generic core to consume an accepted opaque User-Agent. pkg/lipsdk/sessionclassification and internal/agentfacts legitimately name families and are an exact single-entry exemption.
- The five #645 fuzz targets in internal/plugins/features/sessionclassification are NOT registered in scripts/fuzz-targets.tsv, so `make test-fuzz` (65 Tier-1 targets) does not exercise them. They pass when run directly. The TSV feeds the Windows CI contract, so registration is a deliberate 12.2/12.3 decision, not an oversight.
- The neutrality sweep resolves TYPES, not names. internal/plugins/features/reasoningpreservation declares its own unrelated Classification type and internal/infra/metering/journalstore has SQLiteRetryEvent.Classification, so a \bClassification\b name grep flags 11 shipped files the census correctly ignores - and renaming defeats it. Residual limitation, documented in the file header: a string-keyed reflection read (reflect.ValueOf(view).FieldByName("Classification")) names no field or package and is invisible to the sweep. No shipped code does this.
- archForbiddenFeatureTokens deliberately does NOT yet contain "sessionclassification": no guarded aggregate (ProcessServices, ExecutorConfig, BuildOptions, ExtensionsOptions, Generation, ...) has an SDK-classifier field, so adding it today would be provably inert. 12.1 owns that change.
- Technical-chat signals (prose, fences, filenames, language names, model names, compaction markers) are STRUCTURALLY UNREADABLE: sdkclassification.Input/Evidence carry only TraceID/Session/Workspace/Evidence and Evidence has exactly 3 bounded fields, with the SDK contract stating no Call/transcript/tool-defs/headers/route is included (requirements 4.6, 3.8, 5.5; design.md:481 forbids prompt parsing in V1). A reflection census enforces this. Consequence for negative fixtures: they certify that a turn CARRYING these signals is classified from bounded metadata only and stays unknown, while the same prompt delivered byte-identically plus ONE independent decisive rule becomes positive. That pairing IS requirement 12.2 - do not claim a fence was parsed and rejected.
- 12.2 pairing quality bar: same authoritative session, byte-identical prompt asserted on the RUNTIME capture, and only the decisive rule added. The sharpest form adds NO tools (marker-only), so the tool set is identical and the sole delta is the project marker - structurally incapable of being trivially satisfied.
- A remote decider CANNOT be injected at the composed featurehost layer: bindSessionClassifier has no decider seam and GenerationInput exposes only NowFn, so the decider comes from hostclassification.NewRemoteDecider(cfg) -> NewJevDecider. Remote negative certification therefore lives in featurehost/sessionclassification (inside boundary) using the real store/coordinator/lease/classifier with only the vendor transport scripted.
- Detecting a delivered-evidence collapse requires building the census from the RUNTIME-OBSERVED signature (submit-hook capture), not from the fixture declaration. A census computed from the declaration cannot see a delivery regression - that exact mistake survived one round in task 10.1.
- The matrix's modelName cannot be delivered as a route/model field: the SDK classifier Input is deliberately model-free (requirement 3.4). The frozen weakPrompt already carries the model name in prose ('Would gpt-5-codex be suitable...'), so delivering weakPrompt satisfies the row.
- The canonical Input has NO model, route, or workspace-root field (only TraceID/Session/Workspace/Evidence), so a 'structural half' claim must reflect over the real SDK type. Declaring a LOCAL struct that mirrors it is a tautology.
- go test -overlay DOES reach //go:embed content (resolved at compile time); only runtime os.ReadFile tree scans - i.e. internal/archtest - are overlay-blind. A 'cannot overlay the embedded JSON' caveat is wrong and would push you to a weaker instrument.
- A SQL-verb census that only counts SELECT/INSERT/UPDATE/DELETE silently reads a CTE form (WITH ... INSERT) as zero work. Record any classification-table statement whose verb is not counted into an `unclassified` ledger and assert it is empty, so a future dialect change fails loudly instead of weakening the zero-write result.
- Two warm-cache ratchets are layered and BOTH are needed: one pre-sets the projection so it short-circuits at the classifier seam, the other leaves the classification empty so it falls through to store.Load -> coordinator.load -> cachedPositiveLocked. Disabling only the coordinator Load short-circuit does NOT fire the classifier-seam test, and vice versa.
- measurement traps in hot-path work: (a) a reused *lipapi.Call across Execute calls is NOT a client turn (the executor mutates it), so later reuses resolve against a different authoritative session and legitimately read the store - build a fresh call per turn; (b) runtime.NumGoroutine is unusable at the composed layer because database/sql churns pool goroutines, so use a production-frame stack census attributed by spawning frame (it catches a leak whose own stack carries no classification frame).
- make test-cost is Windows-only and fails closed on POSIX by design. 9.3 added ~1.06s across four packages, inside the scripts/test-cost-budget.json budget (existing_delta_seconds 3,15s floor; internal/core/runtime has a 45s override).
- Convergence must be proven by CENSUSING EVERY RECORD THE STORE RETURNED TO ANY CALLER, not by checking final state: a last-write-wins store that ends at exactly one positive can still hand two callers two different snapshots, and only the census catches it.
- BunStore.CompleteRemote's AND kind = '' guard is UNREACHABLE through the Store API - claimRemoteSQL requires kind = '' and promoteSQL clears the lease token, so the token guard always rejects first. Verified by exhaustively enumerating 6250 op-orderings: the positive-and-leased row was reached 0 times. KEEP the guard as multi-writer table-boundary defense, and pin it with a raw-SQL construction because no API sequence can reach it. Without 9.2 that guard (and the neutral token guard) SURVIVED the entire pre-existing suite.
- A concurrency test that releases N positives and 1 negative from one barrier lets the positives win every time and measures nothing. The below-threshold negative must complete FIRST (genuinely accepted, writing into the row) and only then does the positive race start - deterministic, and it makes the negative's acceptance a fact rather than a coin flip. Reusing an exhausted attempt budget across phases silently makes phase 2 vacuous.
- Never claim a 'structural' or 'compile-time' proof using a LOCAL struct that mirrors a real type: it is a tautology, because adding a field to the real type changes nothing. Reflect over the real SDK type instead (TestLineageEvidenceCarriesNoRouteState).
- runRemote is claim -> Decide -> CompleteRemote as three SEPARATE store calls with the network between them. Coordinator.claimRemote releases c.mu before delegating and the remote pair stays out of the flight-coalescing path used by Load/Promote; MemoryStore drops its mutex around the nonce and re-checks on reacquire; BunStore uses single-statement NewRaw().Scan() with no RunInTx anywhere in the package. Lease TTL > timeout + RemoteLeaseSafetyMargin is validated ONCE in RemoteConfig.validate and deliberately not re-checked by the coordinator, so there is no second copy of that rule to drift.
- RetryBackoff and MaxAttemptsPerSession are now consumed by runRemote's loop (waitRemoteBackoff plus the loop bound), discharging the obligation 8.2 left open; the counter is STORE-BACKED and durable, so 17 turns and 12 concurrent claimants still observe exactly the budget in egress calls AND stored attempts. A retry fixture MUST use the process clock, not a fixed logical clock, because the store anchors backoff at the completion timestamp and a fixed clock can never release the deadline.
- Caller cancellation returns the cancellation to the caller and deliberately LEAVES THE LEASE TO EXPIRE instead of completing it with a canceled context (6.11 makes expiry the safe path for an abandoned lease). This is not a capacity leak: the attempt is already consumed pre-network, so reclamation is bounded by MaxAttemptsPerSession.
- Every bounded remote failure returns unknown with err == nil AND a completed lease; the sole non-fail-open remote outcome is a durable-state failure, which is wrapped in ErrStateUnavailable so a caller can tell 'the vendor said no' from 'we could not record the answer'. remoteProposal returns the zero proposal for anything not both valid and above threshold, and store-level guards forbid downgrading an existing positive, so no remote path can write a negative.
- 8.3 required a TEST-ONLY boundary exception: internal/core/runtime/executor_session_classification_{detached,failure}_test.go. Both changes are forced by the task's own criteria (a remote-capable classifier must now supply a decider, and the old test asserted 'no remote claim without a wired decider', which 8.3 makes false by design) and both strengthen assertions.
- CRITICAL vendor quirk: a noul answer carries NO confidence field - only Choice and Score answers do. So CodingProbability = noul and Confidence stays zero. Do NOT synthesize |2p-1| from the vendor's optional convenience formula: it is monotone in p, so any confidence gate on it would algebraically become p >= (1+c)/2, silently doubling the effective threshold and violating 6.8's "the configured positive_threshold alone decides promotion". Requirement 6.8 makes probability the only threshold input, so the zero confidence cannot block a promotion.
- readJevBody must NOT discard its read error. A hard timeout or caller cancellation fires while the body is streaming just as often as while headers are pending, and research.md notes Jev output generation is unbounded, so a fixed transport_failure message mislabels the DOMINANT timeout as remote_error instead of remote_timeout under 9.2/9.3. Always add a body-phase test cell that WriteHeader+Flush()es first, then stalls; the header-phase-only test cannot reach this.
- The vendor reference marks only usage itself as required; input_tokens/output_tokens are optional properties and research.md records Jev has no output-token charge. Model both as *int64 and require >=0 only when present, or every successful classification is refused and the feature silently disables while still failing open.
- RemoteConfig.RetryBackoff and MaxAttemptsPerSession are validated by 8.1 but UNCONSUMED after 8.2: the adapter deliberately makes exactly one HTTP attempt per Decide and surfaces 429/529 as a JevFailure kind. 8.3's coordinator loop MUST consume them, or 6.7's bounded retry is unsatisfied. Do not add internal retry to the adapter - MaxAttemptsPerSession is a per-SESSION counter a stateless per-call adapter cannot own, and internal retry would multiply real attempts past the budget.
- withJevEndpoint is unexported on purpose: it exists for hermetic httptest and must never be sourced from operator configuration, because an exported validated endpoint override permanently offers any in-repo caller a way to aim the bearer credential at an arbitrary https origin.
CRITICAL INSTRUMENT LESSON: go test -overlay is a SILENT NO-OP for internal/archtest tree-walking scanners because they use os.ReadFile (source_scan.go:191). Every archtest mutation proof must edit the real file, run, restore from a /tmp backup, and verify restoration by md5sum. For normally-compiled packages overlay works and is preferable.
Three archtest instrument lessons, all learned the hard way: (a) go test -overlay is a SILENT NO-OP for internal/archtest tree-walking scanners because they use os.ReadFile (source_scan.go:191) - any archtest mutation proof must edit the real file, run, then restore from a /tmp backup and verify by md5sum; (b) FileImportPaths discards ast.ImportSpec.Name, so a vendor rule built on it is ALIAS-BLIND - iterate file.Imports and check spec.Name.Name too; (c) a name-marker AND shape-marker rule plus a ://-only URL rule lets const authHeaderFormat = "Authorization: Bearer %s" through BOTH scanners, so the contract packages additionally reject any wire-shape marker and any brace-delimited quoted-member literal.
internal/archtest enforces a 500-line cap (rules_test.go:348); the vendor-scan fixture table pushed its file to 556, so the table went to a separate fixtures file rather than raising the gate. Widening the literal rule needed a narrow, %-verb-guarded exemption for the api_key_env credential-REFERENCE key that 8.3 obliges the contract to name - removing that exemption fails on 5 shipped config.go strings, which is the proof it is load-bearing.
Task 8.1 has NO production caller by design (8.2 consumes RemotePolicy.Config(), 8.3 consumes Positive/ValidateRemoteInput); 6.1 is currently satisfied by absence, since nothing constructs a decider so egress is structurally impossible. Port method-set pinning is by EQUALITY (interface kind, exactly one method, the name Decide, the frozen signature), not by a forbidden-name blacklist - a NumMethod+MethodByName guard already forces the name, so a forbidden-name loop beside it is dead code.

