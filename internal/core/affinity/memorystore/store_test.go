package memorystore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
)

func TestStoreRoundTripDeleteAndCancel(t *testing.T) {
	t.Parallel()
	s := New()
	key := affinity.Key{Scope: affinity.ScopeSession, ID: "s1"}
	b := affinity.Binding{Key: key, BackendID: "be1", CandidateKey: "be1:m"}
	if err := s.Set(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != b {
		t.Fatalf("got (%+v, %v) want (%+v, true)", got, ok, b)
	}
	if err := s.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("after delete ok=%v err=%v", ok, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Set(canceled, b); err == nil {
		t.Fatal("expected canceled context error")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	t.Parallel()
	s := New()
	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			key := affinity.Key{Scope: affinity.ScopeClient, ID: fmt.Sprintf("u-%d", i%8)}
			_ = s.Set(context.Background(), affinity.Binding{Key: key, BackendID: fmt.Sprintf("be-%d", i), CandidateKey: "k"})
			_, _, _ = s.Get(context.Background(), key)
		})
	}
	wg.Wait()
}
