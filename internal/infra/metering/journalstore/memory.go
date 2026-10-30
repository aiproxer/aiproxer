package journalstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// MemoryConfig configures an in-memory metering journal.
type MemoryConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
	Now             func() time.Time
}

// MemoryStore is a concurrent in-memory metering journal.
type MemoryStore struct {
	cfg             MemoryConfig
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time

	mu       sync.Mutex
	seq      int64
	bySource map[string]int // source_event_key -> index in facts
	facts    []storedFact
}

type storedFact struct {
	seq    int64
	source string
	fact   metering.Fact
}

// NewMemoryStore returns a ready in-memory journal.
func NewMemoryStore(cfg MemoryConfig) (*MemoryStore, error) {
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: memory store id is required")
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
		return nil, fmt.Errorf("metering/journalstore: max page size %d < default %d", maxPageSize, def)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		cfg:             cfg,
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
		now:             now,
		bySource:        make(map[string]int),
	}, nil
}

// Close is a no-op for the memory store.
func (s *MemoryStore) Close() error { return nil }

// CheckReadiness always succeeds for memory.
func (s *MemoryStore) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Append validates and stores one fact with source-key idempotency.
// SameFactReplay → no-op; same source key otherwise → ErrIdentityCollision.
func (s *MemoryStore) Append(ctx context.Context, fact metering.Fact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fact.Validate(); err != nil {
		return fmt.Errorf("metering/journalstore: %w", err)
	}
	cloned, err := cloneFact(fact)
	if err != nil {
		return err
	}
	if cloned.RecordedAt.IsZero() {
		cloned.RecordedAt = s.now().UTC()
	}
	key := cloned.SourceEventKey()
	lookupKeys := cloned.SourceEventLookupKeys()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Only corrections and replacements reference earlier identities, and the
	// durable store short-circuits the same way
	// (DurableStore.validateDurableSupersession). Validating every append used to
	// copy the whole journal into a fact slice and rebuild the full supersession
	// graph per write, so a growing memory journal paid O(n) bytes on facts that
	// cannot extend a supersession graph.
	if cloned.Kind.RequiresSupersedes() {
		lookup := func(factID string) (metering.Fact, bool) {
			for _, row := range s.facts {
				if strings.TrimSpace(row.fact.FactID) == factID {
					return row.fact, true
				}
			}
			return metering.Fact{}, false
		}
		if err := validateSupersessionGraph(cloned, lookup, supersessionEdgesFromFacts(s.facts)); err != nil {
			return err
		}
	}

	for _, candidate := range lookupKeys {
		idx, ok := s.bySource[candidate]
		if !ok {
			continue
		}
		existing := s.facts[idx].fact
		if metering.SameFactReplay(existing, cloned) {
			return nil
		}
		return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
			ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
	}
	for _, row := range s.facts {
		if strings.TrimSpace(row.fact.StreamID) != strings.TrimSpace(cloned.StreamID) ||
			strings.TrimSpace(row.fact.FactID) != strings.TrimSpace(cloned.FactID) {
			continue
		}
		if metering.SameFactReplay(row.fact, cloned) {
			return nil
		}
		return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
			ErrIdentityCollision, cloned.StreamID, cloned.FactID, row.fact.Sequence, cloned.Sequence)
	}

	s.seq++
	s.bySource[key] = len(s.facts)
	s.facts = append(s.facts, storedFact{seq: s.seq, source: key, fact: cloned})
	return nil
}

// SeedSourceKeyForTest indexes fact under an explicit source_event_key for
// process-local legacy/preload replay tests. It does not rewrite durable rows.
func (s *MemoryStore) SeedSourceKeyForTest(key string, fact metering.Fact) error {
	if s == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("metering/journalstore: seed source key is required")
	}
	cloned, err := cloneFact(fact)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bySource[key]; ok {
		return fmt.Errorf("metering/journalstore: seed source key already present")
	}
	s.seq++
	s.bySource[key] = len(s.facts)
	s.facts = append(s.facts, storedFact{seq: s.seq, source: key, fact: cloned})
	return nil
}

// List returns a bounded page of facts matching the query.
func (s *MemoryStore) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	if err := ctx.Err(); err != nil {
		return metering.Page{}, err
	}
	unsupported := metering.QueryUnsupported(q)
	if err := metering.ValidateQuery(q); err != nil {
		return metering.Page{}, err
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
			return metering.Page{}, fmt.Errorf("metering/journalstore: invalid cursor")
		}
		offset = n
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	matched := make([]metering.Fact, 0)
	for _, row := range s.facts {
		if !metering.FactMatchesQuery(row.fact, q) {
			continue
		}
		cloned, err := cloneFact(row.fact)
		if err != nil {
			return metering.Page{}, err
		}
		matched = append(matched, cloned)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].StreamID != matched[j].StreamID {
			return matched[i].StreamID < matched[j].StreamID
		}
		return matched[i].Sequence < matched[j].Sequence
	})
	if offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+limit, len(matched))
	page := metering.Page{
		Facts:       matched[offset:end],
		Unsupported: append([]metering.UnsupportedFilter(nil), unsupported...),
	}
	if end < len(matched) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func cloneFact(f metering.Fact) (metering.Fact, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return metering.Fact{}, fmt.Errorf("metering/journalstore: marshal fact: %w", err)
	}
	var out metering.Fact
	if err := json.Unmarshal(raw, &out); err != nil {
		return metering.Fact{}, fmt.Errorf("metering/journalstore: unmarshal fact: %w", err)
	}
	out.Scope = f.Scope.Clone()
	return out, nil
}

// filterPairs returns safe indexed projections for durable filter rows.
func filterPairs(f metering.Fact) [][2]string {
	pairs := [][2]string{
		{"stream_id", strings.TrimSpace(f.StreamID)},
		{"request_id", strings.TrimSpace(f.Correlation.RequestID)},
		{"a_leg_id", strings.TrimSpace(f.Correlation.ALegID)},
		{"b_leg_id", strings.TrimSpace(f.Correlation.BLegID)},
		{"attempt_id", strings.TrimSpace(f.Correlation.AttemptID)},
		{"trace_id", strings.TrimSpace(f.Correlation.TraceID)},
		{"session_id", strings.TrimSpace(f.Correlation.SessionID)},
		{"perspective", string(f.Perspective)},
		{"boundary", string(f.Boundary)},
		{"lifecycle", string(f.Lifecycle)},
		{"kind", string(f.Kind)},
		{"presence", string(f.Presence)},
		{"source", string(f.Source)},
		{"authority", string(f.Authority)},
		{"identity_version", strconv.Itoa(f.EffectiveIdentityVersion())},
		{"frontend_id", strings.TrimSpace(f.FrontendID)},
		{"backend_id", strings.TrimSpace(f.BackendID)},
		{"model", strings.TrimSpace(f.Model)},
	}
	addScope := func(name string, v scope.Value) {
		if v.IsKnown() && strings.TrimSpace(v.Value) != "" {
			pairs = append(pairs, [2]string{name, strings.TrimSpace(v.Value)})
		}
	}
	addScope("principal_id", f.Scope.PrincipalID)
	addScope("credential_id", f.Scope.CredentialID)
	addScope("tenant_id", f.Scope.TenantID)
	addScope("organization_id", f.Scope.OrganizationID)
	addScope("workspace_id", f.Scope.WorkspaceID)
	addScope("project_id", f.Scope.ProjectID)
	addScope("department_id", f.Scope.DepartmentID)
	addScope("cost_center_id", f.Scope.CostCenterID)
	if id := strings.TrimSpace(f.PolicyVersion.ID); id != "" {
		pairs = append(pairs, [2]string{"rule_id", id})
	}
	out := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

var (
	_ metering.Recorder = (*MemoryStore)(nil)
	_ metering.Querier  = (*MemoryStore)(nil)
)
