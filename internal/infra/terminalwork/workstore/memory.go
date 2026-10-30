package workstore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type MemoryConfig struct {
	StoreID         string
	State           *MemoryState
	DefaultPageSize int
	MaxPageSize     int
	Now             func() time.Time
}

type MemoryState struct {
	mu       sync.Mutex
	byWork   map[string]terminalwork.WorkRecord
	bySource map[string]string
}

func NewMemoryState() *MemoryState {
	return &MemoryState{
		byWork:   make(map[string]terminalwork.WorkRecord),
		bySource: make(map[string]string),
	}
}

type MemoryStore struct {
	cfg             MemoryConfig
	state           *MemoryState
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time
}

func NewMemoryStore(cfg MemoryConfig) (*MemoryStore, error) {
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("terminalwork/workstore: memory store id is required")
	}
	state := cfg.State
	if state == nil {
		state = NewMemoryState()
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if maxPageSize < def {
		return nil, fmt.Errorf("terminalwork/workstore: max page size %d < default %d", maxPageSize, def)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		cfg:             cfg,
		state:           state,
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
		now:             now,
	}, nil
}

func (s *MemoryStore) Close() error { return nil }

func (s *MemoryStore) CheckReadiness(ctx context.Context) error {
	return ctx.Err()
}

func (s *MemoryStore) storeKey(workID string) string {
	return s.cfg.StoreID + "\x00" + workID
}

func (s *MemoryStore) sourceIndex(key terminalwork.SourceKey) string {
	return s.cfg.StoreID + "\x00" + key.String()
}

func (s *MemoryStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	_, err := s.AppendIntentOutcome(ctx, rec)
	return err
}

// AppendIntentOutcome implements the definitive insert-vs-replay seam.
func (s *MemoryStore) AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	if err := ctx.Err(); err != nil {
		return terminalwork.AppendIntentOutcome{}, err
	}
	if err := rec.Validate(); err != nil {
		return terminalwork.AppendIntentOutcome{}, fmt.Errorf("terminalwork/workstore: %w", err)
	}
	if rec.State == "" {
		rec.State = sdk.WorkStateIntent
	}
	now := s.now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	cloned := cloneRecord(rec)

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	if existing, ok := s.state.byWork[s.storeKey(cloned.WorkID)]; ok {
		return resolveExistingOutcome(existing, cloned)
	}
	if workID, ok := s.state.bySource[s.sourceIndex(cloned.SourceKey)]; ok {
		existing := s.state.byWork[s.storeKey(workID)]
		return resolveExistingOutcome(existing, cloned)
	}

	s.state.byWork[s.storeKey(cloned.WorkID)] = cloned
	s.state.bySource[s.sourceIndex(cloned.SourceKey)] = cloned.WorkID
	return terminalwork.AppendIntentOutcome{Inserted: true}, nil
}

func (s *MemoryStore) GetByWorkID(ctx context.Context, workID string) (terminalwork.WorkRecord, error) {
	rec, found, err := s.LookupIntent(ctx, workID)
	if err != nil {
		return terminalwork.WorkRecord{}, err
	}
	if !found {
		return terminalwork.WorkRecord{}, ErrNotFound
	}
	return rec, nil
}

// LookupIntent implements terminalworkapp.IntentLookup.
func (s *MemoryStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return terminalwork.WorkRecord{}, false, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(workID)]
	if !ok {
		return terminalwork.WorkRecord{}, false, nil
	}
	return cloneRecord(rec), true, nil
}

func (s *MemoryStore) GetBySourceKey(ctx context.Context, key terminalwork.SourceKey) (terminalwork.WorkRecord, error) {
	if err := ctx.Err(); err != nil {
		return terminalwork.WorkRecord{}, err
	}
	if err := key.Validate(); err != nil {
		return terminalwork.WorkRecord{}, fmt.Errorf("terminalwork/workstore: %w", err)
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	workID, ok := s.state.bySource[s.sourceIndex(key)]
	if !ok {
		return terminalwork.WorkRecord{}, ErrNotFound
	}
	rec, ok := s.state.byWork[s.storeKey(workID)]
	if !ok {
		return terminalwork.WorkRecord{}, ErrNotFound
	}
	return cloneRecord(rec), nil
}

func (s *MemoryStore) PromotePending(ctx context.Context, cmd PromotePendingCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(cmd.WorkID)]
	if !ok {
		return ErrNotFound
	}
	if rec.State == sdk.WorkStatePending {
		return nil
	}
	item := rec.ToWorkItem()
	if err := item.MarkPending(); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	s.state.byWork[s.storeKey(rec.WorkID)] = rec
	return nil
}

func (s *MemoryStore) ClaimDue(ctx context.Context, cmd ClaimDueCommand) ([]terminalwork.WorkRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := normalizeClaimDueCommand(&cmd, s.now); err != nil {
		return nil, err
	}
	now := cmd.Now
	limit := cmd.Limit

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	prefix := s.cfg.StoreID + "\x00"
	candidates := make([]terminalwork.WorkRecord, 0)
	for key, rec := range s.state.byWork {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if provider := strings.TrimSpace(cmd.ProviderID); provider != "" && rec.ProviderID != provider {
			continue
		}
		if cmd.Kind != "" && rec.Kind != cmd.Kind {
			continue
		}
		if !isDueForClaim(rec, now) {
			continue
		}
		candidates = append(candidates, rec)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].WorkID < candidates[j].WorkID
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	out := make([]terminalwork.WorkRecord, 0, limit)
	for _, rec := range candidates {
		if len(out) >= limit {
			break
		}
		item := rec.ToWorkItem()
		if err := item.Claim(cmd.OwnerID, cmd.TTL, fixedClock{now}); err != nil {
			continue
		}
		rec.ApplyWorkItem(item, now)
		s.state.byWork[s.storeKey(rec.WorkID)] = rec
		out = append(out, cloneRecord(rec))
	}
	return out, nil
}

func (s *MemoryStore) RenewClaim(ctx context.Context, cmd RenewClaimCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := normalizeRenewClaimCommand(&cmd, s.now); err != nil {
		return err
	}
	now := cmd.Now
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(cmd.WorkID)]
	if !ok {
		return ErrNotFound
	}
	item := rec.ToWorkItem()
	if err := item.RenewClaim(cmd.OwnerID, cmd.TTL, fixedClock{now}); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	s.state.byWork[s.storeKey(rec.WorkID)] = rec
	return nil
}

func (s *MemoryStore) Complete(ctx context.Context, cmd CompleteCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(cmd.WorkID)]
	if !ok {
		return ErrNotFound
	}
	if rec.State != sdk.WorkStateClaimed || rec.Lease.OwnerID != cmd.ExpectedOwnerID {
		return ErrConflict
	}
	item := rec.ToWorkItem()
	if err := item.Complete(); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	s.state.byWork[s.storeKey(rec.WorkID)] = rec
	return nil
}

func (s *MemoryStore) ScheduleRetry(ctx context.Context, cmd ScheduleRetryCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(cmd.WorkID)]
	if !ok {
		return ErrNotFound
	}
	if rec.State != sdk.WorkStateClaimed || rec.Lease.OwnerID != cmd.ExpectedOwnerID {
		return ErrConflict
	}
	item := rec.ToWorkItem()
	if err := item.Retry(cmd.Schedule, fixedClock{now}, cmd.Err); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	s.state.byWork[s.storeKey(rec.WorkID)] = rec
	return nil
}

func (s *MemoryStore) Quarantine(ctx context.Context, cmd QuarantineCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	rec, ok := s.state.byWork[s.storeKey(cmd.WorkID)]
	if !ok {
		return ErrNotFound
	}
	item := rec.ToWorkItem()
	if err := item.Quarantine(cmd.Err); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	s.state.byWork[s.storeKey(rec.WorkID)] = rec
	return nil
}

func (s *MemoryStore) List(ctx context.Context, q Query) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := ValidateQuery(q); err != nil {
		return Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	offset := 0
	if cur := strings.TrimSpace(q.Cursor); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			return Page{}, fmt.Errorf("terminalwork/workstore: invalid cursor")
		}
		offset = n
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	prefix := s.cfg.StoreID + "\x00"
	matched := make([]terminalwork.WorkRecord, 0)
	for key, rec := range s.state.byWork {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !recordMatchesQuery(rec, q) {
			continue
		}
		matched = append(matched, cloneRecord(rec))
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].WorkID < matched[j].WorkID
		}
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})
	if offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+limit, len(matched))
	page := Page{Records: matched[offset:end]}
	if end < len(matched) {
		page.Cursor = strconv.Itoa(end)
	}
	return page, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func cloneRecord(r terminalwork.WorkRecord) terminalwork.WorkRecord {
	out := r
	if len(r.Payload) > 0 {
		out.Payload = append([]byte(nil), r.Payload...)
	}
	return out
}

func resolveExistingRecord(existing, incoming terminalwork.WorkRecord) error {
	_, err := resolveExistingOutcome(existing, incoming)
	return err
}

func resolveExistingOutcome(existing, incoming terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	if terminalwork.SameIntentReplay(existing, incoming) {
		return terminalwork.AppendIntentOutcome{Replay: true}, nil
	}
	return terminalwork.AppendIntentOutcome{}, fmt.Errorf("%w: work_id=%q source_key=%q", ErrIdentityCollision, incoming.WorkID, incoming.SourceKey)
}
