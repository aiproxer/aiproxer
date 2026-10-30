// Package continuation contains the small durable continuation adapter used by
// the standard contract tests and local deployments. It owns no goroutines.
package continuation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

type fileKey struct {
	Scope aipcont.Scope
	ID    aipcont.ResponseID
}

type fileEntry struct {
	Record             aipcont.ContinuationRecord `json:"record"`
	ExpiresAt          time.Time                  `json:"expires_at,omitzero"`
	ReservationExpires time.Time                  `json:"reservation_expires_at,omitzero"`
	Bytes              int64                      `json:"bytes,omitempty"`
}

type fileState struct {
	Records  []fileEntry `json:"records,omitempty"`
	Reserved []fileEntry `json:"reserved,omitempty"`
}

// FileStore is a synchronous, restart-capable Store backed by one atomically
// replaced JSON file. It is intentionally not a general database. The file
// requests mode 0600; Windows ACL inheritance is a deployment responsibility,
// so this adapter does not claim native-reference confidentiality.
type FileStore struct {
	mu       sync.Mutex
	path     string
	lock     string
	state    fileState
	limits   aipcont.StorageLimits
	now      func() time.Time
	isClosed bool
}

// fileStoreLockAcquireTimeout bounds the startup lock wait. The per-operation
// acquire paths are request-context bounded; the constructor has no request
// context, so without a deadline a hung peer lock holder would retry forever.
const fileStoreLockAcquireTimeout = 10 * time.Second

// NewFileStore opens or creates path and loads all previously committed state.
func NewFileStore(path string, limits aipcont.StorageLimits) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty path", aipcont.ErrStorageFailure)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: path must be absolute", aipcont.ErrStorageFailure)
	}
	if hasParentTraversal(path) {
		return nil, fmt.Errorf("%w: path traversal", aipcont.ErrStorageFailure)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: store path is a symlink", aipcont.ErrStorageFailure)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: stat: %v", aipcont.ErrStorageFailure, err)
	}
	if err := rejectSymlinkParents(filepath.Dir(path)); err != nil {
		return nil, err
	}
	s := &FileStore{path: path, lock: lockPath(path), limits: mergeLimits(limits), now: time.Now}
	lockCtx, cancelLock := context.WithTimeout(context.Background(), fileStoreLockAcquireTimeout)
	defer cancelLock()
	lock, err := acquireFileLock(lockCtx, s.lock)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.close() }()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.persistLocked(context.Background(), fileState{}); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", aipcont.ErrStorageFailure, err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", aipcont.ErrStorageFailure, err)
	}
	normalizeReservations(s.state.Reserved, s.now())
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("%w: permissions: %v", aipcont.ErrStorageFailure, err)
	}
	if err := validateState(s.state, s.limits); err != nil {
		return nil, err
	}
	s.cleanupLocked(s.now())
	if err := s.persistLocked(context.Background(), s.state); err != nil {
		return nil, err
	}
	return s, nil
}

// Close idempotently closes the store and rejects subsequent operations.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return nil
	}
	s.isClosed = true
	s.state = fileState{}
	// Keep the lock inode in place. Removing it while another FileStore instance
	// still holds an open descriptor could allow a split-brain lock after restart.
	return nil
}

// Reserve allocates and durably records a scoped proxy ID.
func (s *FileStore) Reserve(ctx context.Context, scope aipcont.Scope, policy aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if scope.IsZero() {
		return "", aipcont.ErrPreviousResponseNotFound
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	if policy.Mode == aipcont.PersistenceConnection {
		return "", aipcont.ErrInvalidPolicy
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return "", aipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return "", err
	}
	s.cleanupLocked(s.now())
	if s.limits.MaxRecords > 0 && len(s.state.Records)+len(s.state.Reserved) >= s.limits.MaxRecords {
		return "", aipcont.ErrStorageLimitExceeded
	}
	var id aipcont.ResponseID
	for range 5 {
		id, err = newID(ctx)
		if err != nil {
			return "", err
		}
		if find(s.state.Records, fileKey{scope, id}) < 0 && find(s.state.Reserved, fileKey{scope, id}) < 0 {
			break
		}
		id = ""
	}
	if id.IsZero() {
		return "", fmt.Errorf("continuation: failed to generate unique response id after retries")
	}
	entry := fileEntry{
		Record:             aipcont.ContinuationRecord{ID: id, Scope: scope, Policy: policy},
		ExpiresAt:          expiry(s.now(), policy),
		ReservationExpires: reservationExpiry(s.now(), policy),
	}
	candidate := cloneState(s.state)
	candidate.Reserved = append(candidate.Reserved, entry)
	if err := s.persistLocked(ctx, candidate); err != nil {
		return "", err
	}
	s.state = candidate
	return id, nil
}

// PutTerminal commits a completed eligible record and consumes its reservation.
func (s *FileStore) PutTerminal(ctx context.Context, record aipcont.ContinuationRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Scope.IsZero() || record.ID.IsZero() || !record.Terminal {
		return aipcont.ErrRecordNotReady
	}
	if err := record.ID.Validate(); err != nil {
		return aipcont.ErrPreviousResponseNotFound
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return aipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return err
	}
	s.cleanupLocked(s.now())
	index := find(s.state.Reserved, fileKey{record.Scope, record.ID})
	if index < 0 {
		return aipcont.ErrPreviousResponseNotFound
	}
	reservation := s.state.Reserved[index]
	if !record.Scope.Equal(reservation.Record.Scope) {
		return aipcont.ErrPreviousResponseNotFound
	}
	stored := aipcont.CloneRecord(record)
	stored.Policy = reservation.Record.Policy
	if len(stored.NativeRefs) > 0 {
		return aipcont.ErrNativeReferencesUnprotected
	}
	if stored.ChainDepth > s.limits.MaxChainDepth {
		return aipcont.ErrChainDepthExceeded
	}
	if stored.Status == aipcont.RecordStatusIncomplete && !stored.Policy.AllowIncomplete {
		return aipcont.ErrIncompleteNotEligible
	}
	if stored.Status == aipcont.RecordStatusFailed {
		return aipcont.ErrRecordNotEligible
	}
	bytes := aipcont.RecordSize(stored)
	if s.limits.MaxRecordBytes > 0 && bytes > s.limits.MaxRecordBytes {
		return aipcont.ErrStorageLimitExceeded
	}
	if s.limits.MaxBytes > 0 && currentBytes(s.state.Records)+bytes > s.limits.MaxBytes {
		return aipcont.ErrStorageLimitExceeded
	}
	expiresAt := reservation.ExpiresAt
	if !record.ExpiresAt.IsZero() && (expiresAt.IsZero() || record.ExpiresAt.Before(expiresAt)) {
		expiresAt = record.ExpiresAt
	}
	entry := fileEntry{Record: stored, ExpiresAt: expiresAt, Bytes: bytes}
	candidate := cloneState(s.state)
	candidate.Reserved = append(candidate.Reserved[:index], candidate.Reserved[index+1:]...)
	candidate.Records = append(candidate.Records, entry)
	if err := s.persistLocked(ctx, candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

// Get performs a scoped terminal lookup with lazy expiry cleanup.
func (s *FileStore) Get(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	if err := ctx.Err(); err != nil {
		return aipcont.ContinuationRecord{}, err
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return aipcont.ContinuationRecord{}, err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return aipcont.ContinuationRecord{}, aipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return aipcont.ContinuationRecord{}, err
	}
	if s.cleanupLocked(s.now()) {
		if err := s.persistLocked(ctx, s.state); err != nil {
			return aipcont.ContinuationRecord{}, err
		}
	}
	if index := find(s.state.Records, fileKey{scope, id}); index >= 0 {
		return aipcont.CloneRecord(s.state.Records[index].Record), nil
	}
	if find(s.state.Reserved, fileKey{scope, id}) >= 0 {
		return aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
	}
	return aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
}

// Delete removes committed and reserved state idempotently.
func (s *FileStore) Delete(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := acquireFileLock(ctx, s.lock)
	if err != nil {
		return err
	}
	defer func() { _ = lock.close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return aipcont.ErrStoreClosed
	}
	if err := s.reloadLocked(); err != nil {
		return err
	}
	candidate := cloneState(s.state)
	candidate.Records = remove(candidate.Records, fileKey{scope, id})
	candidate.Reserved = remove(candidate.Reserved, fileKey{scope, id})
	if err := s.persistLocked(ctx, candidate); err != nil {
		return err
	}
	s.state = candidate
	return nil
}

func (s *FileStore) cleanupLocked(now time.Time) bool {
	recordCount := len(s.state.Records)
	reservationCount := len(s.state.Reserved)
	s.state.Records = removeExpired(s.state.Records, now)
	s.state.Reserved = removeExpiredReservations(s.state.Reserved, now)
	return len(s.state.Records) != recordCount || len(s.state.Reserved) != reservationCount
}

func (s *FileStore) persistLocked(ctx context.Context, state fileState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", aipcont.ErrStorageFailure, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".continuation-*")
	if err != nil {
		return fmt.Errorf("%w: create temp: %v", aipcont.ErrStorageFailure, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: temp permissions: %v", aipcont.ErrStorageFailure, err)
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: write: %v", aipcont.ErrStorageFailure, err)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: write: %v", aipcont.ErrStorageFailure, err)
	}
	if err := replaceFile(tmpName, s.path); err != nil {
		return fmt.Errorf("%w: rename: %v", aipcont.ErrStorageFailure, err)
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("%w: directory sync: %v", aipcont.ErrStorageFailure, err)
	}
	return nil
}

func (s *FileStore) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: read: %v", aipcont.ErrStorageFailure, err)
	}
	var state fileState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode: %v", aipcont.ErrStorageFailure, err)
	}
	normalizeReservations(state.Reserved, s.now())
	if err := validateState(state, s.limits); err != nil {
		return err
	}
	s.state = state
	return nil
}

func newID(ctx context.Context) (aipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var raw [aipcont.MinResponseIDEntropyBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("%w: entropy: %v", aipcont.ErrStorageFailure, err)
	}
	return aipcont.ResponseID(aipcont.ResponseIDPrefix + base64.RawURLEncoding.EncodeToString(raw[:])), nil
}

func expiry(now time.Time, policy aipcont.StoragePolicy) time.Time {
	if policy.TTL <= 0 {
		return time.Time{}
	}
	return now.Add(policy.TTL)
}

const defaultReservationTTL = 24 * time.Hour

func reservationExpiry(now time.Time, policy aipcont.StoragePolicy) time.Time {
	if policy.TTL > 0 {
		return now.Add(policy.TTL)
	}
	return now.Add(defaultReservationTTL)
}

func find(entries []fileEntry, key fileKey) int {
	for i, entry := range entries {
		if entry.Record.Scope.Equal(key.Scope) && entry.Record.ID == key.ID {
			return i
		}
	}
	return -1
}

func remove(entries []fileEntry, key fileKey) []fileEntry {
	if index := find(entries, key); index >= 0 {
		return append(entries[:index], entries[index+1:]...)
	}
	return entries
}

func removeExpired(entries []fileEntry, now time.Time) []fileEntry {
	out := entries[:0]
	for _, entry := range entries {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			out = append(out, entry)
		}
	}
	return out
}

func removeExpiredReservations(entries []fileEntry, now time.Time) []fileEntry {
	out := entries[:0]
	for _, entry := range entries {
		expiresAt := entry.ReservationExpires
		if expiresAt.IsZero() {
			expiresAt = entry.ExpiresAt
		}
		if expiresAt.IsZero() || now.Before(expiresAt) {
			out = append(out, entry)
		}
	}
	return out
}

func currentBytes(entries []fileEntry) int64 {
	var total int64
	for _, entry := range entries {
		total += entry.Bytes
	}
	return total
}

func cloneState(in fileState) fileState {
	data, err := json.Marshal(in)
	if err != nil {
		// Unreachable: fileState is fully JSON-serializable.
		return in
	}
	var out fileState
	if err := json.Unmarshal(data, &out); err != nil {
		// Unreachable: data was produced by json.Marshal(in) above.
		return in
	}
	return out
}

func validateState(state fileState, limits aipcont.StorageLimits) error {
	if limits.MaxRecords > 0 && len(state.Records)+len(state.Reserved) > limits.MaxRecords {
		return fmt.Errorf("%w: record count", aipcont.ErrStorageFailure)
	}
	if limits.MaxBytes > 0 && currentBytes(state.Records) > limits.MaxBytes {
		return fmt.Errorf("%w: byte count", aipcont.ErrStorageFailure)
	}
	seen := make(map[fileKey]struct{}, len(state.Records)+len(state.Reserved))
	for _, entry := range append(state.Records, state.Reserved...) {
		key := fileKey{Scope: entry.Record.Scope, ID: entry.Record.ID}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate entry", aipcont.ErrStorageFailure)
		}
		seen[key] = struct{}{}
		if entry.Record.Scope.IsZero() || entry.Record.ID.Validate() != nil {
			return fmt.Errorf("%w: invalid entry", aipcont.ErrStorageFailure)
		}
		if len(entry.Record.NativeRefs) > 0 {
			return fmt.Errorf("%w: native references are not protected", aipcont.ErrStorageFailure)
		}
		if entry.Bytes < 0 || (limits.MaxRecordBytes > 0 && entry.Bytes > limits.MaxRecordBytes) {
			return fmt.Errorf("%w: invalid byte accounting", aipcont.ErrStorageFailure)
		}
		if entry.Record.Terminal && entry.Bytes != aipcont.RecordSize(entry.Record) {
			return fmt.Errorf("%w: invalid byte accounting", aipcont.ErrStorageFailure)
		}
		if !entry.Record.Terminal && entry.ReservationExpires.IsZero() && entry.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: reservation has no expiry", aipcont.ErrStorageFailure)
		}
	}
	return nil
}

func normalizeReservations(entries []fileEntry, now time.Time) {
	for i := range entries {
		if entries[i].ReservationExpires.IsZero() {
			if entries[i].ExpiresAt.IsZero() {
				entries[i].ReservationExpires = now.Add(defaultReservationTTL)
			} else {
				entries[i].ReservationExpires = entries[i].ExpiresAt
			}
		}
	}
}

func hasParentTraversal(path string) bool {
	return slices.Contains(splitPath(path), "..")
}

func rejectSymlinkParents(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: parent path is a symlink", aipcont.ErrStorageFailure)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: parent stat: %v", aipcont.ErrStorageFailure, err)
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
	}
}

func splitPath(path string) []string {
	path = filepath.ToSlash(path)
	parts := make([]string, 0, 8)
	for part := range strings.SplitSeq(path, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func mergeLimits(limits aipcont.StorageLimits) aipcont.StorageLimits {
	defaults := aipcont.DefaultStorageLimits()
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

var _ aipcont.Store = (*FileStore)(nil)
