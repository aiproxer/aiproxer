package openairesponses

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestResponseIDForCallEncodesCancelCarrier(t *testing.T) {
	t.Parallel()
	call := testCallWithSession("a-leg-123", "sess-123")

	responseID := responseIDForCall(call)
	gotALegID, gotSessionID, ok := cancelCarrierFromResponseID(responseID)
	if !ok {
		t.Fatalf("cancelCarrierFromResponseID(%q) did not decode", responseID)
	}
	if gotALegID != "a-leg-123" || gotSessionID != "sess-123" {
		t.Fatalf("decoded cancel carrier = (%q, %q), want (a-leg-123, sess-123)", gotALegID, gotSessionID)
	}
}

func TestCancelCarrierFromResponseIDRejectsLegacyStableIDs(t *testing.T) {
	t.Parallel()
	if gotALegID, gotSessionID, ok := cancelCarrierFromResponseID("resp_legacy_hash"); ok || gotALegID != "" || gotSessionID != "" {
		t.Fatalf("cancelCarrierFromResponseID decoded (%q, %q), ok=%v", gotALegID, gotSessionID, ok)
	}
}

func TestHandler_cancelResponseUsesSessionBindingFromResponseID(t *testing.T) {
	t.Parallel()
	ex := &cancelCaptureExecutor{}
	h := &Handler{Exec: ex}
	responseID := responseIDForCall(testCallWithSession("a-leg-123", "sess-123"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/"+responseID+"/cancel", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ex.canceled.ALegID != "a-leg-123" || ex.canceled.SessionID != "sess-123" {
		t.Fatalf("cancel request = %+v", ex.canceled)
	}
}

func TestHandler_cancelResponseRejectsResponseIDWithoutSessionBinding(t *testing.T) {
	t.Parallel()
	ex := &cancelCaptureExecutor{}
	h := &Handler{Exec: ex}
	legacyAlegOnlyID := responseIDALegPrefix + base64.RawURLEncoding.EncodeToString([]byte("a-leg-123"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/"+legacyAlegOnlyID+"/cancel", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ex.canceled.ALegID != "" {
		t.Fatalf("unexpected cancel request = %+v", ex.canceled)
	}
	if !strings.Contains(rr.Body.String(), "missing session cancellation carrier") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestResponseIDFromCancelPathUsesLastCancelSegment(t *testing.T) {
	t.Parallel()
	got := responseIDFromCancelPath("/v1/responses/fake-id/cancel/responses/real-id/cancel")
	if got != "real-id" {
		t.Fatalf("responseIDFromCancelPath returned %q, want real-id", got)
	}
}

type cancelCaptureExecutor struct {
	canceled aipapi.ALegCancelRequest
}

func (e *cancelCaptureExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (e *cancelCaptureExecutor) WallClock() func() time.Time { return nil }

func (e *cancelCaptureExecutor) CancelALeg(_ context.Context, req aipapi.ALegCancelRequest) error {
	e.canceled = req
	return nil
}

func testCallWithSession(aLegID, sessionID string) *aipapi.Call {
	return &aipapi.Call{Session: aipapi.SessionRef{ALegID: aLegID, AuthoritativeSessionID: sessionID}}
}
