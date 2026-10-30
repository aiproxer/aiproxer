package runtimebundle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestBuild_continuityPostgres_unreachableDoesNotFallback(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	const secret = "NEVER_EMBED_THIS_SECRET"
	dsn := "postgres://u:" + secret + "@127.0.0.1:1/nosuch?sslmode=disable"
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(msg, "continuity") {
		t.Fatalf("want continuity context, got: %s", msg)
	}
}

func TestBuild_startupContextCanceled_continuityPostgres(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	startupCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: "postgres://u:p@127.0.0.1:59999/nosuch?sslmode=disable",
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Startup:        runtimebundle.StartupOptions{StartupContext: startupCtx},
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled in error chain, got: %v", err)
	}
}

func TestBuild_disposesContinuityWhenSecureSessionFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	dbpath := filepath.Join(tmp, "continuity.db")
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: dbpath,
		},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         "postgres://x:y@127.0.0.1:1/db?sslmode=disable",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if rmErr := os.Remove(dbpath); rmErr != nil {
		t.Fatalf("continuity sqlite file should be released after failed secure_session open: %v", rmErr)
	}
}

func TestBuild_secureSessionPostgres_unreachableDurableAuditDoesNotFallback(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	const secret = "NEVER_EMBED_SS_SECRET"
	dsn := "postgres://u:" + secret + "@127.0.0.1:1/nosuch?sslmode=disable"
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         dsn,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "secure_session") {
		t.Fatalf("want secure_session context, got: %s", msg)
	}
}
