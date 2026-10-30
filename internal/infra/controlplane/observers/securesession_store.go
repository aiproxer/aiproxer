package observers

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// SecureSessionStoreDecorator decorates an existing [app.Store] so that mutating
// lifecycle operations are projected into the control-plane recorder while the
// authoritative secure-session behavior stays with the delegate (design "Source
// Event Mapping"; task 4.3, requirements 1.2, 1.3, 1.4, 1.6, 3.1, 3.4, 3.7, 5.1,
// 5.3, 6.6, 8.1, 8.5, 10.7).
//
// Behavior:
//   - Every operation delegates to the existing store first. Recording happens
//     only after delegate success so authoritative session/A-leg/B-leg ids are
//     known (design: "Record after delegate succeeds").
//   - Read methods (Load/Summary/Transcript/Audit/ListAttemptEvidence/etc.) and
//     CheckReadiness are pure pass-through; they never record and never change
//     semantics.
//   - TouchActivity, AppendAttemptTrace, UpdateAttemptOutcome, and AddUsage are
//     always best-effort: recording failures degrade status and never change the
//     delegate outcome (requirement 5.3, 10.7). Post-output recording failures
//     never trigger retry/failover/replacement.
//   - Create and AppendAudit use [controlplane.RecorderService.Record] so a
//     configured required-pre-work policy can fail closed before upstream work
//     (design: "Best-effort unless configured required before upstream"). When
//     the control-plane capability is disabled, Record returns
//     [controlplane.ErrDisabled] and the adapter returns the delegate result
//     unchanged (requirement 8.1).
//   - Source event keys are deterministic and never hash raw payloads, headers,
//     tokens, or provider wire data. When a source lacks a stable time, the
//     usage key falls back to a bounded FNV hash of safe correlation fields
//     (design: "bounded hash of safe correlation fields and event time").
//
// The decorator performs no I/O beyond the recorder call and starts no
// goroutines. It does not alter session, attempt, usage, or audit semantics.
type SecureSessionStoreDecorator struct {
	delegate   app.Store
	normalizer *controlplane.Normalizer
	recorder   *controlplane.RecorderService
}

// SecureSessionStoreDecoratorConfig configures a [SecureSessionStoreDecorator].
type SecureSessionStoreDecoratorConfig struct {
	Delegate   app.Store
	Normalizer *controlplane.Normalizer
	// Recorder appends control-plane events. It is concrete because this decorator
	// requires both Record and RecordBestEffort. May be nil to disable recording
	// (the decorator becomes a pass-through to Delegate).
	Recorder *controlplane.RecorderService
}

// NewSecureSessionStoreDecorator returns a decorator that wraps delegate.
func NewSecureSessionStoreDecorator(cfg SecureSessionStoreDecoratorConfig) *SecureSessionStoreDecorator {
	return &SecureSessionStoreDecorator{
		delegate:   cfg.Delegate,
		normalizer: cfg.Normalizer,
		recorder:   cfg.Recorder,
	}
}

// Create delegates session creation and records a session-create event after the
// delegate succeeds.
func (d *SecureSessionStoreDecorator) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	created, err := d.delegate.Create(ctx, rec)
	if err != nil {
		return created, err
	}
	if d.recorder != nil && d.normalizer != nil {
		src := controlplane.SessionSourceRecord{
			SourceEventKey: "secure-create:" + string(created.SessionID),
			OccurredAt:     created.CreatedAt,
			SessionID:      string(created.SessionID),
			ALegID:         created.ALegID,
			Action:         cp.SessionActionCreated,
			Certainty:      "known",
		}
		if err := d.recordSessionRequired(ctx, src); err != nil {
			return created, err
		}
	}
	return created, nil
}

// LoadByID is a pass-through read; it never records.
func (d *SecureSessionStoreDecorator) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	return d.delegate.LoadByID(ctx, id)
}

// LoadByResumeFingerprint is a pass-through read.
func (d *SecureSessionStoreDecorator) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	return d.delegate.LoadByResumeFingerprint(ctx, fp)
}

// LoadByALegID is a pass-through read.
func (d *SecureSessionStoreDecorator) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	return d.delegate.LoadByALegID(ctx, aLegID)
}

// TouchActivity delegates and records a session-update event (best-effort).
func (d *SecureSessionStoreDecorator) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	if err := d.delegate.TouchActivity(ctx, id, at, source); err != nil {
		return err
	}
	if d.recorder != nil && d.normalizer != nil {
		src := controlplane.SessionSourceRecord{
			SourceEventKey: "secure-touch:" + string(id) + ":" + string(source) + ":" + at.Format(time.RFC3339Nano),
			OccurredAt:     at,
			SessionID:      string(id),
			Action:         cp.SessionActionUpdated,
		}
		d.recordSessionBestEffort(ctx, src)
	}
	return nil
}

// AppendAttemptTrace delegates and records an attempt event (best-effort).
func (d *SecureSessionStoreDecorator) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	if err := d.delegate.AppendAttemptTrace(ctx, trace); err != nil {
		return err
	}
	if d.recorder != nil && d.normalizer != nil {
		src := controlplane.AttemptSourceRecord{
			SourceEventKey: "secure-attempt-trace:" + string(trace.SessionID) + ":" + trace.BLegID + ":" + strconv.Itoa(trace.AttemptSeq),
			OccurredAt:     trace.StartedAt,
			SessionID:      string(trace.SessionID),
			ALegID:         trace.ALegID,
			BLegID:         trace.BLegID,
			AttemptSeq:     trace.AttemptSeq,
			BackendID:      trace.ResolvedBackend,
			Model:          trace.ResolvedModel,
			RouteOutcome:   trace.RouteSource,
			Surfaced:       cp.AttemptSurfacedUnknown,
			Outcome:        cp.AttemptOutcomeUnknown,
			StartedAt:      trace.StartedAt,
		}
		d.recordAttemptBestEffort(ctx, src)
	}
	return nil
}

// UpdateAttemptOutcome delegates and records an attempt-outcome event
// (best-effort). Post-output failures never trigger retry/failover.
func (d *SecureSessionStoreDecorator) UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	if err := d.delegate.UpdateAttemptOutcome(ctx, outcome); err != nil {
		return err
	}
	if d.recorder != nil && d.normalizer != nil {
		surfaced, outcomeKind := mapAttemptOutcome(outcome)
		src := controlplane.AttemptSourceRecord{
			SourceEventKey: "secure-attempt-outcome:" + string(outcome.SessionID) + ":" + outcome.BLegID,
			OccurredAt:     outcome.EndedAt,
			SessionID:      string(outcome.SessionID),
			BLegID:         outcome.BLegID,
			Surfaced:       surfaced,
			Outcome:        outcomeKind,
			ErrorClass:     outcome.ErrorCode,
			FinishedAt:     outcome.EndedAt,
		}
		d.recordAttemptBestEffort(ctx, src)
	}
	return nil
}

// AppendTranscript is a pass-through; transcripts are not projected into the
// control-plane ledger by this decorator (design source mapping does not list
// transcript appends as a recorded category).
func (d *SecureSessionStoreDecorator) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	return d.delegate.AppendTranscript(ctx, item)
}

// NextTranscriptSeq is a pass-through read.
func (d *SecureSessionStoreDecorator) NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	return d.delegate.NextTranscriptSeq(ctx, id)
}

// AddUsage delegates and records a usage event (best-effort). Raw usage JSON
// from the delta is never carried into the normalized event.
func (d *SecureSessionStoreDecorator) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	if err := d.delegate.AddUsage(ctx, delta); err != nil {
		return err
	}
	if d.recorder != nil && d.normalizer != nil {
		occurred := delta.ProxyCompletedAt
		if occurred.IsZero() {
			occurred = delta.RequestStartedAt
		}
		src := controlplane.UsageSourceRecord{
			SourceEventKey:   "secure-usage:" + string(delta.SessionID) + ":" + string(delta.TurnID) + ":" + delta.BLegID + ":" + usageKeySuffix(delta, occurred),
			OccurredAt:       occurred,
			SessionID:        string(delta.SessionID),
			BLegID:           delta.BLegID,
			InputTokens:      int(delta.InputTokens),
			OutputTokens:     int(delta.OutputTokens),
			CacheReadTokens:  int(delta.CacheReadTokens),
			CacheWriteTokens: int(delta.CacheWriteTokens),
			ReasoningTokens:  int(delta.ReasoningTokens),
			TotalTokens:      int(delta.TotalTokens),
			CostNanoUnits:    delta.CostNanoUnits,
			Currency:         delta.Currency,
			CostSource:       delta.CostSource,
		}
		d.recordUsageBestEffort(ctx, src)
	}
	return nil
}

// NextAuditSeq is a pass-through read.
func (d *SecureSessionStoreDecorator) NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	return d.delegate.NextAuditSeq(ctx, id)
}

// AppendAudit delegates and records an audit event after the delegate succeeds.
// When configured required pre-work, a recording failure fails closed before
// upstream work.
func (d *SecureSessionStoreDecorator) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	if err := d.delegate.AppendAudit(ctx, item); err != nil {
		return err
	}
	if d.recorder != nil && d.normalizer != nil {
		src := controlplane.AuditSourceRecord{
			SourceEventKey: "secure-audit:" + string(item.SessionID) + ":" + string(item.TurnID) + ":" + item.Action + ":" + strconv.FormatInt(item.Seq, 10),
			OccurredAt:     item.CreatedAt,
			SessionID:      string(item.SessionID),
			Action:         item.Action,
			Result:         item.Result,
		}
		if err := d.recordAudit(ctx, src); err != nil {
			return err
		}
	}
	return nil
}

// Audit is a pass-through read.
func (d *SecureSessionStoreDecorator) Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error) {
	return d.delegate.Audit(ctx, id, opts)
}

// Summary is a pass-through read.
func (d *SecureSessionStoreDecorator) Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error) {
	return d.delegate.Summary(ctx, query)
}

// Transcript is a pass-through read.
func (d *SecureSessionStoreDecorator) Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error) {
	return d.delegate.Transcript(ctx, id, opts)
}

// ListAttemptEvidence is a pass-through read.
func (d *SecureSessionStoreDecorator) ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	return d.delegate.ListAttemptEvidence(ctx, id, opts)
}

// CheckReadiness is a pass-through.
func (d *SecureSessionStoreDecorator) CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error {
	return d.delegate.CheckReadiness(ctx, policy)
}

// Quarantine is a pass-through to the delegate store.
func (d *SecureSessionStoreDecorator) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	return d.delegate.Quarantine(ctx, in)
}

// UsageTokenTotals preserves the optional [app.SessionUsageRollup] surface when
// the delegate implements it, so existing secure-session detail/by-A-leg
// diagnostics (which type-assert from [app.Store]) keep reporting per-session
// usage totals unchanged when control-plane recording wraps the store
// (requirement 8.5, 10.6). When the delegate does not implement the extension,
// the decorator reports the same not-available result a bare non-rolling store
// would have produced.
func (d *SecureSessionStoreDecorator) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	u, ok := d.delegate.(app.SessionUsageRollup)
	if !ok {
		return 0, 0, nil
	}
	return u.UsageTokenTotals(ctx, id)
}

// recordSessionBestEffort normalizes a session source record and appends it
// best-effort. Failures degrade status only and never surface to the caller.
func (d *SecureSessionStoreDecorator) recordSessionBestEffort(ctx context.Context, src controlplane.SessionSourceRecord) {
	ev, err := d.normalizer.FromSessionRecord(src)
	if err != nil {
		return
	}
	_, recErr := d.recorder.RecordBestEffort(ctx, ev)
	ignoreBestEffortRecorderErr(recErr)
}

// recordAttemptBestEffort normalizes an attempt source record and appends it
// best-effort. Failures degrade status only and never surface to the caller.
func (d *SecureSessionStoreDecorator) recordAttemptBestEffort(ctx context.Context, src controlplane.AttemptSourceRecord) {
	ev, err := d.normalizer.FromAttempt(src)
	if err != nil {
		return
	}
	_, recErr := d.recorder.RecordBestEffort(ctx, ev)
	ignoreBestEffortRecorderErr(recErr)
}

// recordUsageBestEffort normalizes a usage source record and appends it
// best-effort. Failures degrade status only and never surface to the caller.
func (d *SecureSessionStoreDecorator) recordUsageBestEffort(ctx context.Context, src controlplane.UsageSourceRecord) {
	ev, err := d.normalizer.FromUsageRecord(src)
	if err != nil {
		return
	}
	_, recErr := d.recorder.RecordBestEffort(ctx, ev)
	ignoreBestEffortRecorderErr(recErr)
}

// recordSessionRequired normalizes a session source record and appends it via
// Recorder.Record so a configured required-pre-work policy can fail closed. It
// returns the recorder error unless the capability is disabled.
func (d *SecureSessionStoreDecorator) recordSessionRequired(ctx context.Context, src controlplane.SessionSourceRecord) error {
	ev, err := d.normalizer.FromSessionRecord(src)
	if err != nil {
		// Propagate normalization failures so a required-pre-work policy can fail
		// closed instead of silently dropping an unrecordable session event.
		return err
	}
	_, recErr := d.recorder.Record(ctx, ev)
	if errors.Is(recErr, controlplane.ErrDisabled) {
		return nil
	}
	return recErr
}

// recordAudit normalizes an audit source record and appends it via
// Recorder.Record so a configured required-pre-work policy can fail closed.
func (d *SecureSessionStoreDecorator) recordAudit(ctx context.Context, src controlplane.AuditSourceRecord) error {
	ev, err := d.normalizer.FromAudit(src)
	if err != nil {
		// Propagate normalization failures so a required-pre-work policy can fail
		// closed instead of silently dropping an unrecordable audit event.
		return err
	}
	_, recErr := d.recorder.Record(ctx, ev)
	if recErr != nil && errors.Is(recErr, controlplane.ErrDisabled) {
		return nil
	}
	return recErr
}

// mapAttemptOutcome maps a secure-session [domain.AttemptOutcome] to control-
// plane surfaced/outcome kinds (requirement 3.2, 3.3). Keep this separate from
// the B2BUA mapper: the source enums encode different state machines.
func mapAttemptOutcome(o domain.AttemptOutcome) (cp.AttemptSurfaced, cp.AttemptOutcome) {
	switch o.SurfaceState {
	case domain.SurfaceSurfaced:
		if o.Success {
			return cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeSucceeded
		}
		return cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeFailed
	case domain.SurfaceSwallowed:
		return cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeFailed
	case domain.SurfaceFailed:
		return cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeFailed
	case domain.SurfaceTimeout:
		return cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeCancelled
	}
	if o.Success {
		return cp.AttemptSurfacedUnknown, cp.AttemptOutcomeSucceeded
	}
	return cp.AttemptSurfacedUnknown, cp.AttemptOutcomeFailed
}

// usageKeySuffix returns the deterministic suffix for a secure-usage source key.
// When a stable time is known it is formatted; otherwise a bounded FNV hash of
// safe correlation fields is used. It never hashes raw payloads, headers, or
// tokens (design: "bounded hash of safe correlation fields and event time").
func usageKeySuffix(delta domain.UsageDelta, occurred time.Time) string {
	if !occurred.IsZero() {
		return occurred.Format(time.RFC3339Nano)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(string(delta.SessionID)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(string(delta.TurnID)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(delta.BLegID))
	// Discriminate distinct usage deltas that share the same IDs when no stable
	// time is available, so two different token/cost deltas do not collide on
	// SourceEventKey and dedupe away real usage (non-sensitive aggregates only).
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(strconv.FormatInt(delta.InputTokens, 10)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(strconv.FormatInt(delta.OutputTokens, 10)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(strconv.FormatInt(delta.TotalTokens, 10)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(strconv.FormatInt(delta.CostNanoUnits, 10)))
	return "h" + strconv.FormatUint(h.Sum64(), 16)
}

var (
	_ app.Store              = (*SecureSessionStoreDecorator)(nil)
	_ app.SessionUsageRollup = (*SecureSessionStoreDecorator)(nil)
)
