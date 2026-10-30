package stdhttp

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func withRunningAsAdmin(t *testing.T, fn func() (bool, error)) {
	t.Helper()
	old := runningAsAdmin
	runningAsAdmin = fn
	t.Cleanup(func() { runningAsAdmin = old })
}

//nolint:paralleltest // tests mutate package-level runningAsAdmin hook
func TestValidateStartupSecurity_rejectsAdminUser(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return true, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:8080"}}
	err := validateStartupSecurity(cfg)
	if err == nil || !strings.Contains(err.Error(), "administrative") {
		t.Fatalf("want administrative rejection, got %v", err)
	}
}

//nolint:paralleltest // tests mutate package-level runningAsAdmin hook
func TestValidateStartupSecurity_rejectsNoAuthNonLoopback(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeNoAuth}}
	err := validateStartupSecurity(cfg)
	if err == nil || !strings.Contains(err.Error(), "no_auth") {
		t.Fatalf("want no_auth loopback rejection, got %v", err)
	}
}

//nolint:paralleltest // tests mutate package-level runningAsAdmin hook
func TestValidateStartupSecurity_allowsLoopbackNonAdmin(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:8080"}}
	if err := validateStartupSecurity(cfg); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // tests mutate package-level runningAsAdmin hook
func TestValidateStartupSecurity_rejectsNonLoopbackDiagnosticsWithoutSecret(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth:   config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server: config.ServerConfig{Address: "10.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			AttemptsPath: "/admin/attempts",
			SharedSecret: "",
		},
	}
	err := validateStartupSecurity(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") {
		t.Fatalf("want diagnostics posture error, got %v", err)
	}
}

//nolint:paralleltest // tests mutate package-level runningAsAdmin hook
func TestValidateStartupSecurity_allowsNonLoopbackDiagnosticsWithSecret(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth:   config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server: config.ServerConfig{Address: "10.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			AttemptsPath: "/admin/attempts",
			SharedSecret: "twelve-chars-minimum-secret",
		},
	}
	if err := validateStartupSecurity(cfg); err != nil {
		t.Fatal(err)
	}
}
