package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func secureSessionBaselinePlugins() config.PluginsConfig {
	return config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
	}
}

func TestValidate_secureSession_explicitDisabledRejected(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(false),
			Store:               "sqlite",
			TokenFingerprintKey: "",
			AuditDurability:     "durable",
			ResumeWindow:        "not-a-duration",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "secure_session.enabled") {
		t.Fatalf("want enabled:false rejection got %v", err)
	}
}

func TestValidate_secureSession_workspaceResolveOnError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:                 config.BoolPtr(true),
			Store:                   "memory",
			TokenFingerprintKey:     strings.Repeat("k", 32),
			WorkspaceResolveOnError: "maybe",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "workspace_resolve_on_error") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_requiresLongFingerprintKeyWhenSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		store     string
		mutateCfg func(*config.Config)
	}{
		{name: "sqlite", store: "sqlite"},
		{name: "memory", store: "memory"},
		{name: "postgres", store: "postgres", mutateCfg: func(c *config.Config) {
			c.SecureSession.PostgresDSN = "postgres://user:pass@127.0.0.1:1/db?sslmode=disable"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Plugins: secureSessionBaselinePlugins(),
				SecureSession: config.SecureSessionConfig{
					Enabled:             config.BoolPtr(true),
					Store:               tc.store,
					TokenFingerprintKey: "short",
					AuditDurability:     "best_effort",
				},
			}
			if tc.mutateCfg != nil {
				tc.mutateCfg(cfg)
			}
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "token_fingerprint_key") {
				t.Fatalf("want token_fingerprint_key error, got %v", err)
			}
		})
	}
}

func TestValidate_secureSession_memoryEmptyTokenKeyOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: "",
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_resumeWindow(t *testing.T) {
	t.Parallel()
	t.Run("invalid parse", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Plugins: secureSessionBaselinePlugins(),
			SecureSession: config.SecureSessionConfig{
				Enabled:             config.BoolPtr(true),
				Store:               "memory",
				TokenFingerprintKey: strings.Repeat("k", 32),
				ResumeWindow:        "not-a-duration",
			},
		}
		err := config.Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "resume_window") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non positive", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Plugins: secureSessionBaselinePlugins(),
			SecureSession: config.SecureSessionConfig{
				Enabled:             config.BoolPtr(true),
				Store:               "memory",
				TokenFingerprintKey: strings.Repeat("k", 32),
				ResumeWindow:        "0s",
			},
		}
		err := config.Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "resume_window") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestValidate_secureSession_auditDurableRequiresDurableStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: strings.Repeat("k", 32),
			AuditDurability:     "durable",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "audit_durability") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_strictWithMemoryBestEffortOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: strings.Repeat("k", 32),
			AuditDurability:     "best_effort",
			NonDurableWarning:   "strict",
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_diagnosticsExposeRequiresPrefix(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:                    config.BoolPtr(true),
			Store:                      "memory",
			TokenFingerprintKey:        strings.Repeat("k", 32),
			DiagnosticsExposeSummaries: true,
			DiagnosticsPathPrefix:      "",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics_path_prefix") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_diagnosticsExposeRequiresDiagnosticsSharedSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     config.ServerConfig{Address: "10.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    secureSessionBaselinePlugins(),
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: "",
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:                    config.BoolPtr(true),
			Store:                      "memory",
			TokenFingerprintKey:        strings.Repeat("k", 32),
			DiagnosticsExposeSummaries: true,
			DiagnosticsPathPrefix:      "/debug/sessions",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_diagnosticsExposeLoopbackAllowsEmptySharedSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    secureSessionBaselinePlugins(),
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: "",
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:                    config.BoolPtr(true),
			Store:                      "memory",
			TokenFingerprintKey:        strings.Repeat("k", 32),
			DiagnosticsExposeSummaries: true,
			DiagnosticsPathPrefix:      "/debug/sessions",
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_diagnosticsExposeOKWithSharedSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: "twelve-chars-minimum-secret",
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:                    config.BoolPtr(true),
			Store:                      "memory",
			TokenFingerprintKey:        strings.Repeat("k", 32),
			DiagnosticsExposeSummaries: true,
			DiagnosticsPathPrefix:      "/debug/sessions",
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_minimalEnabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: strings.Repeat("k", 32),
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_sqliteRequiresPath(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			TokenFingerprintKey: strings.Repeat("k", 32),
			SQLitePath:          "   ",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "secure_session.sqlite_path") {
		t.Fatalf("want sqlite_path required, got %v", err)
	}
}

func TestValidate_secureSession_sqlitePathRejectsAmbiguousQueryChars(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			TokenFingerprintKey: strings.Repeat("k", 32),
			SQLitePath:          "./data/x?bad=1",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "secure_session.sqlite_path") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_sqliteWithPathOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			TokenFingerprintKey: strings.Repeat("k", 32),
			SQLitePath:          filepath.Join("data", "secure_sessions.db"),
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_sqliteEmptyTokenKeyRejected(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			SQLitePath:          filepath.Join(t.TempDir(), "ss.db"),
			TokenFingerprintKey: "",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "token_fingerprint_key") {
		t.Fatalf("want token key error, got %v", err)
	}
}

func TestLoadFile_normalizesSecureSessionStoreWhenEnabled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	yml := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
secure_session:
  enabled: true
  token_fingerprint_key: "01234567890123456789012345678901"
`
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SecureSessionEffectivelyEnabled() {
		t.Fatal("expected enabled from fixture")
	}
	if cfg.SecureSession.Enabled == nil || !*cfg.SecureSession.Enabled {
		t.Fatal("expected explicit enabled true in fixture")
	}
	if strings.ToLower(strings.TrimSpace(cfg.SecureSession.Store)) != "memory" {
		t.Fatalf("store: %q", cfg.SecureSession.Store)
	}
}
