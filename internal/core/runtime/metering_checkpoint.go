package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type meteringHolderKey struct{}

func withMeteringHolder(ctx context.Context, h *checkpoint.RequestHolder) context.Context {
	if h == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, meteringHolderKey{}, h)
}

func meteringHolderFrom(ctx context.Context) *checkpoint.RequestHolder {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(meteringHolderKey{}).(*checkpoint.RequestHolder)
	return h
}

// trustedFrontendIngressScope prefers the immutable FE-ingress trusted scope over
// later untrusted request metadata (requirement 5.6 / task 3.3).
func trustedFrontendIngressScope(ctx context.Context, fallback scope.PrincipalScopeView) scope.PrincipalScopeView {
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		return holder.FrontendIngress.Public.Scope.Clone()
	}
	return fallback
}

// captureFrontendIngressBeforeSubmit stores one immutable FE-ingress checkpoint
// before submit hooks mutate the working call (requirement 2.1).
func captureFrontendIngressBeforeSubmit(
	ctx context.Context,
	work aipapi.Call,
	reqScope scope.PrincipalScopeView,
	now time.Time,
) (context.Context, *checkpoint.RequestHolder, error) {
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		holder = &checkpoint.RequestHolder{}
		ctx = withMeteringHolder(ctx, holder)
	}
	id := strings.TrimSpace(work.ID)
	if id == "" {
		return ctx, holder, fmt.Errorf("executor: metering frontend ingress requires call id")
	}
	frontendID := ""
	if fe, ok := execview.FrontendIDFromContext(ctx); ok {
		frontendID = fe
	}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         work,
		Scope:        reqScope,
		FrontendID:   frontendID,
		CheckpointID: "customer-request:" + id,
		StreamID:     "customer-request:" + id,
		TraceID:      id,
		Perspective:  metering.PerspectiveCustomer,
		Now:          now,
	})
	if err != nil {
		return ctx, holder, fmt.Errorf("executor: metering frontend ingress: %w", err)
	}
	return ctx, holder, nil
}

// WireFrontendIngressArgs carries the bounded facts required to capture an immutable
// frontend-ingress checkpoint on the wire fast-path (Requirements 15.1–15.3, 16.1–16.6, 19).
type WireFrontendIngressArgs struct {
	RequestID       string
	TraceID         string
	Scope           scope.PrincipalScopeView
	ALegID          string
	SessionID       string
	MaxOutputTokens *int
	Now             time.Time
}

// captureWireFrontendIngress stores one immutable FE-ingress checkpoint from bounded
// wire facts before backend attempt dispatch, sharing exact helpers with the canonical
// path and never cloning or retaining a aipapi.Call (Requirements 15.1–15.3, 16.1–16.6, 19).
func captureWireFrontendIngress(
	ctx context.Context,
	args WireFrontendIngressArgs,
) (context.Context, *checkpoint.RequestHolder, error) {
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		holder = &checkpoint.RequestHolder{}
		ctx = withMeteringHolder(ctx, holder)
	}
	id := strings.TrimSpace(args.RequestID)
	if id == "" {
		return ctx, holder, fmt.Errorf("executor: metering wire frontend ingress requires request id")
	}
	frontendID := ""
	if fe, ok := execview.FrontendIDFromContext(ctx); ok {
		frontendID = fe
	}
	traceID := strings.TrimSpace(args.TraceID)
	if traceID == "" {
		traceID = id
	}
	now := args.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := holder.CaptureOrReuseWireFrontendIngress(checkpoint.WireFrontendIngressInput{
		RequestID:       id,
		TraceID:         traceID,
		CheckpointID:    "customer-request:" + id,
		StreamID:        "customer-request:" + id,
		Scope:           args.Scope,
		FrontendID:      frontendID,
		ALegID:          args.ALegID,
		SessionID:       args.SessionID,
		MaxOutputTokens: args.MaxOutputTokens,
		Perspective:     metering.PerspectiveCustomer,
		Now:             now,
	})
	if err != nil {
		return ctx, holder, fmt.Errorf("executor: metering wire frontend ingress: %w", err)
	}
	return ctx, holder, nil
}

// WireBackendIngressArgs carries bounded facts required to freeze an immutable
// backend-attempt checkpoint on the wire fast-path (Requirements 10, 15.1–15.3, 19).
type WireBackendIngressArgs struct {
	RequestID       string
	TraceID         string
	AttemptID       string
	BLegID          string
	ALegID          string
	SessionID       string
	Scope           scope.PrincipalScopeView
	BackendID       string
	Model           string
	MaxOutputTokens *int
	Now             time.Time

	SourceDigest  [32]byte
	RewriteDigest [32]byte
	AttemptDigest [32]byte
}

// buildWireBackendIngressSnapshot stores one immutable BE-ingress checkpoint from bounded
// wire facts before backend attempt dispatch, sharing exact helpers with the canonical
// path and never cloning or retaining a aipapi.Call (Requirements 10, 15.1–15.3, 19).
func buildWireBackendIngressSnapshot(
	holder *checkpoint.RequestHolder,
	args WireBackendIngressArgs,
) (checkpoint.Snapshot, error) {
	if holder == nil {
		return checkpoint.Snapshot{}, fmt.Errorf("executor: metering holder required for wire backend ingress")
	}
	attemptID := strings.TrimSpace(args.AttemptID)
	if attemptID == "" {
		return checkpoint.Snapshot{}, fmt.Errorf("executor: metering wire backend ingress requires attempt id")
	}
	bLegID := strings.TrimSpace(args.BLegID)
	if bLegID == "" {
		bLegID = attemptID
	}
	reqID := strings.TrimSpace(args.RequestID)
	if reqID == "" {
		return checkpoint.Snapshot{}, fmt.Errorf("executor: metering wire backend ingress requires request id")
	}
	traceID := strings.TrimSpace(args.TraceID)
	if traceID == "" {
		traceID = reqID
	}
	now := args.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	snap, err := holder.StoreWireBackendIngress(checkpoint.WireBackendIngressInput{
		RequestID:       reqID,
		TraceID:         traceID,
		AttemptID:       attemptID,
		BLegID:          bLegID,
		ALegID:          args.ALegID,
		SessionID:       args.SessionID,
		Scope:           args.Scope,
		BackendID:       args.BackendID,
		Model:           args.Model,
		CheckpointID:    "operator-attempt:" + attemptID,
		StreamID:        "operator-attempt:" + attemptID,
		MaxOutputTokens: args.MaxOutputTokens,
		Perspective:     metering.PerspectiveOperator,
		Now:             now,
		SourceDigest:    args.SourceDigest,
		RewriteDigest:   args.RewriteDigest,
		AttemptDigest:   args.AttemptDigest,
	})
	if err != nil {
		return checkpoint.Snapshot{}, fmt.Errorf("executor: metering wire backend ingress: %w", err)
	}
	return snap, nil
}

// captureWireBackendIngress captures an immutable backend-attempt checkpoint
// from bounded wire facts, inheriting correlated session/request facts from
// holder.FrontendIngress when available (Requirements 10, 15.1–15.3, 19).
func (e *Executor) captureWireBackendIngress(
	ctx context.Context,
	holder *checkpoint.RequestHolder,
	args WireBackendIngressArgs,
) (checkpoint.Snapshot, error) {
	if holder == nil {
		holder = meteringHolderFrom(ctx)
	}
	if holder == nil {
		return checkpoint.Snapshot{}, fmt.Errorf("executor: metering holder required for wire backend ingress")
	}
	if args.Now.IsZero() && e != nil {
		args.Now = e.now()
	}
	if holder.FrontendIngress != nil {
		if args.Scope.PrincipalID.IsUnknown() && !holder.FrontendIngress.Public.Scope.PrincipalID.IsUnknown() {
			args.Scope = holder.FrontendIngress.Public.Scope.Clone()
		}
		if strings.TrimSpace(args.RequestID) == "" {
			args.RequestID = holder.FrontendIngress.Public.Correlation.RequestID
		}
		if strings.TrimSpace(args.TraceID) == "" {
			args.TraceID = holder.FrontendIngress.Public.Correlation.TraceID
		}
		if strings.TrimSpace(args.ALegID) == "" {
			args.ALegID = holder.FrontendIngress.Public.Correlation.ALegID
		}
		if strings.TrimSpace(args.SessionID) == "" {
			args.SessionID = holder.FrontendIngress.Public.Correlation.SessionID
		}
	}
	return buildWireBackendIngressSnapshot(holder, args)
}

// assertWireAttemptNotWidened verifies that current bounded attempt evidence has not
// widened beyond the authorized backend-ingress freeze for attemptID (Requirements 10, 15.3, 19).
func (e *Executor) assertWireAttemptNotWidened(
	holder *checkpoint.RequestHolder,
	attemptID string,
	current checkpoint.WireAttemptEvidence,
) error {
	if holder == nil {
		return nil
	}
	snap := holder.BackendIngressFor(attemptID)
	if snap == nil {
		return nil
	}
	ev, ok := snap.WireAttemptEvidence()
	if !ok {
		return nil
	}
	if err := checkpoint.AssertWireNotWidened(ev, current); err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	return nil
}

// appendMeteringFact appends a fact when a Recorder is configured; nil is a no-op.
func (e *Executor) appendMeteringFact(ctx context.Context, fact metering.Fact) error {
	if e == nil || e.MeteringRecorder == nil {
		return nil
	}
	return e.MeteringRecorder.Append(ctx, fact)
}

// persistFrontendIngressFact appends the customer FE-ingress journal fact when a
// MeteringRecorder is configured and binds its FactID for rating/admission.
// Append failure is returned so strict economic paths fail closed (task 3.3).
// FactID/SourceID/Sequence are deterministic from the logical request so retry
// and process restart SameFactReplay without double-count (D6).
func (e *Executor) persistFrontendIngressFact(ctx context.Context, holder *checkpoint.RequestHolder) (string, error) {
	if holder == nil {
		holder = meteringHolderFrom(ctx)
	}
	if e == nil || holder == nil {
		return "", nil
	}
	fe := holder.FrontendIngress
	if fe == nil {
		return "", nil
	}
	if id := strings.TrimSpace(holder.FrontendIngressFactID()); id != "" {
		return id, nil
	}
	if e.MeteringRecorder == nil {
		return "", nil
	}
	reqID := strings.TrimSpace(fe.Public.Correlation.RequestID)
	if reqID == "" {
		reqID = strings.TrimSpace(fe.Call.ID)
	}
	factID, sourceID, seq := checkpoint.FrontendIngressIdentity(reqID)
	holder.ReserveSequenceFloor(seq)
	fact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint:      fe.Public,
		FactID:          factID,
		Sequence:        seq,
		SourceID:        sourceID,
		IdentityVersion: metering.IdentityVersionV1,
		Quantities:      append([]metering.Quantity(nil), fe.Public.Quantities...),
		Now:             e.now(),
	})
	if err != nil {
		return "", err
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		return "", err
	}
	holder.BindFrontendIngressFactID(factID)
	return factID, nil
}

// enrichFrontendIngressQuantities deferred-counts the immutable FE call via the
// existing AdminCountService (tiktoken/provider path) and merges input_token
// without replacing a Present output_token bound (requirements 2.1, 4.1, 7.2).
func (e *Executor) enrichFrontendIngressQuantities(ctx context.Context) error {
	if e == nil || e.AdminCountService == nil {
		return nil
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return nil
	}
	if _, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken); ok {
		return nil
	}
	if holder.FrontendIngress.IsWire() {
		return nil
	}
	call := holder.FrontendIngress.Call
	count, err := e.AdminCountService.CountCall(ctx, accountingapp.CountCallInput{
		CallID: strings.TrimSpace(call.ID),
		Call:   call,
	})
	if err != nil {
		return err
	}
	holder.MergeFrontendIngressQuantities(countedInputQuantities(count))
	return nil
}

// countedInputQuantities maps a CountResult to deferred ingress additions.
// Output bounds are omitted so MergeQuantities keeps QuantitiesFromCall max-output.
func countedInputQuantities(count accountingapp.CountResult) []metering.Quantity {
	out := []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     int64(count.InputTokens),
		Present:   true,
	}}
	if count.CacheReadTokens > 0 {
		out = append(out, metering.Quantity{
			Component: metering.ComponentCacheReadInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(count.CacheReadTokens),
			Present:   true,
		})
	}
	if count.CacheWriteTokens > 0 {
		out = append(out, metering.Quantity{
			Component: metering.ComponentCacheWriteInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(count.CacheWriteTokens),
			Present:   true,
		})
	}
	return out
}

// enrichBackendIngressQuantities merges deferred operator counts into a stored
// BE snapshot without replacing conservative output bounds (reqs 2.2, 5.1).
// Wire snapshots are safely ignored because no token counting is performed (Req 15.4).
func (e *Executor) enrichBackendIngressQuantities(holder *checkpoint.RequestHolder, attemptID string, count accountingapp.CountResult) {
	if holder == nil {
		return
	}
	if snap := holder.BackendIngressFor(attemptID); snap != nil && snap.IsWire() {
		return
	}
	holder.MergeBackendIngressQuantities(attemptID, countedInputQuantities(count))
}

// enrichBackendIngressQuantitiesWithDecision merges counted inputs and the
// conservative output assumption from the final preflight decision.
// Wire snapshots are safely ignored because no token counting is performed (Req 15.4).
func (e *Executor) enrichBackendIngressQuantitiesWithDecision(
	holder *checkpoint.RequestHolder,
	attemptID string,
	decision accountingpreflight.Decision,
) {
	if holder == nil {
		return
	}
	if snap := holder.BackendIngressFor(attemptID); snap != nil && snap.IsWire() {
		return
	}
	qs := countedInputQuantities(decision.Count)
	if out, ok := explicitOutputQuantity(decision); ok {
		qs = append(qs, out)
	}
	holder.MergeBackendIngressQuantities(attemptID, qs)
}

// persistBackendIngressFact appends a real BE-ingress journal fact when a
// MeteringRecorder is configured and binds its FactID for rating/admission.
// FactID/SourceID/Sequence are deterministic per attempt so failover streams
// stay distinct and Append-fail/restart SameFactReplay without double-count.
func (e *Executor) persistBackendIngressFact(ctx context.Context, holder *checkpoint.RequestHolder, attemptID string) (string, error) {
	if holder == nil {
		holder = meteringHolderFrom(ctx)
	}
	if e == nil || holder == nil {
		return "", nil
	}
	be := holder.BackendIngressFor(attemptID)
	if be == nil {
		return "", nil
	}
	if e.MeteringRecorder == nil {
		return "", nil
	}
	if id := strings.TrimSpace(holder.BackendIngressFactID(attemptID)); id != "" {
		return id, nil
	}
	factID, sourceID, seq := checkpoint.BackendIngressIdentity(attemptID)
	holder.ReserveSequenceFloor(seq)
	fact, err := checkpoint.FactFromIngress(checkpoint.IngressFactInput{
		Checkpoint:      be.Public,
		FactID:          factID,
		Sequence:        seq,
		SourceID:        sourceID,
		IdentityVersion: metering.IdentityVersionV1,
		Quantities:      append([]metering.Quantity(nil), be.Public.Quantities...),
		Now:             e.now(),
	})
	if err != nil {
		return "", err
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		return "", err
	}
	holder.BindBackendIngressFactID(attemptID, factID)
	return factID, nil
}

// WirePreflightAssessmentArgs carries the inputs needed to evaluate token preflight
// during dynamic assessment under the held decode permit (Requirements 6.1–6.3, 15.5, 21; Task 10.4).
type WirePreflightAssessmentArgs struct {
	Backend                  string
	Model                    string
	CallID                   string
	ProfileID                string
	Source                   largebody.Source
	RequestedMaxOutputTokens *int
	Facts                    modelcatalog.ModelFacts
	Semantics                largebody.ExactTokenizerSemantics
	MaxScanBytes             int64
	MaxPermitHoldCPU         time.Duration
}

// assessWirePreflight evaluates token preflight admission for a wire request under the
// held decode permit without materializing prompt text or constructing a aipapi.Call.
// If preflight is disabled or nil, it succeeds with AssessmentDecisionAccept.
// If preflight requires counting and only CountCall is supported, or exact tokenizer
// semantics do not exist, or counting is expensive/unbounded, it returns
// (AssessmentDecisionDecline, DeclineReasonCountingUnsupported, decision).
// If preflight token limits are exceeded, it returns
// (AssessmentDecisionDecline, DeclineReasonAuthorityBlocker, decision).
func (e *Executor) assessWirePreflight(
	ctx context.Context,
	args WirePreflightAssessmentArgs,
) (largebody.AssessmentDecision, largebody.DeclineReason, accountingpreflight.Decision) {
	if e == nil || e.Preflight == nil || !e.Preflight.Enabled() {
		return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, accountingpreflight.Decision{
			Allowed: true,
			Reason:  accountingpreflight.ReasonDisabled,
		}
	}

	decision := e.Preflight.CheckWire(ctx, accountingpreflight.WireInput{
		Backend:                  args.Backend,
		Model:                    args.Model,
		CallID:                   args.CallID,
		ProfileID:                args.ProfileID,
		Source:                   args.Source,
		RequestedMaxOutputTokens: args.RequestedMaxOutputTokens,
		Facts:                    args.Facts,
		Semantics:                args.Semantics,
		MaxScanBytes:             args.MaxScanBytes,
		MaxPermitHoldCPU:         args.MaxPermitHoldCPU,
	})

	if !decision.Allowed {
		if decision.Reason == accountingpreflight.ReasonCountUnavailable {
			return largebody.AssessmentDecisionDecline, largebody.DeclineReasonCountingUnsupported, decision
		}
		return largebody.AssessmentDecisionDecline, largebody.DeclineReasonAuthorityBlocker, decision
	}

	return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, decision
}

// enrichWireFrontendIngressQuantities merges exact measured wire token counting results
// into the stored FrontendIngress snapshot without mutating or retaining a Call (Requirements 15.4, 15.5).
func (e *Executor) enrichWireFrontendIngressQuantities(holder *checkpoint.RequestHolder, count largebody.WireCountResult) {
	if holder == nil || holder.FrontendIngress == nil {
		return
	}
	if _, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken); ok {
		return
	}
	holder.MergeFrontendIngressQuantities(countedInputQuantities(accountingapp.CountResult{
		InputTokens: count.InputTokens,
		TotalTokens: count.TotalTokens,
		Accounting:  count.Accounting,
	}))
}

// enrichWireBackendIngressQuantities merges exact measured wire token counting results
// into the stored BackendIngress snapshot for attemptID without mutating or retaining a Call (Requirements 15.4, 15.5).
func (e *Executor) enrichWireBackendIngressQuantities(holder *checkpoint.RequestHolder, attemptID string, count largebody.WireCountResult) {
	if holder == nil {
		return
	}
	snap := holder.BackendIngressFor(attemptID)
	if snap == nil {
		return
	}
	if _, ok := checkpoint.QuantityComponentValue(snap.Public.Quantities, metering.ComponentInputToken); ok {
		return
	}
	holder.MergeBackendIngressQuantities(attemptID, countedInputQuantities(accountingapp.CountResult{
		InputTokens: count.InputTokens,
		TotalTokens: count.TotalTokens,
		Accounting:  count.Accounting,
	}))
}
