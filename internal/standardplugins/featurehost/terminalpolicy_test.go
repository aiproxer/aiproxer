package featurehost_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

func TestTerminalPolicyReaderAdapter_Effective(t *testing.T) {
	t.Parallel()

	store := sessionpolicy.NewStore(sessionpolicy.Config{})
	defer func() { _ = store.Close() }()

	reader := featurehost.NewTerminalPolicyReaderAdapter(store)
	if reader == nil {
		t.Fatal("expected non-nil TerminalPolicyReader adapter")
	}

	ctx := context.Background()
	query := runtime.TerminalPolicyQuery{
		SecureSessionIncarnation: "session-1",
		ALegID:                   "aleg-1",
		FeatureID:                "terminal-decision",
		GenerationDefault:        true,
	}

	// 1. Initial lookup returns generation default.
	snap, err := reader.Effective(ctx, query)
	if err != nil {
		t.Fatalf("unexpected Effective error: %v", err)
	}
	if !snap.EffectiveEnabled {
		t.Errorf("expected EffectiveEnabled=true, got false")
	}
	if snap.Revision != 0 {
		t.Errorf("expected Revision=0, got %d", snap.Revision)
	}

	// 2. Set client override to disabled.
	key := sessionpolicy.Key{
		SecureSessionIncarnation: "session-1",
		ALegID:                   "aleg-1",
		FeatureID:                "terminal-decision",
	}
	auth := sessionpolicy.Authority{
		SecureSessionIncarnation: "session-1",
		ALegID:                   "aleg-1",
		Authorized:               true,
	}
	updatedSnap, err := store.Set(ctx, auth, key, sessionpolicy.ActorClient, sessionpolicy.TriStateDisabled)
	if err != nil {
		t.Fatalf("unexpected Set error: %v", err)
	}
	if updatedSnap.EffectiveEnabled {
		t.Errorf("expected EffectiveEnabled=false after disable")
	}

	// 3. Reader sees effective disabled and updated revision.
	snap2, err := reader.Effective(ctx, query)
	if err != nil {
		t.Fatalf("unexpected Effective error: %v", err)
	}
	if snap2.EffectiveEnabled {
		t.Errorf("expected EffectiveEnabled=false, got true")
	}
	if snap2.Revision != updatedSnap.Revision {
		t.Errorf("expected Revision=%d, got %d", updatedSnap.Revision, snap2.Revision)
	}

	// 4. Closed store returns error.
	if err := store.Close(); err != nil {
		t.Fatalf("unexpected Close error: %v", err)
	}
	_, err = reader.Effective(ctx, query)
	if err == nil {
		t.Fatal("expected error from closed store, got nil")
	}
}

func TestTerminalPolicyReaderAdapter_NilStore(t *testing.T) {
	t.Parallel()

	reader := featurehost.NewTerminalPolicyReaderAdapter(nil)
	if reader == nil {
		t.Fatal("expected non-nil adapter even if store is nil")
	}

	snap, err := reader.Effective(context.Background(), runtime.TerminalPolicyQuery{
		GenerationDefault: true,
	})
	if err != nil {
		t.Fatalf("unexpected error with nil store: %v", err)
	}
	if !snap.EffectiveEnabled {
		t.Errorf("expected EffectiveEnabled=true from generation default, got false")
	}
}
