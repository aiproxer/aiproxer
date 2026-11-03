package openailegacy_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type sumParityExec struct {
	clock func() time.Time
}

func (e *sumParityExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (e *sumParityExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}

func (e *sumParityExec) WallClock() func() time.Time { return e.clock }

// BuildEncodeOpts must produce the same wire IDs and timestamps as the
// previous implementation: token completion ID and clock timestamp when
// configured, else the deterministic seed.
func TestBuildEncodeOptsSingleSumParity(t *testing.T) {
	t.Parallel()
	fixed := time.Unix(1700000000, 0).UTC()
	handlers := map[string]*openailegacy.Handler{
		"nil-clock":   {Exec: &sumParityExec{}},
		"fixed-clock": {Exec: &sumParityExec{clock: func() time.Time { return fixed }}},
	}
	calls := map[string]*aipapi.Call{
		"empty":       {},
		"explicit-id": {ID: "explicit-id"},
	}
	for hname, h := range handlers {
		for cname, call := range calls {
			t.Run(hname+"/"+cname, func(t *testing.T) {
				t.Parallel()
				opts := h.Spec().BuildEncodeOpts(&frontendpipe.Decoded{Call: call})
				wantID := "chatcmpl_" + diag.StableCallToken(call)
				if opts.CompletionID != wantID {
					t.Fatalf("CompletionID = %q, want %q", opts.CompletionID, wantID)
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
