package runtime

// Conversation-view pre-B-leg seam (Task 3.1)
//
// Ordering (characterized in TestExecutor_PreparationOrderCharacterization):
//
//   Validate → SecureSession.BeginTurn → FetchALeg → RouteAuthoritySnapshotBarrier
//   → captureFrontendIngressBeforeSubmit → admitRequestAuthorityOnce → RunSubmit
//   → CTP emit (traffic.LegCTP) → [SEAM] → ToolCatalog/RequestTransform/PreRequest
//   → RouteSelector override + RouteHint → PromptCacheMaintenance.BeginRealTurn → stampBillingCallID
//   → StartALeg → route planning / billing / capability → B-leg open.
//
// The seam is placed after authoritative A-leg + secret/submit/CTP boundaries
// and before inference-specific transforms/billing/route work. It preserves a
// deep-cloned accepted ingress view (ingressCall) separate from the backend
// working call (call). Future tasks will snapshot conversation-view state and
// project at this point; 3.1 only guarantees ordering and clone isolation with
// no-op behavior.
//
// Invariants for 3.1:
//   - ingressCall is aipapi.CloneCall of the accepted call after CTP and before
//     ToolCatalog/RequestTransform/PreRequest. It is the unmodified client/A-leg
//     view for CTP/continuation.
//   - call is a distinct deep clone that carries inference-specific transforms,
//     billing, and routing. Mutating its slices/parts does not affect ingressCall.
//   - With no tags/overlays and no local-turn claim, routing/billing/stream
//     semantics are identical to pre-seam behavior (projection no-op).
//
// Requirements: 5.1-5.2, 5.5, 5.8-5.10, 12.1, 13.6.
// Boundary: internal/core/runtime only. Do not touch runtimebundle/stdhttp here.
//
// Implementation lives in executor_prepare_secure.go (secure path) and
// executor_prepare_detached.go (detached path) where the ingress clone is taken
// after CTP/submit and before backend transforms, and in
// executor_prepare_request.go where preparedRequest carries both views.
//
// No snapshot/project (3.2) or local-turn (3.3) logic is implemented here.
