package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

// TestCheckConfig_omittedContinuityBlockIsValid proves the operator verdict
// agrees with the serving runtime on the continuity backing. A configuration
// without a continuity block resolves to the process-local backing, so
// check-config accepts it exactly as the serving runtime builds it.
func TestCheckConfig_omittedContinuityBlockIsValid(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := stripContinuityBlock(t, string(raw))
	path := filepath.Join(t.TempDir(), "check-config-omitted-continuity.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: path,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout=%q", out.String())
	}
}

// TestCheckConfig_refusesUnparsableContinuityTTL proves the memory backing's
// eviction window is checked by check-config. The same value used to be
// refused only when the serving runtime constructed the store, so the operator
// was told "configuration is valid" and then the process refused to start.
func TestCheckConfig_refusesUnparsableContinuityTTL(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "  in_memory: true\n  store: memory\n", "  in_memory: true\n  store: memory\n  ttl: not-a-duration\n", 1)
	if body == string(raw) {
		t.Fatal("dogfood fixture missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "check-config-bad-continuity-ttl.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
		t.Fatalf("check-config accepted an unparsable continuity ttl: stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "continuity.ttl") {
		t.Fatalf("stderr=%q", errb.String())
	}
}

// TestCheckConfig_refusesContinuityInMemoryWithDurableStore proves check-config
// refuses the contradiction between the legacy in-memory switch and a durable
// store. The same block used to load and run with the durable store silently
// downgraded to the process-local one.
func TestCheckConfig_refusesContinuityInMemoryWithDurableStore(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(raw),
		"  in_memory: true\n  store: memory\n",
		"  in_memory: true\n  store: postgres\n  postgres_dsn: \"postgres://u:p@127.0.0.1:1/aip?sslmode=disable\"\n",
		1,
	)
	if body == string(raw) {
		t.Fatal("dogfood fixture missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "check-config-contradictory-continuity.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
		t.Fatalf("check-config accepted the legacy in-memory switch with a durable store: stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "continuity.in_memory") {
		t.Fatalf("stderr does not name the offending field: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "remove in_memory or set store: memory") {
		t.Fatalf("stderr does not name the remedy: %q", errb.String())
	}
}

// TestCheckConfig_acceptsContinuityInMemoryOnly proves the legacy spelling
// alone is still a valid configuration: in_memory: true is the legacy spelling
// of the default backing.
func TestCheckConfig_acceptsContinuityInMemoryOnly(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "  in_memory: true\n  store: memory\n", "  in_memory: true\n", 1)
	if body == string(raw) {
		t.Fatal("dogfood fixture missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "check-config-in-memory-only.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: path,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout=%q", out.String())
	}
}

// TestCheckConfig_refusesContinuitySQLitePathWithMemoryStore proves check-config
// refuses a sqlite_path that the effective memory store would ignore. The same
// block used to be reported as valid and then serve without persistence.
func TestCheckConfig_refusesContinuitySQLitePathWithMemoryStore(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(raw),
		"  in_memory: true\n  store: memory\n",
		"  in_memory: true\n  store: memory\n  sqlite_path: ./data/ignored-continuity.db\n",
		1,
	)
	if body == string(raw) {
		t.Fatal("dogfood fixture missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "check-config-memory-with-sqlite-path.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
		t.Fatalf("check-config accepted a sqlite_path the memory store ignores: stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "continuity.sqlite_path") {
		t.Fatalf("stderr does not name the offending field: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "remove sqlite_path or set store: sqlite") {
		t.Fatalf("stderr does not name the remedy: %q", errb.String())
	}
}

// TestCheckConfig_refusesUnknownContinuityStoreBeforeLegacyConflict proves the
// operator-facing diagnostic names the unknown backing, not the legacy switch.
// A validator-level test cannot prove this: the loader could mask the
// operator-supplied store name before the validator ever sees it.
func TestCheckConfig_refusesUnknownContinuityStoreBeforeLegacyConflict(t *testing.T) {
	t.Parallel()
	srcPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(raw),
		"  in_memory: true\n  store: memory\n",
		"  in_memory: true\n  store: \" ReDiS \"\n",
		1,
	)
	if body == string(raw) {
		t.Fatal("dogfood fixture missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "check-config-unknown-continuity-store.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
		t.Fatalf("check-config accepted an unknown continuity store: stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "continuity.store: want memory, sqlite, or postgres") {
		t.Fatalf("stderr does not name the unknown backing: %q", errb.String())
	}
	if strings.Contains(errb.String(), "continuity.in_memory") {
		t.Fatalf("stderr blames the legacy switch instead of the unknown backing: %q", errb.String())
	}
}

// stripContinuityBlock removes the whole continuity block from an example so the
// configuration under test carries no continuity stanza at all.
func stripContinuityBlock(t *testing.T, body string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	found := false
	for _, line := range lines {
		if !skipping && strings.TrimSpace(line) == "continuity:" {
			skipping = true
			found = true
			continue
		}
		if skipping {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				continue
			}
			skipping = false
		}
		out = append(out, line)
	}
	if !found {
		t.Fatal("fixture has no continuity block")
	}
	return strings.Join(out, "\n")
}
