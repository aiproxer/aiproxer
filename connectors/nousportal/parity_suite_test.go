package nousportal_test

import (
	"context"
	"encoding/base64"
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
	"github.com/aiproxer/aiproxer/connectors/nousportal/internal/service"
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

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation, userMsg string, nonStreaming bool) *memStream {
	delivery := aipapi.DeliveryModeStreaming
	transport := aipapi.TransportModeStreaming
	if nonStreaming {
		delivery = aipapi.DeliveryModeNonStreaming
		transport = aipapi.TransportModeNonStreaming
	}
	msg := userMsg
	inv := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "al-1",
		BLegID:           "bl-1",
		CanonicalModelID: modelID,
		NativeModelID:    modelID,
		Operation:        string(op),
		DeliveryMode:     string(delivery),
		TransportMode:    string(transport),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
}

func createTestJWT(claims map[string]any) string {
	headerJSON := []byte(`{"alg":"none","typ":"JWT"}`)
	claimsJSON, _ := json.Marshal(claims)
	b64Header := base64.RawURLEncoding.EncodeToString(headerJSON)
	b64Claims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return b64Header + "." + b64Claims + ".testsignature"
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
	return path
}

// Parity_ keeps these real provider-emulator scenarios discoverable by the
// advertised-capability release gate.
func TestParity_JWTMintInferenceInvokeAndChat(t *testing.T) {
	var portalHits, inferenceHits int32
	var capturedAuth, capturedUA string

	portalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&portalHits, 1)
		capturedUA = r.Header.Get("User-Agent")
		if r.URL.Path != "/api/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-nous-refresh-token") != "initial-refresh-token" {
			http.Error(w, "invalid refresh token", http.StatusBadRequest)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "custom-client-123" {
			http.Error(w, "invalid grant or client_id", http.StatusBadRequest)
			return
		}

		jwt := createTestJWT(map[string]any{
			"sub":   "user-nous-1",
			"scope": "inference:invoke",
			"exp":   time.Now().Add(1 * time.Hour).Unix(),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  jwt,
			"refresh_token": "rotated-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "inference:invoke",
		})
	}))
	t.Cleanup(portalSrv.Close)

	inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inferenceHits, 1)
		capturedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hello from nous"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	t.Cleanup(inferenceSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "initial-refresh-token",
	})

	cfgYAML := fmt.Appendf(nil, `
portal_url: %s
inference_url: %s
oauth_client_id: custom-client-123
oauth_token_file: %s
model: anthropic/claude-sonnet-4.6
`, portalSrv.URL, inferenceSrv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	res, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !res.Capabilities.Streaming {
		t.Fatalf("expected streaming capability true")
	}

	testStream := newTestExecuteStream(context.Background(), "nous-portal/anthropic/claude-sonnet-4.6", aipapi.OperationOpenAIChatCompletions, "ping", true)

	if err := inst.Execute(testStream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&portalHits) != 1 {
		t.Fatalf("expected 1 portal hit, got %d", portalHits)
	}
	if atomic.LoadInt32(&inferenceHits) != 1 {
		t.Fatalf("expected 1 inference hit, got %d", inferenceHits)
	}
	if !strings.HasPrefix(capturedAuth, "Bearer ") {
		t.Fatalf("expected Bearer auth on inference request, got %q", capturedAuth)
	}
	if capturedUA != service.DefaultUserAgent {
		t.Fatalf("expected User-Agent %q, got %q", service.DefaultUserAgent, capturedUA)
	}
}

func TestParity_LegacyOpaqueSessionKeyAndChat(t *testing.T) {
	var inferenceHits atomic.Int32
	var capturedAuth string

	inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inferenceHits.Add(1)
		capturedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-opaque",
			"object": "chat.completion",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
		}`))
	}))
	t.Cleanup(inferenceSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken:  "opaque-session-key-98765",
		RefreshToken: "refresh-unused",
		Expiry:       time.Now().Add(2 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_url: %s
oauth_client_id: custom-client-123
oauth_token_file: %s
`, inferenceSrv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	testStream := newTestExecuteStream(context.Background(), "nous-portal/anthropic/claude-sonnet-4.6", aipapi.OperationOpenAIChatCompletions, "hi", true)

	if err := inst.Execute(testStream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if capturedAuth != "Bearer opaque-session-key-98765" {
		t.Fatalf("expected opaque token Bearer, got %q", capturedAuth)
	}
}

func TestParity_OAuthQuarantineOnTerminalError(t *testing.T) {
	var portalHits int32
	portalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&portalHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token is revoked"}`))
	}))
	t.Cleanup(portalSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "bad-refresh-token",
	})

	store := oauthcred.NewFileStore(tokenPath)
	refresher := &service.NousOAuthRefresher{
		PortalURL:  portalSrv.URL,
		ClientID:   "custom-client-123",
		HTTPClient: portalSrv.Client(),
	}
	session := oauthcred.NewSession(store, refresher)
	tp := service.NewOAuthTokenProvider(session)

	// Attempt 1: Calls Portal, receives invalid_grant, marks quarantined.
	_, err := tp.Token(context.Background())
	if err == nil {
		t.Fatalf("expected error on invalid_grant, got nil")
	}

	// Verify file was quarantined
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if !rec.Quarantined {
		t.Fatalf("expected store to be marked Quarantined")
	}

	// Attempt 2: Must fail closed with ErrQuarantined without calling Portal!
	_, err2 := tp.Token(context.Background())
	if err2 == nil {
		t.Fatalf("expected quarantined error, got nil")
	}
	if atomic.LoadInt32(&portalHits) != 1 {
		t.Fatalf("expected portal to be hit exactly once (no replay), got %d", portalHits)
	}
}

func TestParity_DynamicCatalogInventory(t *testing.T) {
	inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
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
				{"id": "anthropic/claude-sonnet-4.6"},
				{"id": "nousresearch/hermes-3-llama-3.1-405b"},
				{"id": "deepseek/deepseek-r1"}
			]
		}`))
	}))
	t.Cleanup(inferenceSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken: "test-token-valid",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_url: %s
oauth_client_id: custom-client-123
oauth_token_file: %s
`, inferenceSrv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 models due to limit, got %d", len(resp.Models))
	}

	expectedCanonical0 := "nous-portal/anthropic/claude-sonnet-4.6"
	expectedNative0 := "anthropic/claude-sonnet-4.6"
	if resp.Models[0].CanonicalModelID != expectedCanonical0 {
		t.Fatalf("expected model 0 canonical ID %q, got %q", expectedCanonical0, resp.Models[0].CanonicalModelID)
	}
	if resp.Models[0].NativeModelID != expectedNative0 {
		t.Fatalf("expected model 0 native ID %q, got %q", expectedNative0, resp.Models[0].NativeModelID)
	}

	expectedCanonical1 := "nous-portal/nousresearch/hermes-3-llama-3.1-405b"
	if resp.Models[1].CanonicalModelID != expectedCanonical1 {
		t.Fatalf("expected model 1 canonical ID %q, got %q", expectedCanonical1, resp.Models[1].CanonicalModelID)
	}
}

func TestParity_Entitlement403Vs401MintRetry(t *testing.T) {
	t.Run("403EntitlementFailsClosedWithoutRefresh", func(t *testing.T) {
		var refreshCalls int32
		tp := &mockTokenProvider{
			token: "valid-tok",
			onForceRefresh: func() (string, error) {
				atomic.AddInt32(&refreshCalls, 1)
				return "refreshed-tok", nil
			},
		}

		inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Subscription required","type":"insufficient_entitlement"}}`))
		}))
		t.Cleanup(inferenceSrv.Close)

		cfg := service.Config{InferenceURL: inferenceSrv.URL}
		_, err := service.ListModels(context.Background(), cfg, tp, inferenceSrv.Client(), 0)
		if err == nil {
			t.Fatalf("expected 403 error, got nil")
		}
		if !strings.Contains(err.Error(), "403") {
			t.Fatalf("expected 403 entitlement in error, got %v", err)
		}
		if atomic.LoadInt32(&refreshCalls) != 0 {
			t.Fatalf("expected 0 refresh calls on 403 entitlement denial, got %d", refreshCalls)
		}
	})

	t.Run("401TransientRetriesOnceWithRefreshedToken", func(t *testing.T) {
		var refreshCalls, inferenceCalls int32
		tp := &mockTokenProvider{
			token: "stale-tok",
			onForceRefresh: func() (string, error) {
				atomic.AddInt32(&refreshCalls, 1)
				return "fresh-tok", nil
			},
		}

		inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callNum := atomic.AddInt32(&inferenceCalls, 1)
			if callNum == 1 {
				if r.Header.Get("Authorization") != "Bearer stale-tok" {
					http.Error(w, "expected stale-tok", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer fresh-tok" {
				http.Error(w, "expected fresh-tok", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"model-recovered"}]}`))
		}))
		t.Cleanup(inferenceSrv.Close)

		cfg := service.Config{InferenceURL: inferenceSrv.URL}
		resp, err := service.ListModels(context.Background(), cfg, tp, inferenceSrv.Client(), 0)
		if err != nil {
			t.Fatalf("expected success after 401 refresh retry, got %v", err)
		}
		if len(resp.Models) != 1 || resp.Models[0].NativeModelID != "model-recovered" {
			t.Fatalf("unexpected models: %+v", resp.Models)
		}
		if atomic.LoadInt32(&refreshCalls) != 1 {
			t.Fatalf("expected 1 refresh call, got %d", refreshCalls)
		}
		if atomic.LoadInt32(&inferenceCalls) != 2 {
			t.Fatalf("expected 2 inference calls (1 fail + 1 retry), got %d", inferenceCalls)
		}
	})
}

func TestParity_HardNegativeNoHermesTagsOrHeaders(t *testing.T) {
	var checkedRequests int
	inspectHandler := func(w http.ResponseWriter, r *http.Request) {
		checkedRequests++
		// Check User-Agent
		ua := r.Header.Get("User-Agent")
		if strings.Contains(strings.ToLower(ua), "hermes") {
			t.Errorf("forbidden hermes User-Agent detected: %q", ua)
		}
		if ua != service.DefaultUserAgent {
			t.Errorf("expected User-Agent %q, got %q", service.DefaultUserAgent, ua)
		}

		// Check all headers for any hermes client tag
		for k, vs := range r.Header {
			lowerK := strings.ToLower(k)
			if strings.Contains(lowerK, "hermes") && lowerK != "x-nous-refresh-token" {
				t.Errorf("forbidden header key containing hermes: %s", k)
			}
			for _, v := range vs {
				lowerV := strings.ToLower(v)
				if strings.Contains(lowerV, "hermes-client") || strings.Contains(lowerV, "client=hermes") {
					t.Errorf("forbidden hermes client tag in header %s: %s", k, v)
				}
			}
		}

		// Send valid dummy response
		if r.URL.Path == "/api/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "valid-jwt",
				"expires_in":   3600,
			})
			return
		}
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"dummy"}]}`))
			return
		}
		http.NotFound(w, r)
	}

	srv := httptest.NewServer(http.HandlerFunc(inspectHandler))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "tok-ref",
	})

	cfgYAML := fmt.Appendf(nil, `
portal_url: %s
inference_url: %s
oauth_client_id: my-third-party-client
oauth_token_file: %s
`, srv.URL, srv.URL, tokenPath)

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

func TestParity_HardNegativeNoHardcodedHermesClientIDInProduction(t *testing.T) {
	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "some-refresh-token",
	})

	// Config omits oauth_client_id
	cfgYAML := fmt.Appendf(nil, `
oauth_token_file: %s
`, tokenPath)

	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail when oauth_client_id is omitted (no hardcoded Hermes client_id allowed), got nil")
	}
	if !strings.Contains(err.Error(), "oauth_client_id is required") {
		t.Fatalf("expected error mentioning oauth_client_id, got: %v", err)
	}
}

type mockTokenProvider struct {
	token          string
	onForceRefresh func() (string, error)
}

func (m *mockTokenProvider) Token(context.Context) (string, error) {
	return m.token, nil
}

func (m *mockTokenProvider) ForceRefresh(context.Context) (string, error) {
	if m.onForceRefresh != nil {
		tok, err := m.onForceRefresh()
		if err == nil {
			m.token = tok
		}
		return tok, err
	}
	return m.token, nil
}
