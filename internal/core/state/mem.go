package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// NewMem returns an in-memory [aipstate.Store] keyed by scope, execctx-derived partition, namespace, and key (tasks 6–6.1).
func NewMem(now func() time.Time) aipstate.Store {
	if now == nil {
		now = time.Now
	}
	return &memStore{now: now, data: make(map[memKey]entry)}
}

type memKey struct {
	scope aipstate.Scope
	part  string
	ns    string
	key   string
}

type entry struct {
	value    any
	deadline time.Time // zero means no TTL
}

type memStore struct {
	mu   sync.Mutex
	now  func() time.Time
	data map[memKey]entry
}

var _ aipstate.Store = (*memStore)(nil)

func (m *memStore) Get(ctx context.Context, scope aipstate.Scope, ns, key string, out any) (bool, error) {
	part, err := partitionForScope(ctx, scope)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ent, ok := m.data[memKey{scope: scope, part: part, ns: ns, key: key}]
	if !ok {
		return false, nil
	}
	if !ent.deadline.IsZero() && !m.now().Before(ent.deadline) {
		delete(m.data, memKey{scope: scope, part: part, ns: ns, key: key})
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	return true, decodeValue(ent.value, out)
}

func (m *memStore) Put(ctx context.Context, scope aipstate.Scope, ns, key string, value any, ttl time.Duration) error {
	part, err := partitionForScope(ctx, scope)
	if err != nil {
		return err
	}
	enc, err := encodePutValue(value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ent := entry{value: enc}
	if ttl > 0 {
		ent.deadline = m.now().Add(ttl)
	}
	m.data[memKey{scope: scope, part: part, ns: ns, key: key}] = ent
	return nil
}

func (m *memStore) Delete(ctx context.Context, scope aipstate.Scope, ns, key string) error {
	part, err := partitionForScope(ctx, scope)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, memKey{scope: scope, part: part, ns: ns, key: key})
	return nil
}

func (m *memStore) InspectTTL(ctx context.Context, scope aipstate.Scope, ns, key string) (time.Duration, bool, error) {
	part, err := partitionForScope(ctx, scope)
	if err != nil {
		return 0, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey{scope: scope, part: part, ns: ns, key: key}
	ent, ok := m.data[k]
	if !ok {
		return 0, false, nil
	}
	if !ent.deadline.IsZero() {
		rem := ent.deadline.Sub(m.now())
		if rem <= 0 {
			delete(m.data, k)
			return 0, false, nil
		}
		return rem, true, nil
	}
	return 0, true, nil
}

func encodePutValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		out := make([]byte, len(v))
		copy(out, v)
		return out, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("core/state: encode value: %w", err)
		}
		return json.RawMessage(b), nil
	}
}

func decodeValue(stored any, out any) error {
	if out == nil {
		return nil
	}
	switch dst := out.(type) {
	case *string:
		switch v := stored.(type) {
		case string:
			*dst = v
			return nil
		case json.RawMessage:
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return fmt.Errorf("core/state: decode string: %w", err)
			}
			*dst = s
			return nil
		default:
			return fmt.Errorf("core/state: value is %T, want string-compatible", stored)
		}
	default:
		b, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("core/state: decode into %T: %w", out, err)
		}
		return nil
	}
}
