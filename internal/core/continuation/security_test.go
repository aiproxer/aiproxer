package continuation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corecont "github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func testScope(principal, session string) aipcont.Scope {
	return aipcont.Scope{PrincipalID: principal, SessionID: session}
}

func terminalRecord(id aipcont.ResponseID, scope aipcont.Scope, prev aipcont.ResponseID, input, output []aipapi.Item) aipcont.ContinuationRecord {
	return aipcont.ContinuationRecord{
		ID:          id,
		Scope:       scope,
		PreviousID:  prev,
		ProfileID:   "profile-test",
		Lineage:     aipcont.Lineage{ProfileID: "profile-test", Model: "m", RouteSelector: "stub:m"},
		InputItems:  input,
		OutputItems: output,
		Policy:      aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: time.Hour},
		Terminal:    true,
	}
}

func TestResponseIDEntropyAndValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	seen := make(map[aipcont.ResponseID]struct{})
	for range 32 {
		id, err := corecont.NewResponseID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := id.Validate(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id.String(), aipcont.ResponseIDPrefix) {
			t.Fatalf("prefix missing: %q", id)
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestMemoryStoreScopeIsolation(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scopeA := testScope("p1", "s1")
	scopeB := testScope("p2", "s1")
	id, err := store.Reserve(ctx, scopeA, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalRecord(id, scopeA, "", []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}}}, nil)
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	_, err = aipcont.Lookup(ctx, store, scopeB, id)
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	got, err := aipcont.Lookup(ctx, store, scopeA, id)
	if err != nil || got.ID != id {
		t.Fatalf("lookup=%v err=%v", got.ID, err)
	}
}

func TestMemoryStoreIndistinguishableLookup(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, _ := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Millisecond})
	missing := aipcont.ResponseID("resp_missing")
	cases := []struct {
		name string
		id   aipcont.ResponseID
	}{
		{"missing", missing},
		{"reserved_not_terminal", id},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := aipcont.Lookup(ctx, store, scope, tc.id)
			if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
				t.Fatalf("got %v", err)
			}
		})
	}
	now := time.Now()
	store.SetClock(func() time.Time { return now.Add(2 * time.Millisecond) })
	_, err := aipcont.Lookup(ctx, store, scope, id)
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("expired/reserved: %v", err)
	}
}

func TestMemoryStoreTTLExpiry(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	now := time.Now()
	store.SetClock(func() time.Time { return now })
	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalRecord(id, scope, "", nil, nil)
	rec.ExpiresAt = now.Add(time.Minute)
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time { return now.Add(2 * time.Minute) })
	_, err = aipcont.Lookup(ctx, store, scope, id)
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("expired: %v", err)
	}
}

func TestMemoryStoreIdempotentDelete(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour, AllowIncomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalRecord(id, scope, "", nil, nil)
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, scope, id); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, scope, id); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	_, err = aipcont.Lookup(ctx, store, scope, id)
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestMaterializeCycleAndDepthBounds(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")

	idA, _ := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	idB, _ := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	idC, _ := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})

	recA := terminalRecord(idA, scope, idB, nil, nil)
	recB := terminalRecord(idB, scope, idA, nil, nil)
	recC := terminalRecord(idC, scope, "", nil, nil)
	for _, rec := range []aipcont.ContinuationRecord{recA, recB, recC} {
		if err := store.PutTerminal(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	_, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: idA,
		Bounds: aipcont.Bounds{MaxChainDepth: 8, MaxMaterializedBytes: 1 << 20},
	})
	if !errors.Is(err, aipcont.ErrCycleDetected) {
		t.Fatalf("cycle: %v", err)
	}

	chain := make([]aipcont.ResponseID, 0, 5)
	var prev aipcont.ResponseID
	for range 5 {
		id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		rec := terminalRecord(id, scope, prev, nil, nil)
		if err := store.PutTerminal(ctx, rec); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, id)
		prev = id
	}
	_, err = aipcont.Materialize(ctx, aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: chain[len(chain)-1],
		Bounds: aipcont.Bounds{MaxChainDepth: 3, MaxMaterializedBytes: 1 << 20},
	})
	if !errors.Is(err, aipcont.ErrChainDepthExceeded) {
		t.Fatalf("depth: %v", err)
	}
}

func TestMaterializeByteBounds(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, _ := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	rec := terminalRecord(id, scope, "", nil, nil)
	rec.MaterializedBytes = 900
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	_, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id,
		NewInput: []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: strings.Repeat("x", 200)}}}},
		Bounds:   aipcont.Bounds{MaxChainDepth: 4, MaxMaterializedBytes: 1000},
	})
	if !errors.Is(err, aipcont.ErrMaterializedSizeExceeded) {
		t.Fatalf("bytes: %v", err)
	}
}

func TestTerminalRecorder(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalRecord(id, scope, "", nil, []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "ok"}}}})
	recorder := aipcont.TerminalRecorder{Store: store}
	if err := recorder.RecordTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := aipcont.Lookup(ctx, store, scope, id)
	if err != nil || len(got.OutputItems) != 1 {
		t.Fatalf("recorded=%v err=%v", got, err)
	}
}

func TestMemoryStoreNoMutableAliasLeakage(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	input := []aipapi.Item{{ID: "in1", Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser}}
	output := []aipapi.Item{{ID: "out1", Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant}}
	rec := terminalRecord(id, scope, "", input, output)

	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}

	// Mutate original slices after PutTerminal
	input[0].ID = "mutated_in"
	output[0].ID = "mutated_out"

	got1, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if got1.InputItems[0].ID == "mutated_in" || got1.OutputItems[0].ID == "mutated_out" {
		t.Fatalf("store mutated by caller modifying input slices post-PutTerminal")
	}

	// Mutate returned slice
	got1.InputItems[0].ID = "mutated_returned"
	got2, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if got2.InputItems[0].ID == "mutated_returned" {
		t.Fatalf("store mutated by caller modifying returned Get slice")
	}
}

func TestMemoryStoreProtectsNestedRecordState(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour, AllowIncomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalRecord(id, scope, "", nil, nil)
	rec.Status = aipcont.RecordStatusIncomplete
	rec.Policy.AllowIncomplete = true
	rec.Requirements = aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{"items"}}
	rec.Lineage.Model = "model-a"
	if err := store.PutTerminal(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Requirements.Capabilities[0] = "mutated"
	rec.Lineage.Model = "mutated"
	got, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requirements.Capabilities[0] != "items" || got.Lineage.Model != "model-a" {
		t.Fatalf("stored protected state was aliased: %+v", got)
	}
	got.Requirements.Capabilities[0] = "returned-mutation"
	got2, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Requirements.Capabilities[0] != "items" {
		t.Fatal("returned protected state was aliased")
	}
}

func TestMemoryStoreLimitsAndIncompleteEligibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := testScope("p", "s")
	store := corecont.NewMemoryStoreWithLimits(aipcont.StorageLimits{MaxRecords: 3, MaxBytes: 1 << 20, MaxRecordBytes: 4096, MaxChainDepth: 2})
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	incomplete := terminalRecord(id, scope, "", nil, nil)
	incomplete.Status = aipcont.RecordStatusIncomplete
	if err := store.PutTerminal(ctx, incomplete); !errors.Is(err, aipcont.ErrIncompleteNotEligible) {
		t.Fatalf("incomplete status: %v", err)
	}
	if err := store.Delete(ctx, scope, id); err != nil {
		t.Fatal(err)
	}
	id, err = store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour, AllowIncomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	incomplete.ID = id
	incomplete.Policy.AllowIncomplete = true
	if err := store.PutTerminal(ctx, incomplete); err != nil {
		t.Fatal(err)
	}
	second, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := terminalRecord(second, scope, "", nil, nil)
	tooLarge.MaterializedBytes = 4097
	if err := store.PutTerminal(ctx, tooLarge); !errors.Is(err, aipcont.ErrStorageLimitExceeded) {
		t.Fatalf("record bytes: %v", err)
	}
}

func TestMemoryStoreHonorsCancellationAtBoundary(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := corecont.NewMemoryStore().Reserve(ctx, testScope("p", "s"), aipcont.StoragePolicy{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reserve cancellation: %v", err)
	}
}

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")

	const workers = 20
	const opsPerWorker = 50
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(workerID int) {
			defer wg.Done()
			for range opsPerWorker {
				id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
				if err != nil {
					t.Errorf("worker %d reserve: %v", workerID, err)
					return
				}
				rec := terminalRecord(id, scope, "", nil, nil)
				if err := store.PutTerminal(ctx, rec); err != nil {
					t.Errorf("worker %d put: %v", workerID, err)
					return
				}
				if _, err := store.Get(ctx, scope, id); err != nil {
					t.Errorf("worker %d get: %v", workerID, err)
					return
				}
				if err := store.Delete(ctx, scope, id); err != nil {
					t.Errorf("worker %d delete: %v", workerID, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrencyProbingStress(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("principal-1", "session-1")
	otherScope := testScope("principal-2", "session-1")

	// Seed valid record
	validID, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(ctx, terminalRecord(validID, scope, "", nil, nil)); err != nil {
		t.Fatal(err)
	}

	// Seed reserved not terminal record
	reservedID, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	probingIDs := []aipcont.ResponseID{
		"resp_non_existent_1234567890",
		"malformed_id_no_prefix",
		"resp_short",
		reservedID,
	}

	const workers = 50
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(workerID int) {
			defer wg.Done()
			for j := range iterations {
				targetID := probingIDs[j%len(probingIDs)]
				targetScope := scope
				if j%2 == 1 {
					targetScope = otherScope
				}

				_, err := aipcont.Lookup(ctx, store, targetScope, targetID)
				if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
					t.Errorf("worker %d: expected ErrPreviousResponseNotFound for %q, got %v", workerID, targetID, err)
					return
				}

				// Look up valid record under valid scope to verify store remains consistent
				if j%10 == 0 {
					rec, err := aipcont.Lookup(ctx, store, scope, validID)
					if err != nil || rec.ID != validID {
						t.Errorf("worker %d: valid record lookup failed: %v", workerID, err)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestMemoryStoreRejectsFailedTerminalRecord(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("failed-principal", "failed-session")
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: time.Hour})
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	record := terminalRecord(id, scope, "", nil, nil)
	record.Status = aipcont.RecordStatusFailed
	if err := store.PutTerminal(ctx, record); !errors.Is(err, aipcont.ErrRecordNotEligible) {
		t.Fatalf("PutTerminal error = %v, want ErrRecordNotEligible", err)
	}
}

func TestNativeReferenceRedactionInLogs(t *testing.T) {
	t.Parallel()
	ref := aipcont.NativeReference{
		Provider: "openai",
		Kind:     "session_token",
		ID:       "sess_secret123",
		Opaque:   []byte("raw_bearer_token"),
	}
	if str := ref.String(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.String() exposed native ref: %q", str)
	}
	if str := ref.GoString(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.GoString() exposed native ref: %q", str)
	}
}
