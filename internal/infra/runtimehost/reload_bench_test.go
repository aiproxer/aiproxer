package runtimehost_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

type benchPlane struct {
	h http.Handler
}

func (p *benchPlane) Handler() http.Handler {
	if p.h != nil {
		return p.h
	}
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}
func (p *benchPlane) Close() error                  { return nil }
func (p *benchPlane) Quiesce(context.Context) error { return nil }

// BenchmarkManager_AcquireRelease measures hot-path lease acquire/release (req 15.1, 15.9).
func BenchmarkManager_AcquireRelease(b *testing.B) {
	m := runtimehost.NewManager(4, nil)
	g := m.PrepareRequestPlane("boot", &benchPlane{})
	if err := m.Publish(g); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		lease, ok := m.Acquire()
		if !ok {
			b.Fatal("acquire failed")
		}
		lease.Release()
	}
}

// BenchmarkGenerationDispatcher_AcquireLease measures dispatcher lease overhead
// including handler delegation (req 15.1, 15.9).
func BenchmarkGenerationDispatcher_AcquireLease(b *testing.B) {
	m := runtimehost.NewManager(4, nil)
	g := m.PrepareRequestPlane("boot", &benchPlane{h: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})})
	if err := m.Publish(g); err != nil {
		b.Fatal(err)
	}
	d := runtimehost.NewGenerationDispatcher(m)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	for b.Loop() {
		rr := httptest.NewRecorder()
		d.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			b.Fatalf("status=%d", rr.Code)
		}
	}
}

// BenchmarkManager_Publish measures atomic publication independent of quiesce/drain (req 15.4, 15.9).
func BenchmarkManager_Publish(b *testing.B) {
	m := runtimehost.NewManager(1<<20, nil)
	boot := m.PrepareRequestPlane("boot", &benchPlane{})
	if err := m.Publish(boot); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		g := m.PrepareRequestPlane(fmt.Sprintf("g-%d", i), &benchPlane{})
		if err := m.Publish(g); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRetainedGenerationOverhead measures observability/retention snapshot cost
// as retained generations grow (req 10.8, 15.6, 15.9).
func BenchmarkRetainedGenerationOverhead(b *testing.B) {
	for _, retained := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("n=%d", retained), func(b *testing.B) {
			m := runtimehost.NewManager(retained+2, nil)
			pins := make([]*runtimehost.Pin, 0, retained)
			boot := m.PrepareRequestPlane("boot", &benchPlane{})
			if err := m.Publish(boot); err != nil {
				b.Fatal(err)
			}
			for i := range retained {
				lease, ok := m.Acquire()
				if !ok {
					b.Fatal("acquire")
				}
				pin, ok := lease.TransferPin(runtimehost.PinSSE)
				if !ok {
					lease.Release()
					b.Fatal("pin")
				}
				pins = append(pins, pin)
				g := m.PrepareRequestPlane(fmt.Sprintf("r-%d", i), &benchPlane{})
				if err := m.Publish(g); err != nil {
					b.Fatal(err)
				}
			}
			b.Cleanup(func() {
				for _, p := range pins {
					p.Release()
				}
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = m.RetainedCount()
				_ = m.ObservabilitySnapshot()
				_ = m.RetentionPressure()
			}
		})
	}
}
