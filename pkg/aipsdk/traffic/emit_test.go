package traffic_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type seqRaw struct {
	mu  sync.Mutex
	seq *[]string
}

func (o *seqRaw) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	o.mu.Lock()
	*o.seq = append(*o.seq, "raw")
	o.mu.Unlock()
	return nil
}

type stripRedactor struct{}

func (stripRedactor) ID() string { return "strip" }

func (stripRedactor) Redact(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, body []byte) ([]byte, error) {
	return bytes.ReplaceAll(body, []byte("secret"), []byte("REDACTED")), nil
}

type seqObs struct {
	mu   sync.Mutex
	seq  *[]string
	last []byte
}

func (o *seqObs) OnObservation(_ context.Context, ev traffic.Observation) error {
	o.mu.Lock()
	*o.seq = append(*o.seq, "obs")
	o.last = append([]byte(nil), ev.Body...)
	o.mu.Unlock()
	return nil
}

func TestPortBundle_Emit_observationCopiesMetaLegAndProtocol(t *testing.T) {
	t.Parallel()
	var got traffic.Observation
	obs := captureObs{fn: func(ev traffic.Observation) { got = ev }}
	meta := traffic.CaptureMeta{
		TraceID:     "tr",
		ALegID:      "a",
		BLegID:      "b",
		PrincipalID: "p",
		SessionID:   "s",
		AttemptSeq:  2,
		BackendID:   "be",
		FrontendID:  "fe",
	}
	b := traffic.PortBundle{Obs: obs}
	b.Emit(
		context.Background(),
		traffic.LegBTP,
		meta,
		"proto/x",
		"text/plain",
		[]byte("rawbody"),
	)
	if got.Leg != traffic.LegBTP {
		t.Fatalf("Leg=%q", got.Leg)
	}
	if got.TraceID != meta.TraceID || got.ALegID != meta.ALegID || got.BLegID != meta.BLegID {
		t.Fatalf("lineage %+v", got)
	}
	if got.PrincipalID != meta.PrincipalID || got.SessionID != meta.SessionID || got.AttemptSeq != meta.AttemptSeq {
		t.Fatalf("ids %+v", got)
	}
	if got.BackendID != meta.BackendID || got.FrontendID != meta.FrontendID {
		t.Fatalf("route labels %+v", got)
	}
	if got.Protocol != "proto/x" || got.ContentType != "text/plain" {
		t.Fatalf("types %+v", got)
	}
	if string(got.Body) != "rawbody" {
		t.Fatalf("Body=%q", got.Body)
	}
	if got.RecordedAt.IsZero() {
		t.Fatal("RecordedAt unset")
	}
}

type captureObs struct {
	fn func(traffic.Observation)
}

func (c captureObs) OnObservation(_ context.Context, ev traffic.Observation) error {
	c.fn(ev)
	return nil
}

func TestPortBundle_Emit_orderRawBeforeObs(t *testing.T) {
	t.Parallel()
	var seq []string
	raw := &seqRaw{seq: &seq}
	obs := &seqObs{seq: &seq}
	b := traffic.PortBundle{
		Raw: raw,
		Obs: obs,
		Red: []traffic.Redactor{stripRedactor{}},
	}
	b.Emit(
		context.Background(),
		traffic.LegPTB,
		traffic.CaptureMeta{TraceID: "t"},
		"p",
		"application/json",
		[]byte(`{"x":"secret"}`),
	)
	if len(seq) != 2 || seq[0] != "raw" || seq[1] != "obs" {
		t.Fatalf("seq=%v", seq)
	}
	obs.mu.Lock()
	body := string(obs.last)
	obs.mu.Unlock()
	if !bytes.Contains([]byte(body), []byte("REDACTED")) || bytes.Contains([]byte(body), []byte("secret")) {
		t.Fatalf("body=%q", body)
	}
}

func TestPortBundle_EmitIsNoop(t *testing.T) {
	t.Parallel()
	if !(traffic.PortBundle{}).EmitIsNoop() {
		t.Fatal("empty bundle should be no-op")
	}
	disabled := traffic.PortBundle{
		Raw: traffic.DisabledRawCapture{},
		Obs: traffic.NoopObserver{},
	}
	if !disabled.EmitIsNoop() {
		t.Fatal("default disabled traffic ports should be no-op")
	}
	if !(traffic.PortBundle{Obs: traffic.NoopObserver{}}).EmitIsNoop() {
		t.Fatal("noop observer without raw sink or redactors should be no-op")
	}
	if (traffic.PortBundle{Obs: traffic.NoopObserver{}, Red: []traffic.Redactor{stripRedactor{}}}).EmitIsNoop() {
		t.Fatal("redactors present should not be no-op")
	}
	raw := &seqRaw{}
	if (traffic.PortBundle{Raw: raw, Obs: traffic.NoopObserver{}}).EmitIsNoop() {
		t.Fatal("active raw sink should not be no-op")
	}
	obs := &seqObs{}
	if (traffic.PortBundle{Obs: obs}).EmitIsNoop() {
		t.Fatal("active observer should not be no-op")
	}
}
