package continuation

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type memoryScopedKey struct {
	scope Scope
	id    ResponseID
}

type memoryStoredRecord struct {
	record             ContinuationRecord
	expiresAt          time.Time
	reservationExpires time.Time
	bytes              int64
}

// MemoryStore is a bounded in-memory implementation of the continuation Store
// port. It is intentionally protocol-neutral and safe for concurrent use.
type MemoryStore struct {
	mu       sync.Mutex
	records  map[memoryScopedKey]memoryStoredRecord
	reserved map[memoryScopedKey]memoryStoredRecord
	now      func() time.Time
	limits   StorageLimits
	closed   bool
}

// NewMemoryStore returns an empty bounded in-memory continuation store.
func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithLimits(DefaultStorageLimits())
}

// NewMemoryStoreWithLimits creates a bounded in-memory continuation store.
func NewMemoryStoreWithLimits(limits StorageLimits) *MemoryStore {
	limits = mergeMemoryLimits(limits)
	return &MemoryStore{
		records:  make(map[memoryScopedKey]memoryStoredRecord),
		reserved: make(map[memoryScopedKey]memoryStoredRecord),
		now:      time.Now,
		limits:   limits,
	}
}

// Close idempotently closes the store and clears records and reservations.
func (s *MemoryStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.records = nil
	s.reserved = nil
	return nil
}

func (s *MemoryStore) Reserve(ctx context.Context, scope Scope, policy StoragePolicy) (ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if scope.IsZero() {
		return "", ErrPreviousResponseNotFound
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrStoreClosed
	}
	s.cleanupLocked(s.now())
	if s.limits.MaxRecords > 0 && len(s.records)+len(s.reserved) >= s.limits.MaxRecords {
		return "", ErrStorageLimitExceeded
	}
	exp := s.expiry(policy)
	for range 5 {
		id, err := NewResponseID(ctx)
		if err != nil {
			return "", err
		}
		key := memoryScopedKey{scope: scope, id: id}
		if _, exists := s.reserved[key]; exists {
			continue
		}
		if _, exists := s.records[key]; exists {
			continue
		}
		s.reserved[key] = memoryStoredRecord{
			record:    ContinuationRecord{ID: id, Scope: scope, Policy: policy},
			expiresAt: exp, reservationExpires: reservationExpiry(s.now(), policy),
		}
		return id, nil
	}
	return "", fmt.Errorf("continuation: failed to generate unique response id after retries")
}

func (s *MemoryStore) PutTerminal(ctx context.Context, record ContinuationRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Scope.IsZero() || record.ID.IsZero() || !record.Terminal {
		if !record.Terminal {
			return ErrRecordNotReady
		}
		return ErrPreviousResponseNotFound
	}
	if err := record.ID.Validate(); err != nil {
		return ErrPreviousResponseNotFound
	}
	key := memoryScopedKey{scope: record.Scope, id: record.ID}
	stored := CloneRecord(record)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	s.cleanupLocked(s.now())
	reservation, reserved := s.reserved[key]
	if !reserved {
		return ErrPreviousResponseNotFound
	}
	stored.Policy = reservation.record.Policy
	exp := reservation.expiresAt
	if len(stored.NativeRefs) > 0 {
		return ErrNativeReferencesUnprotected
	}
	if !record.ExpiresAt.IsZero() && (exp.IsZero() || record.ExpiresAt.Before(exp)) {
		exp = record.ExpiresAt
	}
	if stored.Status == RecordStatusIncomplete && !stored.Policy.AllowIncomplete {
		return ErrIncompleteNotEligible
	}
	if stored.Status == RecordStatusFailed {
		return ErrRecordNotEligible
	}
	if stored.ChainDepth > s.limits.MaxChainDepth {
		return ErrChainDepthExceeded
	}
	size := RecordSize(stored)
	if s.limits.MaxRecordBytes > 0 && size > s.limits.MaxRecordBytes {
		return ErrStorageLimitExceeded
	}
	if s.limits.MaxBytes > 0 && s.currentBytesLocked()+size > s.limits.MaxBytes {
		return ErrStorageLimitExceeded
	}
	delete(s.reserved, key)
	s.records[key] = memoryStoredRecord{record: stored, expiresAt: exp, bytes: size}
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, scope Scope, id ResponseID) (ContinuationRecord, error) {
	if err := ctx.Err(); err != nil {
		return ContinuationRecord{}, err
	}
	if scope.IsZero() || id.IsZero() {
		return ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ContinuationRecord{}, ErrStoreClosed
	}
	key := memoryScopedKey{scope: scope, id: id}
	if rec, ok := s.records[key]; ok {
		if !rec.expiresAt.IsZero() && !s.now().Before(rec.expiresAt) {
			delete(s.records, key)
			return ContinuationRecord{}, ErrPreviousResponseNotFound
		}
		return CloneRecord(rec.record), nil
	}
	return ContinuationRecord{}, ErrPreviousResponseNotFound
}

func (s *MemoryStore) Delete(ctx context.Context, scope Scope, id ResponseID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	key := memoryScopedKey{scope: scope, id: id}
	delete(s.records, key)
	delete(s.reserved, key)
	return nil
}

func (s *MemoryStore) cleanupLocked(now time.Time) {
	for key, rec := range s.records {
		if !rec.expiresAt.IsZero() && !now.Before(rec.expiresAt) {
			delete(s.records, key)
		}
	}
	for key, rec := range s.reserved {
		if !rec.reservationExpires.IsZero() && !now.Before(rec.reservationExpires) {
			delete(s.reserved, key)
		}
	}
}

func (s *MemoryStore) currentBytesLocked() int64 {
	var total int64
	for _, rec := range s.records {
		total += rec.bytes
	}
	return total
}

func mergeMemoryLimits(limits StorageLimits) StorageLimits {
	defaults := DefaultStorageLimits()
	if limits.MaxRecords == 0 {
		limits.MaxRecords = defaults.MaxRecords
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxRecordBytes == 0 {
		limits.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if limits.MaxChainDepth == 0 {
		limits.MaxChainDepth = defaults.MaxChainDepth
	}
	return limits
}

func (s *MemoryStore) expiry(policy StoragePolicy) time.Time {
	if policy.TTL <= 0 {
		return time.Time{}
	}
	return s.now().Add(policy.TTL)
}

const defaultMemoryReservationTTL = 24 * time.Hour

func reservationExpiry(now time.Time, policy StoragePolicy) time.Time {
	if policy.TTL > 0 {
		return now.Add(policy.TTL)
	}
	return now.Add(defaultMemoryReservationTTL)
}

// SetClock replaces the store clock for deterministic tests.
func (s *MemoryStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

var _ Store = (*MemoryStore)(nil)
