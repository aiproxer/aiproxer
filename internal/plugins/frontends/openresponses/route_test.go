package openresponses_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestRoute_DefaultClaimsCoexistWithOpenAI(t *testing.T) {
	t.Parallel()

	reg := httpcontract.NewRouteRegistry()

	// Register existing openai-responses routes
	openaiClaims, err := openairesponses.RouteClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openaiClaims); err != nil {
		t.Fatal(err)
	}

	// Register default openresponses claims
	orClaims, err := openresponses.RouteClaims(openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/openresponses/v1",
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := reg.RegisterAll(orClaims); err != nil {
		t.Fatalf("default OpenResponses routes should coexist with OpenAI: %v", err)
	}

	// Verify route claims count (create, compact, ws)
	if len(orClaims) != 3 {
		t.Fatalf("expected 3 route claims, got %d", len(orClaims))
	}
}

func TestRoute_CanonicalPathTakeoverConflict(t *testing.T) {
	t.Parallel()

	reg := httpcontract.NewRouteRegistry()
	openaiClaims, err := openairesponses.RouteClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openaiClaims); err != nil {
		t.Fatal(err)
	}

	// Try mounting openresponses at /v1 (canonical legacy base path)
	claims, err := openresponses.RouteClaims(openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/v1",
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = reg.ValidateCanonicalPathTakeover(httpcontract.CanonicalLegacyBasePath, claims)
	if err == nil {
		t.Fatal("expected canonical path takeover conflict")
	}
	if _, ok := errors.AsType[httpcontract.RouteConflictError](err); !ok {
		t.Fatalf("expected RouteConflictError error, got %T (%v)", err, err)
	}
}

func TestRoute_RemapBasePath(t *testing.T) {
	t.Parallel()

	claims, err := openresponses.RouteClaims(openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/custom/v1",
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(claims) != 3 {
		t.Fatalf("expected 3 claims, got %d", len(claims))
	}

	for _, c := range claims {
		if !strings.HasPrefix(c.Path, "/custom/v1") {
			t.Errorf("claim path %q does not have prefix /custom/v1", c.Path)
		}
	}
}

func TestRoute_WebSocketDisabledClaims(t *testing.T) {
	t.Parallel()

	claims, err := openresponses.RouteClaims(openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/openresponses/v1",
		WebSocket: openresponses.WebSocketConfig{
			Enabled: false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(claims) != 2 {
		t.Fatalf("expected 2 claims when WS disabled, got %d", len(claims))
	}

	for _, c := range claims {
		if c.Kind == httpcontract.RouteKind("openresponses_websocket") {
			t.Errorf("found WebSocket claim when WS was disabled")
		}
	}
}

func TestRoute_RegisterClaimsIsAtomic(t *testing.T) {
	t.Parallel()

	reg := httpcontract.NewRouteRegistry()
	openaiClaims, err := openairesponses.RouteClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openaiClaims); err != nil {
		t.Fatal(err)
	}

	before := len(reg.Claims())
	_, err = openresponses.RegisterClaims(reg, openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/v1",
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	})
	if err == nil {
		t.Fatal("expected canonical route collision")
	}
	if got := len(reg.Claims()); got != before {
		t.Fatalf("registry changed after failed registration: before=%d after=%d", before, got)
	}
}

func TestRoute_RejectsUnsafeOwnerIdentity(t *testing.T) {
	t.Parallel()

	if _, err := openresponses.RouteClaimsForOwner(openresponses.Config{
		Profile:  openresponses.DefaultProfile,
		BasePath: openresponses.DefaultBasePath,
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          false,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	}, "owner\nforged"); err == nil {
		t.Fatal("expected control characters in owner identity to be rejected")
	}
}

func TestRoute_SanitizedDiagnostics(t *testing.T) {
	t.Parallel()

	cfg := openresponses.Config{
		Profile:  "2026-04-24",
		BasePath: "/openresponses/v1\ncontrol",
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
			AllowedOrigins:   []string{"https://example.com\r\n"},
		},
	}

	diags := openresponses.SanitizedDiagnostics(cfg, "openresponses")
	if strings.Contains(diags.BasePath, "\n") {
		t.Errorf("base path not sanitized: %q", diags.BasePath)
	}
	if len(diags.AllowedOrigins) > 0 && strings.Contains(diags.AllowedOrigins[0], "\r") {
		t.Errorf("origin not sanitized: %q", diags.AllowedOrigins[0])
	}
}

func TestRoute_DiagnosticsUseSanitizedOwner(t *testing.T) {
	t.Parallel()

	diags := openresponses.SanitizedDiagnostics(openresponses.Config{
		Profile:  openresponses.DefaultProfile,
		BasePath: openresponses.DefaultBasePath,
		WebSocket: openresponses.WebSocketConfig{
			Enabled:          false,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	}, "instance\n42")
	if len(diags.RouteClaims) == 0 {
		t.Fatal("expected route diagnostics")
	}
	if got := diags.RouteClaims[0].OwnerID; got != "instance42" {
		t.Fatalf("owner=%q, want sanitized owner %q", got, "instance42")
	}
}

func TestMount_RegistersRoutes(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	var node yaml.Node
	_ = yaml.Unmarshal([]byte("{}"), &node)

	opts := aipsdk.FrontendMountOptions{
		PluginCfg: node,
	}

	if err := openresponses.Mount(mux, opts); err != nil {
		t.Fatalf("Mount failed: %v", err)
	}
}
