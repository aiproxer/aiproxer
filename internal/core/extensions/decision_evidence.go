package extensions

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// EvidenceEmitter delivers bounded, normalized policy decision records to the
// configured policy observer and structured logs (requirements 7.1, 7.3, 7.4, 7.6,
// 7.7). Observer failures and log failures are isolated from request execution in
// this spec: a misbehaving observer or sink cannot change runtime outcomes.
//
// Privileged-visibility gating: records marked EvidencePrivileged are withheld from
// the observer and structured logs unless DiagnosticsEnabled is true. Default
// (EvidenceDefault) records are always eligible for emission after normalization.
//
// High-cardinality values such as trace and leg identifiers may be structured log
// attributes but must not become metric labels (this emitter does not emit metrics).
type EvidenceEmitter struct {
	observer           policydecision.Observer
	logger             *slog.Logger
	diagnosticsEnabled bool
}

// NewEvidenceEmitter returns an emitter that delivers records to obs (defaulting to
// [policydecision.NoopObserver] when nil) and to logger when non-nil.
// diagnosticsEnabled controls whether privileged-visibility records may leave the
// core (requirement 7.4).
func NewEvidenceEmitter(obs policydecision.Observer, logger *slog.Logger, diagnosticsEnabled bool) *EvidenceEmitter {
	if obs == nil {
		obs = policydecision.NoopObserver{}
	}
	return &EvidenceEmitter{
		observer:           obs,
		logger:             logger,
		diagnosticsEnabled: diagnosticsEnabled,
	}
}

// Emit normalizes record and delivers it to the observer and structured logs.
// Privileged records are withheld unless diagnostics is enabled. Illegal records
// (unknown stage/outcome/effect or illegal outcome/effect pair) are dropped before
// normalization and delivery; the drop is logged at warn level with bounded fields
// and never changes request execution (requirements 1.5, 6.6, 7.3, 7.6, 7.7). The
// drop log is a distinct sink call, not a recursive Emit, so a malformed record
// cannot trigger further emission. Observer and log failures are ignored so request
// execution is unaffected. ctx is passed to the observer for its own lifecycle; ctx
// is never stored.
func (e *EvidenceEmitter) Emit(ctx context.Context, record policydecision.Record) {
	if e == nil {
		return
	}
	if record.Visibility == policydecision.EvidencePrivileged && !e.diagnosticsEnabled {
		return
	}
	// Validate before normalization (requirement 1.5, 6.6). Stage identifiers may
	// carry surrounding whitespace that normalization would trim; trim the stage for
	// the legality check only so legitimately bounded records are not rejected.
	validateRecord := record
	validateRecord.Stage = strings.TrimSpace(record.Stage)
	if err := policydecision.ValidateRecord(validateRecord); err != nil {
		if e.logger != nil {
			e.logger.LogAttrs(
				ctx, slog.LevelWarn, "policy decision evidence dropped",
				slog.String("stage", validateRecord.Stage),
				slog.String("outcome", string(record.Outcome)),
				slog.String("effect", string(record.Effect)),
				slog.String("reason", err.Error()),
			)
		}
		return
	}
	normalized := policydecision.NormalizeRecord(record)
	if e.observer != nil {
		_ = e.observer.OnPolicyDecision(ctx, normalized.Clone())
	}
	if e.logger != nil {
		e.logger.LogAttrs(
			ctx, slog.LevelInfo, "policy decision",
			slog.String("stage", normalized.Stage),
			slog.String("provider_id", normalized.Provider.ID),
			slog.String("outcome", string(normalized.Outcome)),
			slog.String("effect", string(normalized.Effect)),
			slog.String("reason_code", normalized.ReasonCode),
			slog.String("failure_behavior", string(normalized.FailureBehavior)),
			slog.String("trace_id", normalized.TraceID),
			slog.String("a_leg_id", normalized.ALegID),
			slog.String("b_leg_id", normalized.BLegID),
			slog.Int("attempt_seq", normalized.AttemptSeq),
			slog.Bool("output_committed", normalized.OutputCommitted),
			slog.Bool("backend_attempted", normalized.BackendAttempted),
		)
	}
}

// DiagnosticsEnabled reports whether privileged-visibility records may leave the
// core through this emitter.
func (e *EvidenceEmitter) DiagnosticsEnabled() bool {
	if e == nil {
		return false
	}
	return e.diagnosticsEnabled
}

// Observer returns the bound policy observer (for composition roots that want to
// fan out additional observers).
func (e *EvidenceEmitter) Observer() policydecision.Observer {
	if e == nil {
		return nil
	}
	return e.observer
}
