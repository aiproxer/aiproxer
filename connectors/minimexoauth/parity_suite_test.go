package minimexoauth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }
func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation, sessionID string, msgs []backendplugin.Message, nonStreaming bool) *memStream {
	delivery := aipapi.DeliveryModeStreaming
	transport := aipapi.TransportModeStreaming
	if nonStreaming {
		delivery = aipapi.DeliveryModeNonStreaming
		transport = aipapi.TransportModeNonStreaming
	}

	inv := backendplugin.Invocation{
		RequestID:           "req-1",
		AttemptID:           "att-1",
		ALegID:              "al-1",
		BLegID:              "bl-1",
		CanonicalModelID:    modelID,
		NativeModelID:       modelID,
		Operation:           string(op),
		DeliveryMode:        string(delivery),
		TransportMode:       string(transport),
		ProxyOwnedSessionID: sessionID,
		SafeMetadata: map[string]string{
			"app": "test-suite",
		},
		Messages: msgs,
	}

	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
}

func writeTokenFile(t *testing.T, rec oauthcred.TokenRecord) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal token record: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return filepath.ToSlash(path)
}

func TestPKCEHelpersAndValidation(t *testing.T) {
	verifier, challenge, state, err := oauthcred.GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE failed: %v", err)
	}
	if verifier == "" || challenge == "" || state == "" {
		t.Fatalf("PKCE values must be non-empty")
	}
	if err := oauthcred.ValidateState(state, state); err != nil {
		t.Fatalf("ValidateState should succeed for equal states: %v", err)
	}
	if err := oauthcred.ValidateState(state, "wrong-state"); err == nil {
		t.Fatalf("ValidateState should fail for mismatched states")
	}
}

func TestTokenRefreshAndQuarantine(t *testing.T) {
	tests := []struct {
		name             string
		handler          http.HandlerFunc
		expectQuarantine bool
	}{
		{
			name: "1. HTTP 400 + invalid_grant",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":             "invalid_grant",
					"error_description": "Refresh token is invalid or expired",
				})
			},
			expectQuarantine: true,
		},
		{
			name: "2a. HTTP 200 JSON status=error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "error",
					"base_resp": map[string]any{
						"status_code": 1001,
						"status_msg":  "token has expired",
					},
				})
			},
			expectQuarantine: true,
		},
		{
			name: "2b. HTTP 200 success-shaped with empty access_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":        "success",
					"access_token":  "",
					"refresh_token": "some-refresh",
					"expired_in":    3600,
				})
			},
			expectQuarantine: true,
		},
		{
			name: "3. HTTP 400 body refresh_token_reused",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":             "refresh_token_reused",
					"error_description": "Refresh token was already used",
				})
			},
			expectQuarantine: true,
		},
		{
			name: "4. HTTP 410 Gone",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusGone)
				_, _ = w.Write([]byte("Gone"))
			},
			expectQuarantine: true,
		},
		{
			name: "5. HTTP 429 Too Many Requests (transient, no quarantine)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("rate limit exceeded"))
			},
			expectQuarantine: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tokenHits int32
			authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oauth/token" {
					http.NotFound(w, r)
					return
				}
				atomic.AddInt32(&tokenHits, 1)
				tc.handler(w, r)
			}))
			t.Cleanup(authSrv.Close)

			tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
				RefreshToken: "bad-refresh-tok",
			})

			store := oauthcred.NewFileStore(tokenPath)
			refresher := &service.MiniMaxOAuthRefresher{
				PortalBaseURL: authSrv.URL,
				ClientID:      "test-client-id",
				HTTPClient:    authSrv.Client(),
			}
			session := oauthcred.NewSession(store, refresher, oauthcred.WithSkew(service.TokenRefreshSkew))
			tp := service.NewOAuthTokenProvider(session)

			// Attempt 1: Refresh fails
			_, err := tp.Token(context.Background())
			if err == nil {
				t.Fatalf("expected error on attempt 1, got nil")
			}
			if atomic.LoadInt32(&tokenHits) != 1 {
				t.Fatalf("expected token endpoint hit once, got %d", tokenHits)
			}

			// Attempt 2
			_, err2 := tp.Token(context.Background())
			if tc.expectQuarantine {
				// Must fail closed with ErrQuarantined without touching server!
				if err2 == nil {
					t.Fatalf("expected quarantined error on attempt 2, got nil")
				}
				if atomic.LoadInt32(&tokenHits) != 1 {
					t.Fatalf("quarantined session hit server again on attempt 2 (tokenHits=%d)", tokenHits)
				}
			} else {
				// Transient errors hit server again
				if atomic.LoadInt32(&tokenHits) != 2 {
					t.Fatalf("transient error should have hit server again on attempt 2 (tokenHits=%d)", tokenHits)
				}
				if err2 == nil {
					t.Fatalf("expected error on attempt 2, got nil")
				}
			}
		})
	}
}

func TestOAuthStateMachine_CodeToPollToExecute(t *testing.T) {
	var codeHits atomic.Int32
	var pollHits atomic.Int32
	var chatHits int32
	var capturedUA string
	var capturedAuth string
	var capturedVersion string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")

		switch r.URL.Path {
		case "/oauth/code":
			codeHits.Add(1)
			_ = r.ParseForm()
			state := r.PostFormValue("state")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_code":        "MINI-9876",
				"verification_uri": "https://minimax.io/device",
				"expired_in":       300,
				"state":            state,
				"interval":         200,
			})
			return

		case "/oauth/token":
			hits := pollHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if hits == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "pending",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":        "success",
				"access_token":  "minted-oauth-token",
				"refresh_token": "minted-refresh-token",
				"expired_in":    3600,
			})
			return

		case "/v1/messages":
			atomic.AddInt32(&chatHits, 1)
			capturedAuth = r.Header.Get("Authorization")
			capturedVersion = r.Header.Get("anthropic-version")

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			chunks := []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":12}}}\n\n",
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from MiniMax!\"}}\n\n",
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":6}}\n\n",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			}
			for _, c := range chunks {
				_, _ = w.Write([]byte(c))
				if ok {
					flusher.Flush()
				}
			}
			return

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// 1. Run device authorization flow: RequestUserCode
	verifier, challenge, state, err := oauthcred.GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	codeResp, err := service.RequestUserCode(context.Background(), srv.Client(), srv.URL, "custom-client", service.DefaultScope, challenge, state)
	if err != nil {
		t.Fatalf("RequestUserCode failed: %v", err)
	}
	if codeResp.UserCode != "MINI-9876" {
		t.Fatalf("expected user code MINI-9876, got %q", codeResp.UserCode)
	}

	// 2. Poll for token
	tokenRec, err := service.PollToken(context.Background(), srv.Client(), srv.URL, "custom-client", codeResp.UserCode, verifier, codeResp.ExpiredIn, codeResp.Interval)
	if err != nil {
		t.Fatalf("PollToken failed: %v", err)
	}
	if tokenRec.AccessToken != "minted-oauth-token" {
		t.Fatalf("expected access token 'minted-oauth-token', got %q", tokenRec.AccessToken)
	}

	// 3. Persist to token file
	tokenPath := writeTokenFile(t, tokenRec)

	// 4. Configure service with token file
	cfgYAML := fmt.Appendf(nil, `
portal_base_url: %s
inference_base_url: %s
oauth_client_id: custom-client
oauth_token_file: %s
`, srv.URL, srv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// 5. Execute inference stream
	userText := "Hello MiniMax"
	testMsgs := []backendplugin.Message{
		{
			Role: backendplugin.RoleUser,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindText, Text: &userText},
			},
		},
	}
	stream := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "sess-1", testMsgs, false)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&chatHits) != 1 {
		t.Fatalf("expected 1 chat hit, got %d", chatHits)
	}
	if capturedUA != service.DefaultUserAgent {
		t.Fatalf("expected UA %q, got %q", service.DefaultUserAgent, capturedUA)
	}
	if capturedAuth != "Bearer minted-oauth-token" {
		t.Fatalf("expected Bearer minted-oauth-token, got %q", capturedAuth)
	}
	if capturedVersion != "2023-06-01" {
		t.Fatalf("expected anthropic-version 2023-06-01, got %q", capturedVersion)
	}

	// Check stream received events
	var sawText bool
	var sawTerminal bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta {
			if f.Event.Delta != nil && *f.Event.Delta == "Hello from MiniMax!" {
				sawText = true
			}
		}
		if f.Kind == backendplugin.ServerFrameTerminal {
			sawTerminal = true
		}
	}
	if !sawText {
		t.Fatalf("expected to receive text delta event")
	}
	if !sawTerminal {
		t.Fatalf("expected to receive terminal frame")
	}
}

func TestAnthropicTransport_UnaryAndStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":5}}}\n\n"))
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"streamed content\"}}\n\n"))
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"))
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"content": [{"type": "text", "text": "unary content"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 5, "output_tokens": 2}
		}`))
	}))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken: "valid-tok",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_base_url: %s
oauth_client_id: client-123
oauth_token_file: %s
`, srv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	userText := "test"
	msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}

	// 1. Streaming test
	stream1 := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "", msgs, false)
	if err := inst.Execute(stream1); err != nil {
		t.Fatalf("Execute streaming failed: %v", err)
	}
	var sawStreamText bool
	for _, f := range stream1.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Delta != nil && *f.Event.Delta == "streamed content" {
			sawStreamText = true
		}
	}
	if !sawStreamText {
		t.Fatalf("expected streamed content event")
	}

	// 2. Unary test
	stream2 := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "", msgs, true)
	if err := inst.Execute(stream2); err != nil {
		t.Fatalf("Execute unary failed: %v", err)
	}
	var sawUnaryText bool
	for _, f := range stream2.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Delta != nil && *f.Event.Delta == "unary content" {
			sawUnaryText = true
		}
	}
	if !sawUnaryText {
		t.Fatalf("expected unary content event")
	}
}

func TestDynamicCatalogInventory(t *testing.T) {
	t.Run("200ReturnsEnumeratedModelsOnly", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "missing auth", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": "MiniMax-M2.7"},
					{"id": "MiniMax-M3"}
				]
			}`))
		}))
		t.Cleanup(srv.Close)

		tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
			AccessToken: "test-token-valid",
			Expiry:      time.Now().Add(1 * time.Hour),
		})

		cfgYAML := fmt.Appendf(nil, `
inference_base_url: %s
oauth_client_id: custom-client-123
oauth_token_file: %s
`, srv.URL, tokenPath)

		svc := service.NewProduction()
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  cfgYAML,
			Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		resp, err := inst.ListModels(context.Background(), 0)
		if err != nil {
			t.Fatalf("ListModels failed: %v", err)
		}
		if len(resp.Models) != 2 {
			t.Fatalf("expected exactly 2 enumerated models, got %d: %+v", len(resp.Models), resp.Models)
		}
		var foundM3 bool
		for _, m := range resp.Models {
			if m.CanonicalModelID == "minimax-oauth/MiniMax-M3" && m.NativeModelID == "MiniMax-M3" {
				foundM3 = true
			}
			if m.CanonicalModelID == "minimax-oauth/"+service.ModelM27Highspeed {
				t.Fatalf("non-enumerated static model must not be advertised, got %+v", resp.Models)
			}
		}
		if !foundM3 {
			t.Fatalf("expected MiniMax-M3 to be discovered and prefixed, got %+v", resp.Models)
		}
		if resp.InventorySource != service.FactoryKind {
			t.Fatalf("InventorySource = %q, want %q", resp.InventorySource, service.FactoryKind)
		}
		if resp.FetchedUnixMS <= 0 {
			t.Fatalf("FetchedUnixMS = %d, want positive", resp.FetchedUnixMS)
		}
	})

	t.Run("Non200FailsClosed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "internal inventory failure"}`))
		}))
		t.Cleanup(srv.Close)

		tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
			AccessToken: "test-token-valid",
			Expiry:      time.Now().Add(1 * time.Hour),
		})

		cfgYAML := fmt.Appendf(nil, `
inference_base_url: %s
oauth_client_id: custom-client-123
oauth_token_file: %s
`, srv.URL, tokenPath)

		svc := service.NewProduction()
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  cfgYAML,
			Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		_, err = inst.ListModels(context.Background(), 0)
		if err == nil {
			t.Fatalf("expected ListModels to fail closed on non-200")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected status 500 in error, got: %v", err)
		}
	})
}

func TestEntitlement403Vs401Retry(t *testing.T) {
	t.Run("403EntitlementFailsClosedWithoutRefresh", func(t *testing.T) {
		var tokenHits int32
		var chatHits atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth/token":
				atomic.AddInt32(&tokenHits, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":        "success",
					"access_token":  "access-token",
					"refresh_token": "refresh-token",
					"expired_in":    3600,
				})
			case "/v1/messages":
				chatHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"Subscription tier not entitled to this model"}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)

		tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
			AccessToken: "valid-tok",
			Expiry:      time.Now().Add(1 * time.Hour),
		})

		cfgYAML := fmt.Appendf(nil, `
portal_base_url: %s
inference_base_url: %s
oauth_client_id: client-123
oauth_token_file: %s
`, srv.URL, srv.URL, tokenPath)

		svc := service.NewProduction()
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  cfgYAML,
			Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		userText := "hi"
		msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}
		stream := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "", msgs, true)
		err = inst.Execute(stream)
		if err == nil {
			t.Fatalf("expected error on 403, got nil")
		}
		if !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "entitlement") {
			t.Fatalf("expected entitlement/403 in error, got: %v", err)
		}
		if atomic.LoadInt32(&tokenHits) != 0 {
			t.Fatalf("403 must fail closed without triggering token refresh, got %d refresh hits", tokenHits)
		}
	})

	t.Run("401TransientRetriesOnceWithRefreshedToken", func(t *testing.T) {
		var tokenHits int32
		var chatHits int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth/token":
				atomic.AddInt32(&tokenHits, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":        "success",
					"access_token":  "minted-new-token",
					"refresh_token": "rotated-refresh-token",
					"expired_in":    3600,
				})
			case "/v1/messages":
				count := atomic.AddInt32(&chatHits, 1)
				auth := r.Header.Get("Authorization")
				if count == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
					return
				}
				if auth != "Bearer minted-new-token" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"still unauthorized"}}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"content": [{"type": "text", "text": "retried success"}],
					"stop_reason": "end_turn",
					"usage": {"input_tokens": 5, "output_tokens": 2}
				}`))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)

		tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
			AccessToken:  "stale-token",
			RefreshToken: "valid-refresh-token",
			Expiry:       time.Now().Add(1 * time.Hour),
		})

		cfgYAML := fmt.Appendf(nil, `
portal_base_url: %s
inference_base_url: %s
oauth_client_id: client-123
oauth_token_file: %s
`, srv.URL, srv.URL, tokenPath)

		svc := service.NewProduction()
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  cfgYAML,
			Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		userText := "hi"
		msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}
		stream := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "", msgs, true)
		if err := inst.Execute(stream); err != nil {
			t.Fatalf("Execute should have succeeded on retry: %v", err)
		}
		if atomic.LoadInt32(&tokenHits) != 1 {
			t.Fatalf("expected 1 token refresh, got %d", tokenHits)
		}
		if atomic.LoadInt32(&chatHits) != 2 {
			t.Fatalf("expected 2 chat requests (1st 401 + retry), got %d", chatHits)
		}
	})
}

func TestHardNegativeNoMinimaxAPIKey(t *testing.T) {
	cfgYAML := []byte("oauth_token_file: /path/to/tokens.json\noauth_client_id: my-client\n")
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"MINIMAX_API_KEY": []byte("sk-some-key"),
		},
	}
	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
	})
	if err == nil {
		t.Fatalf("expected Configure to fail when MINIMAX_API_KEY is supplied, got nil")
	}
	if !strings.Contains(err.Error(), "MINIMAX_API_KEY is not supported") {
		t.Fatalf("expected MINIMAX_API_KEY error, got: %v", err)
	}
}

func TestHardNegativeNoOpenAIChatCompletionsWire(t *testing.T) {
	var requestedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		if strings.Contains(r.URL.Path, "chat/completions") || strings.Contains(r.URL.Path, "chatcompletion") {
			t.Errorf("FORBIDDEN wire request to OpenAI chat completions path: %s", r.URL.Path)
			http.Error(w, "forbidden", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{"type": "text", "text": "ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 5, "output_tokens": 2}
		}`))
	}))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken: "tok-1",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_base_url: %s
oauth_client_id: client-123
oauth_token_file: %s
`, srv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	userText := "hi"
	msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}
	stream := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationAnthropicMessages, "", msgs, true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(requestedPaths) != 1 || requestedPaths[0] != "/v1/messages" {
		t.Fatalf("expected exactly 1 wire request to /v1/messages, got: %v", requestedPaths)
	}
}

func TestHardNegativeNoResponsesOperation(t *testing.T) {
	svc := service.NewProduction()
	cfgYAML := []byte("inference_base_url: https://api.minimax.io/anthropic\n")
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"access_token": []byte("static-tok")},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	userText := "hi"
	msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}
	stream := newTestExecuteStream(context.Background(), "minimax-oauth/MiniMax-M2.7", aipapi.OperationOpenAIResponses, "", msgs, false)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected error rejecting OpenAI Responses operation, got nil")
	}
	if !strings.Contains(err.Error(), "responses operations are not supported") {
		t.Fatalf("expected responses rejection error, got %v", err)
	}
}

func TestHardNegativeNoHermesTags(t *testing.T) {
	var checkedRequests int
	inspectHandler := func(w http.ResponseWriter, r *http.Request) {
		checkedRequests++
		ua := r.Header.Get("User-Agent")
		if strings.Contains(strings.ToLower(ua), "hermes") {
			t.Errorf("forbidden client identity in User-Agent: %s", ua)
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(strings.ToLower(v), "hermes") {
					t.Errorf("forbidden tag in header %s: %s", k, v)
				}
			}
		}

		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"dummy"}]}`))
			return
		}
		http.NotFound(w, r)
	}

	srv := httptest.NewServer(http.HandlerFunc(inspectHandler))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken: "test-tok",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_base_url: %s
oauth_client_id: my-third-party-client
oauth_token_file: %s
`, srv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	_, _ = inst.ListModels(context.Background(), 0)
	if checkedRequests == 0 {
		t.Fatalf("no requests checked")
	}
}

func TestHardNegativeNoHardcodedHermesClientIDInProduction(t *testing.T) {
	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "some-refresh-token",
	})

	cfgYAML := fmt.Appendf(nil, `
inference_base_url: https://api.minimax.io/anthropic
oauth_token_file: %s
`, tokenPath)

	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail when oauth_client_id is omitted (Hermes client_id must not be hardcoded)")
	}
	if !strings.Contains(err.Error(), "oauth_client_id is required") {
		t.Fatalf("expected oauth_client_id required error, got: %v", err)
	}
}

func TestDistinctFromMiniMaxAPIKeyProfile(t *testing.T) {
	if service.FactoryKind == "minimax" || service.FactoryKind == "minimax-cn" {
		t.Fatalf("minimax-oauth must be distinct from minimax and minimax-cn, got %q", service.FactoryKind)
	}
	if service.PluginID == "com.aiproxer.backend.minimax" {
		t.Fatalf("minimax-oauth must be distinct plugin ID, got %q", service.PluginID)
	}
}

func TestChinaRegionOverride(t *testing.T) {
	for _, alias := range []string{"cn", "china", "minimax-cn"} {
		t.Run(alias, func(t *testing.T) {
			cfgYAML := []byte("region: " + alias + "\noauth_token_file: /path/to/tokens.json\noauth_client_id: client\n")
			cfg, err := service.ParseAndValidateConfig(cfgYAML, backendplugin.SecretBundle{})
			if err != nil {
				t.Fatalf("unexpected error for alias %q: %v", alias, err)
			}
			if cfg.PortalBaseURL != service.CNPortalBaseURL {
				t.Fatalf("alias %q: expected CN portal URL %q, got %q", alias, service.CNPortalBaseURL, cfg.PortalBaseURL)
			}
			if cfg.InferenceBaseURL != service.CNInferenceBaseURL {
				t.Fatalf("alias %q: expected CN inference URL %q, got %q", alias, service.CNInferenceBaseURL, cfg.InferenceBaseURL)
			}
		})
	}
}
