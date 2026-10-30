package openresponses

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// WSLocalContinuationConfig bounds the connection-local store:false continuation
// state allocated for one authenticated WebSocket session. State is connection-
// scoped, never written to a durable store, and released when the session closes.
// Reconnects therefore allocate an empty store and every store:false response ID
// from an earlier connection is indistinguishable from a missing parent.
type WSLocalContinuationConfig struct {
	Enabled bool
	// Limits bound one connection's retained terminal records.
	Limits aipcont.StorageLimits
	// MaterializeBounds bound parent-chain reconstruction for a continued turn.
	MaterializeBounds aipcont.Bounds
	// StoreFactory builds the per-connection store. A nil factory uses the
	// bounded in-memory connection-local store; tests inject tracking wrappers.
	StoreFactory func(scope aipcont.Scope) aipcont.Store
}

// DefaultWSLocalContinuation derives connection-local continuation bounds from a
// validated frontend config. Zero continuation fields fall back to profile
// defaults so tests and the default frontend config stay consistent.
func DefaultWSLocalContinuation(cfg Config) WSLocalContinuationConfig {
	depth := cfg.Continuation.MaxChainDepth
	if depth <= 0 {
		depth = DefaultMaxChainDepth
	}
	maxBytes := cfg.Continuation.MaxMaterializedBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMaterializedBytes
	}
	return WSLocalContinuationConfig{
		Enabled: true,
		Limits: aipcont.StorageLimits{
			MaxRecords:     256,
			MaxBytes:       maxBytes,
			MaxRecordBytes: 16 << 20,
			MaxChainDepth:  depth,
		},
		MaterializeBounds: aipcont.Bounds{
			MaxChainDepth:        depth,
			MaxMaterializedBytes: maxBytes,
			MaxMaterializedItems: 100_000,
		},
	}
}

// NewWSLocalStore builds a connection-scoped bounded continuation store. The
// store accepts terminal records without a prior reservation so the incremental
// recorder can persist completed turns under the proxy-issued response ID.
func NewWSLocalStore(scope aipcont.Scope, limits aipcont.StorageLimits) aipcont.Store {
	return newWSLocalStore(scope, limits)
}

// wsContinuationScope isolates one connection's local continuation records.
// The fresh per-connection identity guarantees reconnect isolation even when the
// authenticated principal reconnects on a new socket.
func wsContinuationScope(decision sdkauth.Decision, connectionID string) aipcont.Scope {
	scope := aipcont.Scope{ConnectionID: connectionID}
	if decision.Scope != nil {
		scope.TenantID = decision.Scope.TenantID.String()
		scope.PrincipalID = decision.Scope.PrincipalID.String()
	}
	if scope.PrincipalID == "" {
		scope.PrincipalID = decision.Principal.ID
	}
	return scope
}

func newWSConnectionID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "conn_" + hex.EncodeToString(raw[:])
	}
	return "conn_" + time.Now().Format("20060102150405.000000000")
}

// evictWSContinuationParent deletes a referenced local parent after a classified
// 4xx/5xx-equivalent continuation failure. Disconnect, cancellation, unrelated
// transport failure, and successful turns never call this helper.
func evictWSContinuationParent(store aipcont.Store, scope aipcont.Scope, parentID aipcont.ResponseID) {
	if store == nil || parentID.IsZero() {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = store.Delete(cleanupCtx, scope, parentID)
}

// wsLocalStore is the bounded connection-local continuation store. It is owned
// by one WebSocket session and cleared exactly once when the session closes.
// Records are keyed by the proxy-issued response ID so the client-facing ID is
// the authoritative continuation reference for the active connection.
type wsLocalStore struct {
	mu      sync.Mutex
	scope   aipcont.Scope
	limits  aipcont.StorageLimits
	records map[aipcont.ResponseID]aipcont.ContinuationRecord
	order   []aipcont.ResponseID
	bytes   int64
	closed  bool
}

func newWSLocalStore(scope aipcont.Scope, limits aipcont.StorageLimits) *wsLocalStore {
	defaults := aipcont.DefaultStorageLimits()
	if limits.MaxRecords <= 0 {
		limits.MaxRecords = defaults.MaxRecords
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxRecordBytes <= 0 {
		limits.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if limits.MaxChainDepth <= 0 {
		limits.MaxChainDepth = defaults.MaxChainDepth
	}
	return &wsLocalStore{
		scope:   scope,
		limits:  limits,
		records: make(map[aipcont.ResponseID]aipcont.ContinuationRecord),
	}
}

var _ aipcont.Store = (*wsLocalStore)(nil)

// Reserve satisfies the Store port. The runner records turns under the proxy
// response ID via PutTerminal without a reservation, so Reserve only issues a
// fresh cryptographic ID for the connection scope.
func (s *wsLocalStore) Reserve(ctx context.Context, scope aipcont.Scope, _ aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !s.scope.Equal(scope) {
		return "", aipcont.ErrPreviousResponseNotFound
	}
	id, err := aipcont.NewResponseID(ctx)
	if err != nil {
		return "", aipcont.ErrStorageFailure
	}
	return id, nil
}

// PutTerminal stores a completed record, validating the proxy ID and enforcing
// connection-local bounds. The oldest record is evicted when the store is full.
func (s *wsLocalStore) PutTerminal(ctx context.Context, record aipcont.ContinuationRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipcont.ErrStoreClosed
	}
	if !s.scope.Equal(record.Scope) {
		return aipcont.ErrPreviousResponseNotFound
	}
	if !record.Terminal {
		return aipcont.ErrRecordNotReady
	}
	if err := record.ID.Validate(); err != nil {
		return aipcont.ErrPreviousResponseNotFound
	}
	if record.ChainDepth > s.limits.MaxChainDepth {
		return aipcont.ErrChainDepthExceeded
	}
	size := aipcont.RecordSize(record)
	if s.limits.MaxRecordBytes > 0 && size > s.limits.MaxRecordBytes {
		return aipcont.ErrStorageLimitExceeded
	}
	if s.limits.MaxBytes > 0 && size > s.limits.MaxBytes {
		return aipcont.ErrStorageLimitExceeded
	}
	stored := aipcont.CloneRecord(record)
	if prev, ok := s.records[stored.ID]; ok {
		s.bytes -= aipcont.RecordSize(prev)
	} else {
		s.order = append(s.order, stored.ID)
	}
	s.records[stored.ID] = stored
	s.bytes += size
	s.evictLocked()
	return nil
}

func (s *wsLocalStore) evictLocked() {
	for len(s.order) > 0 {
		overRecords := s.limits.MaxRecords > 0 && len(s.records) > s.limits.MaxRecords
		overBytes := s.limits.MaxBytes > 0 && s.bytes > s.limits.MaxBytes
		if !overRecords && !overBytes {
			return
		}
		oldest := s.order[0]
		s.order = s.order[1:]
		rec, ok := s.records[oldest]
		if !ok {
			continue
		}
		delete(s.records, oldest)
		s.bytes -= aipcont.RecordSize(rec)
	}
}

// Get returns the terminal record for a proxy ID under the connection scope.
func (s *wsLocalStore) Get(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	if err := ctx.Err(); err != nil {
		return aipcont.ContinuationRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipcont.ContinuationRecord{}, aipcont.ErrStoreClosed
	}
	if !s.scope.Equal(scope) {
		return aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
	}
	if rec, ok := s.records[id]; ok {
		return aipcont.CloneRecord(rec), nil
	}
	return aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
}

// Delete removes a record idempotently within the connection scope.
func (s *wsLocalStore) Delete(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipcont.ErrStoreClosed
	}
	if !s.scope.Equal(scope) {
		return nil
	}
	if rec, ok := s.records[id]; ok {
		delete(s.records, id)
		s.bytes -= aipcont.RecordSize(rec)
	}
	for i, rid := range s.order {
		if rid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// Close clears all connection-local state exactly once.
func (s *wsLocalStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.records = nil
	s.order = nil
	s.bytes = 0
	return nil
}
