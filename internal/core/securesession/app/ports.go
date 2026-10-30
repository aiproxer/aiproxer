package app

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// EntropyMaterial carries optional domain-separation inputs for resume fingerprints.
// Uniqueness of session ids and resume tokens does not depend on these fields.
type EntropyMaterial struct {
	PrincipalID        string
	AgentDigest        string
	FirstMessageDigest string
}

// Generator issues proxy-owned session ids and resume bearer tokens.
type Generator interface {
	NewSessionID(ctx context.Context, material EntropyMaterial) (domain.SessionID, error)
	NewResumeToken(ctx context.Context, material EntropyMaterial) (domain.ResumeToken, domain.TokenFingerprint, error)
}

// Store persists secure-session rows, indexes, transcripts, usage, audit, and readiness checks.
// Adapters implement this port; the interface stays consumer-owned in package app.
type Store interface {
	Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error)
	LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error)
	LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error)
	LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error)
	TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error
	AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error
	UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error
	// AppendTranscript appends a row; durable implementations allocate the next seq in the same
	// transaction as the insert (item.Seq is ignored there). NextTranscriptSeq remains a best-effort preview.
	AppendTranscript(ctx context.Context, item domain.TranscriptItem) error
	// NextTranscriptSeq returns the next monotonic sequence number for a new transcript row for the session.
	NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error)
	AddUsage(ctx context.Context, delta domain.UsageDelta) error
	// NextAuditSeq returns the next monotonic sequence number for a new audit entry for the session.
	NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error)
	// AppendAudit appends a row; durable implementations allocate the next seq in the same transaction
	// as the insert (item.Seq is ignored there). NextAuditSeq remains a best-effort preview.
	AppendAudit(ctx context.Context, item domain.AuditItem) error
	Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error)
	Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error)
	Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error)
	// ListAttemptEvidence returns chronological B-leg attempts for operator diagnostics (bounded by opts.Limit, default 100).
	ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error)
	CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error
	// Quarantine performs an atomic, idempotent transition to SessionStatusQuarantined.
	// Durable implementations update status and append a minimal session audit row in one transaction.
	// A second identical quarantine must succeed without duplicating terminal state.
	Quarantine(ctx context.Context, in domain.QuarantineInput) error
}

// GateRecording is the secure-session recorder port used by the runtime after gate success and
// for post-hook canonical stream events (transcript, usage, activity, audit).
type GateRecording interface {
	RecordClientTurnAfterGate(ctx context.Context, in ClientTurnRecordInput) error
	RecordPostHookStreamEvent(ctx context.Context, in StreamEventRecordInput) error
}

// SessionUsageRollup is an optional extension implemented by stores that expose per-session token
// totals for operator diagnostics. Callers type-assert from [Store] when present.
type SessionUsageRollup interface {
	UsageTokenTotals(ctx context.Context, id domain.SessionID) (inputTokens, outputTokens int64, err error)
}
