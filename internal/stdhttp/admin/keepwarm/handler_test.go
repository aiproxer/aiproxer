package keepwarm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	keepwarm "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
)

type wrappedEOFReader struct{}

func (wrappedEOFReader) Read([]byte) (int, error) { return 0, fmt.Errorf("body ended: %w", io.EOF) }

type policyStub struct {
	disabled bool
	calls    int
}

func (p *policyStub) Disable(string) (keepwarm.SessionPolicy, error) {
	p.disabled = true
	p.calls++
	return keepwarm.SessionPolicy{Disabled: true, Revision: 1, UpdatedAt: time.Now()}, nil
}
func (p *policyStub) Clear(string) error { p.disabled = false; p.calls++; return nil }
func (p *policyStub) Get(string) (keepwarm.SessionPolicy, bool) {
	return keepwarm.SessionPolicy{Disabled: p.disabled, Revision: 1}, p.disabled
}

func TestHandlerUsesAuthenticatedResolverNotBodyIdentity(t *testing.T) {
	t.Parallel()
	stub := &policyStub{}
	var audited string
	h := NewHandler(Options{Enabled: true, Service: stub, ResolveALegID: func(_ context.Context, _ *http.Request) (string, error) { return "authority-a", nil }, Audit: func(_ context.Context, action, id string) { audited = action + ":" + id }})
	r := httptest.NewRequest(http.MethodPost, "/disable", strings.NewReader(`{"a_leg_id":"attacker"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !stub.disabled || audited != "disable:authority-a" {
		t.Fatalf("status=%d disabled=%v audit=%q", w.Code, stub.disabled, audited)
	}
}

func TestHandlerMethodsAndBodyLimit(t *testing.T) {
	t.Parallel()
	stub := &policyStub{}
	h := NewHandler(Options{Enabled: true, MaxBodyBytes: 8, Service: stub, ResolveALegID: func(context.Context, *http.Request) (string, error) { return "a", nil }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/disable", strings.NewReader(`{"too":"large"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get code=%d", w.Code)
	}
}

func TestDecodeBoundedAcceptsWrappedEOF(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/disable", nil)
	r.Body = io.NopCloser(wrappedEOFReader{})
	w := httptest.NewRecorder()
	if err := decodeBounded(w, r, 8); err != nil {
		t.Fatalf("wrapped EOF should be treated as an empty body: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestDecodeBoundedAcceptsWhitespaceBody(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/disable", strings.NewReader(" \n\t "))
	w := httptest.NewRecorder()
	if err := decodeBounded(w, r, 8); err != nil {
		t.Fatalf("whitespace body should remain compatible with empty body: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
}
