# Implementation queue

Task 1 was approved nonbehavioral design/spec materialization, RED N/A. The six implementation leaf tasks below completed sequential implementer/reviewer/remediation/verification cycles. Completion evidence and merged provenance are recorded in `validation.md`; approvals are preserved.

## 2. Configuration and ordered matcher

- [x] 2.1 Implement strict enabled feature configuration and immutable ordered matcher
  - Decode existing `plugins.features` row `model-system-prompt`; compile during construction. Validate unknown fields, required unique bounded ASCII/derived IDs, Go regex, UTF-8/NUL/whitespace text, per-overlay/rule-count/total-text limits; preserve append bytes.
  - Produce canonical SDK PutRequests in config order with stable IDs and system stable-prefix/fallback semantics. No runtime regexp compilation, core imports, rendering or route/provider policy.
  - RED/GREEN claim tables for no/single/multiple matches/order/exact bytes and each distinct invalid boundary. Scoped dev-test/dev-build for `./internal/plugins/features/modelsystemprompt/...` and affected registration/config consumers.
  - _Boundary:_ `internal/plugins/features/modelsystemprompt/`; existing standard feature registration/config validation only. No runtime/store integration yet.
  - _Depends:_ Task 1 accepted by independent parent review.
  - _Requirements:_ 1.1, 1.2, 1.3, 1.4, 1.5.

## 3. Atomic bootstrap persistence

- [x] 3.1 Add atomic ordered batch/completion and authoritative activation history
  - Add optional internal operation; completion lookup before callback, zero-overlay decisions, shared bounds, feature-ID collision rejection, ordered slots/revisions, post-commit mutation evidence only.
  - Memory: authoritative NextBLeg history callback holds existing B2BUA mutex through ReferenceStore stage/publish; fixed lock order and post-unlock observers. Bun: same authoritative database, PG A-leg row lock; SQLite write reservation as first transactional statement before reads, independent of connection configuration/process locks.
  - Forward optional capabilities through conversation auto-registration/continuity wrappers. Add forward migration, EnsureSchema and existing dbparity/schema contracts; never alter historical migration bodies.
  - RED/GREEN shared store proof: matched/empty/reuse without callback, concurrency winner, preexisting allocations without attempt records, rollback intermediate write/capacity/collision/cancellation including slots/revisions, other producer limits, SQLite reopen/independent handles, PG independent handles, retirement cascade. No local race.
  - Scoped dev-test/dev-build for conversationview, b2bua, continuity/bunstore and featurehost wrapper consumers; applicable mandatory dual-dialect parity.
  - _Boundary:_ `internal/infra/conversationview/`, narrow optional allocation authority in `internal/core/b2bua/`, `internal/core/continuity/bunstore/` forward migration/schema suites, `internal/standardplugins/featurehost/conversation.go`, required existing continuity decorators and dbparity contracts. No runtime producer invocation.
  - _Depends:_ 2.1 and accepted Task 1.
  - _Requirements:_ 1.3, 2.5, 3.1, 3.2, 3.3, 3.4, 3.5, 4.5.

## 4. Producer and runtime seam

- [x] 4.1 Integrate generation producer before one normal inference snapshot
  - Compose enabled matcher/store adapter in featurehost CompileGeneration; reject missing/mismatched authority/reader/atomic capability before publication. Pass only a generic consumer-owned port through CorePorts/executor wiring; no concrete feature imports in core/runtimebundle and no public plane.
  - Resolve accepted default/alias intent with shared pure routing compilation and frozen A-leg override, all leaves, logical not NativeModel, no candidate choice. Lazy intent resolution and regex matching occur only inside undecided store callback.
  - Split local Match from Handle; retain selected/declined result and validated source requests before bootstrap. Later Handle preserves existing source/reply tag order, failure behavior and release. Never second Match or model/view snapshot. Use same seam for secure/detached private A-leg, no parent mutation.
  - RED/GREEN first-turn/later visibility, default/alias/override/ambiguity/logical-native intent, local-only then first inference, one Match/read, no later matching, error cleanup and owner isolation. Scoped dev-test/dev-build for featurehost/runtime/runtimebundle/routing and touched consumers.
  - _Boundary:_ `internal/standardplugins/featurehost/`, generic executor construction under `internal/infra/runtimebundle/`, runtime bootstrap/local-turn/secure/detached preparation in `internal/core/runtime/`, smallest routing-owned pure traversal helper if needed. Store contract remains 3.1.
  - _Depends:_ 2.1, 3.1.
  - _Requirements:_ 2.1, 2.2, 2.3, 2.4, 2.5, 3.1, 3.3, 4.1, 4.3, 4.5, 5.1.

## 5. Generation and conservative fast path

- [x] 5.1 Preserve frozen decisions across reload/removal and block first-turn wire bypass
  - Occupy existing conservative steering dependency for nonnil generic bootstrap port before overlays exist. Stored steering retains its existing authority blocker after removal; never deactivate on generation retirement.
  - RED/GREEN matched/no-match/ambiguous/preexisting freeze on reload/later model change, new legs use new config, invalid candidate retains old generation, removal preserves selected overlays, first-turn large-body canonical fallback bootstraps once.
  - Scoped dev-test/dev-build for featurehost/runtimebundle/runtime/largebody and changed host-generation consumers.
  - _Boundary:_ existing generation composition/retirement tests and `internal/infra/runtimebundle/build_large_body_assessor.go` plus generic port forwarding; existing largebody authority contracts. No new wire injection mechanism or cache policy.
  - _Depends:_ 4.1.
  - _Requirements:_ 3.4, 4.2, 5.1, 5.2, 5.3.

## 6. Client and family boundaries

- [x] 6.1 Prove hidden client truth and model-visible canonical instruction trajectory
  - Extend existing tests: three-turn stable prefix/original bytes, ingress/CTP/continuation/frontend output/structural records exclude steering while PTB includes it; fix exact recorder input to preserved client view only if regression proves contamination.
  - Prove frozen retry/failover/parallel snapshot and final reassertion after late removal/movement; bounded OpenAI/Anthropic/Gemini family sentinels, no provider-specific feature code or Cartesian matrix. Sentinel plaintext absent from normal diagnostics/metrics.
  - RED/GREEN focused added claims; reused evidence must identify executable tests/commands, not merely cite infrastructure. Update scratch issue-AC evidence covering all 18 mandatory criteria. Scoped dev-test/dev-build across changed runtime/family/host consumers.
  - _Boundary:_ existing runtime conversation-view/client-record/reassertion suites and bounded family adapter/contract tests; one proven recorder correction in secure preparation if necessary. No broad provider/frontend rewrite.
  - _Depends:_ 4.1, 5.1.
  - _Requirements:_ 1.5, 2.1, 3.2, 3.4, 4.1, 4.2, 4.3, 4.4, 4.5.

## 7. Documentation and delivery

- [x] 7.1 Add operator guide/config example and assemble final delivery evidence
  - Explain regex order, bounds, activation/preexisting/ambiguity, frozen lifetime/reload/removal, canonical fallback, provider/model disclosure and explicit PTB capture exposure; do not test Markdown shape.
  - Inspect diff budget (≤1,500 non-test Go lines/about 40 files, tests about 2x production, hard 100 Go files); split/reassess on overflow, no override. Record attributable versus baseline failures separately.
  - Final coherent-SHA evidence sequentially: `make regex-hotpath-check`, `make quality-checks`, `make test`, `make test-db-parity`; ext4 TMPDIR at scratch path, mandatory usable PostgreSQL. Scoped `make dev-build PKGS='./cmd/lipstd'` then `go run ./cmd/lipstd --help` docs smoke.
  - Remote race/fuzz evidence only via authorized `race-fuzz-nightly.yml` with `ref` pinned to final feature SHA; no local race or earlier-SHA certification. No push/PR/merge without user authorization; report unavailable remote evidence honestly.
  - _Boundary:_ `docs/model-system-prompt.md`, `docs/conversation-view.md`, existing tracked config example, task-owned scratch delivery evidence; no unrelated gate fixes, commits or remote mutation by subagents.
  - _Depends:_ 2.1, 3.1, 4.1, 5.1, 6.1.
  - _Requirements:_ 3.5, 4.4, 4.5, 5.4.

## Implementation Notes

- All task implementations and attributable integration remediations passed independent review; local regex, quality, comprehensive tests, SQLite/PostgreSQL parity and built-service health smoke passed on the integrated production tree.
- Architecture limits were preserved: measured copies 370, cleanup sites 23, convergence shrinkage -797 against the unchanged -795 threshold.
- Delivery completed through merged PRs #837 and #836; the integrated feature reached main in b3cd514d54e6b375e0d354006fa01eb574792a0a. Historical substrate checkpoints are not separate delivered features.
- The original broad-lane failure was an unchanged billing-store package timeout, not a detected feature race. Separate CI repair #856 preserved coverage and isolated that exhaustive package. Final six-lane race, backend security and Tier-1 fuzz evidence passed on exact SHA c2e06ec132701bce6f4d3250ecd97a2c25284959.

## Completion Status

- [x] All implementation tasks independently reviewed and implemented on main.
- [x] Fresh feature/runtime/store checks and required SQLite/PostgreSQL continuity parity passed.
- [x] Built distribution validated enabled config, served `/healthz` with HTTP 200, and shut down cleanly without provider inference calls.
- [x] All six remote race lanes and actual security/Tier-1 fuzz stages passed; downloaded evidence SHA verified.
- [x] Feature source equivalence confirmed between certified SHA and closeout main baseline c2eace237f4e128e6439ce79fb616fdd3d401cc8. Deferred scope remains excluded.
