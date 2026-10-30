package gitlabduo_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/aiproxer/aiproxer/connectors/gitlabduo/internal/service"
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

// 1. PAT Configure -> ListModels / Execute
func TestParity_PAT_Configure_ListModels_Execute(t *testing.T) {
	t.Parallel()

	var directAccessCalled atomic.Int32
	var messagesCalled atomic.Int32
	var graphqlCalled atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/ai/third_party_agents/direct_access":
			directAccessCalled.Add(1)
			if r.Header.Get("Authorization") != "Bearer glpat-secret-123" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var reqBody struct {
				FeatureFlags struct {
					DuoAgentPlatform            bool `json:"duo_agent_platform"`
					DuoAgentPlatformAgenticChat bool `json:"duo_agent_platform_agentic_chat"`
				} `json:"feature_flags"`
			}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			if !reqBody.FeatureFlags.DuoAgentPlatform || !reqBody.FeatureFlags.DuoAgentPlatformAgenticChat {
				http.Error(w, "missing required feature_flags in direct_access", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated) // 201 Created per Grape API spec
			_, _ = w.Write([]byte(`{"token":"direct-tok-xyz","headers":{"x-gitlab-instance-id":"inst-42"}}`))

		case "/api/graphql":
			graphqlCalled.Add(1)
			if r.Header.Get("Authorization") != "Bearer glpat-secret-123" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": {
					"aiChatAvailableModels": {
						"defaultModel": {"name":"Claude 3.5 Sonnet","ref":"claude-3-5-sonnet"},
						"selectableModels": [{"name":"Claude 3.7 Sonnet","ref":"claude-3-7-sonnet"}]
					}
				}
			}`))

		case "/ai/v1/proxy/anthropic/v1/messages":
			messagesCalled.Add(1)
			if r.Header.Get("Authorization") != "Bearer direct-tok-xyz" {
				http.Error(w, "bad direct access token", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("x-gitlab-instance-id") != "inst-42" {
				http.Error(w, "missing direct access header", http.StatusBadRequest)
				return
			}
			if r.Header.Get("anthropic-beta") != "context-1m-2025-08-07" {
				http.Error(w, "missing anthropic-beta header", http.StatusBadRequest)
				return
			}
			if !strings.Contains(r.Header.Get("User-Agent"), "aiproxer") {
				http.Error(w, "untruthful User-Agent", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg-1",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"GitLab Duo inference ok"}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":12,"output_tokens":6}
			}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\nai_gateway_url: %s\nroot_namespace_id: \"42\"\n", srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-secret-123")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// ListModels
	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if graphqlCalled.Load() != 1 {
		t.Fatalf("expected graphqlCalled=1, got %d", graphqlCalled.Load())
	}

	modelIDs := make(map[string]bool)
	for _, m := range resp.Models {
		modelIDs[m.NativeModelID] = true
	}
	// Discovery is authoritative: exactly the GraphQL-enumerated models are
	// advertised, with no static union.
	if !modelIDs["claude-3-5-sonnet"] {
		t.Fatalf("missing dynamic model claude-3-5-sonnet: %+v", modelIDs)
	}
	if !modelIDs["claude-3-7-sonnet"] {
		t.Fatalf("missing dynamic model claude-3-7-sonnet: %+v", modelIDs)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected exactly 2 enumerated models, got %d: %+v", len(resp.Models), modelIDs)
	}
	for _, staticID := range []string{service.ModelHaiku45, service.ModelSonnet45, service.ModelOpus45} {
		if modelIDs[staticID] {
			t.Fatalf("non-enumerated static model %q must not be advertised alongside discovery: %+v", staticID, modelIDs)
		}
	}
	if modelIDs["duo-workflow-claude-3-7-sonnet"] {
		t.Fatalf("model ref must not be prefixed with duo-workflow: %+v", modelIDs)
	}

	// Execute
	stream := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Hello GitLab Duo", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if directAccessCalled.Load() != 1 {
		t.Fatalf("expected directAccessCalled=1, got %d", directAccessCalled.Load())
	}
	if messagesCalled.Load() != 1 {
		t.Fatalf("expected messagesCalled=1, got %d", messagesCalled.Load())
	}

	var foundText bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == aipapi.EventTextDelta {
			if f.Event.Delta != nil && *f.Event.Delta == "GitLab Duo inference ok" {
				foundText = true
			}
		}
	}
	if !foundText {
		t.Fatalf("did not receive expected text delta; frames=%+v", stream.outbox)
	}
}

// 2. OAuth token refresh + quarantine (oauthcred)
func TestParity_OAuth_TokenRefresh_And_Quarantine(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	tokFile := filepath.Join(tmpDir, "gitlab_oauth.json")

	// Create initial token file with expired access token and valid refresh token
	store := oauthcred.NewFileStore(tokFile)
	initRec := oauthcred.TokenRecord{
		AccessToken:  "expired-access-token",
		RefreshToken: "valid-refresh-token",
		Expiry:       time.Now().Add(-10 * time.Minute),
	}
	if err := store.Save(initRec); err != nil {
		t.Fatalf("save initial token record: %v", err)
	}

	var refreshCount atomic.Int32
	var directAccessCalls atomic.Int32
	var refreshShouldFail atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshCount.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" {
				http.Error(w, "invalid grant_type", http.StatusBadRequest)
				return
			}
			if refreshShouldFail.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token revoked"}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"access_token": "fresh-access-token-999",
				"token_type": "bearer",
				"refresh_token": "next-refresh-token",
				"expires_in": 3600
			}`))

		case "/api/v4/ai/third_party_agents/direct_access":
			directAccessCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer fresh-access-token-999" {
				http.Error(w, "unauthorized with non-fresh token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"direct-tok-oauth","headers":{}}`))

		case "/ai/v1/proxy/anthropic/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg-oauth",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"oauth ok"}],
				"stop_reason":"end_turn"
			}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\nai_gateway_url: %s\noauth_token_file: %s\noauth_client_id: test-client\n", srv.URL, srv.URL, filepath.ToSlash(tokFile))

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// 1. Proactive refresh succeeds
	stream := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Test OAuth", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if refreshCount.Load() != 1 {
		t.Fatalf("expected refreshCount=1, got %d", refreshCount.Load())
	}
	if directAccessCalls.Load() != 1 {
		t.Fatalf("expected directAccessCalls=1, got %d", directAccessCalls.Load())
	}

	// Verify token file was updated on disk
	updated, err := store.Load()
	if err != nil {
		t.Fatalf("load updated token: %v", err)
	}
	if updated.AccessToken != "fresh-access-token-999" {
		t.Fatalf("expected fresh-access-token-999, got %q", updated.AccessToken)
	}
	if updated.RefreshToken != "next-refresh-token" {
		t.Fatalf("expected next-refresh-token, got %q", updated.RefreshToken)
	}

	// 2. Terminal failure causes quarantine
	refreshShouldFail.Store(true)
	// Force expiration in store so next call triggers refresh
	updated.Expiry = time.Now().Add(-1 * time.Hour)
	if err := store.Save(updated); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	// Reconfigure to get a fresh session reading from store
	inst2, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
	})
	if err != nil {
		t.Fatalf("Configure 2 failed: %v", err)
	}

	stream2 := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Test Quarantine", true)
	err = inst2.Execute(stream2)
	if err == nil {
		t.Fatal("expected error on terminal refresh failure")
	}

	// Verify record is now quarantined
	quarantinedRec, err := store.Load()
	if err != nil {
		t.Fatalf("load quarantined token: %v", err)
	}
	if !quarantinedRec.Quarantined {
		t.Fatalf("expected token record to be quarantined, got: %+v", quarantinedRec)
	}

	// 3. Subsequent calls fail immediately due to quarantine without hitting refresh
	refreshBefore := refreshCount.Load()
	stream3 := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Test Quarantined Call", true)
	err3 := inst2.Execute(stream3)
	if err3 == nil || !strings.Contains(err3.Error(), "quarantined") {
		t.Fatalf("expected quarantined error, got %v", err3)
	}
	if refreshCount.Load() != refreshBefore {
		t.Fatalf("quarantined session should not call refresh endpoint again; called %d vs %d", refreshCount.Load(), refreshBefore)
	}
}

// 3. Self-managed instance_url + required oauth_client_id
func TestParity_SelfManaged_OAuthClientIDRequired(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	tokFile := filepath.Join(tmpDir, "token.json")
	_ = os.WriteFile(tokFile, []byte(`{"access_token":"x","refresh_token":"y"}`), 0o600)

	svc := service.NewProduction()

	// Missing client_id on self-managed instance
	cfgYAMLMissing := fmt.Appendf(nil, "instance_url: https://gitlab.corp.internal\noauth_token_file: %s\n", filepath.ToSlash(tokFile))
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAMLMissing,
	})
	if err == nil || !strings.Contains(err.Error(), "oauth_client_id is required for self-managed") {
		t.Fatalf("expected self-managed oauth_client_id required error, got: %v", err)
	}

	// Providing client_id on self-managed instance succeeds
	cfgYAMLPresent := fmt.Appendf(nil, "instance_url: https://gitlab.corp.internal\noauth_token_file: %s\noauth_client_id: my-corp-app\n", filepath.ToSlash(tokFile))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAMLPresent,
	})
	if err != nil {
		t.Fatalf("Configure with oauth_client_id on self-managed instance failed: %v", err)
	}
	if inst == nil {
		t.Fatal("expected non-nil instance")
	}
}

// 4. Optional ai_gateway_url used when set
func TestParity_Optional_AIGatewayURL(t *testing.T) {
	t.Parallel()

	var gatewayCalled atomic.Int32
	var instanceCalled atomic.Int32

	instanceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		instanceCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"direct-tok","headers":{}}`))
	}))
	t.Cleanup(instanceSrv.Close)

	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalled.Add(1)
		if r.URL.Path != "/ai/v1/proxy/anthropic/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"gateway ok"}]}`))
	}))
	t.Cleanup(gatewaySrv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\nai_gateway_url: %s\n", instanceSrv.URL, gatewaySrv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Test Gateway", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if instanceCalled.Load() != 1 {
		t.Fatalf("expected instance direct_access called 1, got %d", instanceCalled.Load())
	}
	if gatewayCalled.Load() != 1 {
		t.Fatalf("expected custom gateway called 1, got %d", gatewayCalled.Load())
	}
}

// 5. Dynamic model discovery is authoritative and re-discovers on each call:
// successful enumeration publishes exactly the discovered models (no static
// union), and no lifetime in-memory cache serves stale rows within a generation.
func TestParity_Dynamic_ModelDiscovery_RediscoversEachCall(t *testing.T) {
	t.Parallel()

	var graphqlQueries atomic.Int32
	var currentWorkflowModel atomic.Value
	currentWorkflowModel.Store("claude-sonnet-4-6")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/graphql" {
			graphqlQueries.Add(1)
			modelRef, _ := currentWorkflowModel.Load().(string)
			resp := map[string]any{
				"data": map[string]any{
					"aiChatAvailableModels": map[string]any{
						"selectableModels": []map[string]any{
							{"name": "Dynamic Model", "ref": modelRef},
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\nroot_namespace_id: \"99\"\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	// Generation 1
	inst1, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure Gen1 failed: %v", err)
	}

	res1, err := inst1.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels Gen1 failed: %v", err)
	}
	if graphqlQueries.Load() != 1 {
		t.Fatalf("expected 1 query for Gen1, got %d", graphqlQueries.Load())
	}

	hasWorkflow1 := false
	for _, m := range res1.Models {
		if m.NativeModelID == "claude-sonnet-4-6" {
			hasWorkflow1 = true
		}
		if m.NativeModelID == service.ModelHaiku45 ||
			m.NativeModelID == service.ModelSonnet45 ||
			m.NativeModelID == service.ModelOpus45 {
			t.Fatalf("non-enumerated static model %q must not be advertised alongside discovery, got: %+v", m.NativeModelID, res1.Models)
		}
	}
	if !hasWorkflow1 {
		t.Fatalf("expected claude-sonnet-4-6 in Gen1, got: %+v", res1.Models)
	}
	if len(res1.Models) != 1 {
		t.Fatalf("expected exactly 1 enumerated model in Gen1, got %d: %+v", len(res1.Models), res1.Models)
	}

	// Second call on Gen1 must re-discover (no lifetime in-memory cache).
	_, err = inst1.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels Gen1 repeat failed: %v", err)
	}
	if graphqlQueries.Load() != 2 {
		t.Fatalf("expected 2 queries after repeat on Gen1 (re-discovery, no lifetime cache), got %d", graphqlQueries.Load())
	}

	// Generation 2: new Configure call
	currentWorkflowModel.Store("claude-opus-5")
	inst2, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure Gen2 failed: %v", err)
	}

	res2, err := inst2.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels Gen2 failed: %v", err)
	}
	if graphqlQueries.Load() != 3 {
		t.Fatalf("expected 3 queries after Gen2 (re-discovery on every call), got %d", graphqlQueries.Load())
	}

	hasWorkflow2 := false
	for _, m := range res2.Models {
		if m.NativeModelID == "claude-opus-5" {
			hasWorkflow2 = true
		}
		if m.NativeModelID == service.ModelHaiku45 ||
			m.NativeModelID == service.ModelSonnet45 ||
			m.NativeModelID == service.ModelOpus45 {
			t.Fatalf("non-enumerated static model %q must not be advertised alongside discovery, got: %+v", m.NativeModelID, res2.Models)
		}
	}
	if !hasWorkflow2 {
		t.Fatalf("expected claude-opus-5 in Gen2, got: %+v", res2.Models)
	}
	if len(res2.Models) != 1 {
		t.Fatalf("expected exactly 1 enumerated model in Gen2, got %d: %+v", len(res2.Models), res2.Models)
	}
}

// 6. Hard-negative: no ACP; no GitLab repository-management tool execution
func TestParity_HardNegative_NoACP_NoRepoTools(t *testing.T) {
	t.Parallel()

	svc := service.NewProduction()
	cfgYAML := []byte("instance_url: https://gitlab.example.com\n")
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Rejection of ACP operation
	streamACP := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, "agent_control", "Hi", true)
	err = inst.Execute(streamACP)
	if err == nil || !strings.Contains(err.Error(), "ACP is not supported") {
		t.Fatalf("expected ACP not supported error, got: %v", err)
	}

	// Rejection of repository management tools
	msg := "test"
	invWithTool := backendplugin.Invocation{
		RequestID:        "req-tool",
		AttemptID:        "att-tool",
		ALegID:           "al-tool",
		BLegID:           "bl-tool",
		CanonicalModelID: service.FactoryKind + "/" + service.ModelSonnet45,
		NativeModelID:    service.ModelSonnet45,
		Operation:        string(aipapi.OperationOpenAIChatCompletions),
		DeliveryMode:     string(aipapi.DeliveryModeNonStreaming),
		TransportMode:    string(aipapi.TransportModeNonStreaming),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
		Tools: []backendplugin.ToolDef{{
			Name: "gitlab_mr_create",
		}},
	}
	streamTool := &memStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &invWithTool},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
	err = inst.Execute(streamTool)
	if err == nil || !strings.Contains(err.Error(), "repository tools are out of scope") {
		t.Fatalf("expected repository tools out of scope error, got: %v", err)
	}
}

// 7. Fail closed on non-200 inventory
func TestParity_FailClosed_Non200_Inventory(t *testing.T) {
	t.Parallel()

	for _, statusCode := range []int{http.StatusInternalServerError, http.StatusUnauthorized, http.StatusForbidden} {
		code := statusCode
		t.Run(fmt.Sprintf("HTTP_%d", code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "upstream failure", code)
			}))
			t.Cleanup(srv.Close)

			svc := service.NewProduction()
			cfgYAML := fmt.Appendf(nil, "instance_url: %s\nroot_namespace_id: \"1\"\n", srv.URL)
			secrets := backendplugin.SecretBundle{
				Values: map[string][]byte{"pat": []byte("glpat-test")},
			}

			inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				ConfigYAML: cfgYAML,
				Secrets:    secrets,
			})
			if err != nil {
				t.Fatalf("Configure failed: %v", err)
			}

			_, err = inst.ListModels(context.Background(), 0)
			if err == nil {
				t.Fatalf("expected ListModels to fail closed on status %d, got nil error", code)
			}
		})
	}
}

// 8. Entitlement 403 on direct_access (no token refresh loop)
func TestParity_Entitlement_403_NoRefreshLoop(t *testing.T) {
	t.Parallel()

	var directAccessCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/ai/third_party_agents/direct_access":
			directAccessCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"GitLab Duo requires GitLab Ultimate with Duo Enterprise add-on"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Hi", true)
	err = inst.Execute(stream)
	if err == nil || !strings.Contains(err.Error(), "access denied (403)") {
		t.Fatalf("expected entitlement 403 error, got: %v", err)
	}
	if directAccessCalls.Load() != 1 {
		t.Fatalf("expected directAccessCalls=1 (no retry loop on 403), got %d", directAccessCalls.Load())
	}
}

// 9. No namespace config -> ListModels succeeds with only static models and zero GraphQL calls
func TestParity_NoNamespaceConfig_StaticModelsOnly_ZeroGraphQL(t *testing.T) {
	t.Parallel()

	var graphqlCalled atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/graphql" {
			graphqlCalled.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if graphqlCalled.Load() != 0 {
		t.Fatalf("expected 0 GraphQL calls when no namespace is configured, got %d", graphqlCalled.Load())
	}
	if len(resp.Models) != 3 {
		t.Fatalf("expected exactly 3 static models, got %d: %+v", len(resp.Models), resp.Models)
	}
	for _, m := range resp.Models {
		if m.NativeModelID != service.ModelHaiku45 &&
			m.NativeModelID != service.ModelSonnet45 &&
			m.NativeModelID != service.ModelOpus45 {
			t.Fatalf("unexpected model: %s", m.NativeModelID)
		}
	}
}

// 10. project_path resolution and HTTP 500 fail-closed
func TestParity_ProjectPath_Resolution_And_HTTP500_FailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("success_resolves_namespace_and_queries_graphql", func(t *testing.T) {
		var projectCalled atomic.Int32
		var graphqlCalled atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v4/projects/gitlab-org/gitlab" || r.URL.EscapedPath() == "/api/v4/projects/gitlab-org%2Fgitlab" {
				projectCalled.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":100,"namespace":{"id":77,"name":"gitlab-org"}}`))
				return
			}

			switch r.URL.Path {

			case "/api/graphql":
				graphqlCalled.Add(1)
				var req struct {
					Variables struct {
						RootNamespaceID string `json:"rootNamespaceId"`
					} `json:"variables"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Variables.RootNamespaceID != "gid://gitlab/Group/77" {
					http.Error(w, "bad rootNamespaceId", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"aiChatAvailableModels":{"selectableModels":[{"name":"Claude 3.7","ref":"claude-3-7-sonnet"}]}}}`))

			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)

		svc := service.NewProduction()
		cfgYAML := fmt.Appendf(nil, "instance_url: %s\nproject_path: gitlab-org/gitlab\n", srv.URL)
		secrets := backendplugin.SecretBundle{
			Values: map[string][]byte{"pat": []byte("glpat-test")},
		}

		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			ConfigYAML: cfgYAML,
			Secrets:    secrets,
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		resp, err := inst.ListModels(context.Background(), 0)
		if err != nil {
			t.Fatalf("ListModels failed: %v", err)
		}
		if projectCalled.Load() != 1 {
			t.Fatalf("expected projectCalled=1, got %d", projectCalled.Load())
		}
		if graphqlCalled.Load() != 1 {
			t.Fatalf("expected graphqlCalled=1, got %d", graphqlCalled.Load())
		}
		found := false
		for _, m := range resp.Models {
			if m.NativeModelID == "claude-3-7-sonnet" {
				found = true
			}
			if m.NativeModelID == service.ModelHaiku45 ||
				m.NativeModelID == service.ModelSonnet45 ||
				m.NativeModelID == service.ModelOpus45 {
				t.Fatalf("non-enumerated static model %q must not be advertised alongside discovery, got %+v", m.NativeModelID, resp.Models)
			}
		}
		if !found {
			t.Fatalf("expected discovered model claude-3-7-sonnet, got %+v", resp.Models)
		}
		if len(resp.Models) != 1 {
			t.Fatalf("expected exactly 1 enumerated model, got %d: %+v", len(resp.Models), resp.Models)
		}
	})

	t.Run("http_500_fails_closed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v4/projects/") {
				http.Error(w, "database unavailable", http.StatusInternalServerError)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		svc := service.NewProduction()
		cfgYAML := fmt.Appendf(nil, "instance_url: %s\nproject_path: broken/project\n", srv.URL)
		secrets := backendplugin.SecretBundle{
			Values: map[string][]byte{"pat": []byte("glpat-test")},
		}

		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			ConfigYAML: cfgYAML,
			Secrets:    secrets,
		})
		if err != nil {
			t.Fatalf("Configure failed: %v", err)
		}

		_, err = inst.ListModels(context.Background(), 0)
		if err == nil {
			t.Fatal("expected ListModels to fail closed when project API returns 500, got nil")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected error mentioning status 500, got: %v", err)
		}
	})
}

// 10b. Caller cancellation is preserved without fallback.
func TestParity_ListModels_ContextCanceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\nroot_namespace_id: \"99\"\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = inst.ListModels(ctx, 0)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
}

// 11. direct_access 201 Created + dynamic base_url and expires_at payload
func TestParity_DirectAccess_Payload_BaseURL_And_ExpiresAt(t *testing.T) {
	t.Parallel()

	var customGatewayCalled atomic.Int32

	customGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ai/v1/proxy/anthropic/v1/messages" {
			customGatewayCalled.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg-dyn",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"custom gateway ok"}]
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(customGateway.Close)

	instanceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/ai/third_party_agents/direct_access" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated) // 201 Created
			_, _ = fmt.Fprintf(w, `{
				"token":"dyn-tok",
				"base_url":"%s",
				"expires_at":"2030-01-01T00:00:00Z"
			}`, customGateway.URL)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(instanceSrv.Close)

	svc := service.NewProduction()
	// No ai_gateway_url configured in YAML -> should use base_url returned by direct_access
	cfgYAML := fmt.Appendf(nil, "instance_url: %s\n", instanceSrv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"pat": []byte("glpat-test")},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		ConfigYAML: cfgYAML,
		Secrets:    secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), service.FactoryKind+"/"+service.ModelSonnet45, aipapi.OperationOpenAIChatCompletions, "Test dynamic base_url", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if customGatewayCalled.Load() != 1 {
		t.Fatalf("expected custom gateway returned in base_url to be called once, got %d", customGatewayCalled.Load())
	}
}
