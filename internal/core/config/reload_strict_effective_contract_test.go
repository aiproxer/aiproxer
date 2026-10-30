package config_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

func TestStrictReloadDecode_TableFixtures(t *testing.T) {
	t.Parallel()

	pluginYAML := []byte(`
server:
  address: "127.0.0.1:0"
plugins:
  backends:
    - id: stub
      enabled: true
      config:
        foo: bar
        nested:
          x: 1
          list: [a, b]
`)

	cases := []struct {
		name    string
		raw     []byte
		want    configsource.Category
		checkOK func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "malformed_yaml",
			raw:  []byte("server: [\n"),
			want: configsource.CategoryMalformedYAML,
		},
		{
			name: "multiple_documents",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			want: configsource.CategoryMultipleDocuments,
		},
		{
			name: "trailing_content",
			raw: []byte(`server:
  address: "127.0.0.1:0"
...
this is not yaml document content that decoder may still see
`),
			want: configsource.CategoryTrailingContent,
		},
		{
			name: "unknown_core_field",
			raw: []byte(`server:
  address: "127.0.0.1:0"
not_a_core_field: true
`),
			want: configsource.CategoryUnknownCoreField,
		},
		{
			name: "valid_opaque_plugin_yaml_node",
			raw:  pluginYAML,
			want: configsource.CategoryOK,
			checkOK: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if len(cfg.Plugins.Backends) != 1 {
					t.Fatalf("backends=%d", len(cfg.Plugins.Backends))
				}
				n := cfg.Plugins.Backends[0].Config
				if n.Kind == 0 {
					t.Fatal("expected preserved opaque plugin yaml.Node")
				}
				var into map[string]any
				if err := n.Decode(&into); err != nil {
					t.Fatal(err)
				}
				if into["foo"] != "bar" {
					t.Fatalf("plugin config: %#v", into)
				}
			},
		},
		{
			name: "empty_rejected_before_decode",
			raw:  []byte{},
			want: configsource.CategoryEmpty,
		},
		{
			name: "whitespace_rejected_before_decode",
			raw:  []byte("  \n\t\n"),
			want: configsource.CategoryWhitespace,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, cat, err := strictDecodeContract(tc.raw)
			if cat != tc.want {
				t.Fatalf("category: got %q want %q (err=%v)", cat, tc.want, err)
			}
			if tc.want == configsource.CategoryOK {
				if err != nil || cfg == nil {
					t.Fatalf("want OK, err=%v cfg=%v", err, cfg)
				}
				if tc.checkOK != nil {
					tc.checkOK(t, cfg)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if strings.Contains(msg, "not_a_core_field: true") || strings.Contains(msg, "foo: bar") {
				t.Fatalf("error leaked raw config content: %q", msg)
			}
		})
	}
}

func TestStrictEffectiveLoader_ProductionLoadFileParity_RED(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  []byte
		want configsource.Category
	}{
		{
			name: "unknown_core_field",
			raw: []byte(`server:
  address: "127.0.0.1:0"
not_a_core_field: true
`),
			want: configsource.CategoryUnknownCoreField,
		},
		{
			name: "multiple_documents",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			want: configsource.CategoryMultipleDocuments,
		},
		{
			name: "trailing_content",
			raw: []byte(`server:
  address: "127.0.0.1:0"
...
this is not yaml document content that decoder may still see
`),
			want: configsource.CategoryTrailingContent,
		},
		{
			name: "empty",
			raw:  []byte{},
			want: configsource.CategoryEmpty,
		},
		{
			name: "whitespace",
			raw:  []byte("  \n\t\n"),
			want: configsource.CategoryWhitespace,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/StrictDecode", func(t *testing.T) {
			t.Parallel()
			_, cat, err := config.StrictDecode(tc.raw)
			if cat != tc.want || err == nil {
				t.Fatalf("StrictDecode: cat=%q err=%v want %q", cat, err, tc.want)
			}
			msg := err.Error()
			if strings.Contains(msg, "not_a_core_field: true") {
				t.Fatalf("error leaked raw config: %q", msg)
			}
		})
		t.Run(tc.name+"/LoadFile", func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			if err := os.WriteFile(p, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadFile(p)
			if err == nil {
				t.Fatal("LoadFile must reject via shared strict path")
			}
			msg := err.Error()
			if strings.Contains(msg, "not_a_core_field: true") {
				t.Fatalf("LoadFile error leaked raw config: %q", msg)
			}
			if len(tc.raw) > 0 && strings.Contains(msg, string(tc.raw)) {
				t.Fatalf("LoadFile error leaked raw config: %q", msg)
			}
		})
	}

	t.Run("valid_defaults_via_LoadEffective", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`)
		eff, err := config.LoadEffective(context.Background(), raw, config.LoadEffectiveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if eff.Config.Server.Address != "127.0.0.1:8080" {
			t.Fatalf("default listen: %q", eff.Config.Server.Address)
		}
		if eff.Identity.PublicFingerprint == "" || strings.Contains(eff.Identity.PublicFingerprint, "secret") {
			t.Fatalf("public fingerprint: %q", eff.Identity.PublicFingerprint)
		}
	})
}

func TestStrictReloadSource_ReadStableIntegration_RED(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("server:\n  address: \"127.0.0.1:0\"\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(src.AbsolutePath()) {
		t.Fatalf("AbsolutePath must be absolute: %q", src.AbsolutePath())
	}

	snap1, res, err := src.ReadStable(ctx, nil)
	if err != nil || res != configsource.AtomicEligible {
		t.Fatalf("first read: res=%q err=%v", res, err)
	}
	if snap1.PrivateDigest == ([32]byte{}) || len(snap1.Bytes) == 0 {
		t.Fatal("expected digest and bytes")
	}

	active, activeOwner := snap1.TakeBaseline()
	defer func() {
		if closeErr := activeOwner.Close(ctx); closeErr != nil {
			t.Errorf("close accepted source owner: %v", closeErr)
		}
	}()
	if runtime.GOOS == "linux" && active.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		_, res, err = src.ReadStable(ctx, active)
		if res != configsource.AtomicReject || err == nil {
			t.Fatalf("unsupported Linux active lease must fail closed: res=%q err=%v", res, err)
		}
		if category, ok := configsource.CategoryOf(err); !ok || category != configsource.CategoryNonAtomicUpdate || configsource.SafeReasonOf(err) != "lease_unavailable" {
			t.Fatalf("unsupported Linux comparison must retain bounded non-atomic reason: category=%q ok=%v reason=%q err=%v", category, ok, configsource.SafeReasonOf(err), err)
		}
	} else {
		noop, res, err := src.ReadStable(ctx, active)
		if err != nil || res != configsource.AtomicNoop {
			t.Fatalf("noop re-read: res=%q err=%v", res, err)
		}
		if closeErr := noop.Close(ctx); closeErr != nil {
			t.Fatalf("close no-op snapshot: %v", closeErr)
		}

		changed := []byte("server:\n  address: \"127.0.0.1:9\"\n")
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		_, res, err = src.ReadStable(ctx, active)
		if res != configsource.AtomicReject || err == nil {
			t.Fatalf("in-place rewrite: res=%q err=%v", res, err)
		}
		if cat, ok := configsource.CategoryOf(err); !ok || cat != configsource.CategoryNonAtomicUpdate {
			t.Fatalf("want non-atomic category, got cat=%q ok=%v err=%v", cat, ok, err)
		}

		tmp := filepath.Join(dir, "config.yaml.tmp")
		if err := os.WriteFile(tmp, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		snap2, res, err := src.ReadStable(ctx, active)
		if err != nil || res != configsource.AtomicEligible {
			t.Fatalf("atomic replace: res=%q err=%v", res, err)
		}
		if snap2.HandleIdentity == active.HandleIdentity {
			t.Fatal("atomic replace must change handle identity")
		}
		if closeErr := snap2.Close(ctx); closeErr != nil {
			t.Fatalf("close atomic replacement snapshot: %v", closeErr)
		}
	}

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "nope.yaml")
		s, err := configsource.NewFixedSource(missing, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ReadStable(ctx, nil)
		if cat, ok := configsource.CategoryOf(err); !ok || cat != configsource.CategoryMissing {
			t.Fatalf("want missing, got cat=%q ok=%v err=%v", cat, ok, err)
		}
	})

	t.Run("unsupported_directory", func(t *testing.T) {
		t.Parallel()
		s, err := configsource.NewFixedSource(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ReadStable(ctx, nil)
		if cat, ok := configsource.CategoryOf(err); !ok || cat != configsource.CategoryUnsupportedType {
			t.Fatalf("want unsupported, got cat=%q ok=%v err=%v", cat, ok, err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "big.yaml")
		limit := int64(64)
		if err := os.WriteFile(p, bytes.Repeat([]byte("a"), int(limit)+1), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := configsource.NewFixedSource(p, limit)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ReadStable(ctx, nil)
		if cat, ok := configsource.CategoryOf(err); !ok || cat != configsource.CategoryOversize {
			t.Fatalf("want oversize, got cat=%q ok=%v err=%v", cat, ok, err)
		}
		if err != nil && strings.Contains(err.Error(), strings.Repeat("a", 32)) {
			t.Fatalf("oversize error echoed payload: %v", err)
		}
	})
}

func TestNoopEffectiveIdentity_CommentAndKeyOrder(t *testing.T) {
	t.Parallel()

	base := `
server:
  address: "127.0.0.1:0"
logging:
  level: info
  format: json
`
	withComments := `
# leading comment
server:
  address: "127.0.0.1:0" # inline
logging:
  format: json
  level: info
`

	cfgA, cat, err := strictDecodeContract([]byte(base))
	if err != nil || cat != configsource.CategoryOK {
		t.Fatalf("base: cat=%q err=%v", cat, err)
	}
	cfgB, cat, err := strictDecodeContract([]byte(withComments))
	if err != nil || cat != configsource.CategoryOK {
		t.Fatalf("comments: cat=%q err=%v", cat, err)
	}

	idA, err := computeEffectiveIdentity(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := computeEffectiveIdentity(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	if idA.PrivateDigest != idB.PrivateDigest {
		t.Fatal("comment/key-order must be no-op for private effective identity")
	}
	if idA.PublicFingerprint != idB.PublicFingerprint {
		t.Fatal("comment/key-order must be no-op for public fingerprint")
	}
	if strings.Contains(idA.PublicFingerprint, "secret") {
		t.Fatal("fingerprint must stay secret-safe")
	}
}

func TestNoopEffectiveIdentity_SecretOnlyChange(t *testing.T) {
	t.Parallel()

	mk := func(secret string) *config.Config {
		raw := []byte(`
server:
  address: "127.0.0.1:0"
auth:
  local_api_keys:
    - key_id: k1
      principal_id: p1
      key: "` + secret + `"
`)
		cfg, cat, err := strictDecodeContract(raw)
		if err != nil || cat != configsource.CategoryOK {
			t.Fatalf("decode: cat=%q err=%v", cat, err)
		}
		return cfg
	}

	secretA := "super-secret-key-aaaa"
	secretB := "super-secret-key-bbbb"
	idA, err := computeEffectiveIdentity(mk(secretA))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := computeEffectiveIdentity(mk(secretB))
	if err != nil {
		t.Fatal(err)
	}
	if idA.PrivateDigest == idB.PrivateDigest {
		t.Fatal("secret-only change must alter private identity")
	}
	if idA.PublicFingerprint == "" || idB.PublicFingerprint == "" {
		t.Fatal("public fingerprint required")
	}
	if strings.Contains(idA.PublicFingerprint, secretA) || strings.Contains(idB.PublicFingerprint, secretB) {
		t.Fatalf("public fingerprint leaked secret: %q / %q", idA.PublicFingerprint, idB.PublicFingerprint)
	}
	// Redacted public form should still distinguish key material presence via redaction token stability;
	// both secrets redact to the same token so public fingerprint may match — that is acceptable.
	// Private identity is the no-op authority for secret-bearing equality (req 3.7).
}

func TestEffectiveLoadFile_CharacterizationDefaults(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "127.0.0.1:8080" {
		t.Fatalf("default listen: %q", cfg.Server.Address)
	}
	if cfg.Diagnostics.HealthPath != "/healthz" {
		t.Fatalf("health path: %q", cfg.Diagnostics.HealthPath)
	}
	if cfg.Routing.MaxAttempts != 3 {
		t.Fatalf("max attempts: %d", cfg.Routing.MaxAttempts)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "json" {
		t.Fatalf("logging defaults: %+v", cfg.Logging)
	}
	if cfg.Auth.LocalAPIKeys == nil {
		t.Fatal("LocalAPIKeys must be non-nil empty slice after load")
	}
}
