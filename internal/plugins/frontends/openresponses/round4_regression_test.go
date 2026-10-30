package openresponses_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corecont "github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// round4CapturingAuth is separate from the ordinary test authorizer so this
// regression observes exactly what the WebSocket upgrade passes to auth.
type round4CapturingAuth struct {
	mu    sync.Mutex
	token string
}

func (a *round4CapturingAuth) Authenticate(_ context.Context, meta sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	a.mu.Lock()
	a.token = meta.AuthorizationBearer
	a.mu.Unlock()
	return sdkauth.Decision{Outcome: sdkauth.OutcomeAllow}, nil
}

func (a *round4CapturingAuth) tokenValue() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

func TestRound4WebSocketBearerSubprotocolAndHeaderAuth(t *testing.T) {
	cfg := wsTestConfig(nil)
	auth := &round4CapturingAuth{}
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg, Authorizer: auth,
	})
	srv := newWSTestServerFor(t, handler)

	token := "browser-secret"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(token))
	header := http.Header{"Sec-WebSocket-Protocol": []string{"base64url.bearer.authorization.aip", encoded}}
	conn := wsDial(t, srv, header)
	if got := conn.Subprotocol(); got != "base64url.bearer.authorization.aip" {
		t.Fatalf("negotiated subprotocol = %q, want bearer marker", got)
	}
	_ = conn.Close()
	if got := auth.tokenValue(); got != token {
		t.Fatalf("subprotocol bearer token = %q, want %q", got, token)
	}

	authHeader := &round4CapturingAuth{}
	handler = openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg, Authorizer: authHeader,
	})
	srv2 := newWSTestServerFor(t, handler)
	conn = wsDial(t, srv2, http.Header{"Authorization": []string{"Bearer header-secret"}})
	_ = conn.Close()
	if got := authHeader.tokenValue(); got != "header-secret" {
		t.Fatalf("Authorization bearer token = %q, want header fallback", got)
	}
}

func TestRound4HTTPEventErrorReleasesContinuationReservation(t *testing.T) {
	t.Run("/openresponses/v1/responses", func(t *testing.T) {
		store := corecont.NewMemoryStoreWithLimits(aipcont.StorageLimits{MaxRecords: 1})
		stream := aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventError, ErrorCode: "provider_failed", ErrorMessage: "secret provider detail"},
		})
		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Executor:             &round4FixedExecutor{stream: stream},
			ContinuationStore:    store,
		})
		body := `{"model":"m","input":"hi","store":true}`
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Session-ID", "round4-session")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status=%d, want 502; body=%s", rec.Code, rec.Body.String())
		}
		if bytes.Contains(rec.Body.Bytes(), []byte("secret provider detail")) {
			t.Fatal("provider error detail leaked into HTTP response")
		}
		id, err := store.Reserve(context.Background(), aipcont.Scope{SessionID: "round4-session"}, aipcont.StoragePolicy{TTL: time.Hour})
		if err != nil {
			t.Fatalf("reservation remained occupied after EventError: %v", err)
		}
		_ = store.Delete(context.Background(), aipcont.Scope{SessionID: "round4-session"}, id)
	})
}

type round4FixedExecutor struct {
	stream aipapi.EventStream
}

func (e *round4FixedExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return e.stream, nil
}
