package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func validJSON() string {
	return `{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.example",
  "version":"1.0.0",
  "build_id":"b1",
  "executable":"bin/plugin",
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":"linux","arch":"amd64"}],
  "exports":[{
    "kind":"example",
    "display_name":"Example",
    "description":"d",
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`
}

func TestParseStrict_ValidRoundTrip(t *testing.T) {
	t.Parallel()
	m, err := inframanifest.ParseStrictBytes([]byte(validJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.PluginID != "com.aiproxer.example" || m.Exports[0].Kind != "example" {
		t.Fatalf("%+v", m)
	}
}

func TestParseStrict_UnknownField(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"build_id":"b1"`, `"build_id":"b1","extra":1`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "unknown field") && err != sdkmanifest.ErrUnknownField {
		if err == nil {
			t.Fatal("expected error")
		}
	}
}

func TestParseStrict_ForbiddenFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"env", "args", "secrets", "url", "hooks", "shell", "models"} {
		raw := strings.Replace(validJSON(), `"build_id":"b1"`, `"build_id":"b1","`+field+`":{}`, 1)
		_, err := inframanifest.ParseStrictBytes([]byte(raw))
		if err == nil {
			t.Fatalf("%s: expected reject", field)
		}
	}
}

func TestParseStrict_DuplicateExport(t *testing.T) {
	t.Parallel()
	// duplicate export fixture:
	raw := `{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.example",
  "version":"1.0.0",
  "build_id":"b1",
  "executable":"bin/plugin",
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,"protocol_min_minor":0,"protocol_max_minor":0,
  "platforms":[{"os":"linux","arch":"amd64"}],
  "exports":[
    {"kind":"example","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"},
    {"kind":"example","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"}
  ]
}`
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected duplicate reject")
	}
}

func TestParseStrict_ScriptExecutableRejected(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"bin/plugin"`, `"bin/run.sh"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected script reject")
	}
}

func TestParseStrict_WindowsRequiresExe(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validJSON(), `"linux"`, `"windows"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("expected .exe requirement")
	}
	raw = strings.Replace(raw, `"bin/plugin"`, `"bin/plugin.exe"`, 1)
	if _, err := inframanifest.ParseStrictBytes([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestParseStrict_FixtureValid(t *testing.T) {
	t.Parallel()
	p := filepath.Join("testdata", "valid_v1.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inframanifest.ParseStrictBytes(b); err != nil {
		t.Fatal(err)
	}
}

func TestParseStrict_Oversized(t *testing.T) {
	t.Parallel()
	big := []byte(`{"schema":"` + strings.Repeat("x", sdkmanifest.MaxManifestBytes) + `"}`)
	_, err := inframanifest.ParseStrictBytes(big)
	if err == nil {
		t.Fatal("expected bounds")
	}
}

func TestParseStrict_ExecutionClass(t *testing.T) {
	t.Parallel()
	// Omitted execution_class parses as empty/unknown
	m1, err := inframanifest.ParseStrictBytes([]byte(validJSON()))
	if err != nil {
		t.Fatalf("omitted execution_class: %v", err)
	}
	if m1.Exports[0].ExecutionClass != "" {
		t.Fatalf("omitted execution_class: want empty, got %q", m1.Exports[0].ExecutionClass)
	}

	// Explicit inference
	rawInf := strings.Replace(validJSON(), `"process_sharing":"per_instance"`, `"process_sharing":"per_instance","execution_class":"inference"`, 1)
	m2, err := inframanifest.ParseStrictBytes([]byte(rawInf))
	if err != nil {
		t.Fatalf("inference execution_class: %v", err)
	}
	if m2.Exports[0].ExecutionClass != "inference" {
		t.Fatalf("inference execution_class: want inference, got %q", m2.Exports[0].ExecutionClass)
	}

	// Explicit agent_runtime
	rawAgent := strings.Replace(validJSON(), `"process_sharing":"per_instance"`, `"process_sharing":"per_instance","execution_class":"agent_runtime"`, 1)
	m3, err := inframanifest.ParseStrictBytes([]byte(rawAgent))
	if err != nil {
		t.Fatalf("agent_runtime execution_class: %v", err)
	}
	if m3.Exports[0].ExecutionClass != "agent_runtime" {
		t.Fatalf("agent_runtime execution_class: want agent_runtime, got %q", m3.Exports[0].ExecutionClass)
	}

	// Invalid execution_class fails validation
	rawBad := strings.Replace(validJSON(), `"process_sharing":"per_instance"`, `"process_sharing":"per_instance","execution_class":"heavy"`, 1)
	_, err = inframanifest.ParseStrictBytes([]byte(rawBad))
	if err == nil {
		t.Fatal("invalid execution_class should fail validation, got nil")
	}
}

func TestParseStrict_CodexDualExportFixture(t *testing.T) {
	t.Parallel()
	raw := `{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.codex",
  "version":"0.1.0",
  "build_id":"b1",
  "executable":"bin/aip-backend-codex",
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":5,
  "platforms":[{"os":"linux","arch":"amd64"}],
  "exports":[
    {
      "kind":"openai-codex",
      "credential_mode":"static",
      "access_scope":"local_only",
      "process_sharing":"per_instance",
      "execution_class":"inference",
      "credential_contract":{
        "version":2,
        "class":"static_nonrotatable",
        "source":"host_env",
        "env_root":"OPENAI_CODEX_ACCESS_TOKEN",
        "slot":"api_token",
        "selection":"no_rotation",
        "quota_domain":"codex"
      }
    },
    {
      "kind":"openai-codex-app-server",
      "credential_mode":"none",
      "access_scope":"local_only",
      "process_sharing":"per_instance",
      "execution_class":"agent_runtime"
    }
  ]
}`
	m, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err != nil {
		t.Fatalf("Codex dual export parse failed: %v", err)
	}
	if len(m.Exports) != 2 {
		t.Fatalf("expected 2 exports, got %d", len(m.Exports))
	}
	if m.Exports[0].Kind != "openai-codex" || m.Exports[0].ExecutionClass != "inference" {
		t.Fatalf("export 0 mismatch: %+v", m.Exports[0])
	}
	if m.Exports[1].Kind != "openai-codex-app-server" || m.Exports[1].ExecutionClass != "agent_runtime" {
		t.Fatalf("export 1 mismatch: %+v", m.Exports[1])
	}
}
