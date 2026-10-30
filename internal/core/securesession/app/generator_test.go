package app

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestRandGenerator_ConcurrentDistinctIDsAndTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := []byte("test-fingerprint-key-32bytes!!!!") // 32 bytes
	g := NewRandGenerator(key)
	material := EntropyMaterial{
		PrincipalID:        "same-user",
		AgentDigest:        "same-agent",
		FirstMessageDigest: "same-msg",
	}

	const workers = 64
	const perWorker = 128

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
			rt, fp, err := g.NewResumeToken(ctx, material)
			if err != nil {
				t.Errorf("NewResumeToken: %v", err)
				return
			}
			cr := domain.CreateRecord{
				SessionID:         sid,
				ResumeFingerprint: fp,
				Owner:             domain.PrincipalRef{ID: material.PrincipalID},
				Workspace:         domain.WorkspaceRef{},
				ClientHints:       domain.ClientHints{},
				Policy:            domain.PolicyMetadata{PolicyVersion: "1"},
				ALegID:            "a",
				ResumeEligible:    true,
				CreatedAt:         domain.Record{}.CreatedAt,
			}
			if cr.ResumeFingerprint == (domain.TokenFingerprint{}) {
				t.Errorf("empty fingerprint")
				return
			}
			_ = rt // raw token must not be stored on CreateRecord (fingerprint only)
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

func TestResumeTokenFingerprint_DeterministicForSameKeyAndToken(t *testing.T) {
	t.Parallel()

	key := []byte("another-32byte-fingerprint-key!!!!")
	material := EntropyMaterial{PrincipalID: "p", AgentDigest: "a", FirstMessageDigest: "m"}

	rt := domain.ResumeToken("opaque-fixed-token-for-test")
	fp1 := FingerprintResumeToken(key, rt, material)
	fp2 := FingerprintResumeToken(key, rt, material)
	if !fp1.Equal(fp2) {
		t.Fatalf("fingerprint not deterministic")
	}

	// Different material -> different fingerprint (domain separation).
	fpOther := FingerprintResumeToken(key, rt, EntropyMaterial{PrincipalID: "other"})
	if fp1.Equal(fpOther) {
		t.Fatalf("expected different fingerprint for different principal")
	}
}
