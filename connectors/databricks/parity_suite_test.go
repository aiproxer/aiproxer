package databricks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/databricks/internal/service"
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
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != service.PluginID {
		t.Fatalf("plugin_id=%s want %s", d.PluginID, service.PluginID)
	}
	if len(d.Factories) == 0 {
		t.Fatal("no factories described")
	}
	fact := d.Factories[0]
	if fact.Kind != service.FactoryKind {
		t.Fatalf("kind=%s want %s", fact.Kind, service.FactoryKind)
	}
	if fact.DisplayName != service.DisplayName {
		t.Fatalf("display_name=%s want %s", fact.DisplayName, service.DisplayName)
	}
	if len(fact.RoutePrefixes) == 0 || fact.RoutePrefixes[0] != service.FactoryKind {
		t.Fatalf("route_prefixes=%v want [%s]", fact.RoutePrefixes, service.FactoryKind)
	}
	if d.ProtocolMajor != 1 {
		t.Fatalf("protocol_major=%d want 1", d.ProtocolMajor)
	}
	if d.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("protocol_minor=%d want %d", d.ProtocolMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
	hasHandshake := false
	for _, feat := range d.Features {
		if feat.Name == backendplugin.FeatureCancellationHandshake {
			hasHandshake = true
			if feat.Required {
				t.Fatal("FeatureCancellationHandshake must be optional (Required false)")
			}
		}
	}
	if !hasHandshake {
		t.Fatal("missing FeatureCancellationHandshake in described features")
	}
}

func TestConfigure_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	svc := service.New()

	// 1. Missing host
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst1",
		ConfigYAML:  []byte("serving_endpoint: my-endpoint\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok123")}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host") {
		t.Fatalf("expected host error, got %v", err)
	}

	// 2. Missing token
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst2",
		ConfigYAML:  []byte("host: adb-123.azuredatabricks.net\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestConfigure_RejectsLiteralSecretsInYAML(t *testing.T) {
	t.Parallel()
	svc := service.New()

	cases := []struct {
		name string
		yaml string
	}{
		{"token", "host: adb-123.azuredatabricks.net\ntoken: secret-token-123\n"},
		{"api_token", "host: adb-123.azuredatabricks.net\napi_token: secret-tok-456\n"},
		{"api_key", "host: adb-123.azuredatabricks.net\napi_key: secret-key-789\n"},
		{"secret", "host: adb-123.azuredatabricks.net\nsecret: secret-sec-012\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst",
				ConfigYAML:  []byte(tc.yaml),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("valid")}},
			})
			if err == nil {
				t.Fatal("expected error for literal secret in YAML, got nil")
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error echoed sensitive secret value: %s", err.Error())
			}
		})
	}
}

func TestConfigure_BaseURLConstruction(t *testing.T) {
	t.Parallel()

	// 1. Standard host
	cfg1, err := service.ParseConfigYAML([]byte("host: adb-123.azuredatabricks.net\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase1 := "https://adb-123.azuredatabricks.net/ai-gateway/mlflow/v1"
	if cfg1.BaseURL() != wantBase1 {
		t.Fatalf("BaseURL=%q want %q", cfg1.BaseURL(), wantBase1)
	}

	// 2. Host with scheme/trailing slash normalized
	cfg2, err := service.ParseConfigYAML([]byte("host: https://adb-123.azuredatabricks.net/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.BaseURL() != wantBase1 {
		t.Fatalf("BaseURL=%q want %q", cfg2.BaseURL(), wantBase1)
	}

	// 3. Optional api_origin for tests retains /ai-gateway/mlflow/v1 suffix
	cfg3, err := service.ParseConfigYAML([]byte("host: adb-123\napi_origin: http://127.0.0.1:8888\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase3 := "http://127.0.0.1:8888/ai-gateway/mlflow/v1"
	if cfg3.BaseURL() != wantBase3 {
		t.Fatalf("BaseURL=%q want %q", cfg3.BaseURL(), wantBase3)
	}

	// 4. Rejection of /compat in host or api_origin
	_, err = service.ParseConfigYAML([]byte("host: adb-123\napi_origin: http://127.0.0.1:8888/compat\n"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "compat") {
		t.Fatalf("expected /compat error, got %v", err)
	}
}

func TestParity_BearerAuthAndNoCustomHeaders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		servingEndpoint string
	}{
		{name: "with_serving_endpoint", servingEndpoint: "databricks-dbrx-instruct"},
		{name: "without_serving_endpoint", servingEndpoint: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
				_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello databricks"}]}]}`))
			}))
			t.Cleanup(srv.Close)

			yamlCfg := fmt.Sprintf("host: testworkspace\napi_origin: %s\nserving_endpoint: %s\n", srv.URL, tc.servingEndpoint)
			fakeToken := "dapi-secret-token-xyz-123"

			svc := service.New()
			inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-test",
				ConfigYAML:  []byte(yamlCfg),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte(fakeToken)}},
			})
			if err != nil {
				t.Fatal(err)
			}

			// Drive configured instance directly
			stream := newTestExecuteStream(context.Background(), "databricks-ai/dbrx-instruct", aipapi.OperationOpenAIResponses)
			if err := inst.Execute(stream); err != nil {
				t.Fatalf("inst.Execute failed: %v", err)
			}

			mu.Lock()
			authHdr := gotHeaders.Get("Authorization")
			dbrxSvcHdr := gotHeaders.Get("Databricks-Model-Provider-Service")
			inventedHdr := gotHeaders.Get("X-Databricks-Serving-Endpoint")
			p := gotPath
			mu.Unlock()

			// 1. Bearer token sent
			if authHdr != "Bearer "+fakeToken {
				t.Fatalf("Authorization header=%q want Bearer %s", authHdr, fakeToken)
			}

			// 2. Neither Databricks-Model-Provider-Service nor X-Databricks-Serving-Endpoint is sent
			if dbrxSvcHdr != "" {
				t.Fatalf("Databricks-Model-Provider-Service header must NOT be sent on mlflow v1, got %q", dbrxSvcHdr)
			}
			if inventedHdr != "" {
				t.Fatalf("invented header X-Databricks-Serving-Endpoint must NOT be sent, got %q", inventedHdr)
			}

			// 3. Databricks AI Gateway path used
			if !strings.HasPrefix(p, "/ai-gateway/mlflow/v1") {
				t.Fatalf("path=%q want prefix /ai-gateway/mlflow/v1", p)
			}

			// 4. Token never in Describe
			desc, _ := svc.Describe(context.Background())
			if strings.Contains(fmt.Sprintf("%+v", desc), fakeToken) {
				t.Fatal("Token leaked into Describe")
			}
		})
	}
}

func TestParity_ModelSelectionThroughConfiguredInstance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		servingEndpoint      string
		invocationModel      string
		expectedPayloadModel string
	}{
		{
			name:                 "serving_endpoint_set_invocation_empty",
			servingEndpoint:      "system.ai.default-serving-endpoint",
			invocationModel:      "databricks-ai",
			expectedPayloadModel: "system.ai.default-serving-endpoint",
		},
		{
			name:                 "serving_endpoint_set_invocation_explicit_wins",
			servingEndpoint:      "system.ai.default-serving-endpoint",
			invocationModel:      "databricks-ai/system.ai.claude-sonnet-4-5",
			expectedPayloadModel: "system.ai.claude-sonnet-4-5",
		},
		{
			name:                 "serving_endpoint_omitted_invocation_explicit",
			servingEndpoint:      "",
			invocationModel:      "databricks-ai/dbrx-instruct",
			expectedPayloadModel: "dbrx-instruct",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotBody []byte
			var gotHeaders http.Header

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotHeaders = r.Header.Clone()
				gotBody, _ = io.ReadAll(r.Body)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp-test","output":[{"content":[{"type":"output_text","text":"hello"}]}]}`))
			}))
			t.Cleanup(srv.Close)

			var yamlCfg string
			if tc.servingEndpoint != "" {
				yamlCfg = fmt.Sprintf("host: testworkspace\napi_origin: %s\nserving_endpoint: %s\n", srv.URL, tc.servingEndpoint)
			} else {
				yamlCfg = fmt.Sprintf("host: testworkspace\napi_origin: %s\n", srv.URL)
			}
			fakeToken := "dapi-secret-token-xyz-123"

			svc := service.New()
			inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-test-model-select",
				ConfigYAML:  []byte(yamlCfg),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte(fakeToken)}},
			})
			if err != nil {
				t.Fatal(err)
			}

			// Drive configured instance directly
			stream := newTestExecuteStream(context.Background(), tc.invocationModel, aipapi.OperationOpenAIResponses)
			if err := inst.Execute(stream); err != nil {
				t.Fatalf("inst.Execute failed: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			var payload struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(gotBody, &payload); err != nil {
				t.Fatalf("json unmarshal failed: %v, raw body: %s", err, string(gotBody))
			}

			if payload.Model != tc.expectedPayloadModel {
				t.Fatalf("payload model=%q want %q", payload.Model, tc.expectedPayloadModel)
			}

			// Assert neither Databricks-Model-Provider-Service nor X-Databricks-Serving-Endpoint is sent
			if hdr := gotHeaders.Get("Databricks-Model-Provider-Service"); hdr != "" {
				t.Fatalf("Databricks-Model-Provider-Service header must NOT be sent, got %q", hdr)
			}
			if hdr := gotHeaders.Get("X-Databricks-Serving-Endpoint"); hdr != "" {
				t.Fatalf("X-Databricks-Serving-Endpoint header must NOT be sent, got %q", hdr)
			}
		})
	}
}

func TestParity_HardNegativeResponsesNeverFallsBackToChat(t *testing.T) {
	t.Parallel()

	// Scenario A: Chat endpoint 404s, but Responses endpoint succeeds -> Responses call SUCCEEDS.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-a","output":[{"content":[{"type":"output_text","text":"responses ok"}]}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvA.Close)

	svc := service.New()
	instA, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-a",
		ConfigYAML:  []byte("host: h\napi_origin: " + srvA.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamA := newTestExecuteStream(context.Background(), "databricks-ai/dbrx-instruct", aipapi.OperationOpenAIResponses)
	if err := instA.Execute(streamA); err != nil {
		t.Fatalf("Responses call failed on configured instance when responses endpoint was live: %v", err)
	}

	// Scenario B: Chat endpoint 200s, but Responses endpoint 404s -> Responses call MUST FAIL (never fall back to chat).
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-b","choices":[{"message":{"role":"assistant","content":"chat ok"}}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvB.Close)

	instB, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-b",
		ConfigYAML:  []byte("host: h\napi_origin: " + srvB.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamB := newTestExecuteStream(context.Background(), "databricks-ai/dbrx-instruct", aipapi.OperationOpenAIResponses)
	if err := instB.Execute(streamB); err == nil {
		t.Fatal("hard-negative violation: Responses call succeeded on configured instance via Chat fallback when responses 404'd!")
	}
}

func TestInventory_MapsCodingModels(t *testing.T) {
	t.Parallel()

	mixedPayload := `{
		"data": [
			{"id": "databricks-dbrx-instruct", "owned_by": "databricks"},
			{"id": "databricks-meta-llama-3-3-70b-instruct", "owned_by": "databricks"},
			{"id": "databricks-mixtral-8x7b-instruct", "owned_by": "databricks"},
			{"id": "databricks-bge-large-en", "owned_by": "databricks"},
			{"id": "databricks-gte-large-en", "owned_by": "databricks"},
			{"id": "databricks-rerank-v1", "owned_by": "databricks"},
			{"id": "whisper-large", "owned_by": "databricks"},
			{"id": "", "owned_by": ""}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(mixedPayload))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-inv",
		ConfigYAML:  []byte("host: h\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{"databricks-dbrx-instruct", "databricks-meta-llama-3-3-70b-instruct", "databricks-mixtral-8x7b-instruct"}
	if len(resp.Models) != len(wantIDs) {
		var got []string
		for _, m := range resp.Models {
			got = append(got, m.NativeModelID)
		}
		t.Fatalf("got %d models (%v), want %d (%v)", len(resp.Models), got, len(wantIDs), wantIDs)
	}

	for i, want := range wantIDs {
		if resp.Models[i].NativeModelID != want {
			t.Fatalf("model %d NativeModelID=%q want %q", i, resp.Models[i].NativeModelID, want)
		}
		wantCanonical := "databricks-ai/" + want
		if resp.Models[i].CanonicalModelID != wantCanonical {
			t.Fatalf("model %d CanonicalModelID=%q want %q", i, resp.Models[i].CanonicalModelID, wantCanonical)
		}
		if resp.Models[i].FactoryKind != service.FactoryKind {
			t.Fatalf("model %d FactoryKind=%q want %q", i, resp.Models[i].FactoryKind, service.FactoryKind)
		}
	}
}
