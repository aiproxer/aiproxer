package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestOpenContinuityStore_nilContext(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(nil, &config.Config{Continuity: config.ContinuityConfig{ //nolint:staticcheck // contract: nil ctx
		InMemory: true,
		Store:    "memory",
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenContinuityStore_nilConfig(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenContinuityStore_sqlite_requiresPath(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: config.ContinuityConfig{
		InMemory: false,
		Store:    "sqlite",
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sqlite_path") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenContinuityStore_memory_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: config.ContinuityConfig{
		InMemory: true,
		MaxLegs:  -1,
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "max_legs") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenContinuityStore_memory_happyPath(t *testing.T) {
	t.Parallel()
	cfg := config.ContinuityConfig{
		InMemory: true,
		TTL:      "24h",
		MaxLegs:  42,
	}
	store, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
	store2, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if store2 == nil {
		t.Fatal("nil store from OpenContinuityStore")
	}
}

func TestOpenContinuityStore_postgres_doesNotLeakPasswordInError(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_PASSWORD_XY"
	dsn := "postgres://user:" + secret + "@127.0.0.1:1/nosuchdb?sslmode=disable"
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
		Database: config.DatabaseConfig{MaxOpenConns: 2},
	})
	if err == nil {
		t.Fatal("expected error for unreachable postgres")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(msg, "continuity") {
		t.Fatalf("want continuity context in error: %s", msg)
	}
}

func TestNewMemoryContinuityStore_inMemoryFalse(t *testing.T) {
	t.Parallel()
	store, err := runtimebundle.NewMemoryContinuityStore(config.ContinuityConfig{InMemory: false})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestNewMemoryContinuityStore_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	store, err := runtimebundle.NewMemoryContinuityStore(config.ContinuityConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

// TestBuildHost_servesContinuityInMemoryOnly proves the serving runtime
// accepts the legacy in-memory spelling on its own. in_memory: true is the
// legacy spelling of the default backing, so a configuration that only sets it
// must still load and serve.
func TestBuildHost_servesContinuityInMemoryOnly(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "  in_memory: true\n  store: memory\n", "  in_memory: true\n", 1)
	if body == string(raw) {
		t.Fatal("sample config missing the continuity block")
	}
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	if runtimebundle.HostProcess(host) == nil {
		t.Fatal("BuildHost must publish process services")
	}
}
