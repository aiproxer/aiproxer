package configreload_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"gopkg.in/yaml.v3"
)

// secretCorpus is a fixed set of synthetic secret needles that must never appear
// in sanitized reload telemetry (req 14.3; task 5.4).
var secretCorpus = []string{
	"sk-live-reload-secret-TOKEN-9f3a",
	"postgres://reload:S3cretPassw0rd@db.internal:5432/aip",
	"https://api.example.com/v1?api_key=ak_live_reload_xyz",
	"Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secretpayload",
	"password=super-secret-dsn-value",
	"OPENAI_API_KEY=sk-opaque-plugin-config-value",
}

func TestSanitize_SecretCorpusNeverLeaks(t *testing.T) {
	t.Parallel()
	var opaque yaml.Node
	if err := yaml.Unmarshal([]byte("api_key: sk-live-reload-secret-TOKEN-9f3a\n"), &opaque); err != nil {
		t.Fatal(err)
	}

	samples := []string{
		configreload.SanitizeConfigKey("plugins.backends[0].config.api_key"),
		configreload.SanitizeDSN("postgres://reload:S3cretPassw0rd@db.internal:5432/aip"),
		configreload.SanitizeURL("https://api.example.com/v1?api_key=ak_live_reload_xyz"),
		configreload.SanitizeOpaqueYAML(&opaque),
		configreload.SanitizeFailure(errors.New("validate: DSN postgres://reload:S3cretPassw0rd@db.internal:5432/aip rejected")),
		configreload.SanitizeFailure(fmt.Errorf("source read failed: %s", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secretpayload")),
		configreload.SanitizePanicValue("panic: password=super-secret-dsn-value"),
		configreload.SanitizePanicValue(map[string]string{"OPENAI_API_KEY": "sk-opaque-plugin-config-value"}),
	}
	for i, sample := range samples {
		low := strings.ToLower(sample)
		for _, needle := range secretCorpus {
			if strings.Contains(sample, needle) || strings.Contains(low, strings.ToLower(needle)) {
				t.Fatalf("sample[%d]=%q still contains secret needle %q", i, sample, needle)
			}
		}
		if sample == "" {
			t.Fatalf("sample[%d] empty; sanitizer must return a bounded safe token", i)
		}
	}
}

func TestSanitizeConfigKey_KeepsSafePathShape(t *testing.T) {
	t.Parallel()
	got := configreload.SanitizeConfigKey("server.address")
	if got != "server.address" {
		t.Fatalf("got %q want server.address", got)
	}
	hostile := configreload.SanitizeConfigKey("backends[0].config=sk-live-reload-secret-TOKEN-9f3a")
	if strings.Contains(hostile, "sk-live-reload-secret-TOKEN-9f3a") {
		t.Fatalf("hostile key leaked secret: %q", hostile)
	}
}

func TestSanitizeURL_CredentialBearing(t *testing.T) {
	t.Parallel()
	got := configreload.SanitizeURL("https://user:S3cretPassw0rd@api.example.com/v1/models")
	if strings.Contains(got, "S3cretPassw0rd") {
		t.Fatalf("credential leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted]") && !strings.Contains(got, "https://") {
		t.Fatalf("expected redacted URL shape, got %q", got)
	}
}

func TestSanitizeFailure_GenericCredentialForms(t *testing.T) {
	t.Parallel()
	secrets := []string{
		"random-api-key-value-9472",
		"arbitrary-client-secret-5813",
		"opaque-password-2468",
		"url-password-1357",
	}
	raw := "compile failed: OPENAI_API_KEY=" + secrets[0] +
		" client_secret: '" + secrets[1] + "' password=\"" + secrets[2] +
		"\" dsn=postgres://reload:" + secrets[3] + "@db.internal/aip"
	got := configreload.SanitizeFailure(errors.New(raw))
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Fatalf("generic credential leaked from %q: %q", raw, got)
		}
	}
	if !strings.Contains(got, configreload.RedactedPlaceholder) {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}
