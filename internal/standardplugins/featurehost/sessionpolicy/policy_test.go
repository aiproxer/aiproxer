package sessionpolicy

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func policyConfig(maxKeys int) Config {
	return Config{
		MaxKeys:       maxKeys,
		MaxKeyBytes:   128,
		MaxValueBytes: 128,
	}
}

func policyKey(suffix string) Key {
	return Key{
		SecureSessionIncarnation: "secure-session-" + suffix,
		ALegID:                   "a-leg-" + suffix,
		FeatureID:                "feature-terminal-decision",
	}
}

func authorizedAuthority(key Key) Authority {
	return Authority{
		SecureSessionIncarnation: key.SecureSessionIncarnation,
		ALegID:                   key.ALegID,
		Authorized:               true,
	}
}

func waitPolicySignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not arrive", name)
	}
}

func TestPolicyEffectiveState_AllTriStatePairs(t *testing.T) {
	t.Parallel()

	states := []TriState{TriStateUnset, TriStateEnabled, TriStateDisabled}
	for _, generationDefault := range []bool{false, true} {
		for _, clientState := range states {
			for _, operatorState := range states {
				t.Run(testPolicyPairName(generationDefault, clientState, operatorState), func(t *testing.T) {
					t.Parallel()
					store := NewStore(policyConfig(1))
					if store == nil {
						t.Fatal("NewStore returned nil")
					}
					t.Cleanup(func() { _ = store.Close() })

					key := policyKey("truth-table")
					authority := authorizedAuthority(key)
					if _, err := store.Set(t.Context(), authority, key, ActorClient, clientState); err != nil {
						t.Fatalf("set client state: %v", err)
					}
					if _, err := store.Set(t.Context(), authority, key, ActorOperator, operatorState); err != nil {
						t.Fatalf("set operator state: %v", err)
					}

					got, err := store.Snapshot(t.Context(), authority, key, generationDefault)
					if err != nil {
						t.Fatalf("snapshot: %v", err)
					}
					if got.ClientState != clientState || got.OperatorState != operatorState {
						t.Fatalf("actor states=%+v, want client=%q operator=%q", got, clientState, operatorState)
					}
					wantEffective := generationDefault
					if clientState == TriStateDisabled || operatorState == TriStateDisabled {
						wantEffective = false
					} else if clientState == TriStateEnabled || operatorState == TriStateEnabled {
						wantEffective = true
					}
					if got.EffectiveEnabled != wantEffective {
						t.Fatalf("effective_enabled=%v, want %v for client=%q operator=%q default=%v", got.EffectiveEnabled, wantEffective, clientState, operatorState, generationDefault)
					}
					if got.Revision == 0 {
						t.Fatal("successful writes must publish a non-zero revision")
					}
				})
			}
		}
	}
}

func testPolicyPairName(generationDefault bool, client, operator TriState) string {
	return strings.Join([]string{
		"default=" + boolString(generationDefault),
		"client=" + string(client),
		"operator=" + string(operator),
	}, "/")
}

func boolString(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func TestPolicyKeyContainsOnlySafeBoundedScopeIdentity(t *testing.T) {
	t.Parallel()

	keyType := reflect.TypeFor[Key]()
	wantFields := map[string]struct{}{
		"SecureSessionIncarnation": {},
		"ALegID":                   {},
		"FeatureID":                {},
	}
	if keyType.NumField() != len(wantFields) {
		t.Fatalf("Key fields=%d, want exactly %d safe identity fields", keyType.NumField(), len(wantFields))
	}
	for field := range keyType.Fields() {
		if _, ok := wantFields[field.Name]; !ok {
			t.Fatalf("Key contains unexpected field %q", field.Name)
		}
		if field.Type.Kind() != reflect.String {
			t.Fatalf("Key.%s kind=%s, want bounded opaque string", field.Name, field.Type.Kind())
		}
		lower := strings.ToLower(field.Name)
		for _, forbidden := range []string{"token", "credential", "secret", "password", "body", "content"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("Key.%s exposes forbidden sensitive/content vocabulary", field.Name)
			}
		}
	}

	authorityType := reflect.TypeFor[Authority]()
	for field := range authorityType.Fields() {
		if field.Type.Kind() != reflect.String && field.Type.Kind() != reflect.Bool {
			t.Fatalf("Authority.%s kind=%s, want safe identity or authorization state", field.Name, field.Type.Kind())
		}
	}
}

func TestPolicyBoundsCapacityRejectNewWithoutMutation(t *testing.T) {
	t.Parallel()

	store := NewStore(policyConfig(1))
	t.Cleanup(func() { _ = store.Close() })
	firstKey := policyKey("capacity-first")
	firstAuthority := authorizedAuthority(firstKey)
	first, err := store.Set(t.Context(), firstAuthority, firstKey, ActorClient, TriStateEnabled)
	if err != nil {
		t.Fatalf("set first key: %v", err)
	}

	secondKey := policyKey("capacity-second")
	secondAuthority := authorizedAuthority(secondKey)
	if _, err := store.Set(t.Context(), secondAuthority, secondKey, ActorClient, TriStateEnabled); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second key error=%v, want ErrCapacity", err)
	}

	unchanged, err := store.Snapshot(t.Context(), firstAuthority, firstKey, false)
	if err != nil {
		t.Fatalf("snapshot first key after capacity rejection: %v", err)
	}
	if unchanged != first {
		t.Fatalf("capacity rejection mutated existing key: before=%+v after=%+v", first, unchanged)
	}

	tooLargeKey := firstKey
	tooLargeKey.FeatureID = strings.Repeat("x", 129)
	if _, err := store.Set(t.Context(), firstAuthority, tooLargeKey, ActorClient, TriStateEnabled); !errors.Is(err, ErrBounds) {
		t.Fatalf("oversized key error=%v, want ErrBounds", err)
	}
	unchanged, err = store.Snapshot(t.Context(), firstAuthority, firstKey, false)
	if err != nil {
		t.Fatalf("snapshot first key after bounds rejection: %v", err)
	}
	if unchanged != first {
		t.Fatalf("bounds rejection mutated existing key: before=%+v after=%+v", first, unchanged)
	}

	boundedValueStore := NewStore(Config{MaxKeys: 1, MaxKeyBytes: 128, MaxValueBytes: 8})
	t.Cleanup(func() { _ = boundedValueStore.Close() })
	boundedValueKey := policyKey("value-bound")
	if _, err := boundedValueStore.Set(t.Context(), authorizedAuthority(boundedValueKey), boundedValueKey, ActorClient, TriStateEnabled); !errors.Is(err, ErrBounds) {
		t.Fatalf("oversized bounded value error=%v, want ErrBounds", err)
	}

	configType := reflect.TypeFor[Config]()
	for field := range configType.Fields() {
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "ttl") || strings.Contains(name, "evict") {
			t.Fatalf("Config.%s introduces TTL/eviction semantics", field.Name)
		}
	}
}

func TestPolicyConcurrentActorWritesAreSerializedAndSnapshotsAtomic(t *testing.T) {
	t.Parallel()

	store := NewStore(policyConfig(1))
	t.Cleanup(func() { _ = store.Close() })
	key := policyKey("concurrent")
	authority := authorizedAuthority(key)
	before, err := store.Snapshot(t.Context(), authority, key, false)
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	type writeResult struct {
		snapshot Snapshot
		err      error
	}
	results := make(chan writeResult, 2)
	var wg sync.WaitGroup
	for _, write := range []struct {
		actor Actor
		state TriState
	}{
		{actor: ActorClient, state: TriStateEnabled},
		{actor: ActorOperator, state: TriStateDisabled},
	} {
		wg.Go(func() {
			ready <- struct{}{}
			<-release
			snapshot, err := store.Set(t.Context(), authority, key, write.actor, write.state)
			results <- writeResult{snapshot: snapshot, err: err}
		})
	}
	waitPolicySignal(t, ready, "first writer")
	waitPolicySignal(t, ready, "second writer")
	close(release)
	wg.Wait()
	close(results)

	var revisions []uint64
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent write: %v", result.err)
		}
		if result.snapshot.Revision == 0 {
			t.Fatalf("concurrent write returned zero revision: %+v", result.snapshot)
		}
		revisions = append(revisions, result.snapshot.Revision)
	}
	if len(revisions) != 2 {
		t.Fatalf("write results=%d, want 2", len(revisions))
	}
	slices.Sort(revisions)
	if revisions[0] == revisions[1] {
		t.Fatalf("concurrent writes reused revision %d", revisions[0])
	}

	after, err := store.Snapshot(t.Context(), authority, key, true)
	if err != nil {
		t.Fatalf("final snapshot: %v", err)
	}
	if after.ClientState != TriStateEnabled || after.OperatorState != TriStateDisabled || after.EffectiveEnabled {
		t.Fatalf("final snapshot=%+v, want client enabled/operator disabled/effective false", after)
	}
	if after.Revision != revisions[1] {
		t.Fatalf("final revision=%d, want latest write revision=%d", after.Revision, revisions[1])
	}
	if before.ClientState != TriStateUnset || before.OperatorState != TriStateUnset || before.EffectiveEnabled {
		t.Fatalf("admitted snapshot mutated after writes: before=%+v", before)
	}
}

func TestPolicyUnauthorizedScopeCannotReadOrMutate(t *testing.T) {
	t.Parallel()

	store := NewStore(policyConfig(2))
	t.Cleanup(func() { _ = store.Close() })
	target := policyKey("authorized")
	authority := authorizedAuthority(target)
	initial, err := store.Set(t.Context(), authority, target, ActorClient, TriStateEnabled)
	if err != nil {
		t.Fatalf("set authorized state: %v", err)
	}

	unauthorized := authority
	unauthorized.Authorized = false
	if _, err := store.Snapshot(t.Context(), unauthorized, target, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized snapshot error=%v, want ErrUnauthorized", err)
	}
	if _, err := store.Set(t.Context(), unauthorized, target, ActorOperator, TriStateDisabled); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized write error=%v, want ErrUnauthorized", err)
	}

	otherTarget := policyKey("other")
	if _, err := store.Snapshot(t.Context(), authority, otherTarget, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-scope snapshot error=%v, want ErrUnauthorized", err)
	}
	if _, err := store.Set(t.Context(), authority, otherTarget, ActorClient, TriStateDisabled); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-scope write error=%v, want ErrUnauthorized", err)
	}

	unchanged, err := store.Snapshot(t.Context(), authority, target, false)
	if err != nil {
		t.Fatalf("authorized snapshot after denied operations: %v", err)
	}
	if unchanged != initial {
		t.Fatalf("denied scope operation mutated target: initial=%+v after=%+v", initial, unchanged)
	}
}

func TestPolicySnapshotIsImmutableAndWritesApplyToNextAdmission(t *testing.T) {
	t.Parallel()

	store := NewStore(policyConfig(1))
	t.Cleanup(func() { _ = store.Close() })
	key := policyKey("snapshot")
	authority := authorizedAuthority(key)
	admitted, err := store.Snapshot(t.Context(), authority, key, false)
	if err != nil {
		t.Fatalf("admit initial snapshot: %v", err)
	}
	if _, err := store.Set(t.Context(), authority, key, ActorClient, TriStateEnabled); err != nil {
		t.Fatalf("write client override: %v", err)
	}
	if admitted.ClientState != TriStateUnset || admitted.EffectiveEnabled {
		t.Fatalf("in-flight admitted snapshot changed after write: %+v", admitted)
	}

	next, err := store.Snapshot(t.Context(), authority, key, false)
	if err != nil {
		t.Fatalf("admit next snapshot: %v", err)
	}
	if next.ClientState != TriStateEnabled || !next.EffectiveEnabled || next.Revision <= admitted.Revision {
		t.Fatalf("next snapshot=%+v, want enabled state and newer revision than %+v", next, admitted)
	}
	if _, err := store.Set(t.Context(), authority, key, ActorOperator, TriStateDisabled); err != nil {
		t.Fatalf("write operator override: %v", err)
	}
	if next.OperatorState != TriStateUnset || !next.EffectiveEnabled {
		t.Fatalf("later write mutated second admitted snapshot: %+v", next)
	}
}

func TestPolicyOverridesSurviveGenerationReplacementButNotProcessRestart(t *testing.T) {
	t.Parallel()

	config := policyConfig(1)
	store := NewStore(config)
	t.Cleanup(func() { _ = store.Close() })
	key := policyKey("reload")
	authority := authorizedAuthority(key)
	if _, err := store.Set(t.Context(), authority, key, ActorClient, TriStateEnabled); err != nil {
		t.Fatalf("set client override: %v", err)
	}
	if _, err := store.Set(t.Context(), authority, key, ActorOperator, TriStateDisabled); err != nil {
		t.Fatalf("set operator override: %v", err)
	}

	for _, generationDefault := range []bool{false, true, false} {
		snapshot, err := store.Snapshot(t.Context(), authority, key, generationDefault)
		if err != nil {
			t.Fatalf("snapshot after generation replacement default=%v: %v", generationDefault, err)
		}
		if snapshot.ClientState != TriStateEnabled || snapshot.OperatorState != TriStateDisabled || snapshot.EffectiveEnabled {
			t.Fatalf("generation replacement lost process override: default=%v snapshot=%+v", generationDefault, snapshot)
		}
	}

	restarted := NewStore(config)
	t.Cleanup(func() { _ = restarted.Close() })
	fresh, err := restarted.Snapshot(t.Context(), authority, key, true)
	if err != nil {
		t.Fatalf("new process snapshot: %v", err)
	}
	if fresh.ClientState != TriStateUnset || fresh.OperatorState != TriStateUnset || !fresh.EffectiveEnabled || fresh.Revision != 0 {
		t.Fatalf("new process retained old policy state: %+v", fresh)
	}
}

func TestPolicyCloseRejectsWritesCompletesConcurrentOperationAndIsIdempotent(t *testing.T) {
	t.Parallel()

	store := NewStore(policyConfig(1))
	key := policyKey("close")
	authority := authorizedAuthority(key)
	if _, err := store.Set(t.Context(), authority, key, ActorClient, TriStateEnabled); err != nil {
		t.Fatalf("pre-close write: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		close(started)
		<-release
		_, err := store.Set(t.Context(), authority, key, ActorOperator, TriStateDisabled)
		writeDone <- err
	}()
	waitPolicySignal(t, started, "concurrent writer")
	if err := store.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	close(release)
	select {
	case err := <-writeDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("write released after Close error=%v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight writer did not complete after Close")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := store.Set(t.Context(), authority, key, ActorClient, TriStateUnset); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close write error=%v, want ErrClosed", err)
	}
}
