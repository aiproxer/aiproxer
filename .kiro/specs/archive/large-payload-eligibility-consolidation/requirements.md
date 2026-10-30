# Requirements Document

## Project Description (Input)
Consolidate declaration-derived large-payload eligibility metadata and repeated hook/port policy without changing which requests the proxy can execute over the wire. This slice addresses maintainers' repeated catalog edits and operators' need for unchanged fail-closed admission. It does not implement the lifecycle, proof/parity, or unused-assessment repairs owned by sibling specifications.

## Introduction
The standard extension-plane declaration already identifies each plane and its request-access class. Large-request eligibility should consume that declaration rather than require a second handwritten identity/classification catalog. Static compilation and defensive request assessment retain independent evaluations of their inputs but use the same hook and narrow-port decisions.

## V1 Slice
- **Delivers**: One declaration-driven typed-plane catalog and one maintained hook/port decision policy, consumed by the existing serving eligibility path without wider wire support.
- **First consumer**: Standard-host large-body summary compilation, static pre-capture disposition, and defensive authority assessment.
- **Safe degradation**: Invalid or blocking facts retain existing rejection or pre-execution canonical selection; static disposition never authorizes wire execution.

## Deferred
- Runtime metadata-projection optimization beyond the catalog consolidation; no contribution-storage or generator-platform rewrite is required by this slice. Follow-up: outside this delivery, not scheduled.
- Wider response-only, hook, paid-wire, or protocol support; each requires its own proven execution contract, not a classification change here. Follow-up: separate capability slice, not scheduled.
- Replay/cancellation/unlink/audit repair, bounded proof capture, composition-token repair, affinity/economic/clock parity, and alternate-assessor retirement remain with the three existing sibling specs named in `research.md`.
- New public contracts, diagnostic families, runtime registries, mask-width expansion, or external integrations are outside V1. Follow-up: none required for this slice; remote issue creation is not authorized.

## Boundary Context
- **In scope**: Declaration-derived typed-plane metadata and consolidation of the existing hook/port policy used by static and defensive eligibility decisions.
- **Out of scope**: Changing canonical or replay representation, execution owners, proof semantics, capability support, generation-token allocation, financial authorization, or SDK configuration contracts.
- **Boundary ownership**: Internal build-time generation supplies metadata; core retains eligibility policy; standard composition supplies occupancy. No new public SDK contract.
- **Adjacent expectations**: Sibling corrections establish their own implementation baselines. Shared-file overlap requires revalidation, not an unconditional dependency or duplicated ownership.

## Requirement 1: Declaration-driven eligibility metadata
**Objective:** As a maintainer, I want a plane declaration to supply serving eligibility metadata without synchronizing another handwritten catalog.
### Acceptance Criteria
1. When a standard plane is declared with a supported request-access classification and the feature-plane artifacts are regenerated, the proxy shall include that identifier and classification in its serving eligibility catalog without another handwritten eligibility identity or classification entry. **[1.1]**
2. If a declaration lacks a classification, uses an unknown or unclassified value, duplicates an identifier, or references an undeclared plane, the generation workflow shall reject it before installing replacement artifacts. **[1.2]**
3. If a summary-compilation census contains an unknown, duplicate, missing, unclassified, or out-of-range plane, the proxy shall reject that census without publishing an authorizing result. **[1.3]**
4. When equivalent census inputs arrive in a different input order, the proxy shall preserve the same blocker identities and decisions. **[1.4]**
5. When the consolidation replaces the existing catalog, the proxy shall preserve the identifier-to-diagnostic-position mapping of every existing plane. **[1.5]**
6. If a declared inventory exceeds supported blocker-reporting capacity, the generation workflow shall reject it rather than omit or truncate a plane. **[1.6]**

## Requirement 2: Conservative support remains unchanged
**Objective:** As an operator, I want metadata consolidation not to make unsupported requests eligible.
### Acceptance Criteria
1. Where a canonical-required or response-only plane is occupied, the proxy shall retain the current canonical selection and authority rejection for that composition. **[2.1]**
2. Where any current submit, request-part, response-part, or tool hook chain is occupied, the proxy shall retain its existing wire blocker. **[2.2]**
3. If a protected local-turn, secret-guard, terminal-decision, or control-tool authority is occupied or its required classification is weakened, defensive assessment shall retain the existing authority rejection. **[2.3]**
4. Where existing metadata-only planes or supported narrow-port exemption conditions are satisfied, the proxy shall retain their existing nonblocking decision without granting wire authorization; wire execution still requires defensive assessment. **[2.4]**
5. Where static disposition does not identify a blocker, the proxy shall return only the existing needs-assessment decision rather than grant wire authorization. **[2.5]**

## Requirement 3: Independent defensive checks use consistent policy
**Objective:** As an operator, I want both eligibility stages to apply consistent blockers without trusting a prior summary in place of current assessment facts.
### Acceptance Criteria
1. When static compilation and defensive assessment evaluate equivalent hook and narrow-port facts, the proxy shall apply the same blocker conditions and retain their existing result classifications. **[3.1]**
2. If the supplied defensive census has an occupied hook, blocking narrow port, or unavailable two-phase execution capability despite a clean sealed summary, the proxy shall decline authority assessment. **[3.2]**
3. If a custom port is unknown, is an occupied blocker, or matches an existing protected local-turn, secret-guard, or terminal-decision name rule even when marked wire-safe and occupied, the proxy shall retain the existing defensive rejection. **[3.3]**
4. If a summary is unsealed or disagrees with the expected generation, the proxy shall retain the existing rejection reasons and their precedence. **[3.4]**

## Requirement 4: Serving compatibility and bounded rejection
**Objective:** As a client and operator, I want the refactor to preserve admission, configuration, and runtime behavior.
### Acceptance Criteria
1. When pre-capture checks reject a composition, the proxy shall preserve the existing feature-disabled, static-blocker, resolver, compression, and size-threshold reason precedence without creating capture or scanner resources. **[4.1]**
2. Where large-payload optimization is disabled or a request is unsupported, the proxy shall preserve the existing configuration defaults, body-admission policy, and canonical behavior without opening a wire backend. **[4.2]**
3. While an admitted generation is serving, later descriptor mutation or rejected candidate compilation shall not change that generation's captured eligibility metadata or decisions. **[4.3]**
4. When the proxy evaluates the static pre-capture disposition, it shall preserve allocation-free constant-time evaluation. **[4.4]**
5. Where callers use existing public canonical, SDK, runtime, or backend-plugin contracts, the proxy shall preserve those contracts and bounded diagnostic labels. **[4.5]**
