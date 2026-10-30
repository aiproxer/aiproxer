package checkpoint_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestCaptureFrontendIngress_WithMaxOutputTokens_PopulatesQuantities(t *testing.T) {
	t.Parallel()
	maxOut := 256
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{
			ID: "req-1",
			Options: aipapi.GenerationOptions{
				MaxOutputTokens: &maxOut,
			},
		},
		CheckpointID: "cp-fe-qty",
		StreamID:     "stream-fe-qty",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIngressQuantities(t, snap.Public.Quantities, maxOut, true)
	if snap.Public.Presence != metering.PresencePresent {
		t.Fatalf("presence=%q want=%q", snap.Public.Presence, metering.PresencePresent)
	}

	origID := snap.Public.CheckpointID
	origStream := snap.Public.StreamID
	origBoundary := snap.Public.Boundary
	origLifecycle := snap.Public.Lifecycle
	origCorr := snap.Public.Correlation
	origCallID := snap.Call.ID

	snap.ApplyQuantities([]metering.Quantity{
		{Component: metering.ComponentRequest, Unit: metering.UnitCount, Value: 99, Present: true},
	})
	if snap.Public.CheckpointID != origID {
		t.Fatalf("CheckpointID mutated: %q -> %q", origID, snap.Public.CheckpointID)
	}
	if snap.Public.StreamID != origStream {
		t.Fatalf("StreamID mutated: %q -> %q", origStream, snap.Public.StreamID)
	}
	if snap.Public.Boundary != origBoundary || snap.Public.Lifecycle != origLifecycle {
		t.Fatal("ApplyQuantities must not mutate Boundary/Lifecycle")
	}
	if snap.Public.Correlation != origCorr {
		t.Fatal("ApplyQuantities must not mutate Correlation")
	}
	if snap.Call.ID != origCallID {
		t.Fatal("ApplyQuantities must not mutate Call")
	}
}

func TestCaptureFrontendIngress_WithoutMaxOutputTokens_RequestOnly(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1"},
		CheckpointID: "cp-fe-no-max",
		StreamID:     "stream-fe-no-max",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIngressQuantities(t, snap.Public.Quantities, 0, false)
	if snap.Public.Presence != metering.PresencePresent {
		t.Fatalf("presence=%q want=%q", snap.Public.Presence, metering.PresencePresent)
	}
}

func TestCaptureBackendIngress_WithMaxOutputTokens_PopulatesQuantities(t *testing.T) {
	t.Parallel()
	maxOut := 128
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: "req-1",
			Options: aipapi.GenerationOptions{
				MaxOutputTokens: &maxOut,
			},
			Session: aipapi.SessionRef{ALegID: "a-1"},
		},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		BackendID:    "openai",
		Model:        "gpt",
		CheckpointID: "be-in-qty",
		StreamID:     "be-stream-qty",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIngressQuantities(t, snap.Public.Quantities, maxOut, true)
	if snap.Public.Presence != metering.PresencePresent {
		t.Fatalf("presence=%q want=%q", snap.Public.Presence, metering.PresencePresent)
	}
}

func TestCaptureBackendIngress_WithoutMaxOutputTokens_RequestOnly(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID:      "req-1",
			Session: aipapi.SessionRef{ALegID: "a-1"},
		},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		CheckpointID: "be-in-no-max",
		StreamID:     "be-stream-no-max",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIngressQuantities(t, snap.Public.Quantities, 0, false)
}

func TestApplyQuantities_DoesNotChangeIdentityFields(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1"},
		CheckpointID: "cp-identity",
		StreamID:     "stream-identity",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantID := snap.Public.CheckpointID
	wantStream := snap.Public.StreamID
	snap.ApplyQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 7, Present: true},
	})
	if snap.Public.CheckpointID != wantID {
		t.Fatalf("CheckpointID=%q want=%q", snap.Public.CheckpointID, wantID)
	}
	if snap.Public.StreamID != wantStream {
		t.Fatalf("StreamID=%q want=%q", snap.Public.StreamID, wantStream)
	}
}

func TestQuantitiesFromCall_NoInputTokenInvention(t *testing.T) {
	t.Parallel()
	maxOut := 64
	qs := checkpoint.QuantitiesFromCall(aipapi.Call{
		ID: "req-1",
		Options: aipapi.GenerationOptions{
			MaxOutputTokens: &maxOut,
		},
	})
	for _, q := range qs {
		if q.Component == metering.ComponentInputToken {
			t.Fatal("must not invent input_token without tokenization")
		}
	}
	assertIngressQuantities(t, qs, maxOut, true)
}

func TestMergeQuantities_AddsInputPreservesOutputBound(t *testing.T) {
	t.Parallel()
	maxOut := 256
	base := checkpoint.QuantitiesFromCall(aipapi.Call{
		Options: aipapi.GenerationOptions{MaxOutputTokens: &maxOut},
	})
	counted := []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 42, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 9, Present: true},
	}
	got := checkpoint.MergeQuantities(base, counted)
	in, ok := checkpoint.QuantityComponentValue(got, metering.ComponentInputToken)
	if !ok || in != 42 {
		t.Fatalf("input_token=%d ok=%v want 42", in, ok)
	}
	out, ok := checkpoint.QuantityComponentValue(got, metering.ComponentOutputToken)
	if !ok || out != int64(maxOut) {
		t.Fatalf("output_token=%d ok=%v want preserved bound %d", out, ok, maxOut)
	}
}

func TestSnapshot_MergeQuantitiesPreservesCheckpointID(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1"},
		CheckpointID: "cp-merge",
		StreamID:     "stream-merge",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantID := snap.Public.CheckpointID
	snap.MergeQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 11, Present: true},
	})
	if snap.Public.CheckpointID != wantID {
		t.Fatalf("CheckpointID=%q want %q", snap.Public.CheckpointID, wantID)
	}
	if v, ok := checkpoint.QuantityComponentValue(snap.Public.Quantities, metering.ComponentInputToken); !ok || v != 11 {
		t.Fatalf("merged input=%d ok=%v", v, ok)
	}
}

func assertIngressQuantities(t *testing.T, qs []metering.Quantity, wantMax int, expectOutput bool) {
	t.Helper()
	var sawRequest, sawOutput, sawInput bool
	for _, q := range qs {
		switch q.Component {
		case metering.ComponentRequest:
			sawRequest = true
			if q.Unit != metering.UnitCount || q.Value != 1 || !q.Present {
				t.Fatalf("request quantity=%+v want count=1 Present", q)
			}
		case metering.ComponentOutputToken:
			sawOutput = true
			if !expectOutput {
				t.Fatalf("unexpected output_token when MaxOutputTokens omitted: %+v", q)
			}
			if q.Unit != metering.UnitToken || q.Value != int64(wantMax) || !q.Present {
				t.Fatalf("output_token=%+v want value=%d Present", q, wantMax)
			}
		case metering.ComponentInputToken:
			sawInput = true
		}
	}
	if !sawRequest {
		t.Fatal("missing request/count=1 quantity")
	}
	if expectOutput && !sawOutput {
		t.Fatal("missing output_token for MaxOutputTokens")
	}
	if !expectOutput && sawOutput {
		t.Fatal("must not emit zero/unknown output_token as measured")
	}
	if sawInput {
		t.Fatal("must not invent input_token")
	}
}
