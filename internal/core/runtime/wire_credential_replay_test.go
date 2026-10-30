package runtime

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// TestWireLargeBody_ReplayFactoryBoundAtOpen proves the runtime binds a valid
// exact-bytes replay factory at wire open without touching B-leg ownership:
// the adapter sees one B-leg, the first-attempt body and every replay reader
// carry identical bytes, and core routing still performs a single attempt.
func TestWireLargeBody_ReplayFactoryBoundAtOpen(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	raw := `{"model":"gpt-4o","messages":[{"role":"user","content":"replay-bound"}]}`
	src := newTestSource(raw)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	var opens atomic.Int32
	var sawBLEG atomic.Value
	var sawReplayValid atomic.Bool
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				sawBLEG.Store(req.BLegID)
				if !req.Replay.Valid() {
					return nil, io.ErrUnexpectedEOF
				}
				sawReplayValid.Store(true)
				first, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				for range 2 {
					rc, length, err := req.Replay.NewReader()
					if err != nil {
						return nil, err
					}
					replayed, err := io.ReadAll(rc)
					_ = rc.Close()
					if err != nil {
						return nil, err
					}
					if length != int64(len(raw)) || string(replayed) != raw || string(first) != raw {
						return nil, io.ErrUnexpectedEOF
					}
				}
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-replay-bound"})
	ctx = largebody.WithWireIdentity(ctx, "req-replay-bound", "trace-replay-bound")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()
	for {
		if _, rerr := res.Stream.Recv(context.Background()); rerr != nil {
			break
		}
	}

	if opens.Load() != 1 {
		t.Fatalf("wire opens = %d, want exactly 1 (adapter-local retries only)", opens.Load())
	}
	if !sawReplayValid.Load() {
		t.Fatal("runtime must bind a valid replay factory at wire open")
	}
	bleg, _ := sawBLEG.Load().(string)
	if bleg == "" {
		t.Fatal("wire open must carry a B-leg identity")
	}
}
