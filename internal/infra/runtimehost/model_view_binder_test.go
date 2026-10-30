package runtimehost_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

type bindingPlane struct {
	h         http.Handler
	bindCalls atomic.Int64
	modelGen  string
}

func (p *bindingPlane) Handler() http.Handler         { return p.h }
func (p *bindingPlane) Close() error                  { return nil }
func (p *bindingPlane) Quiesce(context.Context) error { return nil }

func (p *bindingPlane) BindModelViews(ctx context.Context) context.Context {
	p.bindCalls.Add(1)
	return context.WithValue(ctx, modelGenCtxKey{}, p.modelGen)
}

type modelGenCtxKey struct{}

func TestGenerationDispatcher_callsModelViewBinderExactlyOnce(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	plane := &bindingPlane{modelGen: "model-gen-7"}
	plane.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Error("missing request binding")
		}
		if b.Meta().ID != 1 {
			t.Errorf("config generation = %d, want 1", b.Meta().ID)
		}
		got, _ := r.Context().Value(modelGenCtxKey{}).(string)
		if got != "model-gen-7" {
			t.Errorf("model generation = %q", got)
		}
		_, _ = io.WriteString(w, "ok")
	})
	g := m.PrepareRequestPlane("gen", plane)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if plane.bindCalls.Load() != 1 {
		t.Fatalf("BindModelViews calls = %d, want 1", plane.bindCalls.Load())
	}
}

func TestGenerationDispatcher_optionalBinderCompatibility(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)
	publishPlane(t, m, "plain", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := runtimehost.BindingFromContext(r.Context()); !ok {
			t.Error("binding missing")
		}
		_, _ = io.WriteString(w, "plain")
	}))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Body.String() != "plain" {
		t.Fatalf("body=%q", rr.Body.String())
	}
}
