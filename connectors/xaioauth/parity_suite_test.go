package xaioauth_test

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
	"github.com/aiproxer/aiproxer/connectors/xaioauth/internal/service"
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

func TestOIDCDiscoveryRefreshAndChat(t *testing.T) {
	var discoveryHits, tokenHits, inferenceHits int32
	var capturedAuth, capturedUA string

	var authServerURL string
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			atomic.AddInt32(&discoveryHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         authServerURL,
				"token_endpoint": authServerURL + "/oauth2/token",
			})
		case "/oauth2/token":
			atomic.AddInt32(&tokenHits, 1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "custom-client-456" {
				http.Error(w, "invalid params", http.StatusBadRequest)
				return
			}
			if r.Form.Get("refresh_token") != "initial-refresh-tok" {
				http.Error(w, "invalid refresh token", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "minted-xai-access-token",
				"refresh_token": "rotated-xai-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(authSrv.Close)
	authServerURL = authSrv.URL

	inferenceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inferenceHits, 1)
		capturedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-xai",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "grok hello"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	t.Cleanup(inferenceSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "initial-refresh-tok",
	})

	cfgYAML := fmt.Appendf(nil, `
issuer_url: %s
inference_url: %s
oauth_client_id: custom-client-456
oauth_token_file: %s
model: grok-2
`, authSrv.URL, inferenceSrv.URL, tokenPath)

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	testStream := newTestExecuteStream(context.Background(), "xai-oauth/grok-2", aipapi.OperationOpenAIChatCompletions, "ping grok", true)

	if err := inst.Execute(testStream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&discoveryHits) != 1 {
		t.Fatalf("expected 1 discovery hit, got %d", discoveryHits)
	}
	if atomic.LoadInt32(&tokenHits) != 1 {
		t.Fatalf("expected 1 token hit, got %d", tokenHits)
	}
	if atomic.LoadInt32(&inferenceHits) != 1 {
		t.Fatalf("expected 1 inference hit, got %d", inferenceHits)
	}
	if capturedAuth != "Bearer minted-xai-access-token" {
		t.Fatalf("expected Bearer minted-xai-access-token, got %q", capturedAuth)
	}
	if capturedUA != service.DefaultUserAgent {
		t.Fatalf("expected User-Agent %q, got %q", service.DefaultUserAgent, capturedUA)
	}
}

func TestOAuthQuarantineOnTerminalError(t *testing.T) {
	var tokenHits int32
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token revoked"}`))
	}))
	t.Cleanup(authSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "bad-refresh-tok",
	})

	store := oauthcred.NewFileStore(tokenPath)
	refresher := &service.XAIOAuthRefresher{
		TokenEndpoint: authSrv.URL + "/oauth2/token",
		ClientID:      "custom-client-123",
		HTTPClient:    authSrv.Client(),
	}
	session := oauthcred.NewSession(store, refresher)
	tp := service.NewOAuthTokenProvider(session)

	// Attempt 1: Calls token endpoint, gets invalid_grant, marks quarantined.
	_, err := tp.Token(context.Background())
	if err == nil {
		t.Fatalf("expected error on invalid_grant, got nil")
	}

	rec, err := store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if !rec.Quarantined {
		t.Fatalf("expected store to be quarantined")
	}

	// Attempt 2: Must fail closed with ErrQuarantined without touching server!
	_, err2 := tp.Token(context.Background())
	if err2 == nil {
		t.Fatalf("expected quarantined error, got nil")
	}
	if atomic.LoadInt32(&tokenHits) != 1 {
		t.Fatalf("expected token endpoint hit exactly once (no replay), got %d", tokenHits)
	}
}

func TestDynamicCatalogInventory(t *testing.T) {
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
				{"id": "grok-2"},
				{"id": "grok-2-mini"}
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

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Models))
	}

	if resp.Models[0].CanonicalModelID != "xai-oauth/grok-2" || resp.Models[0].NativeModelID != "grok-2" {
		t.Fatalf("unexpected model 0: %+v", resp.Models[0])
	}
	if resp.Models[1].CanonicalModelID != "xai-oauth/grok-2-mini" || resp.Models[1].NativeModelID != "grok-2-mini" {
		t.Fatalf("unexpected model 1: %+v", resp.Models[1])
	}
}

func TestEntitlement403Vs401MintRetry(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"error":{"message":"Subscription tier not authorized for API","type":"insufficient_entitlement"}}`))
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
			_, _ = w.Write([]byte(`{"data":[{"id":"grok-recovered"}]}`))
		}))
		t.Cleanup(inferenceSrv.Close)

		cfg := service.Config{InferenceURL: inferenceSrv.URL}
		resp, err := service.ListModels(context.Background(), cfg, tp, inferenceSrv.Client(), 0)
		if err != nil {
			t.Fatalf("expected success after 401 refresh retry, got %v", err)
		}
		if len(resp.Models) != 1 || resp.Models[0].NativeModelID != "grok-recovered" {
			t.Fatalf("unexpected models: %+v", resp.Models)
		}
		if atomic.LoadInt32(&refreshCalls) != 1 {
			t.Fatalf("expected 1 refresh call, got %d", refreshCalls)
		}
		if atomic.LoadInt32(&inferenceCalls) != 2 {
			t.Fatalf("expected 2 inference calls, got %d", inferenceCalls)
		}
	})
}

func TestHardNegativeNoResponsesOperation(t *testing.T) {
	svc := service.New(service.WithTokenProvider(service.NewStaticTokenProvider("dummy-tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("inference_url: http://127.0.0.1:9999\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("dummy")}},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	testStream := newTestExecuteStream(context.Background(), "xai-oauth/grok-2", aipapi.OperationOpenAIResponses, "hi", true)
	err = inst.Execute(testStream)
	if err == nil {
		t.Fatalf("expected Execute to fail for responses operation, got nil")
	}
	if !strings.Contains(err.Error(), "responses operations are not supported") {
		t.Fatalf("expected error mentioning responses not supported, got: %v", err)
	}
}

func TestHardNegativeNoHermesOrGrokCLITags(t *testing.T) {
	var checkedRequests int
	inspectHandler := func(w http.ResponseWriter, r *http.Request) {
		checkedRequests++
		ua := r.Header.Get("User-Agent")
		if strings.Contains(strings.ToLower(ua), "hermes") || strings.Contains(strings.ToLower(ua), "grok-cli") {
			t.Errorf("forbidden User-Agent detected: %q", ua)
		}
		if ua != service.DefaultUserAgent {
			t.Errorf("expected User-Agent %q, got %q", service.DefaultUserAgent, ua)
		}

		for k, vs := range r.Header {
			lowerK := strings.ToLower(k)
			if strings.Contains(lowerK, "hermes") || strings.Contains(lowerK, "grok-cli") {
				t.Errorf("forbidden header key: %s", k)
			}
			for _, v := range vs {
				lowerV := strings.ToLower(v)
				if strings.Contains(lowerV, "hermes") || strings.Contains(lowerV, "grok-cli") {
					t.Errorf("forbidden tag in header %s: %s", k, v)
				}
			}
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
		AccessToken: "test-tok",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_url: %s
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

func TestDistinctKindFromCatalogXAI(t *testing.T) {
	if service.FactoryKind == "xai" {
		t.Fatalf("FactoryKind must be distinct from catalog xai profile")
	}
	if service.FactoryKind != "xai-oauth" {
		t.Fatalf("expected FactoryKind xai-oauth, got %q", service.FactoryKind)
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
