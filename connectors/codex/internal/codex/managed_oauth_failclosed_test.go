package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const managedFailClosedDecoyToken = "decoy-ambient-token-failclosed"

// plantAmbientDecoyAuthJSON writes a decoy ambient ~/.codex/auth.json under a
// temp HOME and points HOME at it. The managed-OAuth store is the sole
// credential source, so this file must never satisfy admission.
// Not parallel-safe by construction: mutating process HOME forbids t.Parallel,
// mirroring the static-mode ambient-ignore proof.
func plantAmbientDecoyAuthJSON(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"access_token": "` + managedFailClosedDecoyToken + `"}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func requireManagedFailClosed(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s admitted without a usable managed oauth store", what)
	}
	if strings.Contains(err.Error(), managedFailClosedDecoyToken) {
		t.Fatalf("%s leaks the ambient decoy token: %v", what, err)
	}
	if !strings.Contains(err.Error(), ID) {
		t.Fatalf("%s error must name the backend: %v", what, err)
	}
	normalized := strings.ReplaceAll(strings.ToLower(err.Error()), "_", " ")
	if !strings.Contains(normalized, "managed oauth") {
		t.Fatalf("%s error must name the managed oauth store: %v", what, err)
	}
}

// TestResolveBackendConfig_ManagedOAuthFailsClosedWithoutUsableStore proves
// the managed-OAuth path fails closed when no usable store is available: an
// empty, missing, empty, or unusable-only storage dir returns a secret-free
// error, and a planted ambient ~/.codex/auth.json neither satisfies admission
// nor changes the outcome (no file fallback).
func TestResolveBackendConfig_ManagedOAuthFailsClosedWithoutUsableStore(t *testing.T) {
	plantAmbientDecoyAuthJSON(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	empty := t.TempDir()
	unusable := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(unusable, "account.json"),
		[]byte(`{"account_id": "acct-1"}`), // no access token: skipped, not admitted
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	for name, storagePath := range map[string]string{
		"empty storage path": "",
		"missing dir":        missing,
		"empty dir":          empty,
		"unusable-only dir":  unusable,
	} {
		_, _, err := resolveBackendConfig(Config{
			BaseURL:                 "http://127.0.0.1:9",
			ManagedOAuthEnabled:     true,
			ManagedOAuthStoragePath: storagePath,
		})
		requireManagedFailClosed(t, err, "storage "+name)
	}
}

// TestNew_ManagedOAuthFailsClosedWithoutUsableStore proves the public
// constructor carries the same fail-closed behavior: with managed OAuth
// enabled and no usable store, the engine reports the configuration error on
// Open instead of admitting via ambient files.
func TestNew_ManagedOAuthFailsClosedWithoutUsableStore(t *testing.T) {
	plantAmbientDecoyAuthJSON(t)

	engine, err := New(Config{
		BaseURL:                 "http://127.0.0.1:9",
		ManagedOAuthEnabled:     true,
		ManagedOAuthStoragePath: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err != nil {
		t.Fatalf("New must surface store failure as a config-error engine, not a constructor error: %v", err)
	}
	call := aipapi.Call{}
	_, openErr := engine.Open(context.Background(), &call, routingstub.AttemptCandidate{})
	requireManagedFailClosed(t, openErr, "engine Open")
}
