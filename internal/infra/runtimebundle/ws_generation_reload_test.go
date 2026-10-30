//go:build integration

package runtimebundle_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/gorilla/websocket"
)

// wsGenRegistry registers the standard bundle (which includes the OpenResponses
// frontend) so a generation can mount and serve its WebSocket surface.
func wsGenRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// openResponsesFrontendID is the standard-bundled OpenResponses frontend id used
// by the generation reload fixtures.
const openResponsesFrontendID = "openresponses"

// openResponsesWSConfig returns a candidate config that enables the OpenResponses
// frontend with WebSocket transport and an idle stub backend.
func openResponsesWSConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "stub-default:gpt-4o",
		},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{
				Kind:    openResponsesFrontendID,
				ID:      openResponsesFrontendID,
				Enabled: true,
				Config: genYAMLNode(t, `
profile: 2026-04-24
base_path: /openresponses/v1
websocket:
  enabled: true
  max_connection_age: 60m
  idle_timeout: 5m
  max_queued_turns: 1
`),
			}},
			Backends: []config.PluginConfig{{
				Kind:    "local-stub",
				ID:      "stub-default",
				Enabled: true,
				Config: genYAMLNode(t, `
text: "stub"
input_tokens: 1
output_tokens: 1
`),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	return cfg
}

func wsGenProcess(t *testing.T, cfg *config.Config, reg *pluginreg.Registry) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func compileWSGeneration(t *testing.T, ps *runtimebundle.ProcessServices, cfg *config.Config) runtimebundle.GenerationRuntime {
	t.Helper()
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })
	return gen
}

// wsGenDial upgrades one WebSocket connection through a generation handler and
// returns the client conn plus the server hosting it (closed by t.Cleanup).
func wsGenDial(t *testing.T, h http.Handler) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/openresponses/v1/responses"
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := d.Dial(u, nil)
	if err != nil {
		t.Fatalf("ws dial through generation handler: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	return conn
}

// wsGenAssertAlive asserts the connection remains open (no close frame) within
// the observation window by expecting a read timeout.
func wsGenAssertAlive(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("connection produced an unexpected frame")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("connection closed unexpectedly: %v", err)
	}
}

// wsGenAssertClosed asserts the connection is closed by the peer.
func wsGenAssertClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func TestGenerationQuiesceClosesOpenResponsesWSSessionsExactlyOnce(t *testing.T) {
	cfg := openResponsesWSConfig(t)
	reg := wsGenRegistry(t)
	ps := wsGenProcess(t, cfg, reg)

	gen := compileWSGeneration(t, ps, cfg)
	conn := wsGenDial(t, gen.Handler())
	wsGenAssertAlive(t, conn)

	if err := gen.Quiesce(context.Background()); err != nil {
		t.Fatalf("generation quiesce: %v", err)
	}
	wsGenAssertClosed(t, conn)
	_ = conn.Close()
}

func TestGenerationReload_OldGenQuiesceClosesOldSessionsNewGenUnaffected(t *testing.T) {
	cfg := openResponsesWSConfig(t)
	reg := wsGenRegistry(t)
	ps := wsGenProcess(t, cfg, reg)

	genOld := compileWSGeneration(t, ps, cfg)
	genNew := compileWSGeneration(t, ps, cfg)

	connOld := wsGenDial(t, genOld.Handler())
	connNew := wsGenDial(t, genNew.Handler())
	wsGenAssertAlive(t, connOld)
	wsGenAssertAlive(t, connNew)

	// Reload retirement quiesces only the old generation.
	if err := genOld.Quiesce(context.Background()); err != nil {
		t.Fatalf("old generation quiesce: %v", err)
	}
	wsGenAssertClosed(t, connOld)
	_ = connOld.Close()

	// The new generation's sessions remain open: no cross-generation shutdown.
	wsGenAssertAlive(t, connNew)
	_ = connNew.Close()
}
