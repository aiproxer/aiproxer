package app

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// ClientInputLine is one ordered accepted client message line after hooks (canonical roles).
type ClientInputLine struct {
	Role    string
	Ordinal int
	Parts   []string // part kinds in order (text, tool_result, …)
}

// ClientTurnRecordInput captures accepted client input for one gated turn.
type ClientTurnRecordInput struct {
	Now       time.Time
	TraceID   string
	SessionID domain.SessionID
	TurnID    domain.TurnID
	Policy    domain.PolicyMetadata
	Lines     []ClientInputLine
}

// StreamEventRecordInput is a post-hook canonical stream slice for recording.
type StreamEventRecordInput struct {
	Now       time.Time
	TraceID   string
	SessionID domain.SessionID
	TurnID    domain.TurnID
	BLegID    string
	BackendID string
	Policy    domain.PolicyMetadata

	EventKind string
	// EventPayloadJSON is a redacted JSON snapshot of the canonical event (never raw resume tokens).
	EventPayloadJSON string

	IsUsageEvent bool
	InputTokens  int64
	OutputTokens int64

	CacheReadTokens          int64
	CacheWriteTokens         int64
	NonCachedInputTokens     int64
	ReasoningTokens          int64
	NonReasoningOutputTokens int64
	TotalTokens              int64
	CostNanoUnits            int64
	CostMinorUnits           int64
	Currency                 string
	CostSource               string
	RawUsageJSON             string
	BillingUnavailable       bool

	RequestStartedAt         time.Time
	FirstRemoteEventAt       time.Time
	FirstMeaningfulTokenAt   time.Time
	RemoteCompletedAt        time.Time
	ProxyCompletedAt         time.Time
	TTFTMillis               int64
	RemoteDurationMillis     int64
	CompletionDurationMillis int64
	CompletionTPSMilli       int64

	// ProviderCorrelationJSON holds non-authoritative provider correlation (never session authority).
	ProviderCorrelationJSON string
}
