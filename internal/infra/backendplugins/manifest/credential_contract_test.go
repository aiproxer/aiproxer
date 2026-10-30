package manifest_test

import (
	"strings"
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
)

func contractJSON() string {
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
    "credential_mode":"static",
    "access_scope":"any",
    "process_sharing":"per_instance",
    "execution_class":"inference",
    "credential_contract":{
      "version":2,
      "class":"api_key",
      "source":"host_env",
      "env_root":"EXAMPLE_API_KEY",
      "slot":"api_key",
      "selection":"quota_pre_output",
      "quota_domain":"example"
    }
  }]
}`
}

// TestParseStrict_StaticExportWithoutContractRejected proves the closed parser
// fails closed on unclassified static-key exports.
func TestParseStrict_StaticExportWithoutContractRejected(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(contractJSON(), `,
    "credential_contract":{
      "version":2,
      "class":"api_key",
      "source":"host_env",
      "env_root":"EXAMPLE_API_KEY",
      "slot":"api_key",
      "selection":"quota_pre_output",
      "quota_domain":"example"
    }`, "", 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("static export without credential contract must be rejected")
	}
}

func TestParseStrict_CredentialContractRoundTrip(t *testing.T) {
	t.Parallel()
	m, err := inframanifest.ParseStrictBytes([]byte(contractJSON()))
	if err != nil {
		t.Fatalf("static export with valid contract rejected: %v", err)
	}
	got := m.Exports[0].CredentialContract
	if got == nil {
		t.Fatal("credential contract was not parsed")
	}
	if got.EnvRoot != "EXAMPLE_API_KEY" || got.Version != 2 || string(got.Class) != "api_key" ||
		string(got.Source) != "host_env" || string(got.Slot) != "api_key" ||
		string(got.Selection) != "quota_pre_output" || got.QuotaDomain != "example" {
		t.Fatalf("contract mismatch: %+v", got)
	}
}

func TestParseStrict_CredentialContractRejectsUnknownField(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(contractJSON(), `"quota_domain":"example"`, `"quota_domain":"example","api_key":"sk-live-secret-value"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("secret-bearing contract field must be rejected")
	}
}

func TestParseStrict_NonStaticExportWithContractRejected(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(contractJSON(), `"credential_mode":"static"`, `"credential_mode":"none"`, 1)
	raw = strings.Replace(raw, `"access_scope":"any"`, `"access_scope":"local_only"`, 1)
	_, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err == nil {
		t.Fatal("non-static export must not carry a credential contract")
	}
}

func TestParseStrict_NonStaticWithoutContractAccepted(t *testing.T) {
	t.Parallel()
	raw := validJSON()
	m, err := inframanifest.ParseStrictBytes([]byte(raw))
	if err != nil {
		t.Fatalf("v1 non-static export must stay valid, got: %v", err)
	}
	if m.Exports[0].CredentialContract != nil {
		t.Fatal("v1 non-static export must parse with nil contract")
	}
}
