package codex

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type checkpointTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *checkpointTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *checkpointTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func testCheckpointKey(suffix string) CheckpointKey {
	return CheckpointKey{
		ConnectorInstanceID: "codex-instance-" + suffix,
		SessionID:           "authoritative-session-" + suffix,
		AccountID:           "account-" + suffix,
		Model:               "gpt-test-" + suffix,
		PromptCacheKey:      "cache-" + suffix,
		ClientFamily:        "responses",
		CompHash:            "comp-hash-" + suffix,
		InstructionsFP:      "instructions-" + suffix,
		ToolsFP:             "tools-" + suffix,
		ContinuityMode:      "required",
	}
}

func testCheckpoint(key CheckpointKey, value string) NativeCheckpoint {
	return NativeCheckpoint{
		Key:                   key,
		SourcePrefixFP:        []string{"source-" + value},
		Replacement:           []inputItem{textMessageItem{Type: "message", Role: "assistant", Content: value}},
		SourceEstimatedTokens: 17,
		ResultEstimatedTokens: 5,
		CompactionUsage:       &NativeUsageEvidence{InputTokens: 31, OutputTokens: 7, TotalTokens: 38},
	}
}

func newTestCheckpointStore(clock *checkpointTestClock, ttl time.Duration, entries, bytes int) *nativeCheckpointStore {
	return newNativeCheckpointStoreWithClock(ttl, entries, bytes, clock.Now)
}

func TestNativeCheckpointStore_RejectsIncompleteOrUnsafeKeys(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 4, 4096)
	defer store.Close()

	base := testCheckpointKey("valid")
	fields := []struct {
		name string
		set  func(*CheckpointKey)
	}{
		{"connector", func(k *CheckpointKey) { k.ConnectorInstanceID = "" }},
		{"session", func(k *CheckpointKey) { k.SessionID = "\x00" }},
		{"account", func(k *CheckpointKey) { k.AccountID = "\n" }},
		{"model", func(k *CheckpointKey) { k.Model = " " }},
		{"cache", func(k *CheckpointKey) { k.PromptCacheKey = "" }},
		{"family", func(k *CheckpointKey) { k.ClientFamily = "responses\r" }},
		{"hash", func(k *CheckpointKey) { k.CompHash = "" }},
		{"instructions", func(k *CheckpointKey) { k.InstructionsFP = "\t" }},
		{"tools", func(k *CheckpointKey) { k.ToolsFP = "" }},
		{"continuity", func(k *CheckpointKey) { k.ContinuityMode = "" }},
	}
	for _, tc := range fields {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key := base
			tc.set(&key)
			if _, ok := store.Reserve(key); ok {
				t.Fatal("unsafe key was reserved")
			}
			if _, ok := store.Get(key); ok {
				t.Fatal("unsafe key was returned")
			}
		})
	}
}

func TestNativeCheckpointStore_RejectsMissingAuthoritativeSession(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 4, 4096)
	defer store.Close()
	key := testCheckpointKey("authority")
	key.SessionID = ""
	if _, ok := store.Reserve(key); ok {
		t.Fatal("checkpoint store accepted missing authoritative session")
	}
}

func TestNativeCheckpointStore_ReserveCommitAbortAndPreviousStateSurvives(t *testing.T) {
	t.Parallel()
	clock := &checkpointTestClock{now: time.Unix(100, 0)}
	store := newTestCheckpointStore(clock, time.Hour, 4, 4096)
	defer store.Close()
	key := testCheckpointKey("lifecycle")

	first, ok := store.Reserve(key)
	if !ok {
		t.Fatal("first reservation failed")
	}
	if _, duplicate := store.Reserve(key); duplicate {
		t.Fatal("duplicate reservation succeeded")
	}
	wrongKey := first
	wrongKey.key = testCheckpointKey("wrong-reservation")
	if err := store.Commit(wrongKey, testCheckpoint(key, "wrong")); !errors.Is(err, ErrCheckpointReservation) {
		t.Fatalf("wrong reservation error = %v", err)
	}
	if err := store.Commit(first, testCheckpoint(key, "first")); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get(key)
	if !ok {
		t.Fatal("first checkpoint missing")
	}
	firstMsg, ok := got.Replacement[0].(textMessageItem)
	if !ok || firstMsg.Content != "first" {
		t.Fatalf("first checkpoint = %+v, %v", got, ok)
	}

	second, ok := store.Reserve(key)
	if !ok {
		t.Fatal("second reservation failed")
	}
	store.Abort(second)
	third, ok := store.Reserve(key)
	if !ok {
		t.Fatal("reservation was not released by abort")
	}
	if err := store.Commit(third, testCheckpoint(key, "third")); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(second, testCheckpoint(key, "stale")); !errors.Is(err, ErrCheckpointReservation) {
		t.Fatalf("stale commit error = %v", err)
	}

	fourth, ok := store.Reserve(key)
	if !ok {
		t.Fatal("fourth reservation failed")
	}
	store.Abort(fourth)
	got, ok = store.Get(key)
	if !ok {
		t.Fatal("third checkpoint missing")
	}
	thirdMsg, ok := got.Replacement[0].(textMessageItem)
	if !ok || thirdMsg.Content != "third" {
		t.Fatalf("committed checkpoint changed after abort = %+v, %v", got, ok)
	}
}

func TestNativeCheckpointStore_ReservationTokensAreOpaqueAndDistinct(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 4, 4096)
	defer store.Close()

	first, ok := store.Reserve(testCheckpointKey("token-a"))
	if !ok {
		t.Fatal("first reservation failed")
	}
	second, ok := store.Reserve(testCheckpointKey("token-b"))
	if !ok {
		t.Fatal("second reservation failed")
	}
	if first.stamp == second.stamp {
		t.Fatal("reservation tokens collided")
	}
	if first.String() == "reservation-1" || second.String() == "reservation-2" {
		t.Fatalf("reservation token is guessable: %q, %q", first, second)
	}
}

func TestNativeCheckpointStore_TTLAndDeterministicLRU(t *testing.T) {
	t.Parallel()
	clock := &checkpointTestClock{now: time.Unix(200, 0)}
	store := newTestCheckpointStore(clock, 10*time.Second, 2, 4096)
	defer store.Close()

	commit := func(suffix string) {
		reservation, ok := store.Reserve(testCheckpointKey(suffix))
		if !ok {
			t.Fatalf("reserve %s failed", suffix)
		}
		if err := store.Commit(reservation, testCheckpoint(testCheckpointKey(suffix), suffix)); err != nil {
			t.Fatal(err)
		}
	}
	commit("a")
	commit("b")
	if _, ok := store.Get(testCheckpointKey("a")); !ok {
		t.Fatal("expected a")
	}
	commit("c")
	if _, ok := store.Get(testCheckpointKey("b")); ok {
		t.Fatal("least recently used b was not evicted")
	}
	if _, ok := store.Get(testCheckpointKey("a")); !ok {
		t.Fatal("recently used a was evicted")
	}
	clock.Advance(11 * time.Second)
	if _, ok := store.Get(testCheckpointKey("a")); ok {
		t.Fatal("expired checkpoint was returned")
	}
}

func TestNativeCheckpointStore_EvictionHookRunsOutsideMutex(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 1, 4096)
	defer store.Close()

	firstKey := testCheckpointKey("hook-first")
	first, ok := store.Reserve(firstKey)
	if !ok {
		t.Fatal("first reservation failed")
	}
	if err := store.Commit(first, testCheckpoint(firstKey, "first")); err != nil {
		t.Fatal(err)
	}

	hookDone := make(chan struct{})
	store.setEvictionHook(func() {
		defer close(hookDone)
		if _, ok := store.Get(firstKey); ok {
			t.Error("evicted checkpoint remained visible during hook")
		}
	})

	secondKey := testCheckpointKey("hook-second")
	second, ok := store.Reserve(secondKey)
	if !ok {
		t.Fatal("second reservation failed")
	}
	if err := store.Commit(second, testCheckpoint(secondKey, "second")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hookDone:
	case <-time.After(time.Second):
		t.Fatal("eviction hook deadlocked while re-entering store")
	}
}

func TestNativeCheckpointStore_CooldownInvalidationAndAccountIsolation(t *testing.T) {
	t.Parallel()
	clock := &checkpointTestClock{now: time.Unix(300, 0)}
	store := newTestCheckpointStore(clock, time.Hour, 4, 4096)
	defer store.Close()
	key := testCheckpointKey("account-a")
	reservation, ok := store.Reserve(key)
	if !ok {
		t.Fatal("reserve failed")
	}
	if err := store.Commit(reservation, testCheckpoint(key, "a")); err != nil {
		t.Fatal(err)
	}
	rotated := key
	rotated.AccountID = "account-b"
	if _, ok := store.Get(rotated); ok {
		t.Fatal("rotated account reused checkpoint")
	}

	until := clock.Now().Add(time.Minute)
	store.MarkFailure(key, until)
	if !store.InCooldown(key) {
		t.Fatal("cooldown was not recorded")
	}
	if _, ok := store.Reserve(key); ok {
		t.Fatal("cooldown did not block reservation")
	}
	clock.Advance(time.Minute)
	if store.InCooldown(key) {
		t.Fatal("expired cooldown remained")
	}
	store.Invalidate(key)
	if _, ok := store.Get(key); ok {
		t.Fatal("invalidated checkpoint was returned")
	}
}

func TestNativeCheckpointStore_AllKeyDimensionsIsolateState(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 32, 4096)
	defer store.Close()
	base := testCheckpointKey("identity")
	reservation, ok := store.Reserve(base)
	if !ok {
		t.Fatal("reserve failed")
	}
	if err := store.Commit(reservation, testCheckpoint(base, "identity")); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*CheckpointKey){
		func(k *CheckpointKey) { k.ConnectorInstanceID += "-other" },
		func(k *CheckpointKey) { k.SessionID += "-other" },
		func(k *CheckpointKey) { k.AccountID += "-other" },
		func(k *CheckpointKey) { k.Model += "-other" },
		func(k *CheckpointKey) { k.PromptCacheKey += "-other" },
		func(k *CheckpointKey) { k.ClientFamily += "-other" },
		func(k *CheckpointKey) { k.CompHash += "-other" },
		func(k *CheckpointKey) { k.InstructionsFP += "-other" },
		func(k *CheckpointKey) { k.ToolsFP += "-other" },
		func(k *CheckpointKey) { k.ContinuityMode = "best_effort" },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if _, ok := store.Get(candidate); ok {
			t.Fatalf("dimension %d crossed checkpoint boundary", i)
		}
	}
}

func TestNativeCheckpointStore_InvalidCandidatePreservesCommittedCheckpoint(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 4, 512)
	defer store.Close()
	key := testCheckpointKey("failure")
	first, _ := store.Reserve(key)
	if err := store.Commit(first, testCheckpoint(key, "good")); err != nil {
		t.Fatal(err)
	}
	candidate, ok := store.Reserve(key)
	if !ok {
		t.Fatal("candidate reservation failed")
	}
	tooLarge := testCheckpoint(key, strings.Repeat("too-large-", 100))
	if err := store.Commit(candidate, tooLarge); !errors.Is(err, ErrCheckpointTooLarge) {
		t.Fatalf("invalid candidate error = %v", err)
	}
	store.Abort(candidate)
	got, ok := store.Get(key)
	if !ok {
		t.Fatal("committed checkpoint missing")
	}
	goodMsg, ok := got.Replacement[0].(textMessageItem)
	if !ok || goodMsg.Content != "good" {
		t.Fatalf("failed candidate replaced committed checkpoint = %+v, %v", got, ok)
	}
}

func TestNativeCheckpointStore_DefensiveCopiesAndClose(t *testing.T) {
	t.Parallel()
	clock := &checkpointTestClock{now: time.Unix(400, 0)}
	store := newTestCheckpointStore(clock, time.Hour, 4, 4096)
	key := testCheckpointKey("copies")
	candidate := testCheckpoint(key, "original")
	reservation, ok := store.Reserve(key)
	if !ok {
		t.Fatal("reserve failed")
	}
	if err := store.Commit(reservation, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.SourcePrefixFP[0] = "mutated"
	candidate.Replacement[0] = textMessageItem{Type: "message", Role: "assistant", Content: "mutated"}
	got, ok := store.Get(key)
	if !ok {
		t.Fatal("checkpoint missing")
	}
	got.SourcePrefixFP[0] = "returned-mutated"
	got.Replacement[0] = textMessageItem{Type: "message", Role: "assistant", Content: "returned-mutated"}
	again, _ := store.Get(key)
	origMsg, ok := again.Replacement[0].(textMessageItem)
	if !ok || again.SourcePrefixFP[0] != "source-original" || origMsg.Content != "original" {
		t.Fatal("store exposed mutable checkpoint state")
	}

	store.Close()
	store.Close()
	if _, ok := store.Reserve(key); ok {
		t.Fatal("closed store reserved a key")
	}
	if err := store.Commit(reservation, testCheckpoint(key, "late")); !errors.Is(err, ErrCheckpointClosed) {
		t.Fatalf("commit after close error = %v", err)
	}
}

func TestNativeCheckpointStore_ConcurrentReserveAndCloseCommit(t *testing.T) {
	t.Parallel()
	store := newNativeCheckpointStore(time.Hour, 8, 4096)
	defer store.Close()
	key := testCheckpointKey("concurrent")
	const workers = 32
	results := make(chan Reservation, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			if reservation, ok := store.Reserve(key); ok {
				results <- reservation
			}
		})
	}
	wg.Wait()
	close(results)
	var reservations []Reservation
	for reservation := range results {
		reservations = append(reservations, reservation)
	}
	if len(reservations) != 1 {
		t.Fatalf("reserved %d times, want one", len(reservations))
	}

	start := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		<-start
		store.Close()
		close(closed)
	}()
	commitResult := make(chan error, 1)
	go func() {
		<-start
		commitResult <- store.Commit(reservations[0], testCheckpoint(key, "race"))
	}()
	close(start)
	err := <-commitResult
	<-closed
	if err != nil && !errors.Is(err, ErrCheckpointClosed) {
		t.Fatalf("commit versus close error = %v", err)
	}
	if got, ok := store.Get(key); ok {
		t.Fatalf("closed store retained checkpoint: %+v", got)
	}
}
