package controlplane

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// Normalizer converts safe auth, session, attempt, usage, policy, and audit
// source DTOs into one validated [cp.Event] shape (requirements 1.1–1.6, 3.1,
// 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 4.1, 4.4, 4.5, 4.6, 4.7, 4.8, 5.3, 9.2, 9.3,
// 10.5, 10.7).
//
// The normalizer enforces:
//   - exactly one typed detail block per event (delegated to [cp.Event.Validate]);
//   - shared trace/request/session/A-leg/B-leg/attempt correlation without
//     inventing identifiers that the source did not supply (requirement 3.6);
//   - safe scope attribution through [ScopeFlattener], preserving unknown vs
//     known-empty values (requirement 4.1, 4.2, 4.3);
//   - rejection of raw credentials, headers, and payload content in any free-
//     text field carried into a normalized event (requirement 4.4, 4.5);
//   - privileged audit/policy evidence is marked privileged + redacted so
//     default-visibility queries do not surface it (requirement 4.6, 9.3).
//
// It does not change source decision outcomes (requirement 1.5, 10.7) and does
// not start goroutines or perform I/O.
type Normalizer struct {
	clock  Clock
	source cp.SourceRef
	scopes *ScopeFlattener
}

// NewNormalizer returns a Normalizer that uses clock for RecordedAt when a
// source DTO does not supply its own time, source to stamp every produced
// event, and scopes to flatten safe attribution.
func NewNormalizer(clock Clock, source cp.SourceRef, scopes *ScopeFlattener) *Normalizer {
	if scopes == nil {
		scopes = NewScopeFlattener()
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &Normalizer{clock: clock, source: source, scopes: scopes}
}

// AttemptSourceRecord is the source DTO for backend attempt evidence projected
// from B2BUA continuity and secure-session attempt records (requirement 1.3,
// 3.2, 3.3). It carries only safe correlation, attribution, and outcome
// fields; raw payloads, headers, and provider wire data must never be placed
// here.
type AttemptSourceRecord struct {
	SourceEventKey string
	OccurredAt     time.Time
	TraceID        string
	RequestID      string
	SessionID      string
	ALegID         string
	BLegID         string
	AttemptSeq     int
	FrontendID     string
	BackendID      string
	Model          string
	RouteOutcome   string
	Surfaced       cp.AttemptSurfaced
	Outcome        cp.AttemptOutcome
	ErrorClass     string
	StartedAt      time.Time
	FinishedAt     time.Time
	Scope          *scope.PrincipalScopeView
}

// AuditSourceRecord is the source DTO for audit evidence projected from
// secure-session audit rows and operator-visible lifecycle events
// (requirement 1.6, 9.3). Action and Result are safe summaries; privileged
// audit evidence is marked via Visibility and RedactionState, not raw fields.
type AuditSourceRecord struct {
	SourceEventKey string
	OccurredAt     time.Time
	TraceID        string
	RequestID      string
	SessionID      string
	ALegID         string
	BLegID         string
	AttemptSeq     int
	Action         string
	Result         string
	ReasonCode     string
	Scope          *scope.PrincipalScopeView
	Visibility     cp.Visibility
	RedactionState cp.RedactionState
}

type eventBaseInput struct {
	SourceEventKey string
	Category       cp.Category
	OccurredAt     time.Time
	Correlation    cp.Correlation
	Scope          *scope.PrincipalScopeView
	Visibility     cp.Visibility
	EvidenceState  cp.EvidenceState
	RedactionState cp.RedactionState
}

// FromAuthDecision converts a safe auth decision event into an auth-category
// control-plane event (requirement 1.1, 3.1, 4.1).
func (n *Normalizer) FromAuthDecision(ev auth.AuthDecisionEvent) (cp.Event, error) {
	if err := rejectUnsafeFreeText(string(ev.Outcome), ev.ReasonCode, ev.PrincipalDisplayName); err != nil {
		return cp.Event{}, fmt.Errorf("%w: auth decision: %v", ErrUnsafeEvidence, err)
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: authDecisionSourceKey(ev),
		Category:       cp.CategoryAuth,
		OccurredAt:     ev.Time,
		Correlation: cp.Correlation{
			TraceID:    ev.TraceID,
			FrontendID: ev.Frontend,
		},
		Scope: ev.Scope,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = &cp.AuthDetail{
		Outcome:    string(ev.Outcome),
		ReasonCode: ev.ReasonCode,
		Frontend:   ev.Frontend,
		AuthMethod: string(ev.HandlerKind),
		IsNew:      false,
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: auth decision: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// FromSessionStart converts a safe session-start event into a session-category
// control-plane event (requirement 1.2, 3.1).
func (n *Normalizer) FromSessionStart(ev auth.SessionStartEvent) (cp.Event, error) {
	if err := rejectUnsafeFreeText(ev.SessionID, ev.ClientSessionRef, ev.PrincipalDisplayName); err != nil {
		return cp.Event{}, fmt.Errorf("%w: session start: %v", ErrUnsafeEvidence, err)
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: sessionStartSourceKey(ev),
		Category:       cp.CategorySession,
		OccurredAt:     ev.Time,
		Correlation: cp.Correlation{
			TraceID:    ev.TraceID,
			SessionID:  ev.SessionID,
			ALegID:     ev.ALegID,
			FrontendID: ev.Frontend,
		},
	})
	if err != nil {
		return cp.Event{}, err
	}
	action := cp.SessionActionResumed
	if ev.IsNew {
		action = cp.SessionActionCreated
	}
	if ev.Certainty == auth.SessionCertaintyUnknown && !ev.IsNew {
		action = cp.SessionActionDenied
	}
	out.Detail = &cp.SessionDetail{
		SessionID:        ev.SessionID,
		ClientSessionRef: ev.ClientSessionRef,
		ALegID:           ev.ALegID,
		Action:           action,
		Certainty:        string(ev.Certainty),
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: session start: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// SessionSourceRecord is the source DTO for session lifecycle evidence projected
// from secure-session store events (design "Source Event Mapping"; task 4.3). It
// carries an explicit SourceEventKey so secure-session decorators can deduplicate
// repeated projections independently of the auth session-start path. Raw
// credentials, headers, and payloads must never be placed here.
type SessionSourceRecord struct {
	SourceEventKey   string
	OccurredAt       time.Time
	TraceID          string
	SessionID        string
	ClientSessionRef string
	ALegID           string
	Action           cp.SessionAction
	Certainty        string
	Scope            *scope.PrincipalScopeView
}

// FromSessionRecord converts a safe session lifecycle record (secure-session
// create/touch) into a session-category control-plane event with an explicit
// source event key (design "Source Event Mapping"; task 4.3). It reuses the same
// scope flattening, unsafe-text rejection, and validation as FromSessionStart.
func (n *Normalizer) FromSessionRecord(rec SessionSourceRecord) (cp.Event, error) {
	if err := rejectUnsafeFreeText(rec.TraceID, rec.SessionID, rec.ClientSessionRef); err != nil {
		return cp.Event{}, fmt.Errorf("%w: session record: %v", ErrUnsafeEvidence, err)
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: rec.SourceEventKey,
		Category:       cp.CategorySession,
		OccurredAt:     rec.OccurredAt,
		Correlation: cp.Correlation{
			TraceID:   rec.TraceID,
			SessionID: rec.SessionID,
			ALegID:    rec.ALegID,
		},
		Scope: rec.Scope,
	})
	if err != nil {
		return cp.Event{}, err
	}
	action := rec.Action
	if !action.IsKnown() {
		action = cp.SessionActionUpdated
	}
	out.Detail = &cp.SessionDetail{
		SessionID:        rec.SessionID,
		ClientSessionRef: rec.ClientSessionRef,
		ALegID:           rec.ALegID,
		Action:           action,
		Certainty:        rec.Certainty,
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: session record: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// UsageSourceRecord is the source DTO for usage evidence projected from
// secure-session usage deltas (design "Source Event Mapping"; task 4.3). It
// carries an explicit SourceEventKey so the secure-session decorator can
// deduplicate repeated projections independently of the usage observer path.
// Raw usage JSON, raw payloads, headers, and tokens must never be placed here.
type UsageSourceRecord struct {
	SourceEventKey   string
	OccurredAt       time.Time
	TraceID          string
	SessionID        string
	ALegID           string
	BLegID           string
	AttemptSeq       int
	FrontendID       string
	BackendID        string
	Model            string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	CostNanoUnits    int64
	Currency         string
	CostSource       string
	Scope            *scope.PrincipalScopeView
}

// FromUsageRecord converts a safe usage record (secure-session usage delta) into
// a usage-category control-plane event with an explicit source event key (design
// "Source Event Mapping"; task 4.3). Raw usage JSON is never carried; only typed
// safe token/cost fields and explicit observed availability are projected.
func (n *Normalizer) FromUsageRecord(rec UsageSourceRecord) (cp.Event, error) {
	if err := rejectUnsafeFreeText(rec.TraceID, rec.BackendID, rec.Model, rec.Currency, rec.CostSource); err != nil {
		return cp.Event{}, fmt.Errorf("%w: usage record: %v", ErrUnsafeEvidence, err)
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: rec.SourceEventKey,
		Category:       cp.CategoryUsage,
		OccurredAt:     rec.OccurredAt,
		Correlation: cp.Correlation{
			TraceID:    rec.TraceID,
			SessionID:  rec.SessionID,
			ALegID:     rec.ALegID,
			BLegID:     rec.BLegID,
			AttemptSeq: rec.AttemptSeq,
			FrontendID: rec.FrontendID,
			BackendID:  rec.BackendID,
			Model:      rec.Model,
		},
		Scope: rec.Scope,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = new(ProjectObservedStreamUsage(ObservedStreamUsageInput{
		InputTokens:      rec.InputTokens,
		OutputTokens:     rec.OutputTokens,
		CacheReadTokens:  rec.CacheReadTokens,
		CacheWriteTokens: rec.CacheWriteTokens,
		ReasoningTokens:  rec.ReasoningTokens,
		TotalTokens:      rec.TotalTokens,
		CostNanoUnits:    rec.CostNanoUnits,
		Currency:         rec.Currency,
		CostSource:       rec.CostSource,
	}))
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: usage record: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// FromAttempt converts a safe backend attempt record into an attempt-category
// control-plane event (requirement 1.3, 3.2, 3.3, 3.7, 5.3).
func (n *Normalizer) FromAttempt(rec AttemptSourceRecord) (cp.Event, error) {
	if err := rejectUnsafeFreeText(rec.TraceID, rec.ErrorClass, rec.RouteOutcome, rec.BackendID, rec.Model); err != nil {
		return cp.Event{}, fmt.Errorf("%w: attempt: %v", ErrUnsafeEvidence, err)
	}
	surfaced := rec.Surfaced
	if !surfaced.IsKnown() {
		surfaced = cp.AttemptSurfacedUnknown
	}
	outcome := rec.Outcome
	if !outcome.IsKnown() {
		outcome = cp.AttemptOutcomeUnknown
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: rec.SourceEventKey,
		Category:       cp.CategoryAttempt,
		OccurredAt:     rec.OccurredAt,
		Correlation: cp.Correlation{
			TraceID:    rec.TraceID,
			RequestID:  rec.RequestID,
			SessionID:  rec.SessionID,
			ALegID:     rec.ALegID,
			BLegID:     rec.BLegID,
			AttemptSeq: rec.AttemptSeq,
			FrontendID: rec.FrontendID,
			BackendID:  rec.BackendID,
			Model:      rec.Model,
		},
		Scope: rec.Scope,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = &cp.AttemptDetail{
		ALegID:       rec.ALegID,
		BLegID:       rec.BLegID,
		AttemptSeq:   rec.AttemptSeq,
		BackendID:    rec.BackendID,
		Model:        rec.Model,
		RouteOutcome: rec.RouteOutcome,
		Surfaced:     surfaced,
		Outcome:      outcome,
		ErrorClass:   rec.ErrorClass,
		StartedAt:    rec.StartedAt,
		FinishedAt:   rec.FinishedAt,
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: attempt: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// FromUsage converts a safe usage observation into a usage-category control-
// plane event (requirement 1.4, 9.2). Raw usage JSON from upstream sources is
// never carried into the normalized event; only typed safe token/cost fields
// and explicit availability state are projected.
func (n *Normalizer) FromUsage(ev usage.Event) (cp.Event, error) {
	if err := rejectUnsafeFreeText(ev.TraceID, ev.BackendID, ev.Model, ev.Currency, ev.CostSource); err != nil {
		return cp.Event{}, fmt.Errorf("%w: usage: %v", ErrUnsafeEvidence, err)
	}
	scopeView := ev.Scope
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: usageSourceKey(ev),
		Category:       cp.CategoryUsage,
		OccurredAt:     ev.RecordedAt,
		Correlation: cp.Correlation{
			TraceID:    ev.TraceID,
			SessionID:  ev.SessionID,
			ALegID:     ev.ALegID,
			BLegID:     ev.BLegID,
			AttemptSeq: ev.AttemptSeq,
			FrontendID: ev.FrontendID,
			BackendID:  ev.BackendID,
			Model:      ev.Model,
		},
		Scope: &scopeView,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = new(ProjectObservedStreamUsage(ObservedStreamInputFromUsageEvent(ev)))
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: usage: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// FromPolicyDecision converts a safe policy decision record into a policy-
// category control-plane event (requirement 1.5, 3.4, 3.5, 9.3). The original
// decision outcome is preserved and never changed by normalization.
func (n *Normalizer) FromPolicyDecision(rec policydecision.Record) (cp.Event, error) {
	if err := rejectUnsafeFreeText(rec.TraceID, rec.ReasonCode, rec.Stage, rec.Provider.ID); err != nil {
		return cp.Event{}, fmt.Errorf("%w: policy decision: %v", ErrUnsafeEvidence, err)
	}
	scopeView := rec.Scope
	visibility := cp.VisibilityDefault
	redaction := cp.RedactionNone
	evidenceState := cp.EvidenceRecorded
	if rec.Visibility == policydecision.EvidencePrivileged {
		visibility = cp.VisibilityPrivileged
		redaction = cp.RedactionPrivileged
		evidenceState = cp.EvidenceRedacted
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: policySourceKey(rec),
		Category:       cp.CategoryPolicy,
		OccurredAt:     n.clock.Now(),
		Correlation: cp.Correlation{
			TraceID:    rec.TraceID,
			ALegID:     rec.ALegID,
			BLegID:     rec.BLegID,
			AttemptSeq: rec.AttemptSeq,
		},
		Scope:          &scopeView,
		Visibility:     visibility,
		EvidenceState:  evidenceState,
		RedactionState: redaction,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = &cp.PolicyDetail{
		Stage:      rec.Stage,
		Outcome:    string(rec.Outcome),
		Effect:     string(rec.Effect),
		ReasonCode: rec.ReasonCode,
		ProviderID: rec.Provider.ID,
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: policy decision: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// FromAudit converts a safe audit record into an audit-category control-plane
// event (requirement 1.6, 9.3). Privileged audit evidence is marked privileged
// + redacted so default-visibility queries return redacted evidence state
// rather than privileged raw content.
func (n *Normalizer) FromAudit(rec AuditSourceRecord) (cp.Event, error) {
	if err := rejectUnsafeFreeText(rec.TraceID, rec.Action, rec.Result, rec.ReasonCode); err != nil {
		return cp.Event{}, fmt.Errorf("%w: audit: %v", ErrUnsafeEvidence, err)
	}
	visibility := cp.VisibilityDefault
	redaction := cp.RedactionNone
	evidenceState := cp.EvidenceRecorded
	if rec.Visibility == cp.VisibilityPrivileged || rec.RedactionState == cp.RedactionPrivileged {
		visibility = cp.VisibilityPrivileged
		redaction = cp.RedactionPrivileged
		evidenceState = cp.EvidenceRedacted
	}
	out, err := n.baseEvent(eventBaseInput{
		SourceEventKey: rec.SourceEventKey,
		Category:       cp.CategoryAudit,
		OccurredAt:     rec.OccurredAt,
		Correlation: cp.Correlation{
			TraceID:    rec.TraceID,
			RequestID:  rec.RequestID,
			SessionID:  rec.SessionID,
			ALegID:     rec.ALegID,
			BLegID:     rec.BLegID,
			AttemptSeq: rec.AttemptSeq,
		},
		Scope:          rec.Scope,
		Visibility:     visibility,
		EvidenceState:  evidenceState,
		RedactionState: redaction,
	})
	if err != nil {
		return cp.Event{}, err
	}
	out.Detail = &cp.AuditDetail{
		Action:     rec.Action,
		Result:     rec.Result,
		ReasonCode: rec.ReasonCode,
	}
	if err := ValidateEvent(out); err != nil {
		return cp.Event{}, fmt.Errorf("%w: audit: %v", ErrUnsafeEvidence, err)
	}
	return out, nil
}

// recordedAt returns the recorded timestamp, never earlier than occurred.
func (n *Normalizer) recordedAt(occurred time.Time) time.Time {
	now := n.clock.Now()
	if now.Before(occurred) {
		return occurred
	}
	return now
}

func (n *Normalizer) baseEvent(in eventBaseInput) (cp.Event, error) {
	snap, err := flattenOrError(n.scopes, in.Scope)
	if err != nil {
		return cp.Event{}, err
	}
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = n.clock.Now()
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = cp.VisibilityDefault
	}
	evidenceState := in.EvidenceState
	if evidenceState == "" {
		evidenceState = cp.EvidenceRecorded
	}
	redactionState := in.RedactionState
	if redactionState == "" {
		redactionState = cp.RedactionNone
	}
	return cp.Event{
		SourceEventKey: in.SourceEventKey,
		Category:       in.Category,
		OccurredAt:     occurred,
		RecordedAt:     n.recordedAt(occurred),
		Correlation:    in.Correlation,
		Scope:          snap,
		Source:         n.source,
		Visibility:     visibility,
		EvidenceState:  evidenceState,
		RedactionState: redactionState,
	}, nil
}

// rejectUnsafeFreeText returns ErrUnsafeEvidence when any of the supplied free
// text fields carry credential-like content (requirement 4.4, 4.5). It
// reuses the bounded unsafe-summary substrings shared with event validation so
// the normalizer and the validator agree on what is unsafe.
func rejectUnsafeFreeText(fields ...string) error {
	for _, f := range fields {
		low := strings.ToLower(f)
		for _, bad := range unsafeSummarySubstrings {
			if strings.Contains(low, bad) {
				return fmt.Errorf("free-text field contains unsafe token-like content: %q", bad)
			}
		}
	}
	return nil
}

// authDecisionSourceKey builds a deterministic source event key for an auth
// decision (design "Source Event Mapping"). It never hashes raw payloads,
// headers, or tokens.
func authDecisionSourceKey(ev auth.AuthDecisionEvent) string {
	return joinKey("auth", ev.TraceID, string(ev.Outcome), ev.ReasonCode)
}

func sessionStartSourceKey(ev auth.SessionStartEvent) string {
	return joinKey("session-start", ev.TraceID, ev.SessionID, ev.ALegID)
}

func usageSourceKey(ev usage.Event) string {
	return joinKey("usage", ev.TraceID, ev.SessionID, ev.BLegID, strconv.Itoa(ev.AttemptSeq), string(ev.CostSource))
}

func policySourceKey(rec policydecision.Record) string {
	return joinKey("policy", rec.TraceID, rec.Stage, rec.Provider.ID, rec.ALegID, rec.BLegID, strconv.Itoa(rec.AttemptSeq), rec.ReasonCode)
}

func joinKey(parts ...string) string {
	for i, p := range parts {
		if p == "" {
			parts[i] = "_"
		}
	}
	return strings.Join(parts, ":")
}

//go:fix inline
func ptrUsage(d cp.UsageDetail) *cp.UsageDetail {
	return new(d)
}
