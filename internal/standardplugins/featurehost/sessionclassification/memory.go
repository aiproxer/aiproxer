package sessionclassification

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

const (
	defaultMemoryStoreEntries = 4096
	maxMemoryStoreEntries     = 65536
	defaultMemoryStoreIdleTTL = 30 * time.Minute
	maxMemoryStoreIdleTTL     = 30 * 24 * time.Hour
	idleCleanupScanBudget     = 16
	maxLeaseNonceBytes        = featurestate.MaxRemoteLeaseIDBytes - 21
)

// MemoryStoreConfig places finite process-local bounds on the authoritative
// store. Idle cleanup applies only to unused unknown records with no remote
// attempts or active control state; positives and attempted unknowns are pinned.
type MemoryStoreConfig struct {
	MaxEntries int
	IdleTTL    time.Duration
}

// MemoryStoreOption supplies process-owned clocks and lease-token generation.
// Both callbacks run outside the store mutex.
type MemoryStoreOption func(*memoryStoreSettings)

type memoryStoreSettings struct {
	now   func() time.Time
	nonce func() (string, error)
}

type memorySlot struct {
	occupied bool
	record   featurestate.Record
}

// MemoryStore is an authoritative, bounded in-process store. It does not evict
// established classifications or remote-attempt history to admit new keys.
type MemoryStore struct {
	mu sync.Mutex

	config MemoryStoreConfig
	now    func() time.Time
	nonce  func() (string, error)

	entries       map[featurestate.Key]int
	slots         []memorySlot
	freeSlots     []int
	cleanupCursor int
	leaseSequence uint64
}

var _ featurestate.Store = (*MemoryStore)(nil)

// NewMemoryStore creates a finite process-local state store. Zero configuration
// fields use documented bounded defaults; negative or oversized bounds fail.
func NewMemoryStore(config MemoryStoreConfig, options ...MemoryStoreOption) (*MemoryStore, error) {
	if config.MaxEntries == 0 {
		config.MaxEntries = defaultMemoryStoreEntries
	}
	if config.MaxEntries < 1 || config.MaxEntries > maxMemoryStoreEntries {
		return nil, featurestate.ErrInvalidStoreConfig
	}
	if config.IdleTTL == 0 {
		config.IdleTTL = defaultMemoryStoreIdleTTL
	}
	if config.IdleTTL < 0 || config.IdleTTL > maxMemoryStoreIdleTTL {
		return nil, featurestate.ErrInvalidStoreConfig
	}

	settings := memoryStoreSettings{now: time.Now, nonce: randomLeaseNonce}
	for _, option := range options {
		if option == nil {
			return nil, featurestate.ErrInvalidStoreConfig
		}
		option(&settings)
	}
	if settings.now == nil || settings.nonce == nil {
		return nil, featurestate.ErrInvalidStoreConfig
	}

	freeSlots := make([]int, config.MaxEntries)
	for i := range freeSlots {
		freeSlots[i] = len(freeSlots) - i - 1
	}
	return &MemoryStore{
		config:    config,
		now:       settings.now,
		nonce:     settings.nonce,
		entries:   make(map[featurestate.Key]int, config.MaxEntries),
		slots:     make([]memorySlot, config.MaxEntries),
		freeSlots: freeSlots,
	}, nil
}

// WithClock injects the clock used only by lazy cleanup during Load. Calls to
// the clock happen before the store lock is acquired.
func WithClock(now func() time.Time) MemoryStoreOption {
	return func(settings *memoryStoreSettings) { settings.now = now }
}

// WithLeaseNonceGenerator injects a bounded nonce source. The source is called
// outside the store lock; a per-store sequence makes emitted lease IDs unique.
func WithLeaseNonceGenerator(nonce func() (string, error)) MemoryStoreOption {
	return func(settings *memoryStoreSettings) { settings.nonce = nonce }
}

// Load returns a record without allocating a missing key. A bounded idle-cleanup
// slice runs lazily using the injected clock before the map lookup.
func (s *MemoryStore) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}
	if s == nil {
		return featurestate.Record{}, false, featurestate.ErrInvalidStoreConfig
	}
	now := s.now()
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}

	s.mu.Lock()
	s.cleanupIdleLocked(now)
	index, found := s.entries[key]
	if !found {
		s.mu.Unlock()
		return featurestate.Record{}, false, nil
	}
	record := s.slots[index].record
	s.mu.Unlock()
	return record, true, nil
}

// Promote atomically stores the first valid positive classification at the
// store-owned revision 1. Later proposals cannot rewrite source or evidence.
func (s *MemoryStore) Promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidatePositiveProposal(proposal); err != nil {
		return featurestate.Record{}, false, err
	}
	if s == nil {
		return featurestate.Record{}, false, featurestate.ErrInvalidStoreConfig
	}
	proposal.Revision = 1

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupIdleLocked(now)
	if index, found := s.entries[key]; found {
		current := s.slots[index].record
		if current.Classification.IsCodingAgent() {
			return current, false, nil
		}
		current.Classification = proposal
		current.RemoteLeaseID = ""
		current.RemoteLeaseUntil = time.Time{}
		current.RemoteNextEligibleAt = time.Time{}
		current.UpdatedAt = now
		s.slots[index].record = current
		return current, true, nil
	}

	record := featurestate.Record{Key: key, Classification: proposal, UpdatedAt: now}
	if err := s.insertRecordLocked(record); err != nil {
		return featurestate.Record{}, false, err
	}
	return record, true, nil
}

// ClaimRemote consumes one finite attempt and publishes its lease before the
// caller performs network I/O. Backoff is applied at completion, so an
// abandoned claim becomes eligible immediately when its lease expires.
func (s *MemoryStore) ClaimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if maxAttempts == 0 || maxAttempts > featurestate.MaxRemoteAttemptsPerSession || leaseTTL <= 0 || leaseTTL > featurestate.MaxRemoteLeaseTTL || retryBackoff < 0 || retryBackoff > featurestate.MaxRemoteRetryBackoff {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrInvalidRemoteOptions
	}
	if s == nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrInvalidStoreConfig
	}

	// Check whether the attempt can be accepted before asking the nonce source.
	s.mu.Lock()
	s.cleanupIdleLocked(now)
	current, exists := s.recordLocked(key)
	if !remoteClaimEligible(current, exists, now, maxAttempts) {
		s.mu.Unlock()
		return featurestate.RemoteClaim{}, current, false, nil
	}
	if !exists && len(s.freeSlots) == 0 {
		s.mu.Unlock()
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrStoreCapacity
	}
	s.mu.Unlock()

	// The injected source may block or perform entropy I/O, so never call it while
	// holding the shared map mutex. Recheck state after reacquiring the lock.
	nonce, err := s.nonce()
	if err != nil || !validLeaseNonce(nonce) {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrLeaseNonce
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupIdleLocked(now)
	current, exists = s.recordLocked(key)
	if !remoteClaimEligible(current, exists, now, maxAttempts) {
		return featurestate.RemoteClaim{}, current, false, nil
	}
	if !exists && len(s.freeSlots) == 0 {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrStoreCapacity
	}
	if s.leaseSequence == math.MaxUint64 {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrLeaseSequenceExhausted
	}
	s.leaseSequence++
	leaseID := nonce + "-" + strconv.FormatUint(s.leaseSequence, 10)
	if len(leaseID) > featurestate.MaxRemoteLeaseIDBytes {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrLeaseNonce
	}

	if !exists {
		current = featurestate.Record{Key: key}
	}
	current.RemoteAttempts++
	current.RemoteLeaseID = leaseID
	current.RemoteLeaseUntil = featurestate.RoundRemoteDeadlineUpToMicrosecond(now.Add(leaseTTL))
	current.UpdatedAt = now
	if exists {
		s.slots[s.entries[key]].record = current
	} else if err := s.insertRecordLocked(current); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	claim := featurestate.RemoteClaim{Key: key, LeaseID: leaseID, Attempt: current.RemoteAttempts, RetryBackoff: retryBackoff}
	return claim, current, true, nil
}

// CompleteRemote completes only the currently held lease. Neutral completions
// clear the lease and start bounded backoff without writing a negative class.
func (s *MemoryStore) CompleteRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	if err := featurestate.ValidateKey(claim.Key); err != nil || !validLeaseID(claim.LeaseID) || claim.Attempt == 0 || claim.Attempt > featurestate.MaxRemoteAttemptsPerSession || claim.RetryBackoff < 0 || claim.RetryBackoff > featurestate.MaxRemoteRetryBackoff {
		return featurestate.Record{}, featurestate.ErrInvalidRemoteClaim
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, err
	}
	proposal := result.Proposal
	if proposal != (session.Classification{}) {
		if err := featurestate.ValidatePositiveProposal(proposal); err != nil || proposal.Source != session.SourceRemote {
			return featurestate.Record{}, featurestate.ErrInvalidProposal
		}
		proposal.Revision = 1
	}
	if s == nil {
		return featurestate.Record{}, featurestate.ErrInvalidStoreConfig
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupIdleLocked(now)
	current, exists := s.recordLocked(claim.Key)
	if !exists || current.RemoteLeaseID != claim.LeaseID || current.RemoteAttempts != claim.Attempt || current.RemoteLeaseUntil.IsZero() || !now.Before(current.RemoteLeaseUntil) {
		return current, featurestate.ErrStaleRemoteClaim
	}
	current.RemoteLeaseID = ""
	current.RemoteLeaseUntil = time.Time{}
	if proposal != (session.Classification{}) && !current.Classification.IsCodingAgent() {
		current.Classification = proposal
		current.RemoteNextEligibleAt = time.Time{}
	} else if current.Classification.IsCodingAgent() {
		current.RemoteNextEligibleAt = time.Time{}
	} else if claim.RetryBackoff == 0 {
		current.RemoteNextEligibleAt = time.Time{}
	} else {
		current.RemoteNextEligibleAt = featurestate.RoundRemoteDeadlineUpToMicrosecond(now.Add(claim.RetryBackoff))
	}
	current.UpdatedAt = now
	s.slots[s.entries[claim.Key]].record = current
	return current, nil
}

func (s *MemoryStore) recordLocked(key featurestate.Key) (featurestate.Record, bool) {
	index, found := s.entries[key]
	if !found {
		return featurestate.Record{}, false
	}
	return s.slots[index].record, true
}

// insertRecordLocked is also used by package tests to characterize cleanup of
// an otherwise unreachable zero-attempt placeholder record.
func (s *MemoryStore) insertRecordLocked(record featurestate.Record) error {
	if err := featurestate.ValidateKey(record.Key); err != nil {
		return err
	}
	if _, exists := s.entries[record.Key]; exists || len(s.freeSlots) == 0 {
		return featurestate.ErrStoreCapacity
	}
	last := len(s.freeSlots) - 1
	index := s.freeSlots[last]
	s.freeSlots = s.freeSlots[:last]
	s.entries[record.Key] = index
	s.slots[index] = memorySlot{occupied: true, record: record}
	return nil
}

func (s *MemoryStore) cleanupIdleLocked(now time.Time) {
	if len(s.slots) == 0 {
		return
	}
	scans := min(len(s.slots), idleCleanupScanBudget)
	for range scans {
		index := s.cleanupCursor
		s.cleanupCursor++
		if s.cleanupCursor == len(s.slots) {
			s.cleanupCursor = 0
		}
		slot := s.slots[index]
		if !slot.occupied || !eligibleIdleUnknown(slot.record) || slot.record.UpdatedAt.IsZero() {
			continue
		}
		if now.Before(slot.record.UpdatedAt.Add(s.config.IdleTTL)) {
			continue
		}
		s.deleteSlotLocked(index)
	}
}

func (s *MemoryStore) deleteSlotLocked(index int) {
	slot := s.slots[index]
	if !slot.occupied {
		return
	}
	delete(s.entries, slot.record.Key)
	s.slots[index] = memorySlot{}
	s.freeSlots = append(s.freeSlots, index)
}

func eligibleIdleUnknown(record featurestate.Record) bool {
	return record.Classification == (session.Classification{}) &&
		record.RemoteAttempts == 0 &&
		record.RemoteLeaseID == "" &&
		record.RemoteLeaseUntil.IsZero() &&
		record.RemoteNextEligibleAt.IsZero()
}

func remoteClaimEligible(record featurestate.Record, exists bool, now time.Time, maxAttempts uint32) bool {
	if !exists {
		return true
	}
	if record.Classification != (session.Classification{}) {
		return false
	}
	if record.RemoteLeaseID != "" && (record.RemoteLeaseUntil.IsZero() || now.Before(record.RemoteLeaseUntil)) {
		return false
	}
	if record.RemoteAttempts >= maxAttempts {
		return false
	}
	return record.RemoteNextEligibleAt.IsZero() || !now.Before(record.RemoteNextEligibleAt)
}

func validLeaseNonce(nonce string) bool {
	if nonce == "" || len(nonce) > maxLeaseNonceBytes || !utf8.ValidString(nonce) {
		return false
	}
	for i := 0; i < len(nonce); i++ {
		ch := nonce[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func validLeaseID(id string) bool {
	if id == "" || len(id) > featurestate.MaxRemoteLeaseIDBytes || !utf8.ValidString(id) {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func randomLeaseNonce() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", featurestate.ErrLeaseNonce
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}
