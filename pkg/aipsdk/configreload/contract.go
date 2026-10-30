// Package configreload is the dependency-neutral, secret-safe canonical reload contract.
//
// It declares trigger kinds, terminal result categories, and bounded result/status/history
// shapes used by runtimehost, public facades, management HTTP, and diagnostics.
// This package must not import internal config, source, or runtime packages and must not
// carry raw YAML, credentials, DSNs, private digests, filesystem paths, or opaque plugin
// configuration (requirements 7.1–7.8).
package configreload

import "time"

// TriggerKind identifies the explicit reload trigger surface.
// Triggers never carry paths, YAML, URLs, or plugin-install instructions.
type TriggerKind string

const (
	TriggerSIGHUP TriggerKind = "sighup"
	TriggerAPI    TriggerKind = "api"
)

// Trigger is the host-accepted explicit reload envelope.
type Trigger struct {
	Kind       TriggerKind
	AcceptedAt time.Time
	SafeActor  string
}

// ResultCategory is the terminal reload attempt classification.
type ResultCategory string

const (
	ResultPublished         ResultCategory = "published"
	ResultNoop              ResultCategory = "no-op"
	ResultBusy              ResultCategory = "busy"
	ResultRestartRequired   ResultCategory = "restart-required"
	ResultRetentionBlocked  ResultCategory = "retention-blocked"
	ResultInvalid           ResultCategory = "invalid"
	ResultSourceIntegrity   ResultCategory = "source-integrity-failed"
	ResultCanceled          ResultCategory = "canceled"
	ResultPreparationFailed ResultCategory = "preparation-failed"
	ResultInternalFailed    ResultCategory = "internal-failed"
)

// allResultCategories is the private closed vocabulary for terminal attempt results.
// It is never exposed by reference; callers receive a defensive copy via [ResultCategories].
var allResultCategories = []ResultCategory{
	ResultPublished,
	ResultNoop,
	ResultBusy,
	ResultRestartRequired,
	ResultRetentionBlocked,
	ResultInvalid,
	ResultSourceIntegrity,
	ResultCanceled,
	ResultPreparationFailed,
	ResultInternalFailed,
}

// ResultCategories returns a defensive copy of the closed vocabulary for terminal
// attempt results. Mutating the returned slice affects neither later calls nor
// category validation policy.
func ResultCategories() []ResultCategory {
	return append([]ResultCategory(nil), allResultCategories...)
}

// Result is the bounded, secret-safe terminal outcome of one attempt.
// It never carries raw YAML, private digests, credentials, paths, or configuration values.
type Result struct {
	Category           ResultCategory
	AttemptID          int64
	ActiveGeneration   int64
	PreviousGeneration int64
	RestartFields      []string
	RestartFieldCount  int
	ReasonCategory     string
	CoalescedSignals   int64
}

// HistoryEntry is one bounded, secret-safe reload attempt/status record.
type HistoryEntry struct {
	AttemptID           int64
	Trigger             TriggerKind
	Stage               string
	Category            ResultCategory
	ActiveGeneration    int64
	CandidateGeneration int64
	DurationMs          int64
	RestartFieldCount   int
	ReasonCategory      string
	SafeActor           string
	RecordedAt          time.Time
}

// Status is the bounded public status snapshot.
// It never carries raw YAML, credentials, DSNs, filesystem paths, or opaque plugin configuration.
// Fixed-source filesystem paths are intentionally omitted; management adapters retrieve them
// through a narrow coordinator capability (for example FixedSourcePath()).
type Status struct {
	ActiveGeneration    int64
	CurrentAttempt      *Result // non-nil while Busy
	LastResult          Result
	LastSuccess         Result
	LastFailure         Result // most recent failed or no-op attempt
	SourceIntegrity     string // bounded posture category (ok|failed|unknown)
	RetainedGenerations int
	RetentionPressure   bool
	ControlDegraded     bool // reload-control posture; independent of data-plane readiness
	ModelGeneration     string
	History             []HistoryEntry
	Busy                bool
	PendingSignal       bool
	CoalescedSignals    int64
}

// IsKnownTriggerKind reports whether k is in the closed trigger vocabulary.
func IsKnownTriggerKind(k TriggerKind) bool {
	switch k {
	case TriggerSIGHUP, TriggerAPI:
		return true
	default:
		return false
	}
}

// NormalizeResultCategory maps an unknown category to ResultInternalFailed once at the
// error boundary. Empty stays empty so callers can distinguish "unset".
// Policy uses a private closed switch independent of the [ResultCategories]
// enumeration copy handed to callers.
func NormalizeResultCategory(c ResultCategory) ResultCategory {
	if c == "" {
		return ""
	}
	switch c {
	case ResultPublished,
		ResultNoop,
		ResultBusy,
		ResultRestartRequired,
		ResultRetentionBlocked,
		ResultInvalid,
		ResultSourceIntegrity,
		ResultCanceled,
		ResultPreparationFailed,
		ResultInternalFailed:
		return c
	default:
		return ResultInternalFailed
	}
}

// Clone returns a defensive copy with an independent RestartFields slice.
func (r Result) Clone() Result {
	out := r
	if r.RestartFields != nil {
		out.RestartFields = append([]string(nil), r.RestartFields...)
	}
	return out
}

// CloneHistory returns a defensive copy of history entries (nil stays nil).
func CloneHistory(in []HistoryEntry) []HistoryEntry {
	if in == nil {
		return nil
	}
	out := make([]HistoryEntry, len(in))
	copy(out, in)
	return out
}

// Clone returns a defensive copy of status, including CurrentAttempt and History.
func (s Status) Clone() Status {
	out := s
	if s.CurrentAttempt != nil {
		cur := s.CurrentAttempt.Clone()
		out.CurrentAttempt = &cur
	}
	out.LastResult = s.LastResult.Clone()
	out.LastSuccess = s.LastSuccess.Clone()
	out.LastFailure = s.LastFailure.Clone()
	out.History = CloneHistory(s.History)
	return out
}
