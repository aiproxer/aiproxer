package traffic_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type errObserver struct{}

func (errObserver) OnObservation(context.Context, traffic.Observation) error {
	return errors.New("boom")
}

type appendObserver struct {
	b *bytes.Buffer
}

func (a appendObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	a.b.Write(ev.Body)
	return nil
}

func TestChainObservers_failOpen(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	o := traffic.ChainObservers(errObserver{}, appendObserver{&buf})
	if err := o.OnObservation(context.Background(), traffic.Observation{Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "x" {
		t.Fatalf("got %q", buf.String())
	}
}

type memSink struct {
	seen [][]byte
}

func (m *memSink) WriteRaw(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, payload []byte) error {
	m.seen = append(m.seen, slices.Clone(payload))
	return nil
}

func TestMultiRawCapture_failOpen(t *testing.T) {
	t.Parallel()
	var a, b memSink
	bad := errSink{}
	s := traffic.MultiRawCapture(&a, bad, &b)
	if err := s.WriteRaw(context.Background(), traffic.LegCTP, traffic.CaptureMeta{TraceID: "t"}, []byte("p")); err != nil {
		t.Fatal(err)
	}
	if len(a.seen) != 1 || string(a.seen[0]) != "p" {
		t.Fatalf("a=%v", a.seen)
	}
	if len(b.seen) != 1 || string(b.seen[0]) != "p" {
		t.Fatalf("b=%v", b.seen)
	}
}

type errSink struct{}

func (errSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return errors.New("raw boom")
}

func TestMultiRawCapture_skipsDisabled(t *testing.T) {
	t.Parallel()
	var a memSink
	s := traffic.MultiRawCapture(traffic.DisabledRawCapture{}, &a)
	_ = s.WriteRaw(context.Background(), traffic.LegPTB, traffic.CaptureMeta{}, []byte("z"))
	if len(a.seen) != 1 {
		t.Fatalf("want 1 write got %d", len(a.seen))
	}
}

type tagRedactor struct {
	id  string
	pri int
	out []byte
	err bool
}

func (r tagRedactor) ID() string { return r.id }

func (r tagRedactor) Redact(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, body []byte) ([]byte, error) {
	if r.err {
		return nil, errors.New("redact err")
	}
	_ = ctx
	_ = leg
	_ = meta
	if r.out != nil {
		return r.out, nil
	}
	return body, nil
}

func (r tagRedactor) Priority() int { return r.pri }

func TestApplyRedactors_failOpen(t *testing.T) {
	t.Parallel()
	rs := []traffic.Redactor{
		tagRedactor{id: "a", err: true},
		tagRedactor{id: "b", out: []byte("ok")},
	}
	got := traffic.ApplyRedactors(context.Background(), traffic.LegCTP, traffic.CaptureMeta{}, []byte("in"), rs)
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestMaterializeSortedRedactors_order(t *testing.T) {
	t.Parallel()
	rs := []traffic.Redactor{
		tagRedactor{id: "z", pri: 1},
		tagRedactor{id: "a", pri: 1},
		tagRedactor{id: "m", pri: 0},
	}
	got := traffic.MaterializeSortedRedactors(rs)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].ID() != "m" {
		t.Fatalf("first=%s", got[0].ID())
	}
	if got[1].ID() != "a" || got[2].ID() != "z" {
		t.Fatalf("order %s %s %s", got[0].ID(), got[1].ID(), got[2].ID())
	}
}
