package proxymemory

import (
	"context"
	"errors"
	"time"
)

// Store sentinel errors.
//
// ErrNotFound reports that the caller's owner has no such artifact; a reader
// must not be able to tell a missing row from another owner's row
// (Requirement 1.5).
//
// ErrLeaseLost reports that a claim, lease or compare-and-set precondition no
// longer holds, so the caller must not persist its result.
//
// ErrInvalid is for Service input validation only; store implementations do
// not return it.
var (
	ErrNotFound  = errors.New("proxymemory: not found")
	ErrLeaseLost = errors.New("proxymemory: lease lost")
	ErrInvalid   = errors.New("proxymemory: invalid input")
)

// SessionCapture is one captured turn of one session, written by the capture
// writer. Owner is the artifact's owner: tenant, principal and opaque project
// key. Transcript is the redacted JSON array of the transcript.
type SessionCapture struct {
	Owner        Owner
	ProjectLabel string
	ALegID       string
	Transcript   string // redacted JSON array
	ActivityMS   int64
	SourceSeq    int64 // durable request-admission order; never a completion timestamp
	ErasureEpoch int64 // epoch observed by the bounded request-path read
}

// FirstTurnOutcome is the immutable outcome frozen for one authoritative first
// turn. The stored injection is already rendered and bounded; later turns never
// consult the current bank to replace it.
type FirstTurnOutcome string

const (
	FirstTurnMatched     FirstTurnOutcome = "matched"
	FirstTurnNoSummary   FirstTurnOutcome = "no_summary"
	FirstTurnNoProject   FirstTurnOutcome = "no_project"
	FirstTurnUnavailable FirstTurnOutcome = "unavailable"
)

// FirstTurnDecision is the content-free decision metadata plus the exact
// rendered injection selected for an authoritative first turn.
type FirstTurnDecision struct {
	Outcome        FirstTurnOutcome
	Injection      string
	SummaryVersion int64
}

// AdmissionQuery asks for one bounded, read-only snapshot before inference.
// Candidate is meaningful only when AuthoritativeFirstTurn is true.
// CachedProject is an already-frozen project hint, used only to validate a
// process-local decision whose durable write has not completed yet.
type AdmissionQuery struct {
	Principal              Principal
	ALegID                 string
	AuthoritativeFirstTurn bool
	Candidate              Project
	CachedProject          *Project
}

// AdmissionSnapshot contains only facts observed by ReadAdmission. A nil
// Decision means no frozen decision exists for this A-leg. A missing summary
// is distinct from a failed summary query, so the caller can freeze the
// latter as an unavailable negative decision.
type AdmissionSnapshot struct {
	Bound              bool
	Project            Project
	Decision           *FirstTurnDecision
	NextBSeq           int64
	ErasureEpoch       int64
	Summary            string
	SummaryVersion     int64
	SummaryUnavailable bool
}

// FirstTurnJob is the immutable value queued to persist a first-turn binding
// and decision. EpochKnown is false only when the request-path read failed;
// such a decision is cached negatively but never written to storage.
type FirstTurnJob struct {
	Principal    Principal
	ALegID       string
	Project      Project
	Decision     FirstTurnDecision
	ErasureEpoch int64
	EpochKnown   bool
}

// UpsertOutcome classifies an owner-scoped session upsert: UpsertStored,
// UpsertOwnerMismatch (the A-leg already belongs to another owner or project),
// UpsertForgotten (a prior Forget fenced the A-leg), UpsertUnbound (the A-leg
// has no durable first-turn binding for this owner, so the capture has no
// authoritative decision to learn from) or UpsertStale (a live session row or
// durable extraction watermark already covers the capture's admission
// sequence, so its transcript is not replaced). A stale successful release may
// still advance idle activity and invalidate a leased revision.
//
// UpsertUnbound is a refusal, never a repair: a capture whose authoritative
// first-turn decision never committed is dropped, because a binding that does
// not exist cannot be re-created from a capture.
//
// UpsertStale is the truthful store classification for a refused write, not a
// metric contract: design §13's capture counter set is worker-side
// implementation surface, and a consumer that cannot name a dedicated counter
// must not report a stale refusal as a stored capture or as an owner mismatch.
type UpsertOutcome int

const (
	UpsertStored UpsertOutcome = iota
	UpsertOwnerMismatch
	UpsertForgotten
	UpsertUnbound
	UpsertStale
)

// ClaimedSession is a session row held under a lease: the claim inputs that
// CompleteSession and FailSession consume.
type ClaimedSession struct {
	Owner          Owner
	ProjectLabel   string
	ALegID         string
	Transcript     string
	Revision       int64
	LastActivityMS int64
	SourceSeq      int64
	LeaseOwner     string
	// LeaseUntilMS and ErasureEpoch are captured inside the conditional claim
	// transaction: the deadline the claim wrote and the owner's erasure epoch
	// observed under its lock before dispatch.
	LeaseUntilMS int64
	ErasureEpoch int64
}

// Extraction is the phase 1 result of one session plus its ownership and
// timestamps. SessionRef is SessionRef(ALegID); SourceUpdatedMS is the claimed
// session's LastActivityMS; GeneratedAtMS is the extraction time.
type Extraction struct {
	Revision        int64 // store-assigned content revision, independent of clocks
	Owner           Owner
	ALegID          string
	SessionRef      string
	RawMemory       string
	RolloutSummary  string
	RolloutSlug     string
	SourceUpdatedMS int64
	GeneratedAtMS   int64
}

// Note is one explicit ad-hoc note added by its owner.
type Note struct {
	NoteID      string
	Text        string
	CreatedAtMS int64
}

// ClaimedBank is a bank row held under a lease: the claim inputs that
// PublishBank and FailBank consume.
type ClaimedBank struct {
	Owner             Owner
	ProjectLabel      string
	Version           int64
	DirtySeq          int64
	MemoryMD          string
	MemorySummary     string
	ConsolidatedNames []string
	LeaseOwner        string
	// LeaseUntilMS and ErasureEpoch are captured inside the conditional claim
	// transaction: the deadline the claim wrote and the owner's erasure epoch
	// observed under its lock before dispatch.
	LeaseUntilMS int64
	ErasureEpoch int64
}

// ExtractionRef identifies the store-assigned revision a publication carried.
// Generation time is checked too, but cannot identify a revision by itself.
type ExtractionRef struct {
	ALegID        string
	Revision      int64
	GeneratedAtMS int64
}

// ConsolidationBatch is one phase 2 read. Extractions are the outstanding
// extractions of the window, oldest first, capped by the caller's limit.
// WindowNames lists every in-window summary name, including already
// consolidated ones, so a batch omission is never mistaken for a removal.
type ConsolidationBatch struct {
	Extractions []Extraction
	WindowNames []string
	Notes       []Note
}

// Publication is the phase 2 result that replaces a bank's published version:
// the two Codex documents, the consolidation names they consumed, the consumed
// note IDs and the exact extraction revisions the request carried. WindowSinceMS
// is the consolidation window the input was read with; the bank keeps its
// consolidated sequence while an in-window extraction remains unacknowledged.
type Publication struct {
	MemoryMD          string
	MemorySummary     string
	ConsolidatedNames []string
	ConsumedNoteIDs   []string
	Acknowledged      []ExtractionRef
	WindowSinceMS     int64
	PublishedAtMS     int64
}

// BankStatus is one bank's read model. ScopeID equals Owner.ProjectKey.
type BankStatus struct {
	ScopeID         string
	ProjectLabel    string
	Version         int64
	PublishedAtMS   int64
	PendingSessions int
	Extractions     int
	PendingNotes    int
}

// Documents is one bank's current readable content. RolloutSummaries is keyed
// by SummaryName(e)+".md".
type Documents struct {
	MemoryMD         string
	MemorySummary    string
	RolloutSummaries map[string]string // key: SummaryName(e)+".md"
	ProjectLabel     string
}

// ClaimQuery bounds one lease claim. NowMS is the claim time,
// IdleBeforeMS/NotBeforeMS bound the session idle window, LeaseUntilMS is the
// lease expiry a successful claim writes, LeaseOwner is the lease holder to
// record, and Limit is the maximum number of rows claimed.
type ClaimQuery struct {
	NowMS        int64
	IdleBeforeMS int64
	NotBeforeMS  int64
	LeaseUntilMS int64
	LeaseOwner   string
	Limit        int
}

// OwnerOverrides holds one owner's stored, unprojected per-phase overrides
// (design §4).
//
// The overrides are the owner's own per-field values, never the projected
// effective values: the effective value of every field is recomputed on read
// from the operator's defaults, the operator's locks and these overrides, so a
// changed default or a newly introduced lock takes effect at once.
//
// The owner key holds the tenant and the principal only. There is no project
// in it, so the settings are global for that owner and apply to every project
// of that owner, and forgetting a bank does not delete them (Requirement 2.1).
type OwnerOverrides struct {
	// Extraction holds the extraction phase's overrides.
	Extraction TargetOverrides
	// Consolidation holds the consolidation phase's overrides.
	Consolidation TargetOverrides
}

// For returns the overrides of the named phase. An unknown phase has no
// overrides.
func (o OwnerOverrides) For(phase Phase) TargetOverrides {
	switch phase {
	case PhaseExtraction:
		return o.Extraction
	case PhaseConsolidation:
		return o.Consolidation
	}
	return TargetOverrides{}
}

// Store persists proxy memory. The single owner-predicate invariant governs
// every method: its SQL includes tenant_id = ? AND principal_id = ? (plus
// project_key = ? where scoped), or it operates on a row reached by primary
// key and re-checks the owner columns or the lease owner. ScopeID equals
// Owner.ProjectKey. No method can read, change, claim or publish another
// owner's artifact (Requirement 1.5).
type Store interface {
	// ReadAdmission performs the bounded read-only request-path snapshot. It
	// returns ErrNotFound when the A-leg is no longer active or belongs to a
	// different principal.
	ReadAdmission(ctx context.Context, q AdmissionQuery) (AdmissionSnapshot, error)
	// CommitFirstTurn atomically saves a binding and frozen decision when the
	// active A-leg, owner, project and observed erasure epoch still match. A
	// false result means the single-shot job was stale or conflicted.
	CommitFirstTurn(ctx context.Context, job FirstTurnJob) (bool, error)
	// UpsertSession stores or replaces the capture of one session under its
	// owner: UpsertStored on success, UpsertOwnerMismatch when the A-leg
	// already belongs to another owner or project, UpsertForgotten when its
	// A-leg is fenced, UpsertUnbound when the A-leg has no durable first-turn
	// binding for this owner (the authoritative decision never committed, so
	// the capture is dropped), and UpsertStale when a live session or durable
	// extraction watermark already covers the capture's sequence, in which
	// case its content is not written; successful-release activity may advance.
	UpsertSession(ctx context.Context, c SessionCapture) (UpsertOutcome, error)

	// PublishedSummary returns the bank's published summary and version. It
	// returns ErrNotFound when no bank exists or the summary is empty.
	PublishedSummary(ctx context.Context, o Owner) (summary string, version int64, err error)

	// ClaimSessions claims up to q.Limit idle sessions under q.LeaseOwner.
	ClaimSessions(ctx context.Context, q ClaimQuery) ([]ClaimedSession, error)

	// CompleteSession stores e for the claimed session per design §12.4. A
	// claimed-revision mismatch does not return ErrLeaseLost: the extraction
	// is stored, the bank is dirtied, and the retained newer session's lease
	// is cleared. ErrLeaseLost covers §12.4's failure cases: the lease owner
	// no longer matches, the A-leg is fenced, or an existing extraction row
	// belongs to a different owner.
	CompleteSession(ctx context.Context, s ClaimedSession, e Extraction) error

	// FailSession clears the session's lease and schedules its next attempt
	// at retryAtMS.
	FailSession(ctx context.Context, s ClaimedSession, retryAtMS int64) error

	// ClaimBanks claims up to q.Limit dirty banks under q.LeaseOwner.
	ClaimBanks(ctx context.Context, q ClaimQuery) ([]ClaimedBank, error)

	// ConsolidationInput returns the window's outstanding extractions (oldest
	// first, at most limit), every in-window summary name, and the bank's
	// pending notes.
	ConsolidationInput(ctx context.Context, b ClaimedBank, sinceMS int64, limit int) (ConsolidationBatch, error)

	// PublishBank stores p as the bank's next published version. It acknowledges
	// exactly the extraction revisions in p.Acknowledged and advances
	// consolidated_seq to the claimed dirty sequence only when no in-window
	// extraction remains unacknowledged. It returns ErrLeaseLost when the lease
	// owner or version no longer matches, and acknowledges nothing in that case.
	PublishBank(ctx context.Context, b ClaimedBank, p Publication) error

	// FailBank clears the bank's lease and schedules its next attempt at
	// retryAtMS.
	FailBank(ctx context.Context, b ClaimedBank, retryAtMS int64) error

	// RecordPhaseFailure records one immutable memory-processing failure fact
	// and, in the same transaction, conditionally releases and reschedules the
	// caller's still-live claim. A stale claim may append its own immutable
	// fact but never mutates a successor. An owner erasure or an expired
	// receipt writes nothing.
	RecordPhaseFailure(ctx context.Context, f FailureEvidence) (FailureOutcome, error)

	// Purge deletes sessions and extractions older than the two cutoffs, the
	// sessions of fenced A-legs, the bindings of retired A-legs and expired
	// failure evidence, and returns the total number of deleted rows. The
	// abandoned-session step is bounded by its cutoff; the other steps are
	// bounded by limit. Fence rows are never purged.
	Purge(ctx context.Context, nowMS, sessionsBeforeMS, extractionsBeforeMS int64, limit int) (int, error)

	// ListBanks returns the caller's own bank statuses.
	ListBanks(ctx context.Context, p Principal) ([]BankStatus, error)

	// BankStatus returns one bank's status, or ErrNotFound.
	BankStatus(ctx context.Context, o Owner) (BankStatus, error)

	// Documents returns the bank's current documents, or ErrNotFound.
	Documents(ctx context.Context, o Owner) (Documents, error)

	// AddNote appends n to the bank. It returns ErrNotFound when no bank
	// exists.
	AddNote(ctx context.Context, o Owner, n Note) error

	// Forget deletes the owner's bank, sessions, extractions and notes and
	// fences the owner's A-legs. It returns ErrNotFound when there is no bank
	// and no row to forget. It never touches the owner's stored settings:
	// forgetting a bank does not revoke the owner's overrides
	// (Requirement 2.1).
	Forget(ctx context.Context, o Owner, nowMS int64) error

	// ReadSettings returns the owner's stored per-phase settings: the raw,
	// unprojected overrides and the trusted write-time dependency context of
	// each phase. An owner who never wrote one has empty settings rather than
	// a missing row: the settings of such an owner are the operator defaults.
	// The key holds the tenant and the principal only, so the settings are
	// global for that owner (Requirement 2.1).
	ReadSettings(ctx context.Context, p Principal) (StoredOwnerSettings, error)

	// WriteSettings replaces the owner's stored per-phase settings. Both phase
	// rows and their dependency context change together or neither does, so a
	// refused write never persists half of the settings and never separates an
	// override from the context it was accepted against (Requirement 2.2).
	WriteSettings(ctx context.Context, p Principal, s StoredOwnerSettings) error

	// DeleteSettings removes the owner's stored per-phase settings, the
	// trusted context included. It returns ErrNotFound when the owner has no
	// stored settings.
	DeleteSettings(ctx context.Context, p Principal) error
}

// SummaryName renders the rollout-summary document name of one extraction per
// design §7.3: the UTC time of GeneratedAtMS formatted "2006-01-02T15-04-05",
// then e.SessionRef[8:14] (the first six hex characters after the "session-"
// prefix), then the slug, joined by "-". It is the single definition used by
// the phase 2 input, the store's Documents and the memory service.
func SummaryName(e Extraction) string {
	return time.UnixMilli(e.GeneratedAtMS).UTC().Format("2006-01-02T15-04-05") + "-" + e.SessionRef[8:14] + "-" + e.RolloutSlug
}
