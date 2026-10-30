# Requirements Document

## Project Description (Input)
Proxy clients and operators currently face gaps in large-request proof bounds and canonical-versus-replay parity: metadata can bypass configured budgets, composition stamps can collide, admitted session and economic facts can be lost, and response timestamps can ignore the configured clock. Repair these five contracts through existing owners while preserving bounded replay, canonical fallback, provider neutrality, billing eligibility blockers, and pre-output-only recovery.

## Introduction
Repair the proof-to-execution fact handoff in the existing accepted-wire path. A large request may retain its bounded replay representation without losing the trusted facts and frontend response semantics already supplied to canonical execution.

## V1 Slice
- **Delivers**: Budget-bounded metadata proof, composition-isolated acceptance, authoritative session affinity, retained native economic evidence, and configured-clock response parity.
- **First consumer**: Existing three certified frontend proof profiles, production assessor/executor, and OpenAI Responses/Chat response encoders.
- **Safe degradation**: Unprovable metadata declines to existing canonical processing under held admission before execution. Stamp disagreement is terminal and never causes canonical re-execution. Absent trusted economic identity stays absent rather than fabricated.

## Deferred
- `large-payload-lifecycle-correctness` — completed prerequisite in `../archive/large-payload-lifecycle-correctness/`, integrated in the implementation base: replay/source lifetime, canceled-result disposal, logical terminal outcome, and failed unlink observability remain its responsibility; no duplicate implementation here.
- `large-payload-assessment-simplification` — follow-up brief: remove unused assessment machinery and production test scaffolding after moving meaningful tests onto serving boundaries.
- `large-payload-eligibility-consolidation` — follow-up brief: derive duplicated access classifications from the frozen SDK manifest and consolidate blockers without expanding supported semantics.
- Additional certified protocols, broader paid-wire activation, live-provider latency/charge-impact measurement, and general performance cleanup — follow-up brief: require separate capability and measured benefit scope. No remote issue creation is authorized by this document-generation request.

## Boundary Context
- **In scope**: Existing certified metadata proof, composition-bound stamps, bound-session handoff, trusted StoreID/native provider evidence, and OpenAI response creation timestamps.
- **Out of scope**: Changing billing/rating/settlement policy, removing eligibility blockers, enabling resumed sessions or new shapes, rewriting affinity/routing, adding public contracts/configuration, or replacing canonical/replay representations.
- **Adjacent expectations**: Financial-safety owns paid-dispatch funding and durable finality; this slice preserves its trusted evidence inputs without claiming paid-wire readiness. High-concurrency owns scale/retention optimization; this slice retains existing bounded replay and admission contracts. Lifecycle correctness owns stream/source cleanup even where files overlap.
- **Boundary ownership**: Frontends own protocol metadata and response encoding; composition owns immutable proof identity; core owns trusted session/economic facts and existing attempt/terminal policy; backend adapters own native usage interpretation.

## Requirement 1: Metadata proof obeys the configured budget
**Objective:** As an operator, I want request metadata to remain bounded before the fast path claims it is safe.
### Acceptance Criteria
1. While compiling a certified request proof, the proxy shall charge every decoded byte retained in metadata string buffers against one cumulative metadata budget before buffering, including nested role/type/status and string-valued control fields, without discounting whitespace normalization or buffer resets. **[1.1]**
2. If a metadata write would exceed the configured budget, the proxy shall stop proof compilation with the established semantic-budget failure and shall not admit the request for wire execution. **[1.2]**
3. When proof compilation declines before execution, the proxy shall preserve existing same-admission canonical fallback and request validation without opening a wire backend or performing a second execution. **[1.3]**
4. When supported large text content remains within body, shape, and metadata limits, the proxy shall preserve streaming proof and replay without retaining the full prompt in metadata buffers, and shall preserve canonical semantic identity and final aggregate-fact limits. **[1.4]**

## Requirement 2: Acceptance belongs to one runtime composition
**Objective:** As an operator reloading configuration, I want acceptance from one composition to be unusable in a different composition.
### Acceptance Criteria
1. When independently built assessor/executor compositions coexist, the proxy shall assign distinct bounded acceptance identities even if their economic publication is unchanged or absent. **[2.1]**
2. If an accepted proof is executed against another composition's identity or candidate-domain identity, the proxy shall reject before session mutation or upstream opening, without falling back to canonical execution. **[2.2]**
3. While a request remains pinned to its admitted composition, the proxy shall retain that composition's identity and execution domain despite publication of a replacement. **[2.3]**
4. When a candidate composition fails or is discarded, the proxy shall leave active composition identity, economic publication, and pinned request behavior unchanged. **[2.4]**

## Requirement 3: Routing receives the authoritative bound session
**Objective:** As a client using session-affine routing, I want an accepted fresh-session wire request to use the session actually created for my turn.
### Acceptance Criteria
1. When wire execution binds its authoritative session, the proxy shall make that bound session, resolved principal, scope, and workspace available to the same downstream routing consumers as canonical execution. **[3.1]**
2. When session-affine routing is selected and an authoritative session exists, the proxy shall use its identity for affinity rather than ignore affinity or report missing identity solely because the request used replay. **[3.2]**
3. If the required trusted affinity identity is genuinely absent, the proxy shall retain the configured ignore or fail-closed behavior without synthesizing an identity from body metadata or an untrusted client hint. **[3.3]**
4. While initial, parallel, or permitted replacement attempts consume admitted facts, the proxy shall preserve the frozen bound session and related views rather than reread replacement-context or newly published request facts. **[3.4]**

## Requirement 4: Trusted native economic evidence survives wire acquisition
**Objective:** As an operator observing provider work, I want the wire representation to retain the native evidence and trusted attribution already available to canonical execution.
### Acceptance Criteria
1. When an eligible request has a trusted StoreID resolver, the proxy shall resolve it once for that request and freeze the result with its existing billing identity; an empty result shall remain explicitly absent. **[4.1]**
2. When a successful non-nil provider stream supports native evidence binding, the proxy shall bind it once to the current trusted store, request, billing call, A-leg, B-leg, and attempt sequence before publishing the attempt for downstream consumption. **[4.2]**
3. While provider streams pass through wire acquisition adapters, the proxy shall preserve their existing native observation and economic-checkpoint access so the current evidence consumers can obtain it. **[4.3]**
4. When initial, parallel, failed, canceled, or permitted replacement attempts produce native economic evidence, the proxy shall retain existing bounded per-attempt attribution and terminal ownership without duplicating observations or mixing sibling identities. **[4.4]**
5. If trusted identity or native evidence capability is unavailable, the proxy shall preserve established absent-evidence behavior without fabricating StoreID, native usage, or financial finality, and shall preserve all existing paid-exposure and unsupported-feature wire blockers. **[4.5]**

## Requirement 5: Response creation uses the configured clock
**Objective:** As an OpenAI client, I want representation choice not to change response timestamp semantics.
### Acceptance Criteria
1. When an OpenAI Responses or Chat handler has a configured response wall clock, the proxy shall use it for wire response creation timestamps as it does for canonical encoding. **[5.1]**
2. When encoding one streaming response, the proxy shall sample the selected response creation time once and reuse that value across its frames rather than advance it per event. **[5.2]**
3. If no response wall clock is configured, the proxy shall retain existing deterministic identity-derived timestamp fallback and identifier semantics. **[5.3]**
4. When a wire response is collected through an existing non-streaming writer, the proxy shall apply the same clock precedence without enabling a new non-streaming wire eligibility path or changing session/cancellation carriers. **[5.4]**

## Requirements Review
Five areas and 21 criteria cover only the selected correctness slice. A pre-write review clarified cumulative decoded metadata charging versus final fact size, trusted absent identity versus a fabricated default, and configured response time versus immutable identity seeds. Streaming execution eligibility and existing billing blockers remain unchanged; testing and measurement are task evidence, not separate feature requirements.
