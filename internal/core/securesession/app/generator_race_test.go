package app

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// TestRandGenerator_concurrentHighFanoutDistinct exercises the default generator under higher
// goroutine fanout for the race detector (complements TestRandGenerator_ConcurrentDistinctIDsAndTokens).
func TestRandGenerator_concurrentHighFanoutDistinct(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := []byte("test-fingerprint-key-32bytes!!!!") // 32 bytes
	g := NewRandGenerator(key)
	material := EntropyMaterial{
		PrincipalID:        "p",
		AgentDigest:        "a",
		FirstMessageDigest: "m",
	}

	const workers = 96
	const perWorker = 64

	var wg sync.WaitGroup
	ids := make(map[domain.SessionID]struct{})
	tokens := make(map[domain.ResumeToken]struct{})
	var mu sync.Mutex

	run := func() {
		defer wg.Done()
		for range perWorker {
			sid, err := g.NewSessionID(ctx, material)
			if err != nil {
				t.Errorf("NewSessionID: %v", err)
				return
			}
			rt, _, err := g.NewResumeToken(ctx, material)
			if err != nil {
				t.Errorf("NewResumeToken: %v", err)
				return
			}
			mu.Lock()
			if _, dup := ids[sid]; dup {
				t.Errorf("duplicate session id %q", sid)
			}
			ids[sid] = struct{}{}
			if _, dup := tokens[rt]; dup {
				t.Errorf("duplicate resume token")
			}
			tokens[rt] = struct{}{}
			mu.Unlock()
		}
	}

	wg.Add(workers)
	for range workers {
		go run()
	}
	wg.Wait()

	want := workers * perWorker
	if len(ids) != want {
		t.Fatalf("session ids: got %d want %d", len(ids), want)
	}
	if len(tokens) != want {
		t.Fatalf("resume tokens: got %d want %d", len(tokens), want)
	}
}
