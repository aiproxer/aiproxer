package runtimebundle

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestBuildSessionAuditPolicy_defaults(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:9"},
		Access: config.AccessConfig{Mode: "single_user"},
		Auth:   config.AuthConfig{Handler: "local_noop", RequiredLevel: "none"},
	}
	pol, err := buildSessionAuditPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if pol.AccessMode != sdkauth.AccessSingleUser {
		t.Fatalf("access: %q", pol.AccessMode)
	}
	if pol.HandlerKind != sdkauth.HandlerLocalNoop || pol.RequiredLevel != sdkauth.LevelNone {
		t.Fatalf("auth: %#v", pol)
	}
}

func TestBuildSessionAuditPolicy_multiUserAccessMode(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "0.0.0.0:8080"},
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth:   config.AuthConfig{Handler: "local_api_key", RequiredLevel: "api_key"},
	}
	pol, err := buildSessionAuditPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if pol.AccessMode != sdkauth.AccessMultiUser {
		t.Fatalf("access: want %q got %q", sdkauth.AccessMultiUser, pol.AccessMode)
	}
	if pol.HandlerKind != sdkauth.HandlerLocalAPIKey || pol.RequiredLevel != sdkauth.LevelAPIKey {
		t.Fatalf("auth: %#v", pol)
	}
}

func TestBuildSessionAuditPolicy_nilConfig(t *testing.T) {
	t.Parallel()
	_, err := buildSessionAuditPolicy(nil)
	if err == nil {
		t.Fatal("want error for nil config")
	}
}

func TestBuildSessionAuditPolicy_unknownHandler(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:9"},
		Access: config.AccessConfig{Mode: "single_user"},
		Auth:   config.AuthConfig{Handler: "saml", RequiredLevel: "none"},
	}
	_, err := buildSessionAuditPolicy(cfg)
	if err == nil {
		t.Fatal("want error for unknown handler")
	}
}

func TestBuildSessionAuditPolicy_unknownRequiredLevel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:9"},
		Access: config.AccessConfig{Mode: "single_user"},
		Auth:   config.AuthConfig{Handler: "local_noop", RequiredLevel: "mTLS"},
	}
	_, err := buildSessionAuditPolicy(cfg)
	if err == nil {
		t.Fatal("want error for unknown required_level")
	}
}
