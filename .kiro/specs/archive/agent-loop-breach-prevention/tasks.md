# Implementation Plan

Implement ALG as a concrete provider for the approved `terminal-decision-feature-extension` platform. The platform spec owns FeatureBundle exclusivity, the core terminal chokepoint, transactional continuation, conversation-view steering, immutable generations, process policy, and generic endpoints. These tasks own ALG policy only. The former ALG Task 11 remediation is superseded: its generic steering/terminal work must be completed and verified by the platform spec before Task 6 here.

Use strict RED -> minimal implementation -> GREEN -> refactor. Do not edit core terminal ownership or create a second policy/continuation authority. Tasks remain unchecked until implementation.

## Dependency and Baseline

- [x] 1. Establish platform dependency and ALG baseline
- [x] 1.1 (P) Verify the platform contract and approved dependency gate
  - Verify `.kiro/specs/terminal-decision-feature-extension/` contains approved requirements, design, tasks, and the provider/transaction/policy/endpoint contracts required by ALG.
  - Record the exact platform task IDs that must complete before provider integration and assert that ALG has no direct dependency on platform internals.
  - The dependency check fails closed when the platform spec is missing, not ready, or its provider contract is incompatible.
  - _Requirements: 1.2, 9.1, 9.4, 11.2_
  - _Boundary: feature-spec dependency and contract tests_
  - _Depends: terminal-decision-feature-extension tasks 2.2, 3.2, 4.2, 5.2, 6.2, 7.2, 8.2, 9.1_
  - _Validation: JSON/spec status checks and platform handoff test_

- [x] 1.2 (P) Characterize existing ALG behavior and removal target
  - Capture disabled-mode behavior, current semantic-stop fixtures, transport recovery expectations, and concrete ALG references in core before migration.
  - Identify old `stopguard`, `stopgate`, `continuationsafety`, `stopguardverify`, direct call append, and `turnTerminal.guardHidden` paths as migration targets, not new owners.
  - The baseline distinguishes policy behavior retained in ALG from generic lifecycle behavior moved to the platform.
  - _Requirements: 1.1, 1.4, 9.5, 10.7, 11.3_
  - _Boundary: ALG characterization and architecture tests_
  - _Validation: focused existing ALG/runtime tests and deterministic reference report_

## Provider and Configuration

- [x] 2. Build the opt-in ALG provider contribution
- [x] 2.1 (P) Add RED provider contract tests
  - Test ALG returns bounded allow-stop/continue decisions through the platform provider contract and never claims terminals, opens backends, mutates snapshots, or appends canonical calls.
  - Test no-provider and disabled behavior preserve existing runtime semantics and feature removal leaves the generic platform buildable.
  - Test provider errors are compatible with platform allow-stop normalization and no recursive self-invocation occurs.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 6.5, 9.5, 11.1, 11.3_
  - _Boundary: ALG provider contract tests_
  - _Depends: 1.1_
  - _Validation: focused provider package tests_

- [x] 2.2 Implement ALG configuration, provider construction, and FeatureBundle registration
  - Add nested feature configuration with opt-in enablement, verifier role/timeout, semantic cap, no-progress limit, and explicit-completion policy using existing validation conventions.
  - Construct the provider only when enabled and contribute it through the platform's singular FeatureBundle field; do not add an ALG branch to core.
  - Invalid bounds, enum values, or missing platform capability fail before candidate generation publication.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 7.1, 7.2, 9.1, 9.4, 11.1_
  - _Boundary: ALG config and feature composition_
  - _Depends: 1.1, 2.1_
  - _Validation: focused config/provider/registry tests_

## Canonical Evidence and Cause Policy

- [x] 3. Implement canonical ALG evidence and transport policy
- [x] 3.1 (P) Add RED cause/evidence and transport boundary fixtures
  - Cover clean, empty, pause, output limit, pre/post transport, partial tool, refusal/filter, cancellation, and unknown causes using canonical facts and commitment state.
  - Assert no provider-name/raw-frame classification, bounded evidence projection, completed tool/result preservation, unsafe state rejection, and delegation to existing pre-output recovery.
  - The fixtures demonstrate no duplicate transport retry configuration and no post-output retry/replacement intent.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 3.1, 3.2, 3.3, 3.4, 4.1, 4.3, 4.4, 4.6_
  - _Boundary: ALG evidence and cause policy_
  - _Depends: 1.1_
  - _Validation: focused evidence/cause tests and stream-recovery contract tests_

- [x] 3.2 Implement bounded canonical evidence projection and cause decisions
  - Map platform evidence into ALG's bounded cause/evidence model using canonical request, trajectory, tool, completion, lineage, and commitment facts.
  - Return conservative allow-stop for explicit non-recoverable/unknown/unsafe causes; delegate pre-output transport to the platform's existing recovery behavior.
  - Preserve completed tool/result evidence and allow post-output policy to request continuation only from a safe retained trajectory.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 3.1, 3.2, 3.3, 3.4, 4.1, 4.3, 4.4, 4.6_
  - _Boundary: ALG evidence projector and provider policy_
  - _Depends: 3.1, 2.2_
  - _Validation: focused cause/evidence tests and platform provider integration tests_

## Semantic Verifier

- [x] 4. Add independent bounded completion verification
- [x] 4.1 (P) Add RED verifier adapter and verdict parser tests
  - Assert detached/private auxiliary execution, parent trace/A-leg/B-leg/branch lineage, dedicated role, plugin/ALG recursion suppression, no tools, bounded deadline, and strict structured verdict parsing.
  - Assert timeout, transport error, malformed output, unknown verdict, empty objective, and inconsistent evidence normalize to `UNCERTAIN`/allow-stop.
  - Assert verifier reason/objective bounds and no chain-of-thought propagation.
  - _Requirements: 2.3, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 7.2, 7.4, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 10.2_
  - _Boundary: ALG auxiliary verifier adapter_
  - _Depends: 1.1_
  - _Validation: focused verifier/parser tests with fixed canonical fixtures_

- [x] 4.2 Implement conservative verifier prompt, adapter, and semantic policy
  - Evaluate current objective, recent instructions, candidate output, and canonical action trajectory; distinguish complete work, concrete existing work, user-dependent next steps, external blocks, optional suggestions, user-owned next steps, and quoted future language.
  - Honor trusted normalized explicit completion according to `trust`/`verify` configuration; malformed or absent signals use conservative normal policy.
  - Return continuation only for concrete in-scope unfinished work executable without new input; all uncertainty returns allow-stop.
  - The verifier cannot authorize user approval, credentials, permissions, tools, or scope expansion.
  - _Requirements: 2.3, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 10.2_
  - _Boundary: ALG verifier policy and auxiliary adapter_
  - _Depends: 3.2, 4.1_
  - _Validation: focused verifier, auxiliary, and semantic fixture tests_

## Progress and Recovery Intent

- [x] 5. Build bounded progress and conditional provider intent
- [x] 5.1 (P) Add RED progress and intent-content tests
  - Cover materially equivalent output, tool/argument/result/error cycles, verdict/objective repetition, volatile-ID immunity, new progress, semantic max budget, and no-progress limit.
  - Assert recovery content explicitly denies new user intent/approval/scope expansion, ends when complete, resumes only existing work, and stops for user-dependent steps.
  - Assert direct `Call.Messages`/`Call.Items` append, `turnTerminal.guardHidden`, backend opening, terminal claims, and mutable snapshot updates are impossible from the provider boundary.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 5.3, 5.4, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 7.1, 7.3, 7.5, 8.2, 8.3, 8.4, 8.5, 8.6, 10.7, 11.1_
  - _Boundary: ALG progress and recovery intent policy_
  - _Depends: 1.1_
  - _Validation: focused progress/instruction tests and static boundary checks_

- [x] 5.2 Implement stable progress tracker and bounded recovery intent builder
  - Fingerprint stable canonical output/tool/result/continuation/verdict facts without retaining raw sensitive payloads; enforce semantic cap independently from no-progress threshold.
  - Build bounded internal recovery content and a provider-neutral continuation intent with concrete objective, reason, attempt/max values, internal provenance, and fixed scoped overlay ID `alg-rec` where permitted by the platform contract.
  - Preserve the logical maximum across new progress and return allow-stop when limits or platform bounds are reached.
  - Leave anchor resolution, snapshot freeze, `Reassert`, overlay persistence/deactivation, stale cleanup, and client transcript isolation to the platform transaction; do not duplicate those authorities.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 5.3, 5.4, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 7.1, 7.3, 7.5, 8.2, 8.3, 8.4, 8.5, 8.6, 10.7, 11.1_
  - _Boundary: ALG progress and recovery intent policy_
  - _Depends: 4.2, 5.1_
  - _Validation: focused progress/intent tests and platform intent contract tests_

## Platform Integration and Lifecycle

- [x] 6. Integrate ALG as a removable provider through the platform
- [x] 6.1 Add RED integration tests for one platform terminal transaction
  - Exercise immediate promised action with missing trajectory action, complete answer, user-directed question, optional/user-owned next steps, and quoted future action through the platform chokepoint using a fake ALG provider/verifier.
  - Exercise pre-output EOF, post-output interruption, completed tool/result, incomplete arguments, cancellation, verifier failure, no-progress, and maximum budget; assert no intermediate terminal or duplicate side effect.
  - Exercise platform conversation-view steering contract: accepted user-ingress anchor, fixed scoped overlay ID, snapshot N+1, exact-once reassertion, deactivation, stale cleanup, and absence from A-side output/continuation records. ALG tests verify the contract, but do not implement its lifecycle.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 4.5, 5.2, 5.3, 5.4, 5.6, 7.3, 7.5, 8.2, 8.3, 8.4, 8.5, 10.3, 10.4, 10.5, 10.6_
  - _Boundary: ALG/platform contract integration tests_
  - _Depends: 1.1, 5.2, platform terminal-decision tasks 3.2 and 4.2_
  - _Validation: focused runtime/conversation-view integration tests_

- [x] 6.2 Migrate ALG policy and verify platform-owned artifact removal
  - Register the concrete provider only through standard FeatureBundle composition and consume the platform's next-request policy snapshot; no ALG endpoint/store or generation mutation is added.
  - Migrate policy code out of old core ALG packages; verify that the platform-owned tasks removed direct call append/`guardHidden` implementation artifacts, and retain only provider-specific policy in the feature package.
  - Prove disabled, reload, provider withdrawal, feature removal, and platform contract failure behavior follows immutable generation and no-provider semantics.
  - The concrete ALG provider can be removed without core imports, provider-name branches, terminal authorities, or hidden-content owners remaining.
  - _Requirements: 1.1, 1.4, 9.1, 9.2, 9.3, 9.4, 9.5, 11.1, 11.2, 11.3, 11.4_
  - _Boundary: ALG feature registration and removal migration_
  - _Depends: 2.2, 5.2, 6.1, platform terminal-decision tasks 2.2, 5.2, 6.2, 7.2_
  - _Validation: focused registry/generation tests, import ratchets, no-provider build test_

## Observability and Regression Matrix

- [x] 7. Add bounded ALG observability and acceptance fixtures
- [x] 7.1 (P) Add RED telemetry/privacy and regression tests
  - Assert bounded ALG cause/verdict/action/continuation/no-progress/failure evidence and preserved A/B/trace/usage relationships.
  - Assert no prompts, output, tool arguments, verifier reason/objective, recovery content, secrets, or unbounded IDs appear in labels/default logs.
  - Add deterministic positive and negative fixtures for every false-stop and false-continuation boundary in the requirements.
  - _Requirements: 7.1, 7.2, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: ALG telemetry and regression tests_
  - _Depends: 1.1_
  - _Validation: focused telemetry/privacy and semantic regression tests_

- [x] 7.2 Implement ALG telemetry and complete the acceptance matrix
  - Emit bounded provider-specific evidence through existing observability seams without inventing a parallel metrics owner.
  - Cover immediate promised action, complete answer, user question, optional improvement, user-owned next steps, quoted future action, refusal/filter, transport safety, completed tool/result, unsafe partial state, cancellation, verifier uncertainty, no-progress, budget exhaustion, and supported frontend integration.
  - Add ratchets proving zero direct call append, zero `turnTerminal.guardHidden`, no provider-specific core imports, no second ALG policy store, and no recursive verifier guard.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 11.1, 11.3_
  - _Boundary: ALG telemetry, fixtures, and architecture ratchets_
  - _Depends: 1.1, 6.2, 7.1, platform architecture task 8.2_
  - _Validation: focused/full ALG tests and `go test ./internal/archtest/...`_

## Final Gates

- [x] 8. Run ALG simplification and repository verification gates
- [x] 8.1 Perform the ALG boundary and simplification review
  - Compare the baseline against the target: one provider contribution, zero concrete ALG core policy branches, no second terminal/continuation/policy/steering owner, bounded verifier/progress work, and removable feature registration.
  - Remove helpers or abstractions that do not support a measured requirement; return platform-boundary changes to the platform spec instead of expanding ALG.
  - The review records GO only when platform dependency, architecture ratchets, false-positive matrix, and no-provider removal evidence pass.
  - _Requirements: 1.4, 9.5, 10.7, 11.1, 11.2, 11.3, 11.4_
  - _Boundary: ALG simplification/architecture review_
  - _Depends: 6.2, 7.2_
  - _Validation: deterministic baseline/target report and self-review gate_

- [x] 8.2 Run focused and repository quality verification
  - Run focused provider, config, evidence, verifier, progress, intent, integration, telemetry, and architecture tests; run targeted race tests where supported and retain Linux CI coverage where Windows TSAN cannot allocate.
  - Run `go test ./...`, `make quality-checks`, `make test`, and `make qa` when permitted by repository state; report skipped integration/external gates plainly.
  - Confirm both spec dependency JSON files parse, both specs are ready-for-implementation, `git diff --check` passes for owned paths, and no stale Task 11/HANDOFF claim remains.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 2.1, 3.1, 4.1, 5.1, 7.1, 8.1, 9.1, 10.1, 11.2, 11.3_
  - _Boundary: ALG/repository verification_
  - _Depends: 8.1_
  - _Validation: all applicable commands listed above_

## Dependency Graph

```text
terminal-decision handoff {2.2, 3.2, 4.2, 5.2, 6.2, 7.2, 8.2, 9.1} -> 1.1
1.2 (pre-gate characterization; parallel exception)
1.1 -> 2.1 -> 2.2
      ├-> 3.1
      ├-> 4.1
      ├-> 5.1
      └-> 7.1
3.1, 2.2 -> 3.2; 3.2, 4.1 -> 4.2; 4.2, 5.1 -> 5.2
3.2, 4.2, 5.2 -> 6.1 -> 6.2
6.2, 7.1 -> 7.2 -> 8.1 -> 8.2
```

Tasks 3, 4, 5, and 7 can proceed in parallel after the provider contract/configuration is stable because their feature-policy boundaries are separate. Task 6 is the convergence point and cannot proceed until the platform terminal/transaction tasks are complete. No task authorizes commits, branch operations, Kiro-status changes, or PR work.

## Requirement Coverage Matrix

| Requirement | Tasks |
|---|---|
| 1 | 1.2, 2.1, 2.2, 3.2, 6.2, 8.2 |
| 2 | 3.1, 3.2, 4.1, 4.2, 6.1, 7.2 |
| 3 | 3.1, 3.2, 6.1, 8.2 |
| 4 | 3.1, 3.2, 5.1, 5.2, 6.1, 7.2 |
| 5 | 4.1, 4.2, 5.1, 6.1, 7.2 |
| 6 | 5.1, 5.2, 6.1, 7.2 |
| 7 | 2.2, 4.1, 4.2, 5.1, 5.2, 7.1, 7.2 |
| 8 | 4.1, 4.2, 5.1, 5.2, 6.1, 7.1, 7.2 |
| 9 | 1.1, 1.2, 2.2, 6.1, 6.2, 7.2, 8.1 |
| 10 | 2.1, 4.1, 6.1, 7.1, 7.2, 8.2 |
| 11 | 1.1, 1.2, 2.1, 2.2, 5.1, 5.2, 6.2, 7.2, 8.1, 8.2 |

## Completion Status

- [x] All 24 task entries are complete at branch baseline `eb1d85908d249cbf7300814b82be578b14572a08`.
- [x] Focused SDK, policy, runtime, plugin, and architecture tests pass; `make quality-checks` passes on the exact rewritten tree.
- [x] The concrete provider is removable: production core has no ALG import, provider-name branch, terminal authority, policy store, or hidden-content owner.
- [x] The dependent terminal-decision platform tasks and removal ratchets are complete; no successor-only work is claimed by this spec.
- [x] Windows race execution is unavailable in this worktree; Linux CI retains race ownership.
- [x] This archive is intentionally included in the same implementation changeset and upcoming PR under user override, so merged-main SHA verification is pending PR delivery.
