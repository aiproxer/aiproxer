package tokenaccounting

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDisabledReturnsNotFoundAndDoesNotCallService(t *testing.T) {
	t.Parallel()
	svc := &fakeService{}
	h := NewHandler(Options{Enabled: false, Service: svc})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(`{`)))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if svc.calls != 0 {
		t.Fatalf("service calls = %d", svc.calls)
	}
}

func TestProtectedAccessUsesDiagnosticsSecret(t *testing.T) {
	t.Parallel()
	svc := &fakeService{}
	h := diag.WrapDiagnosticsProtect("secret", NewHandler(Options{Enabled: true, Service: svc}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if svc.calls != 0 {
		t.Fatalf("service calls = %d", svc.calls)
	}
}

func TestMethodAndJSONValidation(t *testing.T) {
	t.Parallel()
	svc := &fakeService{}
	h := NewHandler(Options{Enabled: true, Service: svc})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/token-count", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(`{"messages":"secret prompt"`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret prompt") || strings.Contains(rr.Body.String(), "messages") {
		t.Fatalf("bad JSON response leaked request details: %s", rr.Body.String())
	}

	h = NewHandler(Options{Enabled: true, Service: svc, MaxBodyBytes: 16})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status %d body=%s", rr.Code, rr.Body.String())
	}
	if svc.calls != 0 {
		t.Fatalf("service calls = %d", svc.calls)
	}
}

func TestSuccessfulCountShapeDoesNotEchoContentOrSecrets(t *testing.T) {
	t.Parallel()
	svc := &fakeService{response: CountResponse{Planes: []PlaneCount{{
		Plane:             aipapi.UsagePlaneProviderBillable,
		Tokens:            TokenDimensions{Input: 7, Output: 3, Total: 10},
		Source:            aipapi.UsageSourceLocalTokenizer,
		Authority:         aipapi.UsageAuthorityEstimated,
		Tokenizer:         aipapi.TokenizerRef{Type: "tiktoken", ID: "o200k_base", Version: "2026-01", Source: "admin", ModelUsed: "gpt-test"},
		UnavailableReason: "provider_unsupported",
		FallbackReason:    "local_default_encoding",
	}}}}
	h := NewHandler(Options{Enabled: true, Service: svc})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody()))
	req.Header.Set("Authorization", "Bearer request-secret")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"\"planes\"", "provider_billable", "\"tokens\"", "local_tokenizer", "estimated", "tiktoken", "provider_unsupported", "local_default_encoding"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q: %s", want, body)
		}
	}
	for _, forbidden := range []string{"secret prompt", "request-secret", "messages", "content", "text"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	if svc.last.Backend != "openai" || svc.last.Model != "gpt-test" || len(svc.last.Call.Messages) != 1 {
		t.Fatalf("service request not decoded: %#v", svc.last)
	}
}

func TestServiceFailureReturnsSafeError(t *testing.T) {
	t.Parallel()
	svc := &fakeService{err: errors.New("provider failed with bearer secret-token and raw prompt")}
	h := NewHandler(Options{Enabled: true, Service: svc})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "count_unavailable") {
		t.Fatalf("response missing safe classification: %s", body)
	}
	for _, forbidden := range []string{"secret-token", "raw prompt", "provider failed"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

type ctxMarker struct{}

func TestCanceledRequestReturnsRequestCanceledWithoutCallingService(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &fakeService{}
	h := NewHandler(Options{Enabled: true, Service: svc})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", bytes.NewBufferString(validRequestBody())).WithContext(ctx)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status %d body=%s, want 408 request_canceled", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "request_canceled") {
		t.Fatalf("body=%s, want request_canceled wire code", rr.Body.String())
	}
	if svc.calls != 0 {
		t.Fatalf("service calls = %d, want 0 for canceled request", svc.calls)
	}
}

func TestRequestContextPropagatesToService(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxMarker{}, "token-count-ctx")
	svc := &fakeService{}
	h := NewHandler(Options{Enabled: true, Service: svc})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())).WithContext(ctx)
	h.ServeHTTP(rr, req)

	if svc.calls != 1 {
		t.Fatalf("service calls = %d", svc.calls)
	}
	if svc.ctxValue != "token-count-ctx" {
		t.Fatalf("service context value = %v, want request context value", svc.ctxValue)
	}
	if svc.ctxErr != nil {
		t.Fatalf("service context error = %v, want nil for live request", svc.ctxErr)
	}
}

func validRequestBody() string {
	return `{"backend":"openai","model":"gpt-test","mode":"provider_first","call":{"ID":"call-1","Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"secret prompt"}]}]}}`
}

type fakeService struct {
	calls    int
	last     CountRequest
	ctxErr   error
	ctxValue any
	response CountResponse
	err      error
}

func (s *fakeService) Count(ctx context.Context, req CountRequest) (CountResponse, error) {
	s.calls++
	s.last = req
	s.ctxErr = ctx.Err()
	s.ctxValue = ctx.Value(ctxMarker{})
	if s.err != nil {
		return CountResponse{}, s.err
	}
	return s.response, nil
}

func TestNilServiceReturnsCountUnavailable(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Enabled: true, Service: nil})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "count_unavailable") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

func TestAdaptCountCallService_nilAndConcrete(t *testing.T) {
	t.Parallel()
	if AdaptCountCallService(nil) != nil {
		t.Fatal("nil CountCallService must adapt to nil Service")
	}
	var typedNil *app.Service
	if AdaptCountCallService(typedNil) != nil {
		t.Fatal("typed-nil *app.Service must adapt to nil Service")
	}
	call := &fakeCountCall{
		result: app.CountResult{
			InputTokens: 11, TotalTokens: 11,
			Accounting: aipapi.UsageAccountingMetadata{
				Plane:     aipapi.UsagePlaneClientVisible,
				Source:    aipapi.UsageSourceLocalTokenizer,
				Authority: aipapi.UsageAuthorityEstimated,
			},
		},
	}
	svc := AdaptCountCallService(call)
	if svc == nil {
		t.Fatal("expected adapted Service")
	}
	h := NewHandler(Options{Enabled: true, Service: svc})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(validRequestBody())))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if call.calls != 1 {
		t.Fatalf("CountCall calls=%d", call.calls)
	}
	if !strings.Contains(rr.Body.String(), `"input":11`) {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

type fakeCountCall struct {
	calls  int
	result app.CountResult
	err    error
}

func (f *fakeCountCall) CountCall(context.Context, app.CountCallInput) (app.CountResult, error) {
	f.calls++
	return f.result, f.err
}

// Compile-time: concrete app service satisfies CountCallService.
var _ CountCallService = (*app.Service)(nil)
