package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Requirements 2.1/2.6: config validation and consent precede runtime exec.
func TestConfigure_ValidationAndConsentBeforeExecution(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"malformed", "[", "invalid configuration"},
		{"literal", "api_key: private-test-value", "invalid configuration"},
		{"duplicate", "admin_policy: honor\nadmin_policy: refuse", "invalid configuration"},
		{"extra document", "admin_policy: honor\n---\n{}", "invalid configuration"},
		{"unknown field", "sdk_settings: {}", "invalid configuration"},
		{"bad duration", "idle_timeout_seconds: .inf", "invalid configuration"},
		{"bad admin", "admin_policy: private-test-value", "admin_policy"},
		{"relative workspace", "default_workspace: .", "default_workspace"},
		{"unknown", "credential_source: other", "credential_source"},
		{"missing", "credential_source: subscription_token", "claude_oauth_token"},
		{"removed policy", "permission_policy: allow", "permission_policy must be bypass or deny"},
		{"unsafe environment", "env_allowlist: [ANTHROPIC_BASE_URL]", "env_allowlist"},
		{"refuse", "default_workspace: /workspace", "admin_policy: honor"},
		{"gateway without endpoint", "credential_source: gateway", "gateway base_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New().Configure(context.Background(), backendplugin.ConfigureRequest{ConfigYAML: []byte(tc.yaml), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("private-test-value")}}})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-test-value") {
				t.Fatalf("expected safe %q rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestParseConfig_PermissionPolicyDefaultsToBypass(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{{"", "bypass"}, {"permission_policy: ''", "bypass"}, {"permission_policy: bypass", "bypass"}, {"permission_policy: deny", "deny"}} {
		cfg, err := ParseConfigYAML([]byte(tc.yaml))
		if err != nil || cfg.PermissionPolicy != tc.want {
			t.Fatalf("%q: %+v %v", tc.yaml, cfg, err)
		}
	}
}

func TestConfigure_GatewayPolicyAndSecretRefs(t *testing.T) {
	for _, tc := range []struct{ name, gateway, want string }{
		{"https", "gateway:\n  base_url: https://gw.example.com\n  headers:\n    - name: X-Route\n", ""},
		{"loopback-http", "gateway:\n  base_url: http://127.0.0.1:18911\n", ""},
		{"loopback-localhost", "gateway:\n  base_url: http://localhost:18911\n", ""},
		{"loopback-ipv6", "gateway:\n  base_url: http://[::1]:18911\n", ""},
		{"plain-http", "gateway:\n  base_url: http://gw.example.com\n", "loopback"},
		{"userinfo", "gateway:\n  base_url: https://user:pw@gw.example.com\n", "user information"},
		{"fragment", "gateway:\n  base_url: https://gw.example.com#x\n", "https URL"},
		{"ftp", "gateway:\n  base_url: ftp://gw.example.com\n", "https URL"},
		{"empty", "gateway:\n", "https URL"},
		{"too long", "gateway:\n  base_url: https://" + strings.Repeat("a", 5000) + ".com\n", "bounded length"},
		{"header value literal", "gateway:\n  base_url: https://gw.example.com\n  headers:\n    - name: X-Route\n    - {name: X-A, value: private-value}\n", "invalid configuration"},
		{"dupe header", "gateway:\n  base_url: https://gw.example.com\n  headers:\n    - name: X-Route\n    - name: x-rOUte\n", "repeats an existing name"},
		{"unknown header shape", "gateway:\n  base_url: https://gw.example.com\n  headers: private-value\n", "invalid configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfigYAML(fmt.Appendf(nil, "credential_source: gateway\nadmin_policy: honor\n%s", tc.gateway))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-value") {
					t.Fatalf("expected %q rejection: %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if cfg.Gateway.BaseURL == "" {
				t.Fatal("accepted configuration lost the endpoint")
			}
		})
	}
	// Explicitly deny a gateway section on a non-gateway source.
	if _, err := ParseConfigYAML([]byte("admin_policy: honor\ncredential_source: api_key\ngateway:\n  base_url: https://gw.example.com\n")); err == nil || !strings.Contains(err.Error(), "credential_source gateway") {
		t.Fatalf("expected inter-source rejection: %v", err)
	}
}

// The gateway source must reject pre-exec on missing header secret refs and
// never resolve a partial configuration.
func TestConfigure_GatewayMissingHeaderSecretRefFailsClosed(t *testing.T) {
	svc, _, _, _ := serviceFixture(t, "gateway")
	body := fmt.Sprintf("default_workspace: %s\ngateway:\n  base_url: https://gw.example.com\n  headers:\n    - name: X-Route\n", t.TempDir())
	_, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: []byte(body), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{}}})
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("expected missing-secret rejection: %v", err)
	}
}
