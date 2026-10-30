package codex_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/service"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/testemu"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func tokenReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKindHTTP,
		InstanceID:    "cred-contract",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

// TestCredentialContract_RejectsYAMLTokenFields proves literal static
// tokens and credential-file pointers in connector YAML fail closed: the
// openai-codex export is no_rotation over host-delivered api_token slots.
func TestCredentialContract_RejectsYAMLTokenFields(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"access_token: hunter2\n",
		"refresh_token: hunter2\n",
		"auth_json_path: /tmp/auth.json\n",
		"auth_json: {\"access_token\": \"hunter2\"}\n",
	} {
		_, err := service.New().Configure(context.Background(), tokenReq(
			raw,
			map[string]string{"api_token": "host-token"},
		))
		if err == nil {
			t.Fatalf("expected literal YAML credential field to be rejected (yaml=%q)", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks secret value: %v", err)
		}
		lowered := strings.ToLower(err.Error())
		if !strings.Contains(lowered, "access_token") && !strings.Contains(lowered, "refresh_token") &&
			!strings.Contains(lowered, "auth_json") {
			t.Fatalf("rejection must name the field path: %v", err)
		}
		if !strings.Contains(err.Error(), "OPENAI_CODEX_ACCESS_TOKEN") {
			t.Fatalf("rejection must name the approved env root: %v", err)
		}
	}
}

// TestCredentialContract_RequiresHostSecrets proves Configure fails closed
// without host-delivered slots, with an actionable secret-free error. In
// particular no ambient auth.json may satisfy admission.
func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), tokenReq(
		"base_url: http://127.0.0.1:9\ncatalog_enabled: false\n", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail closed")
	}
	if !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "OPENAI_CODEX_ACCESS_TOKEN") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "Configure secrets") {
		t.Fatalf("rejection must direct the operator to host Configure secrets: %v", err)
	}
}

// TestCredentialContract_RejectsSecondSlot proves static_nonrotatable binds
// exactly one host slot: a spare numbered slot fails closed instead of being
// silently ignored or pooled as a quota peer.
func TestCredentialContract_RejectsSecondSlot(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), tokenReq(
		"base_url: http://127.0.0.1:9\ncatalog_enabled: false\n",
		map[string]string{"api_token": "first-token", "api_token_2": "second-token"},
	))
	if err == nil {
		t.Fatal("expected a second host slot to fail closed for no_rotation")
	}
	if strings.Contains(err.Error(), "first-token") || strings.Contains(err.Error(), "second-token") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_token_2") {
		t.Fatalf("rejection must name the surplus slot: %v", err)
	}
}

// plantDecoyAuthJSON writes a decoy Codex auth.json under a temp HOME and
// points HOME at it, proving the connector never consults ambient files.
func plantDecoyAuthJSON(t *testing.T, token string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestCredentialContract_IgnoresAmbientAuthJSON proves connector-side
// filesystem fallback is gone: a planted ~/.codex/auth.json neither admits
// an instance nor overrides the host-delivered token.
func TestCredentialContract_IgnoresAmbientAuthJSON(t *testing.T) {
	plantDecoyAuthJSON(t, "decoy-file-token")

	emu := testemu.New(testemu.Config{Token: "host-token", OutputText: "ok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)

	// No host secrets: the decoy file must not satisfy admission.
	if _, err := service.New().Configure(context.Background(), tokenReq(
		"base_url: "+srv.URL+"\ncatalog_enabled: false\n", nil)); err == nil {
		t.Fatal("ambient auth.json must not satisfy admission")
	}

	// Host secret present: the wire must carry the host token, never the file.
	inst, err := service.New().Configure(context.Background(), tokenReq(
		"base_url: "+srv.URL+"\ncatalog_enabled: false\n",
		map[string]string{"api_token": "host-token"},
	))
	if err != nil {
		t.Fatal(err)
	}
	mustExecute(t, inst, "openai-codex/gpt-5.3-codex-spark", "hi")
	if got := emu.LatestRequest().Authorization; got != "Bearer host-token" {
		t.Fatalf("wire bearer=%q, want the host-delivered token only", got)
	}
}

// TestCredentialContract_NoRotationOnAuthFailure proves no_rotation and no
// quota pooling: an auth-invalid token yields a classified failure after a
// single attempt, and a later call still presents the same single token
// (no advancement, no cross-account pooling).
func TestCredentialContract_NoRotationOnAuthFailure(t *testing.T) {
	t.Parallel()
	emu := testemu.New(testemu.Config{Token: "good-token", OutputText: "ok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), tokenReq(
		"base_url: "+srv.URL+"\ncatalog_enabled: false\n",
		map[string]string{"api_token": "bad-token"},
	))
	if err != nil {
		t.Fatal(err)
	}
	slug := firstNative(t, inst)
	if err := tryExecuteCodex(t, inst, "openai-codex/"+slug); err == nil {
		t.Fatal("expected auth-invalid failure")
	} else if strings.Contains(err.Error(), "bad-token") {
		t.Fatalf("failure leaks secret: %v", err)
	}
	if got := emu.LatestRequest().Authorization; got != "Bearer bad-token" {
		t.Fatalf("single attempt must present the admitted token, got %q", got)
	}
	// A subsequent call presents the same admitted token: no rotation state.
	if err := tryExecuteCodex(t, inst, "openai-codex/"+slug); err == nil {
		t.Fatal("expected auth-invalid failure again")
	}
	if got := emu.LatestRequest().Authorization; got != "Bearer bad-token" {
		t.Fatalf("no rotation: got %q", got)
	}
}

// tryExecuteCodex runs one Execute and returns its terminal error (nil on
// success) for negative no_rotation proofs.
func tryExecuteCodex(t *testing.T, inst backendplugin.ConfiguredInstance, model string) error {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: model,
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		return err
	}
	return nil
}

// TestCredentialContract_ManifestClassAndScope pins the packaged manifest
// classification: openai-codex is static/static_nonrotatable/no_rotation over
// OPENAI_CODEX_ACCESS_TOKEN with local_only scope (subscription identity is
// one account, never an indiscriminate quota pool); the app-server export is
// credential-free local_only. The runtime descriptor must agree.
func TestCredentialContract_ManifestClassAndScope(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exports []struct {
			Kind               string `json:"kind"`
			CredentialMode     string `json:"credential_mode"`
			AccessScope        string `json:"access_scope"`
			CredentialContract *struct {
				Version     int    `json:"version"`
				Class       string `json:"class"`
				Source      string `json:"source"`
				EnvRoot     string `json:"env_root"`
				Slot        string `json:"slot"`
				Selection   string `json:"selection"`
				QuotaDomain string `json:"quota_domain"`
			} `json:"credential_contract"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	byKind := map[string]int{}
	for i, ex := range doc.Exports {
		byKind[ex.Kind] = i
	}
	httpIdx, ok := byKind[service.FactoryKindHTTP]
	if !ok {
		t.Fatalf("manifest missing %q export", service.FactoryKindHTTP)
	}
	httpEx := doc.Exports[httpIdx]
	if httpEx.CredentialMode != "static" || httpEx.AccessScope != "local_only" {
		t.Fatalf("http posture=%q/%q, want static/local_only", httpEx.CredentialMode, httpEx.AccessScope)
	}
	if httpEx.CredentialContract == nil {
		t.Fatal("missing credential_contract for the static HTTP export")
	}
	cc := httpEx.CredentialContract
	if cc.Version != 2 || cc.Class != "static_nonrotatable" || cc.Source != "host_env" ||
		cc.EnvRoot != "OPENAI_CODEX_ACCESS_TOKEN" || cc.Slot != "api_token" ||
		cc.Selection != "no_rotation" || cc.QuotaDomain != "codex" {
		t.Fatalf("credential_contract=%+v", cc)
	}
	appIdx, ok := byKind[service.FactoryKindAppServer]
	if !ok {
		t.Fatalf("manifest missing %q export", service.FactoryKindAppServer)
	}
	appEx := doc.Exports[appIdx]
	if appEx.CredentialMode != "none" || appEx.AccessScope != "local_only" {
		t.Fatalf("app-server posture=%q/%q, want none/local_only", appEx.CredentialMode, appEx.AccessScope)
	}
	if appEx.CredentialContract != nil {
		t.Fatalf("app-server export must carry no credential contract: %+v", appEx.CredentialContract)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]backendplugin.FactoryDescriptor{}
	for _, f := range d.Factories {
		modes[f.Kind] = f
	}
	if modes[service.FactoryKindHTTP].CredentialMode != backendplugin.CredentialModeStatic ||
		modes[service.FactoryKindHTTP].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("descriptor disagrees with manifest HTTP posture: %+v", modes[service.FactoryKindHTTP])
	}
	if modes[service.FactoryKindAppServer].CredentialMode != backendplugin.CredentialModeNone ||
		modes[service.FactoryKindAppServer].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("descriptor disagrees with manifest app-server posture: %+v", modes[service.FactoryKindAppServer])
	}
}
