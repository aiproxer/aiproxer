package infomaniak_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/infomaniak/internal/service"
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

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation) *memStream {
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID:        "r1",
		AttemptID:        "a1",
		ALegID:           "al1",
		BLegID:           "bl1",
		CanonicalModelID: modelID,
		Operation:        string(op),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{{
			Kind:       backendplugin.ClientFrameStart,
			InstanceID: "inst",
			Invocation: &inv,
		}},
	}
}

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()

	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}

	if desc.PluginID != service.PluginID {
		t.Fatalf("plugin_id=%s want %s", desc.PluginID, service.PluginID)
	}
	if desc.ProtocolMajor != 1 {
		t.Fatalf("major=%d want 1", desc.ProtocolMajor)
	}
	if desc.ProtocolMinor < backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("minor=%d must be at least %d", desc.ProtocolMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}

	var found bool
	for _, f := range desc.Factories {
		if f.Kind == service.FactoryKind {
			found = true
			if f.DisplayName != service.DisplayName {
				t.Fatalf("display_name=%s want %s", f.DisplayName, service.DisplayName)
			}
			if !f.SupportsDynamicInventory {
				t.Fatal("SupportsDynamicInventory must be true")
			}
			if !f.StaticCapabilities.Streaming {
				t.Fatal("StaticCapabilities.Streaming must be true")
			}
			if !f.TransportCapabilities.Cancellation {
				t.Fatal("TransportCapabilities.Cancellation must be true")
			}
			if !f.TransportCapabilities.BidirectionalStream {
				t.Fatal("TransportCapabilities.BidirectionalStream must be true")
			}
		}
	}
	if !found {
		t.Fatalf("factory kind %s not found in descriptor", service.FactoryKind)
	}
}

func TestConfigure_RejectsMissingInputs(t *testing.T) {
	t.Parallel()

	svc := service.New()
	ctx := context.Background()

	// 1. Missing product_id
	_, err := svc.Configure(ctx, backendplugin.ConfigureRequest{
		ConfigYAML: []byte(""),
		Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test")}},
	})
	if err == nil {
		t.Fatal("expected product_id error, got nil")
	}

	// 2. Missing secret
	_, err = svc.Configure(ctx, backendplugin.ConfigureRequest{
		ConfigYAML: []byte("product_id: 103281\n"),
		Secrets:    backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatal("expected secret error, got nil")
	}

	// 3. Non-numeric product_id
	for _, badID := range []string{"abc", "103a281", "-5", "0", ""} {
		_, err = svc.Configure(ctx, backendplugin.ConfigureRequest{
			ConfigYAML: fmt.Appendf(nil, "product_id: %q\n", badID),
			Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test")}},
		})
		if err == nil {
			t.Fatalf("expected error for non-numeric/invalid product_id %q, got nil", badID)
		}
	}
}

func TestConfigure_RejectsLiteralSecretsInYAML(t *testing.T) {
	t.Parallel()

	svc := service.New()
	ctx := context.Background()

	forbiddenKeys := []string{"api_key", "token", "secret"}
	for _, k := range forbiddenKeys {
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			yamlContent := fmt.Sprintf("product_id: 103281\n%s: literal-secret-xyz\n", k)
			_, err := svc.Configure(ctx, backendplugin.ConfigureRequest{
				ConfigYAML: []byte(yamlContent),
				Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("real-secret")}},
			})
			if err == nil {
				t.Fatal("expected error for literal secret in YAML, got nil")
			}
		})
	}
}

func TestConfigure_BaseURLConstruction(t *testing.T) {
	t.Parallel()

	// 1. Integer product_id
	cfgInt, err := service.ParseConfigYAML([]byte("product_id: 103281\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfgInt.BaseURL(), "https://api.infomaniak.com/2/ai/103281/openai/v1"; got != want {
		t.Fatalf("BaseURL=%q want %q", got, want)
	}

	// 2. Quoted string product_id
	cfgStr, err := service.ParseConfigYAML([]byte("product_id: \"103281\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfgStr.BaseURL(), "https://api.infomaniak.com/2/ai/103281/openai/v1"; got != want {
		t.Fatalf("BaseURL=%q want %q", got, want)
	}

	// 3. Trimmed whitespace string product_id
	cfgTrim, err := service.ParseConfigYAML([]byte("product_id: \"  103281  \"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfgTrim.BaseURL(), "https://api.infomaniak.com/2/ai/103281/openai/v1"; got != want {
		t.Fatalf("BaseURL=%q want %q", got, want)
	}

	// 4. api_origin override
	cfgOrigin, err := service.ParseConfigYAML([]byte("product_id: 103281\napi_origin: http://127.0.0.1:8080/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfgOrigin.BaseURL(), "http://127.0.0.1:8080/2/ai/103281/openai/v1"; got != want {
		t.Fatalf("BaseURL=%q want %q", got, want)
	}

	// 5. Rejection of /compat in api_origin
	_, err = service.ParseConfigYAML([]byte("product_id: 103281\napi_origin: http://127.0.0.1:8888/compat\n"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "compat") {
		t.Fatalf("expected /compat error, got %v", err)
	}
}

func TestParity_BearerAuth(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotHeaders http.Header
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	t.Cleanup(srv.Close)

	yamlCfg := fmt.Sprintf("product_id: 103281\napi_origin: %s\n", srv.URL)
	fakeToken := "infomaniak-secret-token-xyz-123"

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-test",
		ConfigYAML:  []byte(yamlCfg),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte(fakeToken)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drive configured instance directly
	stream := newTestExecuteStream(context.Background(), "infomaniak-ai/mixtral-8x7b", aipapi.OperationOpenAIChatCompletions)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("inst.Execute failed: %v", err)
	}

	mu.Lock()
	authHdr := gotHeaders.Get("Authorization")
	p := gotPath
	mu.Unlock()

	// 1. Bearer token sent
	if authHdr != "Bearer "+fakeToken {
		t.Fatalf("Authorization header=%q want Bearer %s", authHdr, fakeToken)
	}

	// 2. Infomaniak AI OpenAI path used
	wantPathPrefix := "/2/ai/103281/openai/v1/chat/completions"
	if p != wantPathPrefix {
		t.Fatalf("path=%q want %q", p, wantPathPrefix)
	}

	// 3. Token never in Describe
	desc, _ := svc.Describe(context.Background())
	if strings.Contains(fmt.Sprintf("%+v", desc), fakeToken) {
		t.Fatal("Token leaked into Describe")
	}
}

func TestParity_DefaultOperationHitsChatCompletions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		operation aipapi.Operation
		wantPath  string
	}{
		{
			name:      "default_ambiguous_empty_operation",
			operation: aipapi.Operation(""),
			wantPath:  "/2/ai/103281/openai/v1/chat/completions",
		},
		{
			name:      "explicit_chat_completions",
			operation: aipapi.OperationOpenAIChatCompletions,
			wantPath:  "/2/ai/103281/openai/v1/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotPath string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotPath = r.URL.Path
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
			}))
			t.Cleanup(srv.Close)

			yamlCfg := fmt.Sprintf("product_id: 103281\napi_origin: %s\n", srv.URL)
			fakeToken := "infomaniak-secret-token-xyz-123"

			svc := service.New()
			inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-test-flavor",
				ConfigYAML:  []byte(yamlCfg),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte(fakeToken)}},
			})
			if err != nil {
				t.Fatal(err)
			}

			stream := newTestExecuteStream(context.Background(), "infomaniak-ai/mixtral-8x7b", tc.operation)
			if err := inst.Execute(stream); err != nil {
				t.Fatalf("inst.Execute failed: %v", err)
			}

			mu.Lock()
			p := gotPath
			mu.Unlock()

			if p != tc.wantPath {
				t.Fatalf("path=%q want %q", p, tc.wantPath)
			}
		})
	}
}

func TestParity_HardNegativeResponsesNeverFallsBackToChat(t *testing.T) {
	t.Parallel()

	// Chat succeeds (200), but Responses returns 404.
	// Responses operation MUST fail closed and NEVER fall back to Chat.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, `{"error":{"message":"responses not supported"}}`, http.StatusNotFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"chat fallback"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	yamlCfg := fmt.Sprintf("product_id: 103281\napi_origin: %s\n", srv.URL)
	fakeToken := "infomaniak-secret-token-xyz-123"

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-test-hard-neg",
		ConfigYAML:  []byte(yamlCfg),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte(fakeToken)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stream := newTestExecuteStream(context.Background(), "infomaniak-ai/mixtral-8x7b", aipapi.OperationOpenAIResponses)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected Responses operation to fail closed against 404, but succeeded (silent fallback to chat occurred)")
	}
}

func TestInventory_MapsCodingModels(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "mixtral-8x7b-instruct"},
				{"id": "llama-3-70b-instruct"},
				{"id": "bge-large-en"},
				{"id": "gte-large-en"},
				{"id": "whisper-1"},
				{"id": "flux-schnell"},
				{"id": "qwen-2.5-coder-32b"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	yamlCfg := fmt.Sprintf("product_id: 103281\napi_origin: %s\n", srv.URL)
	fakeToken := "infomaniak-secret-token-xyz-123"

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-test-inv",
		ConfigYAML:  []byte(yamlCfg),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte(fakeToken)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Should drop bge-large-en, gte-large-en, whisper-1, flux-schnell
	// Should keep mixtral-8x7b-instruct, llama-3-70b-instruct, qwen-2.5-coder-32b
	wantIDs := []string{
		"infomaniak-ai/mixtral-8x7b-instruct",
		"infomaniak-ai/llama-3-70b-instruct",
		"infomaniak-ai/qwen-2.5-coder-32b",
	}

	var gotIDs []string
	for _, m := range resp.Models {
		gotIDs = append(gotIDs, m.CanonicalModelID)
	}

	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("got %d models (%v), want %d (%v)", len(gotIDs), gotIDs, len(wantIDs), wantIDs)
	}
	for i, want := range wantIDs {
		if gotIDs[i] != want {
			t.Errorf("model[%d]=%q want %q", i, gotIDs[i], want)
		}
	}
}
