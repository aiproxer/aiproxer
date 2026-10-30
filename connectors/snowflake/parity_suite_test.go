package snowflake_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/snowflake/internal/service"
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

	// 1. Missing account
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst1",
		ConfigYAML:  []byte("role: dev\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok123")}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "account") {
		t.Fatalf("expected account error, got %v", err)
	}

	// 2. Missing PAT/token
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst2",
		ConfigYAML:  []byte("account: my-account\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "pat") && !strings.Contains(strings.ToLower(err.Error()), "token")) {
		t.Fatalf("expected pat/token error, got %v", err)
	}
}

func TestConfigure_RejectsLiteralSecretsInYAML(t *testing.T) {
	t.Parallel()
	svc := service.New()

	cases := []struct {
		name string
		yaml string
	}{
		{"pat", "account: my-account\npat: secret-pat-123\n"},
		{"api_token", "account: my-account\napi_token: secret-tok-456\n"},
		{"token", "account: my-account\ntoken: secret-raw-789\n"},
		{"api_key", "account: my-account\napi_key: secret-key-012\n"},
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

	// 1. Standard account
	cfg1, err := service.ParseConfigYAML([]byte("account: xy12345.us-east-1\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase1 := "https://xy12345.us-east-1.snowflakecomputing.com/api/v2/cortex/v1"
	if cfg1.BaseURL() != wantBase1 {
		t.Fatalf("BaseURL=%q want %q", cfg1.BaseURL(), wantBase1)
	}

	// 2. Account with scheme/hostname normalized
	cfg2, err := service.ParseConfigYAML([]byte("account: https://xy12345.us-east-1.snowflakecomputing.com/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.BaseURL() != wantBase1 {
		t.Fatalf("BaseURL=%q want %q", cfg2.BaseURL(), wantBase1)
	}

	// 3. Optional api_origin for tests retains /api/v2/cortex/v1 suffix
	cfg3, err := service.ParseConfigYAML([]byte("account: xy12345\napi_origin: http://127.0.0.1:8888\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase3 := "http://127.0.0.1:8888/api/v2/cortex/v1"
	if cfg3.BaseURL() != wantBase3 {
		t.Fatalf("BaseURL=%q want %q", cfg3.BaseURL(), wantBase3)
	}

	// 4. Rejection of /compat in account or api_origin
	_, err = service.ParseConfigYAML([]byte("account: xy12345\napi_origin: http://127.0.0.1:8888/compat\n"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "compat") {
		t.Fatalf("expected /compat error, got %v", err)
	}
}

func TestParity_BearerAuthAndRoleHeaders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		role         string
		expectedRole string
	}{
		{name: "with_role", role: "analyst_role", expectedRole: "analyst_role"},
		{name: "without_role", role: "", expectedRole: ""},
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
				_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello cortex"}]}]}`))
			}))
			t.Cleanup(srv.Close)

			yamlCfg := fmt.Sprintf("account: testacc\napi_origin: %s\nrole: %s\n", srv.URL, tc.role)
			fakePAT := "snowflake-pat-secret-xyz-999"

			svc := service.New()
			inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-test",
				ConfigYAML:  []byte(yamlCfg),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte(fakePAT)}},
			})
			if err != nil {
				t.Fatal(err)
			}

			// Drive configured instance directly
			stream := newTestExecuteStream(context.Background(), "snowflake-cortex/mistral-large2", aipapi.OperationOpenAIResponses)
			if err := inst.Execute(stream); err != nil {
				t.Fatalf("inst.Execute failed: %v", err)
			}

			mu.Lock()
			authHdr := gotHeaders.Get("Authorization")
			roleHdr := gotHeaders.Get("X-Snowflake-Role")
			p := gotPath
			mu.Unlock()

			// 1. Bearer token sent
			if authHdr != "Bearer "+fakePAT {
				t.Fatalf("Authorization header=%q want Bearer %s", authHdr, fakePAT)
			}

			// 2. Role header matches expectation
			if roleHdr != tc.expectedRole {
				t.Fatalf("X-Snowflake-Role header=%q want %q", roleHdr, tc.expectedRole)
			}

			// 3. Cortex path used
			if !strings.HasPrefix(p, "/api/v2/cortex/v1") {
				t.Fatalf("path=%q want prefix /api/v2/cortex/v1", p)
			}

			// 4. Token never in Describe
			desc, _ := svc.Describe(context.Background())
			if strings.Contains(fmt.Sprintf("%+v", desc), fakePAT) {
				t.Fatal("PAT leaked into Describe")
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
		ConfigYAML:  []byte("account: acc\napi_origin: " + srvA.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamA := newTestExecuteStream(context.Background(), "snowflake-cortex/mistral-large2", aipapi.OperationOpenAIResponses)
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
		ConfigYAML:  []byte("account: acc\napi_origin: " + srvB.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamB := newTestExecuteStream(context.Background(), "snowflake-cortex/mistral-large2", aipapi.OperationOpenAIResponses)
	if err := instB.Execute(streamB); err == nil {
		t.Fatal("hard-negative violation: Responses call succeeded on configured instance via Chat fallback when responses 404'd!")
	}
}

func TestInventory_MapsCodingModels(t *testing.T) {
	t.Parallel()

	mixedPayload := `{
		"data": [
			{"id": "mistral-large2", "owned_by": "snowflake"},
			{"id": "llama3.3-70b", "owned_by": "snowflake"},
			{"id": "snowflake-arctic", "owned_by": "snowflake"},
			{"id": "deepseek-r1", "owned_by": "snowflake"},
			{"id": "arctic-embed-m-v1.5", "owned_by": "snowflake"},
			{"id": "bge-large-en-v1.5", "owned_by": "snowflake"},
			{"id": "rerank-v1", "owned_by": "snowflake"},
			{"id": "whisper-tiny", "owned_by": "snowflake"},
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
		ConfigYAML:  []byte("account: acc\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{"mistral-large2", "llama3.3-70b", "snowflake-arctic", "deepseek-r1"}
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
		wantCanonical := "snowflake-cortex/" + want
		if resp.Models[i].CanonicalModelID != wantCanonical {
			t.Fatalf("model %d CanonicalModelID=%q want %q", i, resp.Models[i].CanonicalModelID, wantCanonical)
		}
		if resp.Models[i].FactoryKind != service.FactoryKind {
			t.Fatalf("model %d FactoryKind=%q want %q", i, resp.Models[i].FactoryKind, service.FactoryKind)
		}
	}
}
