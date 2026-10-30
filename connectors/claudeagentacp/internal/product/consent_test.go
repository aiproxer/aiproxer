package product

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// Requirement 2.6 / debug-policy FIX_PLAN: recorded auth status cannot exclude
// server authority. Mutant: infer absent from no local files or Pro/Max labels.
func TestStartupGate_PotentialServerPolicyRequiresPreExecConsent(t *testing.T) {
	for _, tc := range []struct{ source, status string }{
		{"api_key", `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","analyticsDisabled":true,"projectsDirectory":"/tmp/claude-current/remote-matrix-r1/api_key-clean/home/.claude/projects","configDirectory":"/tmp/claude-current/remote-matrix-r1/api_key-clean/home/.claude","apiKeySource":"ANTHROPIC_API_KEY"}`},
		{"subscription_token", `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty","analyticsDisabled":true,"projectsDirectory":"/tmp/claude-current/remote-gates-r1/subscription_token-clean/home/.claude/projects","configDirectory":"/tmp/claude-current/remote-gates-r1/subscription_token-clean/home/.claude"}`},
		{"host_login", `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","analyticsDisabled":true,"projectsDirectory":"/tmp/claude-current/remote-matrix-r1/host_login-clean/home/.claude/projects","configDirectory":"/tmp/claude-current/remote-matrix-r1/host_login-clean/home/.claude","email":null,"orgId":null,"orgName":null,"subscriptionType":"pro"}`},
		{"host_login", `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"private-email"}`},
		{"api_key", `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY","subscriptionType":"pro"}`},
		{"subscription_token", `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty","subscriptionType":"max"}`},
	} {
		for _, mode := range []string{"", "refuse", "honor"} {
			t.Run(tc.source+tc.status+mode, func(t *testing.T) {
				probes := 0
				var output bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&output, nil))
				g := Gates{
					Policy:           PolicyGate{Files: fstest.MapFS{}},
					CredentialSource: tc.source,
					AdminPolicy:      mode,
					Identity: IdentityGate{Probe: func(context.Context, string, string, []string) ([]byte, error) {
						probes++
						return []byte(tc.status), nil
					}},
				}
				auth, err := NewAuthEnv(AuthConfig{CredentialSource: tc.source}, map[string]string{"api_key": "synthetic-secret", "claude_oauth_token": "synthetic-secret"}, []string{"HOME=" + t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				state, next := t.TempDir(), &authStarter{}
				var profile Disclosure
				starter := auth.StarterWithGate(state, next, func(cmd []string, cwd string, env []string) error {
					profile, err = g.Startup(t.Context(), cmd[0], cwd, env, logger)
					return err
				})
				proc, err := starter.Start([]string{"/pinned"}, "/workspace", nil)
				if mode != "honor" {
					if err == nil || !strings.Contains(err.Error(), "potential organisation/server") || !strings.Contains(err.Error(), "admin_policy: honor") || probes != 0 || next.starts != 0 || output.Len() != 0 {
						t.Fatalf("refusal err=%v probes=%d starts=%d log=%s", err, probes, next.starts, &output)
					}
				} else {
					if err != nil || probes != 1 || next.starts != 1 || profile.CredentialSource != tc.source || profile.RuntimeIdentity != "firstParty/"+tc.source || profile.PolicyDisposition != "honored_potential_server_policy" {
						t.Fatalf("honor profile=%+v err=%v probes=%d starts=%d", profile, err, probes, next.starts)
					}
					if err := proc.Wait(); err != nil {
						t.Fatal(err)
					}
					if strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), "honored_potential_server_policy") {
						t.Fatalf("missing truthful log: %s", &output)
					}
				}
				if text := fmt.Sprintf("%+v %v %s", profile, err, &output); strings.Contains(text, "private-email") || strings.Contains(text, "synthetic-secret") || strings.Contains(text, "absent") {
					t.Fatal("private data or false absence disclosed")
				}
				if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
					t.Fatalf("owned root leaked: %v %v", entries, err)
				}
			})
		}
	}
}
