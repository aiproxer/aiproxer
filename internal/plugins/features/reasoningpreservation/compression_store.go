package reasoningpreservation

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// CompressionLimits configures bounded optional-state budgets. Zero means unlimited
// for backward compatibility with disabled compression.
type CompressionLimits struct {
	MaxPendingPerSession        int
	MaxPendingTotal             int
	MaxSurrogateBytesPerTurn    int
	MaxSurrogateBytesPerSession int
	MaxSurrogateBytesTotal      int
}

// SurrogateSegment is a minimal placement-indexed surrogate text.
type SurrogateSegment struct {
	PlacementIndex int
	Text           string
	Bytes          int
}

const (
	SanitizationNone     = "none"
	SanitizationRedacted = "redacted"
)

func isValidSanitization(s string) bool {
	return s == SanitizationNone || s == SanitizationRedacted
}

// ReasoningSurrogate is the validated optional replacement for semantic-text placements.
// SemanticDigest is the SHA-256 of the canonical semantic-text payload that was
// compressed; EgressPolicyHash is the hash of the egress policy version that
// authorized submission. Both are immutable correlation digests verified by
// AttachSurrogate CAS. A zero SemanticDigest is invalid (real artifacts always
// have content) and Attach rejects it as a conflict.
// Sanitization is the content-free class that authorized the surrogate (none/redacted).
// AuthorizedRouteHash is sha256(route) at promotion, verified by Attach CAS.
type ReasoningSurrogate struct {
	OriginalDigest      [32]byte
	PolicyRevision      string
	Sanitization        string
	Segments            []SurrogateSegment
	Bytes               int
	SemanticDigest      [32]byte
	EgressPolicyHash    [32]byte
	AuthorizedRouteHash [32]byte
}

// PendingCompression tracks a reservation awaiting background result.
// SemanticDigest is the SHA-256 of the source semantic text; EgressPolicyHash
// identifies the egress policy revision that authorized the pending work.
// Both are recorded at Reserve and immutable via Bind; Attach verifies they
// match. Zero SemanticDigest is invalid and Attach rejects it as a
// conflict (real artifacts have content). Zero EgressPolicyHash is allowed
// but CAS-checked — a mismatch still yields a conflict.
// Sanitization is the content-free class (none/redacted) derived from the egress decision.
// AuthorizedRouteHash is sha256(route) at promotion, verified by adoption.
// PolicyHashAuthoritative is false at Reserve (provisional ref hash) and
// becomes true after successful UpdateReservationPolicyHash CAS promotion.
// Bind and Attach reject unless authoritative.
type PendingCompression struct {
	JobID                   auxiliary.JobID
	OriginalDigest          [32]byte
	SemanticDigest          [32]byte
	EgressPolicyHash        [32]byte
	AuthorizedRouteHash     [32]byte
	Sanitization            string
	PolicyHashAuthoritative bool
	CreatedAt               time.Time
	PolicyRevision          string
	ReservationID           string
}

// CompressionState is additive optional state per artifact revision.
type CompressionState struct {
	PolicyRevision string
	ReservationID  string
	Pending        *PendingCompression
	Surrogate      *ReasoningSurrogate
}

// CompressionStats exposes aggregate counters for tests.
type CompressionStats struct {
	TotalPending             int
	TotalSurrogateBytes      int
	PendingPerSession        map[string]int
	SurrogateBytesPerSession map[string]int
}

// BudgetKind distinguishes which limit was exceeded.
type BudgetKind string

const (
	BudgetPendingPerSession   BudgetKind = "pending_per_session"
	BudgetPendingTotal        BudgetKind = "pending_total"
	BudgetSurrogatePerTurn    BudgetKind = "surrogate_per_turn"
	BudgetSurrogatePerSession BudgetKind = "surrogate_per_session"
	BudgetSurrogateTotal      BudgetKind = "surrogate_total"
)

// BudgetError is returned when a per-session or aggregate budget rejects admission.
type BudgetError struct {
	Kind    BudgetKind
	Limit   int
	Current int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s: budget %s exceeded limit %d current %d", ID, e.Kind, e.Limit, e.Current)
}

func (e *BudgetError) Unwrap() error { return ErrCompressionBudgetExceeded }

var (
	ErrCompressionNotFound       = errors.New(ID + ": compression not found")
	ErrCompressionConflict       = errors.New(ID + ": compression conflict")
	ErrCompressionBudgetExceeded = errors.New(ID + ": compression budget exceeded")
)

// IsBudgetError reports whether err is a budget rejection.
func IsBudgetError(err error) bool { return errors.Is(err, ErrCompressionBudgetExceeded) }

// IsConflictError reports whether err is a CAS/stale conflict.
func IsConflictError(err error) bool { return errors.Is(err, ErrCompressionConflict) }

// IsNotFoundError reports whether err is a not-found.
func IsNotFoundError(err error) bool { return errors.Is(err, ErrCompressionNotFound) }

// CompressionClaim identifies one in-flight compression reservation with value semantics.
// Passed as an untrusted input by value to CompressionStore mutation operations;
// the store fully validates all fields against its internal authoritative records.
type CompressionClaim struct {
	Partition      SessionPartition
	ArtifactID     string
	ReservationID  string
	OriginalDigest [32]byte
	PolicyRevision string
}

// CompressionStore extends TurnStore with optional-state operations.
type CompressionStore interface {
	TurnStore
	ReserveCompression(ctx context.Context, partition SessionPartition, artifactID string, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, egressPolicyHash [32]byte) (CompressionClaim, error)
	UpdateReservationPolicyHash(ctx context.Context, claim CompressionClaim, expectedOldHash [32]byte, semanticDigest [32]byte, newHash [32]byte, sanitization string, routeHash [32]byte) error
	BindCompressionJob(ctx context.Context, claim CompressionClaim, jobID auxiliary.JobID) error
	AttachSurrogate(ctx context.Context, claim CompressionClaim, jobID auxiliary.JobID, surrogate ReasoningSurrogate) error
	ClearCompression(ctx context.Context, partition SessionPartition, artifactID string, expectedReservationID string) error
	GetCompressionState(ctx context.Context, partition SessionPartition, artifactID string) (CompressionState, bool, error)
	CompressionStats() CompressionStats
}

var _ CompressionStore = (*memoryTurnStore)(nil)

// compressionClaim is the immutable CAS correlation created at ReserveCompression
// and verified by every subsequent mutation.
type compressionClaim struct {
	reservationID  string
	originalDigest [32]byte
	policyRevision string
}

type compressionEntry struct {
	pending   *PendingCompression
	surrogate *ReasoningSurrogate
	claim     compressionClaim
}

// artifactExistsLocked reports whether artifactID exists in session key.
// Caller must hold s.mu.
func (s *memoryTurnStore) artifactExistsLocked(key, artifactID string) bool {
	for i := range s.by[key] {
		if s.by[key][i].ID == artifactID {
			return true
		}
	}
	return false
}

// claimLocked validates and returns the pending entry for artifactID.
// Caller must hold s.mu. Errors wrap ErrCompressionConflict with exact legacy messages.
func (s *memoryTurnStore) claimLocked(key, artifactID, reservationID string) (*compressionEntry, error) {
	m, ok := s.compBy[key]
	if !ok {
		return nil, fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	entry, ok := m[artifactID]
	if !ok || entry.pending == nil {
		return nil, fmt.Errorf("%w: no pending for %q", ErrCompressionConflict, artifactID)
	}
	if entry.claim.reservationID != reservationID {
		return nil, fmt.Errorf("%w: reservation mismatch", ErrCompressionConflict)
	}
	return entry, nil
}

func validateCompressionClaim(entry *compressionEntry, claim CompressionClaim) error {
	if entry.claim.originalDigest != claim.OriginalDigest {
		return fmt.Errorf("%w: digest mismatch", ErrCompressionConflict)
	}
	if entry.claim.policyRevision != claim.PolicyRevision {
		return fmt.Errorf("%w: policy mismatch", ErrCompressionConflict)
	}
	return nil
}

func (s *memoryTurnStore) ReserveCompression(ctx context.Context, partition SessionPartition, artifactID string, originalDigest [32]byte, policyRevision string, semanticDigest [32]byte, egressPolicyHash [32]byte) (CompressionClaim, error) {
	if err := ctx.Err(); err != nil {
		return CompressionClaim{}, err
	}
	if artifactID == "" || policyRevision == "" {
		return CompressionClaim{}, fmt.Errorf("%w: missing id or policy", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	now := s.opts.Now()
	// expire expired originals first to free counters
	_ = s.expireLocked(key, now, EvictionSummary{})
	if !s.artifactExistsLocked(key, artifactID) {
		return CompressionClaim{}, fmt.Errorf("%w: artifact %q not found", ErrCompressionNotFound, artifactID)
	}
	// check existing optional state
	if m, ok := s.compBy[key]; ok {
		if e, ok := m[artifactID]; ok {
			if e.pending != nil {
				return CompressionClaim{}, fmt.Errorf("%w: pending already exists for %q", ErrCompressionConflict, artifactID)
			}
			if e.surrogate != nil && e.surrogate.PolicyRevision == policyRevision {
				return CompressionClaim{}, fmt.Errorf("%w: surrogate already exists for %q policy %q", ErrCompressionConflict, artifactID, policyRevision)
			}
		}
	}
	// per-session pending
	if s.opts.CompressionLimits.MaxPendingPerSession > 0 && s.pendingPerSession[key] >= s.opts.CompressionLimits.MaxPendingPerSession {
		return CompressionClaim{}, &BudgetError{Kind: BudgetPendingPerSession, Limit: s.opts.CompressionLimits.MaxPendingPerSession, Current: s.pendingPerSession[key]}
	}
	if s.opts.CompressionLimits.MaxPendingTotal > 0 && s.totalPending >= s.opts.CompressionLimits.MaxPendingTotal {
		return CompressionClaim{}, &BudgetError{Kind: BudgetPendingTotal, Limit: s.opts.CompressionLimits.MaxPendingTotal, Current: s.totalPending}
	}
	s.reservationSeq++
	reservationID := fmt.Sprintf("res-%d", s.reservationSeq)
	if s.compBy[key] == nil {
		if s.compBy == nil {
			s.compBy = make(map[string]map[string]*compressionEntry)
		}
		s.compBy[key] = make(map[string]*compressionEntry)
	}
	entry, ok := s.compBy[key][artifactID]
	if !ok {
		entry = &compressionEntry{}
		s.compBy[key][artifactID] = entry
	}
	entry.claim = compressionClaim{
		reservationID:  reservationID,
		originalDigest: originalDigest,
		policyRevision: policyRevision,
	}
	entry.pending = &PendingCompression{
		OriginalDigest:          originalDigest,
		SemanticDigest:          semanticDigest,
		EgressPolicyHash:        egressPolicyHash,
		PolicyHashAuthoritative: false,
		CreatedAt:               now,
		PolicyRevision:          policyRevision,
		ReservationID:           reservationID,
	}
	if s.pendingPerSession == nil {
		s.pendingPerSession = make(map[string]int)
	}
	s.pendingPerSession[key]++
	s.totalPending++
	return CompressionClaim{
		Partition:      partition,
		ArtifactID:     artifactID,
		ReservationID:  reservationID,
		OriginalDigest: originalDigest,
		PolicyRevision: policyRevision,
	}, nil
}

func (s *memoryTurnStore) UpdateReservationPolicyHash(ctx context.Context, claim CompressionClaim, expectedOldHash [32]byte, semanticDigest [32]byte, newHash [32]byte, sanitization string, routeHash [32]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if claim.ArtifactID == "" || claim.ReservationID == "" || claim.PolicyRevision == "" {
		return fmt.Errorf("%w: missing update fields", ErrCompressionConflict)
	}
	if !isValidSanitization(sanitization) {
		return fmt.Errorf("%w: invalid sanitization %q", ErrCompressionConflict, sanitization)
	}
	var zeroHash [32]byte
	if newHash == zeroHash {
		return fmt.Errorf("%w: zero authoritative hash", ErrCompressionConflict)
	}
	if routeHash == zeroHash {
		return fmt.Errorf("%w: zero route hash", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := claim.Partition.key()
	entry, err := s.claimLocked(key, claim.ArtifactID, claim.ReservationID)
	if err != nil {
		return err
	}
	if entry.pending.EgressPolicyHash != expectedOldHash {
		return fmt.Errorf("%w: expected old hash mismatch", ErrCompressionConflict)
	}
	if err := validateCompressionClaim(entry, claim); err != nil {
		return err
	}
	if entry.pending.SemanticDigest != semanticDigest {
		return fmt.Errorf("%w: semantic digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: already authoritative", ErrCompressionConflict)
	}
	// verify artifact still exists LAST
	if !s.artifactExistsLocked(key, claim.ArtifactID) {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	entry.pending.EgressPolicyHash = newHash
	entry.pending.Sanitization = sanitization
	entry.pending.AuthorizedRouteHash = routeHash
	entry.pending.PolicyHashAuthoritative = true
	return nil
}

func (s *memoryTurnStore) BindCompressionJob(ctx context.Context, claim CompressionClaim, jobID auxiliary.JobID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if claim.ReservationID == "" || jobID == "" || claim.ArtifactID == "" {
		return fmt.Errorf("%w: missing binding fields", ErrCompressionConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := claim.Partition.key()
	entry, err := s.claimLocked(key, claim.ArtifactID, claim.ReservationID)
	if err != nil {
		return err
	}
	if err := validateCompressionClaim(entry, claim); err != nil {
		return err
	}
	if !entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: not authoritative", ErrCompressionConflict)
	}
	// verify artifact still exists LAST
	if !s.artifactExistsLocked(key, claim.ArtifactID) {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	entry.pending.JobID = jobID
	return nil
}

func (s *memoryTurnStore) AttachSurrogate(ctx context.Context, claim CompressionClaim, jobID auxiliary.JobID, surrogate ReasoningSurrogate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if claim.ArtifactID == "" || claim.ReservationID == "" {
		return fmt.Errorf("%w: missing attach fields", ErrCompressionConflict)
	}
	// Defensive copy outside lock to avoid holding mutex across allocation.
	clone := cloneSurrogate(surrogate)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := claim.Partition.key()
	// verify artifact exists FIRST (preserved precedence for Attach)
	if !s.artifactExistsLocked(key, claim.ArtifactID) {
		return fmt.Errorf("%w: artifact not found", ErrCompressionNotFound)
	}
	entry, err := s.claimLocked(key, claim.ArtifactID, claim.ReservationID)
	if err != nil {
		return err
	}
	if err := validateCompressionClaim(entry, claim); err != nil {
		return err
	}
	if !entry.pending.PolicyHashAuthoritative {
		return fmt.Errorf("%w: not authoritative", ErrCompressionConflict)
	}
	if entry.pending.JobID != "" && entry.pending.JobID != jobID {
		return fmt.Errorf("%w: job mismatch", ErrCompressionConflict)
	}
	if entry.pending.OriginalDigest != clone.OriginalDigest {
		return fmt.Errorf("%w: digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.PolicyRevision != clone.PolicyRevision {
		return fmt.Errorf("%w: policy mismatch", ErrCompressionConflict)
	}
	// Correlation-digest CAS: SemanticDigest and EgressPolicyHash must match
	// the pending reservation. Zero semantic digest is invalid (real artifact
	// always has content) and is rejected as a conflict.
	var zeroDigest [32]byte
	if clone.SemanticDigest == zeroDigest {
		return fmt.Errorf("%w: zero semantic digest", ErrCompressionConflict)
	}
	if entry.pending.SemanticDigest == zeroDigest {
		return fmt.Errorf("%w: zero semantic digest", ErrCompressionConflict)
	}
	if entry.pending.SemanticDigest != clone.SemanticDigest {
		return fmt.Errorf("%w: semantic digest mismatch", ErrCompressionConflict)
	}
	if entry.pending.EgressPolicyHash != clone.EgressPolicyHash {
		return fmt.Errorf("%w: egress policy hash mismatch", ErrCompressionConflict)
	}
	if !isValidSanitization(clone.Sanitization) {
		return fmt.Errorf("%w: invalid surrogate sanitization %q", ErrCompressionConflict, clone.Sanitization)
	}
	if !isValidSanitization(entry.pending.Sanitization) {
		return fmt.Errorf("%w: invalid pending sanitization %q", ErrCompressionConflict, entry.pending.Sanitization)
	}
	if entry.pending.Sanitization != clone.Sanitization {
		return fmt.Errorf("%w: sanitization mismatch", ErrCompressionConflict)
	}
	var zeroRouteHash [32]byte
	if entry.pending.AuthorizedRouteHash == zeroRouteHash {
		return fmt.Errorf("%w: zero pending route hash", ErrCompressionConflict)
	}
	if clone.AuthorizedRouteHash == zeroRouteHash {
		return fmt.Errorf("%w: zero surrogate route hash", ErrCompressionConflict)
	}
	if entry.pending.AuthorizedRouteHash != clone.AuthorizedRouteHash {
		return fmt.Errorf("%w: route hash mismatch", ErrCompressionConflict)
	}
	// oldBytes is the existing surrogate size for this artifact (replacement path).
	// By construction oldBytes <= cur session/total counters.
	oldBytes := 0
	if entry.surrogate != nil {
		oldBytes = entry.surrogate.Bytes
	}
	// budget checks with delta accounting for replacement.
	if s.opts.CompressionLimits.MaxSurrogateBytesPerTurn > 0 && clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesPerTurn {
		return &BudgetError{Kind: BudgetSurrogatePerTurn, Limit: s.opts.CompressionLimits.MaxSurrogateBytesPerTurn, Current: clone.Bytes}
	}
	if s.opts.CompressionLimits.MaxSurrogateBytesPerSession > 0 {
		cur := s.surrogateBytesPerSession[key]
		// oldBytes <= cur by invariant; cur-oldBytes+new is the post-replacement total.
		if cur-oldBytes+clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesPerSession {
			return &BudgetError{Kind: BudgetSurrogatePerSession, Limit: s.opts.CompressionLimits.MaxSurrogateBytesPerSession, Current: cur}
		}
	}
	if s.opts.CompressionLimits.MaxSurrogateBytesTotal > 0 {
		curTotal := s.totalSurrogateBytes
		if curTotal-oldBytes+clone.Bytes > s.opts.CompressionLimits.MaxSurrogateBytesTotal {
			return &BudgetError{Kind: BudgetSurrogateTotal, Limit: s.opts.CompressionLimits.MaxSurrogateBytesTotal, Current: curTotal}
		}
	}
	// success: decrement pending exactly once
	if s.pendingPerSession[key] > 0 {
		s.pendingPerSession[key]--
		if s.pendingPerSession[key] == 0 {
			delete(s.pendingPerSession, key)
		}
	}
	if s.totalPending > 0 {
		s.totalPending--
	}
	entry.surrogate = &clone
	entry.pending = nil
	// keep reservationID/policy for lookup but pending cleared
	if s.surrogateBytesPerSession == nil {
		s.surrogateBytesPerSession = make(map[string]int)
	}
	curSess := s.surrogateBytesPerSession[key]
	newSess := curSess - oldBytes + clone.Bytes
	if newSess <= 0 {
		delete(s.surrogateBytesPerSession, key)
	} else {
		s.surrogateBytesPerSession[key] = newSess
	}
	s.totalSurrogateBytes = s.totalSurrogateBytes - oldBytes + clone.Bytes
	return nil
}

func (s *memoryTurnStore) ClearCompression(ctx context.Context, partition SessionPartition, artifactID string, expectedReservationID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifactID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return nil
	}
	entry, ok := m[artifactID]
	if !ok {
		return nil
	}
	if expectedReservationID != "" && entry.claim.reservationID != expectedReservationID {
		return fmt.Errorf("%w: reservation mismatch on clear", ErrCompressionConflict)
	}
	if expectedReservationID != "" && entry.pending != nil && entry.pending.ReservationID != expectedReservationID {
		return fmt.Errorf("%w: pending reservation mismatch on clear", ErrCompressionConflict)
	}
	// CAS clear of expected pending: preserve surrogate if exists.
	if expectedReservationID != "" {
		if entry.pending != nil {
			if c := s.pendingPerSession[key]; c > 0 {
				s.pendingPerSession[key] = c - 1
				if s.pendingPerSession[key] == 0 {
					delete(s.pendingPerSession, key)
				}
			}
			if s.totalPending > 0 {
				s.totalPending--
			}
			entry.pending = nil
			if entry.surrogate == nil {
				delete(m, artifactID)
				if len(m) == 0 {
					delete(s.compBy, key)
				}
			}
		}
		return nil
	}
	s.clearOptionalLocked(key, artifactID)
	return nil
}

func (s *memoryTurnStore) GetCompressionState(ctx context.Context, partition SessionPartition, artifactID string) (CompressionState, bool, error) {
	if err := ctx.Err(); err != nil {
		return CompressionState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	m, ok := s.compBy[key]
	if !ok {
		return CompressionState{}, false, nil
	}
	entry, ok := m[artifactID]
	if !ok {
		return CompressionState{}, false, nil
	}
	state := CompressionState{
		PolicyRevision: entry.claim.policyRevision,
		ReservationID:  entry.claim.reservationID,
	}
	if entry.pending != nil {
		cp := *entry.pending
		state.Pending = &cp
	}
	if entry.surrogate != nil {
		cs := cloneSurrogate(*entry.surrogate)
		state.Surrogate = &cs
	}
	return state, true, nil
}

func (s *memoryTurnStore) CompressionStats() CompressionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := CompressionStats{
		TotalPending:             s.totalPending,
		TotalSurrogateBytes:      s.totalSurrogateBytes,
		PendingPerSession:        make(map[string]int, len(s.pendingPerSession)),
		SurrogateBytesPerSession: make(map[string]int, len(s.surrogateBytesPerSession)),
	}
	maps.Copy(out.PendingPerSession, s.pendingPerSession)
	maps.Copy(out.SurrogateBytesPerSession, s.surrogateBytesPerSession)
	return out
}

func cloneSurrogate(in ReasoningSurrogate) ReasoningSurrogate {
	out := in
	if len(in.Segments) > 0 {
		out.Segments = make([]SurrogateSegment, len(in.Segments))
		copy(out.Segments, in.Segments)
	}
	return out
}
