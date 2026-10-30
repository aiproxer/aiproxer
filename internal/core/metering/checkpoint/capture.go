package checkpoint

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Snapshot is an in-memory metering checkpoint: public journal-safe fields plus
// a sanitized Call clone used for recount/rerate. The Call body is never written
// to the metering journal by default (requirement 2.7).
type Snapshot struct {
	Public   metering.Checkpoint
	Call     aipapi.Call
	Evidence *WireAttemptEvidence
}

// BindScope updates Public.Scope without mutating the immutable Call clone.
func (s *Snapshot) BindScope(sc scope.PrincipalScopeView) {
	if s == nil {
		return
	}
	s.Public.Scope = sc.Clone()
}

// FrontendIngressInput captures a logical-request frontend-ingress checkpoint.
type FrontendIngressInput struct {
	Call         aipapi.Call
	Scope        scope.PrincipalScopeView
	FrontendID   string
	CheckpointID string
	StreamID     string
	TraceID      string // runtime trace; defaults to Call.ID when empty
	Perspective  metering.EconomicPerspective
	Now          time.Time
}

func buildFrontendIngressPublicCheckpoint(
	id string,
	streamID string,
	perspective metering.EconomicPerspective,
	corr metering.Correlation,
	sc scope.PrincipalScopeView,
	frontendID string,
	now time.Time,
) (metering.Checkpoint, error) {
	if id == "" {
		return metering.Checkpoint{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	if streamID == "" || streamID == "customer-request:" {
		return metering.Checkpoint{}, fmt.Errorf("metering/checkpoint: stream_id or call.id required")
	}
	if perspective == "" {
		perspective = metering.PerspectiveCustomer
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	pub := metering.Checkpoint{
		CheckpointID: id,
		StreamID:     streamID,
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  perspective,
		Correlation:  corr,
		Scope:        sc.Clone(),
		FrontendID:   strings.TrimSpace(frontendID),
		Presence:     metering.PresenceUnknown,
		Source:       metering.SourceObserved,
		Authority:    metering.AuthorityEstimated,
		CapturedAt:   now,
	}
	if err := pub.Validate(); err != nil {
		return metering.Checkpoint{}, err
	}
	return pub, nil
}

// CaptureFrontendIngress clones the call before submit mutation, strips resume
// secrets, and builds a public Checkpoint (requirements 2.1, 2.5–2.8).
// It does not create usage-authority reservations.
func CaptureFrontendIngress(in FrontendIngressInput) (Snapshot, error) {
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "customer-request:" + strings.TrimSpace(in.Call.ID)
	}
	if streamID == "customer-request:" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: stream_id or call.id required")
	}
	cloned := SanitizeCall(aipapi.CloneCall(in.Call))
	traceID := strings.TrimSpace(in.TraceID)
	if traceID == "" {
		traceID = strings.TrimSpace(cloned.ID)
	}
	corr := metering.Correlation{
		RequestID: strings.TrimSpace(cloned.ID),
		ALegID:    strings.TrimSpace(cloned.Session.ALegID),
		SessionID: cloned.Session.CorrelationID(),
		TraceID:   traceID,
	}
	pub, err := buildFrontendIngressPublicCheckpoint(
		id,
		streamID,
		in.Perspective,
		corr,
		in.Scope,
		in.FrontendID,
		in.Now,
	)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Public: pub, Call: cloned}
	snap.DeriveAndApplyIngressQuantities()
	return snap, nil
}

// WireFrontendIngressInput captures a logical-request frontend-ingress checkpoint
// from bounded facts on the wire path without requiring or retaining a aipapi.Call
// (Requirements 15.1–15.3, 16.1–16.6, 19).
type WireFrontendIngressInput struct {
	RequestID       string
	TraceID         string // runtime trace; defaults to RequestID when empty
	CheckpointID    string // defaults to "customer-request:" + RequestID
	StreamID        string // defaults to "customer-request:" + RequestID
	Scope           scope.PrincipalScopeView
	FrontendID      string
	ALegID          string
	SessionID       string // authoritative session ID or correlation ID
	MaxOutputTokens *int   // optional max output token bound
	Perspective     metering.EconomicPerspective
	Now             time.Time
}

// CaptureWireFrontendIngress builds an immutable frontend-ingress checkpoint strictly
// from bounded wire facts, sharing exact quantity and checkpoint validation logic
// with canonical execution while guaranteeing that no aipapi.Call is cloned or retained
// (Requirements 15.1–15.3, 16.1–16.6, 19).
func CaptureWireFrontendIngress(in WireFrontendIngressInput) (Snapshot, error) {
	reqID := strings.TrimSpace(in.RequestID)
	if reqID == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: request_id required")
	}
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		id = "customer-request:" + reqID
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "customer-request:" + reqID
	}
	traceID := strings.TrimSpace(in.TraceID)
	if traceID == "" {
		traceID = reqID
	}
	corr := metering.Correlation{
		RequestID: reqID,
		ALegID:    strings.TrimSpace(in.ALegID),
		SessionID: strings.TrimSpace(in.SessionID),
		TraceID:   traceID,
	}
	pub, err := buildFrontendIngressPublicCheckpoint(
		id,
		streamID,
		in.Perspective,
		corr,
		in.Scope,
		in.FrontendID,
		in.Now,
	)
	if err != nil {
		return Snapshot{}, err
	}

	var maxOutput *int64
	if in.MaxOutputTokens != nil {
		v := int64(*in.MaxOutputTokens)
		maxOutput = &v
	}

	snap := Snapshot{Public: pub, Call: aipapi.Call{}}
	snap.ApplyQuantities(QuantitiesFromCountAndMaxOutput64(maxOutput))
	return snap, nil
}

// RequestHolder retains the single frontend-ingress snapshot for one logical request
// and per-attempt backend-ingress freezes. Methods are safe for concurrent use by
// parallel racing attempts that store distinct AttemptID keys.
type RequestHolder struct {
	mu                    sync.Mutex
	FrontendIngress       *Snapshot
	BackendIngress        map[string]*Snapshot // keyed by AttemptID
	frontendIngressFactID string
	backendIngressFactIDs map[string]string // AttemptID -> bound metering FactID
	nextSeq               int64
}

// IngressSequence is the deterministic stream sequence for the single ingress
// fact on a customer-request or operator-attempt stream (task 3.3 / D6).
const IngressSequence int64 = 1

// FrontendIngressIdentity returns restart-stable FactID, SourceID, and Sequence
// for one logical-request FE ingress (independent of retry count).
func FrontendIngressIdentity(requestID string) (factID, sourceID string, seq int64) {
	id := "fe-ingress:" + strings.TrimSpace(requestID)
	return id, id, IngressSequence
}

// BackendIngressIdentity returns restart-stable FactID, SourceID, and Sequence
// for one operator-attempt BE ingress (independent of retry count).
func BackendIngressIdentity(attemptID string) (factID, sourceID string, seq int64) {
	id := "be-ingress:" + strings.TrimSpace(attemptID)
	return id, id, IngressSequence
}

// NextSequence returns a monotonically increasing fact sequence for this request.
func (h *RequestHolder) NextSequence() int64 {
	if h == nil {
		return 1
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSeq++
	return h.nextSeq
}

// ReserveSequenceFloor ensures later NextSequence values stay above reserved
// deterministic ingress sequences on this holder.
func (h *RequestHolder) ReserveSequenceFloor(seq int64) {
	if h == nil || seq < 1 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.nextSeq < seq {
		h.nextSeq = seq
	}
}

// CaptureOrReuseFrontendIngress returns the existing FE ingress snapshot when set,
// otherwise captures and stores a new one (requirement 2.8).
func (h *RequestHolder) CaptureOrReuseFrontendIngress(in FrontendIngressInput) (Snapshot, error) {
	if h == nil {
		return CaptureFrontendIngress(in)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FrontendIngress != nil {
		return *h.FrontendIngress, nil
	}
	snap, err := CaptureFrontendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	cp := snap
	h.FrontendIngress = &cp
	return snap, nil
}

// CaptureOrReuseWireFrontendIngress returns the existing FE ingress snapshot when set,
// otherwise captures and stores a new wire-native one without retaining a Call
// (Requirements 15.1–15.3, 16, 19).
func (h *RequestHolder) CaptureOrReuseWireFrontendIngress(in WireFrontendIngressInput) (Snapshot, error) {
	if h == nil {
		return CaptureWireFrontendIngress(in)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FrontendIngress != nil {
		return *h.FrontendIngress, nil
	}
	snap, err := CaptureWireFrontendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	cp := snap
	h.FrontendIngress = &cp
	return snap, nil
}

// BackendIngressInput captures a backend-attempt freeze immediately before Open.
type BackendIngressInput struct {
	Call         aipapi.Call
	Scope        scope.PrincipalScopeView
	AttemptID    string
	BLegID       string
	ALegID       string
	BackendID    string
	Model        string
	CheckpointID string
	StreamID     string
	TraceID      string // runtime trace; defaults to Call.ID when empty (never FE stream id)
	Perspective  metering.EconomicPerspective
	Now          time.Time
}

func buildBackendIngressPublicCheckpoint(
	id string,
	streamID string,
	perspective metering.EconomicPerspective,
	corr metering.Correlation,
	sc scope.PrincipalScopeView,
	backendID string,
	model string,
	now time.Time,
) (metering.Checkpoint, error) {
	if id == "" {
		return metering.Checkpoint{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	if streamID == "" {
		return metering.Checkpoint{}, fmt.Errorf("metering/checkpoint: stream_id required")
	}
	if perspective == "" {
		perspective = metering.PerspectiveOperator
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	pub := metering.Checkpoint{
		CheckpointID: id,
		StreamID:     streamID,
		Boundary:     metering.BoundaryBackendIngress,
		Lifecycle:    metering.LifecycleBackendAttempt,
		Perspective:  perspective,
		Correlation:  corr,
		Scope:        sc.Clone(),
		BackendID:    strings.TrimSpace(backendID),
		Model:        strings.TrimSpace(model),
		Presence:     metering.PresenceUnknown,
		Source:       metering.SourceObserved,
		Authority:    metering.AuthorityEstimated,
		CapturedAt:   now,
	}
	if err := pub.Validate(); err != nil {
		return metering.Checkpoint{}, err
	}
	return pub, nil
}

// CaptureBackendIngress freezes the final provider-neutral attempt call
// (requirements 2.2, 5.1). Callers must AssertNotWidened before Open if the
// working call may still mutate.
func CaptureBackendIngress(in BackendIngressInput) (Snapshot, error) {
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: checkpoint_id required")
	}
	attemptID := strings.TrimSpace(in.AttemptID)
	bLegID := strings.TrimSpace(in.BLegID)
	if attemptID == "" || bLegID == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: attempt_id and b_leg_id required")
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "operator-attempt:" + attemptID
	}
	perspective := in.Perspective
	if perspective == "" {
		perspective = metering.PerspectiveOperator
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cloned := SanitizeCall(aipapi.CloneCall(in.Call))
	aLeg := strings.TrimSpace(in.ALegID)
	if aLeg == "" {
		aLeg = strings.TrimSpace(cloned.Session.ALegID)
	}
	traceID := strings.TrimSpace(in.TraceID)
	if traceID == "" {
		traceID = strings.TrimSpace(cloned.ID)
	}
	pub, err := buildBackendIngressPublicCheckpoint(
		id,
		streamID,
		perspective,
		metering.Correlation{
			RequestID: strings.TrimSpace(cloned.ID),
			ALegID:    aLeg,
			BLegID:    bLegID,
			AttemptID: attemptID,
			SessionID: cloned.Session.CorrelationID(),
			TraceID:   traceID,
		},
		in.Scope,
		in.BackendID,
		in.Model,
		now,
	)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Public: pub, Call: cloned}
	snap.DeriveAndApplyIngressQuantities()
	return snap, nil
}

// WireBackendIngressInput captures an immutable backend-attempt freeze from bounded
// wire facts immediately before Open, without requiring or retaining a aipapi.Call
// (Requirements 10, 15.1–15.3, 19).
type WireBackendIngressInput struct {
	RequestID       string
	TraceID         string // runtime trace; defaults to RequestID when empty
	AttemptID       string
	BLegID          string // defaults to AttemptID when empty
	ALegID          string
	SessionID       string // authoritative session ID or correlation ID
	Scope           scope.PrincipalScopeView
	BackendID       string
	Model           string
	CheckpointID    string // defaults to "operator-attempt:" + AttemptID
	StreamID        string // defaults to "operator-attempt:" + AttemptID
	MaxOutputTokens *int   // optional max output token bound
	Perspective     metering.EconomicPerspective
	Now             time.Time

	// Digest evidence (Req 15.3, Design 10):
	SourceDigest  [32]byte // SHA-256 digest of captured source payload
	RewriteDigest [32]byte // digest of model rewrite token / splice (or zero if no rewrite)
	AttemptDigest [32]byte // composite attempt digest
}

// CaptureWireBackendIngress builds an immutable backend-attempt checkpoint strictly
// from bounded wire facts, sharing exact quantity and checkpoint validation logic
// with canonical execution while guaranteeing that no aipapi.Call is cloned or retained
// (Requirements 10, 15.1–15.3, 19).
func CaptureWireBackendIngress(in WireBackendIngressInput) (Snapshot, error) {
	reqID := strings.TrimSpace(in.RequestID)
	if reqID == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: request_id required")
	}
	attemptID := strings.TrimSpace(in.AttemptID)
	if attemptID == "" {
		return Snapshot{}, fmt.Errorf("metering/checkpoint: attempt_id required")
	}
	bLegID := strings.TrimSpace(in.BLegID)
	if bLegID == "" {
		bLegID = attemptID
	}
	id := strings.TrimSpace(in.CheckpointID)
	if id == "" {
		id = "operator-attempt:" + attemptID
	}
	streamID := strings.TrimSpace(in.StreamID)
	if streamID == "" {
		streamID = "operator-attempt:" + attemptID
	}
	traceID := strings.TrimSpace(in.TraceID)
	if traceID == "" {
		traceID = reqID
	}
	perspective := in.Perspective
	if perspective == "" {
		perspective = metering.PerspectiveOperator
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	corr := metering.Correlation{
		RequestID: reqID,
		ALegID:    strings.TrimSpace(in.ALegID),
		BLegID:    bLegID,
		AttemptID: attemptID,
		SessionID: strings.TrimSpace(in.SessionID),
		TraceID:   traceID,
	}
	pub, err := buildBackendIngressPublicCheckpoint(
		id,
		streamID,
		perspective,
		corr,
		in.Scope,
		in.BackendID,
		in.Model,
		now,
	)
	if err != nil {
		return Snapshot{}, err
	}

	attDigest := in.AttemptDigest
	if attDigest == [32]byte{} && in.SourceDigest != [32]byte{} {
		attDigest = ComputeAttemptDigest(in.SourceDigest, in.RewriteDigest, in.Model)
	}

	snap := Snapshot{
		Public: pub,
		Call:   aipapi.Call{},
		Evidence: &WireAttemptEvidence{
			SourceDigest:    in.SourceDigest,
			RewriteDigest:   in.RewriteDigest,
			AttemptDigest:   attDigest,
			Model:           strings.TrimSpace(in.Model),
			MaxOutputTokens: in.MaxOutputTokens,
		},
	}
	snap.ApplyQuantities(QuantitiesFromCountAndMaxOutput(in.MaxOutputTokens))
	return snap, nil
}

// StoreBackendIngress captures and retains a backend-ingress snapshot for an attempt.
func (h *RequestHolder) StoreBackendIngress(in BackendIngressInput) (Snapshot, error) {
	snap, err := CaptureBackendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	if h == nil {
		return snap, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		h.BackendIngress = make(map[string]*Snapshot)
	}
	cp := snap
	h.BackendIngress[strings.TrimSpace(in.AttemptID)] = &cp
	return snap, nil
}

// StoreWireBackendIngress captures and retains a wire-native backend-ingress snapshot for an attempt
// (Requirements 10, 15.1–15.3, 19).
func (h *RequestHolder) StoreWireBackendIngress(in WireBackendIngressInput) (Snapshot, error) {
	snap, err := CaptureWireBackendIngress(in)
	if err != nil {
		return Snapshot{}, err
	}
	if h == nil {
		return snap, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		h.BackendIngress = make(map[string]*Snapshot)
	}
	cp := snap
	h.BackendIngress[strings.TrimSpace(in.AttemptID)] = &cp
	return snap, nil
}

// BackendIngressFor returns the frozen snapshot for an attempt, if any.
func (h *RequestHolder) BackendIngressFor(attemptID string) *Snapshot {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		return nil
	}
	return h.BackendIngress[strings.TrimSpace(attemptID)]
}

// MergeFrontendIngressQuantities merges deferred counts into the FE snapshot
// without changing CheckpointID or the frozen Call (design deferred counting).
func (h *RequestHolder) MergeFrontendIngressQuantities(additions []metering.Quantity) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FrontendIngress == nil {
		return
	}
	h.FrontendIngress.MergeQuantities(additions)
}

// MergeBackendIngressQuantities merges deferred counts into a stored BE snapshot.
func (h *RequestHolder) MergeBackendIngressQuantities(attemptID string, additions []metering.Quantity) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.BackendIngress == nil {
		return false
	}
	snap := h.BackendIngress[strings.TrimSpace(attemptID)]
	if snap == nil {
		return false
	}
	snap.MergeQuantities(additions)
	return true
}

// BindFrontendIngressFactID records the journal FactID for the FE ingress fact.
func (h *RequestHolder) BindFrontendIngressFactID(factID string) {
	if h == nil {
		return
	}
	factID = strings.TrimSpace(factID)
	if factID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frontendIngressFactID = factID
}

// FrontendIngressFactID returns the bound FE ingress journal FactID, if any.
func (h *RequestHolder) FrontendIngressFactID() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frontendIngressFactID
}

// BindBackendIngressFactID records the journal FactID for a frozen attempt.
func (h *RequestHolder) BindBackendIngressFactID(attemptID, factID string) {
	if h == nil {
		return
	}
	attemptID = strings.TrimSpace(attemptID)
	factID = strings.TrimSpace(factID)
	if attemptID == "" || factID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.backendIngressFactIDs == nil {
		h.backendIngressFactIDs = make(map[string]string)
	}
	h.backendIngressFactIDs[attemptID] = factID
}

// BackendIngressFactID returns the bound journal FactID for an attempt, if any.
func (h *RequestHolder) BackendIngressFactID(attemptID string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.backendIngressFactIDs == nil {
		return ""
	}
	return h.backendIngressFactIDs[strings.TrimSpace(attemptID)]
}
