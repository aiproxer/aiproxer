package reftraffictranscript_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reftraffictranscript"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

func TestBundleEmit_redactsForObserverNotRaw(t *testing.T) {
	t.Parallel()
	cfg := reftraffictranscript.DefaultConfig()
	pb, obs, raw := reftraffictranscript.BundleForTest(cfg)
	meta := traffic.CaptureMeta{TraceID: "tr", ALegID: "a", BLegID: "b"}
	payload := []byte(`before ` + defaultSecret() + ` after`)
	pb.Emit(context.Background(), traffic.LegCTP, meta, "p", "t", payload)
	if len(raw.Rows) != 1 || !bytes.Equal(raw.Rows[0].Bytes, payload) {
		t.Fatalf("raw %q", raw.Rows[0].Bytes)
	}
	if len(obs.Rows) != 1 {
		t.Fatalf("obs count %d", len(obs.Rows))
	}
	if bytes.Contains(obs.Rows[0].Body, []byte("REF_SECRET")) {
		t.Fatalf("observer should be redacted: %q", obs.Rows[0].Body)
	}
	if !bytes.Contains(obs.Rows[0].Body, []byte("[redacted]")) {
		t.Fatalf("expected placeholder, got %q", obs.Rows[0].Body)
	}
}

func defaultSecret() string { return "REF_SECRET" }

func TestEmit_allFourLegs(t *testing.T) {
	t.Parallel()
	cfg := reftraffictranscript.DefaultConfig()
	pb, obs, _ := reftraffictranscript.BundleForTest(cfg)
	meta := traffic.CaptureMeta{TraceID: "t1"}
	legs := []traffic.Leg{traffic.LegCTP, traffic.LegPTB, traffic.LegBTP, traffic.LegPTC}
	for _, leg := range legs {
		pb.Emit(context.Background(), leg, meta, "p", "t", []byte{leg[0]})
	}
	if len(obs.Rows) != 4 {
		t.Fatalf("got %d rows", len(obs.Rows))
	}
}

func TestTranscript_preservesAttemptAndLegCorrelation(t *testing.T) {
	t.Parallel()
	cfg := reftraffictranscript.DefaultConfig()
	pb, obs, _ := reftraffictranscript.BundleForTest(cfg)
	pb.Emit(context.Background(), traffic.LegCTP, traffic.CaptureMeta{
		TraceID: "tr", ALegID: "a1", AttemptSeq: 0, BackendID: "", FrontendID: "fe",
	}, "p", "t", []byte("ctp"))
	pb.Emit(context.Background(), traffic.LegPTB, traffic.CaptureMeta{
		TraceID: "tr", ALegID: "a1", BLegID: "b-leg-7", AttemptSeq: 1, BackendID: "openai",
	}, "p", "t", []byte("ptb"))
	if len(obs.Rows) != 2 {
		t.Fatalf("rows %d", len(obs.Rows))
	}
	r0, r1 := obs.Rows[0], obs.Rows[1]
	if r0.Leg != traffic.LegCTP || r0.AttemptSeq != 0 || r0.BLegID != "" || r0.BackendID != "" {
		t.Fatalf("ctp row: %+v", r0)
	}
	if r1.Leg != traffic.LegPTB || r1.AttemptSeq != 1 || r1.BLegID != "b-leg-7" || r1.BackendID != "openai" {
		t.Fatalf("ptb row: %+v", r1)
	}
}

func TestUsageLedger_recordsEvents(t *testing.T) {
	t.Parallel()
	ledger := reftraffictranscript.NewUsageLedger()
	ev := usage.Event{TraceID: "t", ALegID: "a", BLegID: "b", AttemptSeq: 2, BackendID: "openai", InputTokens: 1, OutputTokens: 2}
	if err := ledger.OnUsage(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	evs := ledger.EventsSnapshot()
	if len(evs) != 1 || evs[0].AttemptSeq != 2 || evs[0].BLegID != "b" {
		t.Fatalf("%+v", evs)
	}
}
