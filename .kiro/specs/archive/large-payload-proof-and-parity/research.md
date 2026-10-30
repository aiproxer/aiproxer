# Research & Design Decisions

## Summary
- **Feature**: `large-payload-proof-and-parity`.
- **Discovery scope**: Full brownfield, limited to five already selected correctness findings; not a greenfield architecture or external integration.
- **Baseline**: `c6598d5d770f8a9b1b981abdf03fe6ca5fbd2b77`; source-level checks plus fresh scratch-overlay probes.
- **Key finding**: Two representations already share execution and terminal owners. Remaining gaps are bounds, identity-domain confusion, and fact/capability transfer, not two independent orchestration engines.
- **Technology alignment**: Existing Go/std-library mechanisms and internal contracts suffice. No dependency upgrade or new library; web research would not resolve these repository-specific contracts.

## Research Log
### Metadata budgets
- Sources: three frontend `profile.go` files; `frontendpipe/proofmechanics.go`; `frontendpipe/candidate.go:494–518,691–720`.
- Top-level model/instructions/tool-choice writers charge a shared counter before writing. Nested role/type buffers bypass it; OpenResponses also bypasses for item status and string-valued store.
- Raw envelope captures in event handlers and chunk hooks increment/check only after retaining the slice. The design must repair write order, not merely add a final string-length check.
- Final `ProofOutput.Validate` and `Proof.AggregateFactBytes` see normalized facts and cannot prove transient buffer bounds. Buffer reset or whitespace trimming must not refund the cumulative scan budget.
- Prompt content is different: identity hashing/discard sinks and optional bounded small-input-array caches preserve content semantics without retaining the large prompt. Their existing body/shape bounds remain separate from metadata admission.

### Session facts
- Sources: `executor_execute_large_body.go:488–515,637–661`; `executor_prepare_secure.go:147–155`; `recv_turn_facts.go:185–207,249–335`; `executor_route_plan.go:70–127`.
- Wire already binds the authoritative session and supplies it to classification; routing receives a view containing only Principal. Scope/workspace are also absent from that frozen view.
- Do not fabricate a partial canonical Call. Populate the existing views from preparation/binding and freeze before route planning. Valid view availability is not equivalent to principal presence.
- Context projection is write-only after freeze. Replacements and parallel attempts must retain the admitted view, not recover missing facts from a caller context.

### Economic evidence
- Sources: `billing_call_id.go:68–83`; `executor_prepare_request.go:255,296–327`; `executor_open_attempt.go:903–923`; `executor_execute_large_body.go:637–661,903–958,1123–1183`.
- Canonical assembly resolves StoreID once and marks even an empty result stamped. Wire omits both fields.
- Canonical successful-open binding attributes buffered first-event provider drafts before publication. Wire open omits binding and adds wrappers with narrower static method sets.
- Existing attempt/terminal consumers, not new wrapper drains, must own observation consumption. Preserve observations and checkpoint capability across adapters without changing native interpretation or financial finality.
- Production eligibility intentionally blocks occupied exposure admission and unsupported identity callbacks. The reproduction leaves exposure admission unoccupied; no universal mischarging assertion is justified.

### Clock precedence
- Sources: OpenAI Responses `handler.go:120–140`, `encode_stream.go:31–47`, `encode_nonstream.go:27–46`; Chat `handler.go:95–114`, `encode.go:185–200,263–278`; `frontendpipe/response_context.go:141–199`.
- Canonical OpenAI encoding overrides deterministic creation time when a wall clock exists. Wire uses identity-derived time instead. Reuse frontend encode options to sample once.
- Proof identity/seeds are immutable semantic data; mutating them to implement wall-clock parity would violate existing seed validation. OpenResponses has a separate timestamp contract and is not assigned OpenAI clock semantics by this slice.

## Brownfield Authority and Effect Inventory
| State/resource | Constructor and current owner | Identity / mutation / observer | Cleanup and withdrawal |
| --- | --- | --- | --- |
| Proof metadata buffers | Frontend profile per compile | Local counter; profile/scanner mutate, assessor observes facts | Lexical source reader close; proof/error returns release buffers |
| Replay root and readers | Existing capture/source lifecycle | Digest/size; immutable body; attempt body readers | Existing runtime/source owner; lifecycle spec repairs handoff and unlink |
| Composition/assessor | Runtimebundle candidate builder | Generation-local stamp/domain; immutable after build | Host generation manager drains and retires; failed candidate uses existing ledger |
| Bound session/views | Secure-session preparation and BeginTurn; core admission | Authoritative session/A-leg/turn plus resolved scope/workspace; frozen views | Existing secure turn finish/authority release; no new fact resource |
| Economic StoreID | Trusted internal host resolver; admitted request | Frozen value and stamped absence | Plain bounded fact; terminal consumers retain established identity |
| Provider stream/drafts | Backend parser/Open; shared attempt acquisition | Trusted B-leg binding; provider records, terminal/evidence consumers drain | Existing attempt terminalizer cancels/closes; wrapper methods only delegate |
| Response creation time | Frontend encoder construction | Selected scalar; frontend observes | Plain per-response fact, no resource |

Effects: reader/metadata retention is lexical reversible work; composition is immutable generation-owned state; authoritative BeginTurn is a state mutation, not a side-effect-free assessment; provider dispatch and client content emission are irreversible; usage evidence has existing append/terminal ownership and is not rollback-able money settlement.

Withdrawal graph remains `stop admissions → pinned request/attempt borrowers finish → existing generation retirement → existing resource ledger cleanup`. Per-attempt rollback/cancel precedes request authority/source cleanup as defined by lifecycle correctness. No new worker, lease, queue, lock, cleanup claimant, or physical resource is introduced here.

## Alternatives and Synthesis
- **No change** leaves the five directly observed gaps; reject.
- **Local hardening** at proof, composition, view freeze, acquisition, and encoder seams is favored over representation collapse.
- **Narrow private reuse** is warranted only for the already duplicated trusted successful-open binding and existing view projection; reuse their owner, not another execution coordinator.
- **Generic lifecycle/container/protocol registry** adds no demonstrated value; reject.
- **Generalization**: Missing admitted facts belong in `recvTurnFacts`; missing acquisition facts belong in `attemptTx`. Keep those two existing lifetimes separate instead of introducing a universal facts registry.
- **Build vs adopt**: Reuse bounded writers, immutable generation construction, standard identity generation already in the repository, SDK evidence contracts, and frontend encode options. No third-party adoption needed.
- **Simplification gate**: No new owner, platform plane, persisted table, provider switch, request-time resolver, or public contract. Keep canonical/replay payloads and shared attempt/receive execution.

### Construction Identity Decision
Independent source review found host-manager publication IDs are assigned after assessor compilation, at successful publication (`runtimehost/manager.go:224–285`; startup `host_build.go:142–147,272–289`; reload `runtimehost/attempt_runner.go:285–343`). Reserving or predicting a publication number would unnecessarily change lifecycle ownership.
- Select a bounded collision-resistant composition nonce from standard-library `crypto/rand.Text()`, allocated once at `buildLargeBodyAssessor` construction and reused through its existing returned identity string.
- Remove economic snapshot lookup and the package-global fallback counter from this identity path. This is an incarnation identity, not a config fingerprint, billing snapshot version, or persistent replay key.
- Existing manager publication/pinning/rollback remains authoritative. Old executors retain their identity; failed builds only discard their own nonce; snapshot refresh cannot retarget the assessor.

### Optional Evidence Capability Decision
Independent review identified a precedence trap: `response_pipeline_observations.go:474–481` and `attempt_usage_evidence.go:635–682` prefer `execbackend.EconomicEvidenceSource` over `metering.ObservationSource`. A new rich drain returning nil unconditionally would still lose native evidence.
- Preserve binder, legacy usage, native observations, rich records, and `AccountingEvidenceEnabled` through both wire adapters, following `streampeek/prepend.go:91–148` without importing that plugin into core.
- Rich drain delegates real rich records unchanged; otherwise it lifts each native observation into the existing `EconomicEvidence{Observation: observation}` carrier, matching `rememberEconomicObservationOnce` (`attempt_usage_evidence.go:557–558`). It must not drain both interfaces for the same batch or invent coverage metadata.
- Explicit accounting enabled/disabled state wins; fallback recognizes genuine underlying evidence capability, not the wrapper's own newly added methods. No sideband means false.
- Existing accepted-observation checkpoint queue (`attempt_usage_evidence.go:449–481`) carries durable checkpoint access; there is no missing additional stream checkpoint interface to invent.
- Shared binding belongs immediately after successful non-nil provider open, before wrappers/publication. Adapters delegate; existing attempt/terminal consumers drain, dedupe, checkpoint, and dispose.

### Baseline Suites
All seven complete default package suites passed with `go test -count=1` on largebody, runtime, runtimebundle, frontendpipe, OpenAI Responses, Chat, and OpenResponses. Output artifact: `proof-parity-baseline-suites.log` in external audit scratch. This passing baseline coexists with the deliberately failing missing-contract probes; it is not implementation verification.

## Fresh Reproduction Evidence
Development readiness passed: `make dev-doctor` (Go 1.27.2, ext4 scratch, canonical hooks and guarded toolchain available; PostgreSQL not requested).

Commands executed on the baseline (scratch paths normalized to `$AUDIT_SCRATCH`; set it to the external directory containing the overlay and supporting test sources):
```sh
go test -count=1 -overlay="$AUDIT_SCRATCH/proof-parity-current-overlay.json" -run='^TestAudit(LargeRoleMustBeFactBudgetBounded|ResponseClockParity|PopulatedPublisherDoesNotAliasWireComposition)$' -v ./internal/plugins/frontends/frontendpipe ./internal/infra/runtimebundle
go test -count=1 -overlay="$AUDIT_SCRATCH/proof-parity-current-overlay.json" -run='^TestIndependentAudit[BC]$' -v ./internal/core/runtime
```
First command intentionally **FAILS desired-contract assertions**; second **PASSES defect-characterization assertions**. Neither outcome means the slice is implemented.

| Probe / portable fixture recipe | Observed outcome | Limit |
| --- | --- | --- |
| Each profile: `stream:true`, model `m`, item role = 1 MiB spaces + `user`, plain text content; OpenResponses adds `store:false`; budget 4096 | Accepted; final fact bytes 130/127/141 | Role branch measured; type/status/store branches verified in source, require TDD cases |
| Same populated economic publisher queried twice for composition identity | `gen-1`, `gen-1` | No external cross-generation HTTP exploit proven; normal dispatcher pinning mitigates exposure |
| Responses/Chat handlers with fixed wall clock 1700000000, identical events, wire toggled | Canonical 1700000000; wire 1715631327 / 1715653203 | Permissive test assessor exercises non-stream writers; real eligible streaming path requires regression |
| Runtime canonical/wire with actual OpenAI Responses SSE parser: created, text delta, completed usage 10 input / 2 output / 12 total | Store/bind/native evidence 1/1/1 vs 0/0/0 | Exposure admission absent; no financial posting/charge assertion |
| Fresh session proof through production assessor; alias requests session affinity; test both missing-identity policies | Ignore leaves no affinity binding; fail-closed returns identity-required after session creation | No resumed-session support implied |

Original external scratch sources: `frontend-proof-audit_test.go`, `audit-8a041a2a-independent/probe_test.go`, and `architecture-audit-20261013-194245/generation_probe_test.go`. Overlay rewrites only test paths; regenerate its mappings for the target checkout if reproducing elsewhere. Future implementation must add stable repository regressions with desired expectations; these local files are not required task inputs.

## Risks and Evidence Limits
- Tests already passing do not cover all these missing contracts. New desired-oracle tests must be observed red before fixes.
- Session/economic work touches shared receive/acquisition files also used by financial-safety and lifecycle work; rebaseline source ownership before implementation and after integration.
- Full HTTP receive-replacement is currently affected by premature source close; require lifecycle fix for that sentinel or report it unresolved, never claim coverage from backend-Open-only retries.
- No paid-wire activation, whole-repository QA, remote race/OS runs, live latency benchmark, or money-loss measurement is part of this document-generation evidence.
