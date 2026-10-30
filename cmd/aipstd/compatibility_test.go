package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

func TestCheckConfigCompatibility_StrictFixturesRejectSecretSafe(t *testing.T) {
	t.Parallel()
	secret := "sk-check-config-secret-leak"
	cases := []struct {
		name    string
		raw     []byte
		wantCat configsource.Category
	}{
		{
			name:    "malformed",
			raw:     []byte("server: [\napi_key: " + secret + "\n"),
			wantCat: configsource.CategoryMalformedYAML,
		},
		{
			name: "multi_doc",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			wantCat: configsource.CategoryMultipleDocuments,
		},
		{
			name:    "empty",
			raw:     []byte{},
			wantCat: configsource.CategoryEmpty,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cfg.yaml")
			if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errb bytes.Buffer
			code := RunCommand(context.Background(), CommandOptions{
				Name:       CommandCheckConfig,
				ConfigPath: path,
				Output:     &out,
				ErrorOut:   &errb,
			})
			if code == 0 {
				t.Fatal("check-config must reject strict fixture")
			}
			msg := errb.String()
			if strings.Contains(msg, secret) {
				t.Fatalf("stderr leaked secret: %q", msg)
			}
			if !strings.Contains(msg, string(tc.wantCat)) {
				t.Fatalf("stderr=%q want category token %q", msg, tc.wantCat)
			}
		})
	}
}

func TestRoutesInventoryCompatibility_ValidExample(t *testing.T) {
	t.Parallel()
	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)

	t.Run("routes", func(t *testing.T) {
		t.Parallel()
		var out, errb bytes.Buffer
		code := RunCommand(context.Background(), CommandOptions{
			Name:       CommandRoutes,
			ConfigPath: cfgPath,
			Output:     &out,
			ErrorOut:   &errb,
		})
		if code != 0 {
			t.Fatalf("routes exit %d stderr=%s", code, errb.String())
		}
		if !bytes.Contains(out.Bytes(), []byte(`"effective_default_route"`)) {
			t.Fatalf("routes stdout: %s", out.String())
		}
	})
	t.Run("inventory", func(t *testing.T) {
		t.Parallel()
		var out, errb bytes.Buffer
		code := RunCommand(context.Background(), CommandOptions{
			Name:       CommandInventory,
			ConfigPath: cfgPath,
			Output:     &out,
			ErrorOut:   &errb,
		})
		if code != 0 {
			t.Fatalf("inventory exit %d stderr=%s", code, errb.String())
		}
		if !bytes.Contains(out.Bytes(), []byte(`"frontends"`)) {
			t.Fatalf("inventory stdout: %s", out.String())
		}
	})
}

func TestCheckConfigCompatibility_MissingPathCategory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.yaml")
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: path,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code == 0 {
		t.Fatal("expected failure")
	}
	msg := errb.String()
	if !strings.Contains(msg, string(configsource.CategoryMissing)) {
		t.Fatalf("stderr=%q want missing category %q", msg, configsource.CategoryMissing)
	}
}
