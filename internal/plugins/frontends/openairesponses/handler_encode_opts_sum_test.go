package openairesponses

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The lazy single-sum derivation must equal the previous per-ID hashing for
// every session shape, including nil calls.
func TestResponseIDForCallFromSumMatchesHashing(t *testing.T) {
	t.Parallel()
	calls := map[string]*aipapi.Call{
		"nil":          nil,
		"empty":        {},
		"explicit-id":  {ID: "explicit-id"},
		"aleg-only":    testCallWithSession("a-leg-123", ""),
		"aleg-session": testCallWithSession("a-leg-123", "sess-123"),
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := responseIDForCall(call)
			var sumCalls int
			getSum := func() [32]byte {
				sumCalls++
				return diag.StableCallSum(call)
			}
			if got := responseIDForCallFromSum(call, getSum); got != want {
				t.Fatalf("responseIDForCallFromSum = %q, want %q", got, want)
			}
			_, carrier := responseCarrierID(call)
			switch {
			case carrier && sumCalls != 0:
				t.Fatalf("carrier path hashed the call %d times, want 0", sumCalls)
			case !carrier && sumCalls != 1:
				t.Fatalf("token path hashed the call %d times, want 1", sumCalls)
			}
		})
	}
}

type fixedClockExec struct {
	cancelCaptureExecutor
	clock func() time.Time
}

func (e *fixedClockExec) WallClock() func() time.Time { return e.clock }

// BuildEncodeOpts must produce the same wire IDs and timestamps as the
// previous implementation: carrier-or-token response ID, msg-prefixed message
// ID, and clock timestamp when configured, else the deterministic seed.
func TestBuildEncodeOptsSingleSumParity(t *testing.T) {
	t.Parallel()
	fixed := time.Unix(1700000000, 0).UTC()
	handlers := map[string]*Handler{
		"nil-clock":   {Exec: &cancelCaptureExecutor{}},
		"fixed-clock": {Exec: &fixedClockExec{clock: func() time.Time { return fixed }}},
	}
	calls := map[string]*aipapi.Call{
		"empty":        {},
		"explicit-id":  {ID: "explicit-id"},
		"aleg-only":    testCallWithSession("a-leg-123", ""),
		"aleg-session": testCallWithSession("a-leg-123", "sess-123"),
	}
	for hname, h := range handlers {
		for cname, call := range calls {
			t.Run(hname+"/"+cname, func(t *testing.T) {
				t.Parallel()
				opts := h.Spec().BuildEncodeOpts(&frontendpipe.Decoded{Call: call})
				wantID := responseIDForCall(call)
				if opts.ResponseID != wantID {
					t.Fatalf("ResponseID = %q, want %q", opts.ResponseID, wantID)
				}
				if opts.MessageID != "msg_"+wantID {
					t.Fatalf("MessageID = %q, want %q", opts.MessageID, "msg_"+wantID)
				}
				wantCreated := diag.StableUnix(call)
				if clk := h.Exec.WallClock(); clk != nil {
					wantCreated = clk().Unix()
				}
				if opts.CreatedAt != wantCreated {
					t.Fatalf("CreatedAt = %d, want %d", opts.CreatedAt, wantCreated)
				}
			})
		}
	}
}
