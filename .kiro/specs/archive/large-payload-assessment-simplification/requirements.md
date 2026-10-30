# Requirements Document

## Project Description (Input)
Retire unused alternative large-payload assessment and execution machinery, retaining meaningful behavioral coverage at the actual serving owners. Preserve canonical calls, bounded replay, conservative admission, and shared execution semantics. This is behavior-preserving simplification, not a lifecycle, proof/parity, or eligibility-policy repair.

## Introduction
Maintainers should encounter the serving large-request acceptance and execution authorities, not a second apparent authority maintained only for isolated tests. Retire proven-unused internal machinery without replacing either intentional payload representation or changing clients' and operators' established behavior.

## V1 Slice
- **Delivers**: Fewer maintained assessment/execution concepts and one discoverable serving policy path for large requests, with unchanged observable behavior.
- **First consumer**: Maintainers of existing production large-request admission, execution, and their behavioral coverage.
- **Safe degradation**: Unprovable requests continue through existing pre-execution canonical fallback; accepted stale proofs continue to fail before side effects. A candidate with a serving consumer is retained and revalidated rather than deleted speculatively.

## Deferred
- Lifecycle repair — adjacent [large-payload-lifecycle-correctness](../large-payload-lifecycle-correctness/requirements.md) owns replay lifetime, cancellation disposal, logical terminal audit, and failed unlink observability.
- Proof and parity repair — adjacent [large-payload-proof-and-parity](../large-payload-proof-and-parity/requirements.md) owns metadata budgeting, composition identities, trusted session/economic facts, and response clocks.
- Eligibility consolidation — follow-up brief: derive duplicate classifications from frozen SDK metadata while preserving conservative support; separate `large-payload-eligibility-consolidation` specification.
- Measured performance optimization, broader protocol support, paid-wire activation, and unrelated wrapper cleanup — follow-up brief: establish separate consumers and measured/contract evidence before widening scope. This request authorizes no remote issue creation.

## Boundary Context
- **In scope**: Unused internal assessment/projection families and isolated alternative execution scaffolding, plus directly dependent tests and misleading references.
- **Out of scope**: Public API/ABI/configuration changes; frontend/backend protocol behavior; routing, billing, session, eligibility, or replay-storage redesign; new framework, state owner, or architecture gate.
- **Adjacent expectations**: Preserve financial-safety funding/evidence and high-concurrency boundedness contracts. Sibling specifications are not completed repairs. Coordinated corrective changes establish the new baseline, not work to undo under equivalence.

## Requirement 1: One maintained serving authority
**Objective:** As a maintainer, I want large-request policy changes to concern the real serving path rather than an unused alternative model.
### Acceptance Criteria
1. Where an internal assessment or execution concept has no serving consumer and does not participate in a public contract, the proxy shall retire it and its exclusively dependent non-public representations. **[1.1]**
2. When large-request acceptance or execution is maintained, the proxy shall retain the existing production authorities without introducing a replacement policy framework or duplicate state owner. **[1.2]**
3. Where a candidate has a serving consumer or participates in a public contract, the proxy shall retain it outside the proven-unused deletion set. **[1.3]**

## Requirement 2: Admission remains conservative
**Objective:** As an operator, I want deletion of unused machinery not to broaden fast-path support or bypass acceptance checks.
### Acceptance Criteria
1. When a supported request is admitted, the proxy shall preserve existing proof-bound replay execution and bounded request representation. **[2.1]**
2. If proof or configured features make wire execution unsupported, the proxy shall preserve the existing decline and same-admission canonical fallback before execution without opening a wire backend. **[2.2]**
3. If accepted proof disagrees with its execution composition or candidate domain, the proxy shall retain rejection before session mutation or upstream opening and shall not re-execute canonically. **[2.3]**

## Requirement 3: Execution behavior and compatibility remain unchanged
**Objective:** As a client and operator, I want the same streaming, recovery, and cleanup behavior after the internal simplification.
### Acceptance Criteria
1. When initial, parallel, or permitted replacement attempts execute a supported request, the proxy shall retain existing shared attempt, receive, routing, continuity, and terminal ownership. **[3.1]**
2. When downstream content is committed, the proxy shall preserve the prohibition on transparent retry or failover. **[3.2]**
3. When execution completes, fails, or is canceled, the proxy shall preserve established error identity, event ordering, disposal, usage, audit, and replay-release behavior of the implementation baseline selected for this slice. **[3.3]**
4. Where callers use public canonical, SDK, runtime, or backend-plugin contracts, the proxy shall preserve those contracts and configuration behavior. **[3.4]**

## Requirements Review
Three areas and ten EARS criteria describe the simplification and preservation outcomes. Independent pre-write review found two local wording issues: public-contract participation must exclude deletion in 1.1 as well as 1.3; implementation constraints must not obscure compatibility in 3.4. Both were repaired and checked against the gate. Tests and inventory checks remain implementation evidence. Equivalence does not require preserving known defects after sibling corrective changes land; their accepted fixes become the selected implementation baseline.
