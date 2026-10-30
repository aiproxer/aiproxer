package qwenoauth_test

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
	"github.com/aiproxer/aiproxer/connectors/qwenoauth/internal/service"
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
	var tokenHits int32
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&tokenHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "Refresh token is invalid or expired",
		})
	}))
	t.Cleanup(authSrv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "bad-refresh-tok",
	})

	store := oauthcred.NewFileStore(tokenPath)
	refresher := &service.QwenOAuthRefresher{
		TokenURL:   authSrv.URL + "/api/v1/oauth2/token",
		ClientID:   "test-client-id",
		HTTPClient: authSrv.Client(),
	}
	session := oauthcred.NewSession(store, refresher, oauthcred.WithSkew(service.TokenRefreshSkew))
	tp := service.NewOAuthTokenProvider(session)

	// Attempt 1: Refresh fails terminally and sets quarantine
	_, err := tp.Token(context.Background())
	if err == nil {
		t.Fatalf("expected error on invalid_grant, got nil")
	}
	if atomic.LoadInt32(&tokenHits) != 1 {
		t.Fatalf("expected token endpoint hit once, got %d", tokenHits)
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

func TestChatExecuteWireAdaptations(t *testing.T) {
	var capturedUA string
	var capturedAuth string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		capturedAuth = r.Header.Get("Authorization")

		switch r.URL.Path {
		case "/api/v1/oauth2/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "minted-qwen-token",
				"refresh_token": "rotated-qwen-refresh",
				"expires_in":    3600,
			})
			return

		case "/chat/completions":
			bodyBytes, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(bodyBytes, &capturedBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "chatcmpl-test",
				"object": "chat.completion",
				"choices": [{
					"index": 0,
					"message": {"role": "assistant", "content": "hello world"},
					"finish_reason": "stop"
				}]
			}`))
			return

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		RefreshToken: "initial-qwen-refresh",
	})

	cfgYAML := fmt.Appendf(nil, `
inference_url: %s
token_url: %s/api/v1/oauth2/token
oauth_client_id: custom-qwen-client
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

	sysText := "System instruction here"
	userText := "User prompt"
	testMsgs := []backendplugin.Message{
		{
			Role: backendplugin.RoleSystem,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindText, Text: &sysText},
			},
		},
		{
			Role: backendplugin.RoleUser,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindText, Text: &userText},
			},
		},
	}

	stream := newTestExecuteStream(context.Background(), "qwen-oauth/qwen-coder-plus", aipapi.OperationOpenAIChatCompletions, "qwen_sess_42", testMsgs, true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if capturedUA != service.DefaultUserAgent {
		t.Fatalf("expected User-Agent %q, got %q", service.DefaultUserAgent, capturedUA)
	}
	if capturedAuth != "Bearer minted-qwen-token" {
		t.Fatalf("expected Bearer minted-qwen-token, got %q", capturedAuth)
	}

	// Verify the 5 wire JSON adaptations:
	// 1 & 3: System message content normalized to list of typed parts, last part has cache_control ephemeral
	msgs, ok := capturedBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages in wire body, got %#v", capturedBody["messages"])
	}
	sysMsg, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any for sysMsg, got %#v", msgs[0])
	}
	sysParts, ok := sysMsg["content"].([]any)
	if !ok || len(sysParts) != 1 {
		t.Fatalf("expected 1 system part, got %#v", sysMsg["content"])
	}
	firstPart, ok := sysParts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any for firstPart, got %#v", sysParts[0])
	}
	if firstPart["type"] != "text" || firstPart["text"] != "System instruction here" {
		t.Fatalf("unexpected system part content: %#v", firstPart)
	}
	cacheControl, ok := firstPart["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected cache_control {type: ephemeral} on system last part, got %#v", firstPart["cache_control"])
	}

	// 4: vl_high_resolution_images is true
	if capturedBody["vl_high_resolution_images"] != true {
		t.Fatalf("expected vl_high_resolution_images: true, got %#v", capturedBody["vl_high_resolution_images"])
	}

	// 5: top-level metadata contains sessionId and promptId (camelCase), not session_id or unrelated safe metadata
	meta, ok := capturedBody["metadata"].(map[string]any)
	if !ok || meta["sessionId"] != "qwen_sess_42" {
		t.Fatalf("expected top-level metadata sessionId 'qwen_sess_42', got %#v", capturedBody["metadata"])
	}
	if meta["promptId"] != "req-1" {
		t.Fatalf("expected top-level metadata promptId 'req-1', got %#v", meta["promptId"])
	}
	if _, hasSnake := meta["session_id"]; hasSnake {
		t.Fatalf("metadata must NOT contain snake_case session_id: %#v", meta)
	}
	if _, hasApp := meta["app"]; hasApp {
		t.Fatalf("metadata must NOT contain unrelated SafeMetadata: %#v", meta)
	}
}

func TestDynamicCatalogInventory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				{"id": "qwen-coder-plus"},
				{"id": "qwen-max"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
		AccessToken: "test-token-valid",
		Expiry:      time.Now().Add(1 * time.Hour),
	})

	cfgYAML := fmt.Appendf(nil, `
inference_url: %s
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
		t.Fatalf("expected 2 models, got %d", len(resp.Models))
	}
	if resp.Models[0].CanonicalModelID != "qwen-oauth/qwen-coder-plus" || resp.Models[0].NativeModelID != "qwen-coder-plus" {
		t.Fatalf("unexpected model 0: %+v", resp.Models[0])
	}
	if resp.Models[1].CanonicalModelID != "qwen-oauth/qwen-max" || resp.Models[1].NativeModelID != "qwen-max" {
		t.Fatalf("unexpected model 1: %+v", resp.Models[1])
	}
}

func TestEntitlement403Vs401MintRetry(t *testing.T) {
	t.Run("403EntitlementFailsClosedWithoutRefresh", func(t *testing.T) {
		var tokenHits int32
		var chatHits atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/oauth2/token":
				atomic.AddInt32(&tokenHits, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "access-token",
					"refresh_token": "refresh-token",
					"expires_in":    3600,
				})
			case "/chat/completions":
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
inference_url: %s
token_url: %s/api/v1/oauth2/token
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
		stream := newTestExecuteStream(context.Background(), "qwen-oauth/qwen-coder-plus", aipapi.OperationOpenAIChatCompletions, "", msgs, true)
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
			case "/api/v1/oauth2/token":
				atomic.AddInt32(&tokenHits, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "minted-new-token",
					"refresh_token": "rotated-refresh-token",
					"expires_in":    3600,
				})
			case "/chat/completions":
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
					"id": "chatcmpl-retried",
					"object": "chat.completion",
					"choices": [{"index": 0, "message": {"role": "assistant", "content": "retried success"}, "finish_reason": "stop"}]
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
inference_url: %s
token_url: %s/api/v1/oauth2/token
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
		stream := newTestExecuteStream(context.Background(), "qwen-oauth/qwen-coder-plus", aipapi.OperationOpenAIChatCompletions, "", msgs, true)
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

func TestHardNegativeNoResponsesOperation(t *testing.T) {
	svc := service.NewProduction()
	cfgYAML := []byte("inference_url: https://portal.qwen.ai/v1\n")
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_key": []byte("static-tok")},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	userText := "hi"
	msgs := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &userText}}}}
	stream := newTestExecuteStream(context.Background(), "qwen-oauth/qwen-coder-plus", aipapi.OperationOpenAIResponses, "", msgs, false)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected error rejecting OpenAI Responses operation, got nil")
	}
	if !strings.Contains(err.Error(), "responses operations are not supported") {
		t.Fatalf("expected responses rejection error, got %v", err)
	}
}

func TestHardNegativeNoHermesOrQwenCLITags(t *testing.T) {
	var checkedRequests int
	inspectHandler := func(w http.ResponseWriter, r *http.Request) {
		checkedRequests++
		ua := r.Header.Get("User-Agent")
		if strings.Contains(strings.ToLower(ua), "hermes") || strings.Contains(strings.ToLower(ua), "qwen-cli") {
			t.Errorf("forbidden client identity in User-Agent: %s", ua)
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				lowerV := strings.ToLower(v)
				if strings.Contains(lowerV, "hermes") || strings.Contains(lowerV, "qwen-cli") {
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

	// Missing oauth_client_id in config and secrets
	cfgYAML := fmt.Appendf(nil, `
inference_url: https://portal.qwen.ai/v1
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

func TestDistinctFromDashScopeAndAlibaba(t *testing.T) {
	if service.FactoryKind == "alibaba" || service.FactoryKind == "dashscope" {
		t.Fatalf("qwen-oauth must be distinct from alibaba and dashscope, got %q", service.FactoryKind)
	}
	if service.PluginID == "com.aiproxer.backend.alibaba" {
		t.Fatalf("qwen-oauth must be distinct plugin ID, got %q", service.PluginID)
	}
}
