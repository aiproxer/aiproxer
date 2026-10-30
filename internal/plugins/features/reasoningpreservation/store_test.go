package reasoningpreservation_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func defaultStoreOptions(now func() time.Time) reasoningpreservation.StoreOptions {
	return reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 256,
		MaxSessionBytes:          1024,
		Now:                      now,
	}
}

func sampleArtifact(id string, reasoningText string, bytes int) reasoningpreservation.TurnArtifact {
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, reasoningText, "", nil)
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         [32]byte{1, 2, 3},
		SourceBackend:  "backend",
		SourceModel:    "model",
		Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		ReasoningBytes: bytes,
	}
}

func TestNewMemoryTurnStore_TTLExpiry(t *testing.T) {
	t.Parallel()
	now, advance := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	if _, err := st.Append(ctx, partition, sampleArtifact("t1", "one", 64)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	advance(2 * time.Hour)
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("TTL expiry must evict artifacts, got %d", len(snap))
	}
}

func TestNewMemoryTurnStore_maxTurnsPerSession(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	for i := range 6 {
		sum, err := st.Append(ctx, partition, sampleArtifact(fmt.Sprintf("t%d", i), "payload", 32))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if i >= 4 && sum.EvictedTurns == 0 {
			t.Fatalf("append %d expected eviction summary, got %+v", i, sum)
		}
	}
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) > 4 {
		t.Fatalf("max turns exceeded: len=%d", len(snap))
	}
}

func TestNewMemoryTurnStore_perTurnBytesLimit(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	sum, err := st.Append(ctx, partition, sampleArtifact("oversize", "too-big", 512))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if sum.EvictedTurns == 0 && sum.EvictedBytes == 0 {
		t.Fatalf("oversize turn must produce eviction summary, got %+v", sum)
	}
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatal("oversize per-turn artifact must not remain reachable")
	}
}

func TestNewMemoryTurnStore_sessionBytesLimit(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	var lastSummary reasoningpreservation.EvictionSummary
	for i := range 8 {
		sum, err := st.Append(ctx, partition, sampleArtifact(fmt.Sprintf("s%d", i), "chunk", 200))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		lastSummary = sum
	}
	if lastSummary.EvictedBytes == 0 {
		t.Fatalf("session byte cap must evict, last summary=%+v", lastSummary)
	}
}

func TestNewMemoryTurnStore_evictionSummaryAtomic(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	sum, err := st.Append(ctx, partition, sampleArtifact("first", "a", 900))
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if sum.EvictedTurns < 0 || sum.EvictedBytes < 0 || sum.ExpiredTurns < 0 || sum.ExpiredBytes < 0 {
		t.Fatalf("negative eviction summary: %+v", sum)
	}
	sum2, err := st.Append(ctx, partition, sampleArtifact("second", "b", 900))
	if err != nil {
		t.Fatalf("second Append: %v", err)
	}
	if sum2.EvictedTurns+sum2.ExpiredTurns == 0 && sum2.EvictedBytes+sum2.ExpiredBytes == 0 {
		t.Fatalf("second append must report eviction activity, got %+v", sum2)
	}
}

func TestNewMemoryTurnStore_defensiveCopiesOnReadAndWrite(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	artifact := sampleArtifact("copy", "secret-reasoning", 64)
	artifact.Reasoning[0].Part.Reasoning.Signature = "orig-signature"
	artifact.Reasoning[0].Part.Reasoning.Opaque = mustOpaqueJSON(t, `{"k":"v"}`)
	if _, err := st.Append(ctx, partition, artifact); err != nil {
		t.Fatalf("Append: %v", err)
	}
	artifact.Reasoning[0].Part.Reasoning.Text = "mutated-after-append"
	artifact.Reasoning[0].Part.Reasoning.Signature = "mutated-signature"
	artifact.Reasoning[0].Part.Reasoning.Opaque = mustOpaqueJSON(t, `{"k":"mutated"}`)
	artifact.ReasoningBytes = 9999

	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	stored := snap[0].Reasoning[0].Part.Reasoning
	if stored.Text == "mutated-after-append" {
		t.Fatal("store must not retain caller text mutation after Append")
	}
	if stored.Signature == "mutated-signature" {
		t.Fatal("store must not retain caller signature mutation after Append")
	}
	if string(stored.Opaque) == `{"k":"mutated"}` {
		t.Fatal("store must not retain caller opaque mutation after Append")
	}
	snap[0].Reasoning[0].Part.Reasoning.Text = "mutated-after-snapshot"
	snap[0].Reasoning[0].Part.Reasoning.Signature = "mutated-after-snapshot"
	snap[0].Reasoning[0].Part.Reasoning.Opaque = mustOpaqueJSON(t, `{"k":"snapshot-mutated"}`)
	snap2, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	stored2 := snap2[0].Reasoning[0].Part.Reasoning
	if stored2.Text == "mutated-after-snapshot" {
		t.Fatal("Snapshot must return defensive copies for text")
	}
	if stored2.Signature == "mutated-after-snapshot" {
		t.Fatal("Snapshot must return defensive copies for signature")
	}
	if string(stored2.Opaque) == `{"k":"snapshot-mutated"}` {
		t.Fatal("Snapshot must return defensive copies for opaque")
	}
}

func TestNewMemoryTurnStore_sessionPartitionIsolation(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	ctx := context.Background()
	p1 := reasoningpreservation.NewSessionPartition("session-one")
	p2 := reasoningpreservation.NewSessionPartition("session-two")

	if _, err := st.Append(ctx, p1, sampleArtifact("only-p1", "r1", 64)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	snap, err := st.Snapshot(ctx, p2)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatal("partitions must be isolated")
	}
}

func TestNewMemoryTurnStore_concurrentAppendSnapshot(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	opts := defaultStoreOptions(now)
	st := newMemoryStore(t, opts)
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	const workers = 16
	errCh := make(chan error, workers*2)
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	for i := range workers {
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("panic in append goroutine: %v", r)
				}
			}()
			_, err := st.Append(ctx, partition, sampleArtifact(fmt.Sprintf("w%d", i), "payload", 32))
			if err != nil {
				errCh <- fmt.Errorf("Append: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("panic in snapshot goroutine: %v", r)
				}
			}()
			_, err := st.Snapshot(ctx, partition)
			if err != nil {
				errCh <- fmt.Errorf("Snapshot: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("final Snapshot: %v", err)
	}
	if len(snap) > opts.MaxTurnsPerSession {
		t.Fatalf("max turns exceeded: len=%d want <= %d", len(snap), opts.MaxTurnsPerSession)
	}
}

func TestNewMemoryTurnStore_deleteRemovesReachablePayload(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	partition := reasoningpreservation.NewSessionPartition("session-a")
	ctx := context.Background()

	if _, err := st.Append(ctx, partition, sampleArtifact("del-me", "payload", 64)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Delete(ctx, partition, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, a := range snap {
		if a.ID == "del-me" {
			t.Fatal("Delete must remove reachable artifact")
		}
	}
}

func TestSessionPartition_StringEmptyForPrivacy_contractLock(t *testing.T) {
	t.Parallel()
	p := reasoningpreservation.NewSessionPartition("super-secret-session-partition")
	if p.String() != "" {
		t.Fatalf("SessionPartition.String() must be empty, got %q", p.String())
	}
}
