package state

import "time"

// MemoState is the persisted thinker memo and its observable metadata.
type MemoState struct {
	// Memo is the bounded planning content captured from a thinker turn.
	Memo string
	// SourceSelector is the selector string that selected the thinker branch.
	SourceSelector string
	// Backend identifies the backend instance that produced the memo.
	Backend string
	// Model identifies the model that produced the memo.
	Model string
	// RequestID identifies the thinker request that produced the memo.
	RequestID string
	// CreatedAt is when the memo was captured.
	CreatedAt time.Time
	// InjectedCount is how many executor turns have received this memo.
	InjectedCount int
	// RegularTurnsRemaining is the remaining memo injection budget.
	RegularTurnsRemaining int
	// VisibleToClient records whether the memo content has been surfaced to
	// the client (visible mode). Used to suppress duplicate injection.
	VisibleToClient bool
	// ExtractionSource describes how Memo was derived. Whole-output capture is
	// the only derivation path, so this is always ExtractionSourceFull.
	ExtractionSource string
	// StreamInterrupted records whether the thinker stream was interrupted
	// before completion while capturing this memo.
	StreamInterrupted bool
}
