package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func writeOAuthTokenFile(t *testing.T, rec oauthcred.TokenRecord) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens.json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal token record: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

func TestConfigure_PreProvisionedRefreshOnly_MissingCredential(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "tokens.json")
	svc := NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: FactoryKind,
		ConfigYAML:  []byte("oauth_client_id: client-1\noauth_token_file: " + filepath.ToSlash(missing) + "\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail without a credential, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, oauthcred.LoginModePreProvisionedRefreshOnly) {
		t.Fatalf("error must mark qwen-oauth as pre-provisioned-refresh-only, got: %v", err)
	}
	if !strings.Contains(msg, filepath.ToSlash(missing)) {
		t.Fatalf("error must name the credential path, got: %v", err)
	}
	if strings.Contains(msg, "no such file") {
		t.Fatalf("error must be actionable, not a raw file-not-found, got: %v", err)
	}
}

func TestConfigure_PreProvisionedRefreshOnly_NoTokenFileConfigured(t *testing.T) {
	svc := NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: FactoryKind,
		ConfigYAML:  []byte("oauth_client_id: client-1\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail without credentials, got nil")
	}
	if !strings.Contains(err.Error(), oauthcred.LoginModePreProvisionedRefreshOnly) {
		t.Fatalf("error must mark qwen-oauth as pre-provisioned-refresh-only, got: %v", err)
	}
}

func TestConfigure_PreProvisionedRefreshOnly_MissingClientID(t *testing.T) {
	tokenPath := writeOAuthTokenFile(t, oauthcred.TokenRecord{RefreshToken: "pre-provisioned-refresh"})
	svc := NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: FactoryKind,
		ConfigYAML:  []byte("oauth_token_file: " + filepath.ToSlash(tokenPath) + "\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail without oauth_client_id, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "oauth_client_id is required") {
		t.Fatalf("error must keep the oauth_client_id requirement, got: %v", err)
	}
	if !strings.Contains(msg, oauthcred.LoginModePreProvisionedRefreshOnly) {
		t.Fatalf("error must mark qwen-oauth as pre-provisioned-refresh-only, got: %v", err)
	}
}

func TestConfigure_PreProvisionedRefreshOnly_RefreshTokenAccepted(t *testing.T) {
	// A pre-provisioned refresh-only record must Configure successfully; the
	// unchanged refresh path mints access tokens on demand.
	tokenPath := writeOAuthTokenFile(t, oauthcred.TokenRecord{RefreshToken: "pre-provisioned-refresh"})
	svc := NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: FactoryKind,
		ConfigYAML:  []byte("oauth_client_id: client-1\noauth_token_file: " + filepath.ToSlash(tokenPath) + "\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err != nil {
		t.Fatalf("Configure with pre-provisioned refresh token failed: %v", err)
	}
	if inst == nil {
		t.Fatalf("expected non-nil instance")
	}
}
