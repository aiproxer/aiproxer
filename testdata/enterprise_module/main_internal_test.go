package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteEssentialConfig_OwnedCleanupRemovesDir(t *testing.T) {
	t.Setenv("AIP_ENTERPRISE_CONFIG", "")
	path, cleanup, err := writeEssentialConfig("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if cleanup == nil {
		t.Fatal("writeEssentialConfig must return an owned cleanup func for a fixture-created config")
	}
	if !strings.HasPrefix(filepath.Base(dir), "aip-enterprise-module-") {
		t.Fatalf("config dir %q must use aip-enterprise-module- prefix", filepath.Base(dir))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file must exist before cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("owned config dir %q must be removed after cleanup", dir)
	}
}

func TestWriteEssentialConfig_ExternalConfigHasNoOwnedCleanup(t *testing.T) {
	external := filepath.Join(t.TempDir(), "external.yaml")
	if err := os.WriteFile(external, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIP_ENTERPRISE_CONFIG", external)
	path, cleanup, err := writeEssentialConfig("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("AIP_ENTERPRISE_CONFIG override must not return an owned cleanup func")
	}
	if path != external {
		t.Fatalf("path=%q want %q", path, external)
	}
}
