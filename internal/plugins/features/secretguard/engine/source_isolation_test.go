package engine_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
)

type panicEnvironment struct {
	calls int
}

func (p *panicEnvironment) Lookup(string) (string, bool) {
	p.calls++
	panic("secretguard: multi_user must never call environment lookup")
}

func (p *panicEnvironment) Snapshot() []string {
	p.calls++
	panic("secretguard: multi_user must never call environment snapshot")
}

func TestNewMultiUserSource_zeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := engine.NewMultiUserSource(env)
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != engine.ModeMultiUser {
		t.Fatalf("access mode: got %v", src.AccessMode())
	}
	if env.calls != 0 {
		t.Fatalf("Environment calls: got %d want 0", env.calls)
	}
	_ = src.EntryCount()
	_ = src.MatcherResolver()
	if env.calls != 0 {
		t.Fatalf("Environment calls after inspect: got %d want 0", env.calls)
	}
}

func TestNewDisabledSource_zeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src := engine.NewDisabledSource()
	_ = src.EntryCount()
	_ = src.MatcherResolver()
	_ = src.AccessMode()
	if env.calls != 0 {
		t.Fatalf("disabled source must not call Environment; calls=%d", env.calls)
	}
}

func TestSource_hasNoRawCatalogAccessor(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[engine.Source]()
	for m := range rt.Methods() {
		switch m.Name {
		case "Values", "Secrets", "Catalog", "Env", "RawSecrets":
			t.Fatalf("Source must not expose raw accessor %q", m.Name)
		}
	}
	src, err := engine.NewMultiUserSource(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = src
}
