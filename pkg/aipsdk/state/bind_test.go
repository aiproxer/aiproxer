package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

type memStore map[string]string

func (m memStore) Get(_ context.Context, _ state.Scope, ns, key string, out any) (bool, error) {
	k := ns + "|" + key
	v, ok := m[k]
	if !ok {
		return false, nil
	}
	if p, ok := out.(*string); ok {
		*p = v
	}
	return true, nil
}

func (m memStore) Put(_ context.Context, _ state.Scope, ns, key string, value any, _ time.Duration) error {
	k := ns + "|" + key
	s, _ := value.(string)
	m[k] = s
	return nil
}

func (m memStore) Delete(_ context.Context, _ state.Scope, ns, key string) error {
	delete(m, ns+"|"+key)
	return nil
}

func (m memStore) InspectTTL(context.Context, state.Scope, string, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func TestBindPlugin_prefixesNamespace(t *testing.T) {
	t.Parallel()
	backing := memStore{}
	boundA := state.BindPlugin(backing, "plugin-a")
	boundB := state.BindPlugin(backing, "plugin-b")
	ctx := context.Background()
	if err := boundA.Put(ctx, state.ScopeGlobal, "ns", "k", "from-a", 0); err != nil {
		t.Fatal(err)
	}
	if err := boundB.Put(ctx, state.ScopeGlobal, "ns", "k", "from-b", 0); err != nil {
		t.Fatal(err)
	}
	var out string
	found, err := boundA.Get(ctx, state.ScopeGlobal, "ns", "k", &out)
	if err != nil || !found || out != "from-a" {
		t.Fatalf("a: found=%v out=%q err=%v", found, out, err)
	}
	found, err = boundB.Get(ctx, state.ScopeGlobal, "ns", "k", &out)
	if err != nil || !found || out != "from-b" {
		t.Fatalf("b: found=%v out=%q err=%v", found, out, err)
	}
	if backing["plugin-a/ns|k"] != "from-a" || backing["plugin-b/ns|k"] != "from-b" {
		t.Fatalf("backing keys: %#v", backing)
	}
}

func TestBindPlugin_emptyPluginIDUsesDisabled(t *testing.T) {
	t.Parallel()
	backing := memStore{}
	b := state.BindPlugin(backing, "   ")
	ctx := context.Background()
	err := b.Put(ctx, state.ScopeGlobal, "ns", "k", "v", 0)
	if err != state.ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured got %v", err)
	}
}

func TestBindPlugin_nilStoreUsesDisabled(t *testing.T) {
	t.Parallel()
	b := state.BindPlugin(nil, "p")
	ctx := context.Background()
	err := b.Put(ctx, state.ScopeGlobal, "ns", "k", "v", 0)
	if err != state.ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured got %v", err)
	}
}
