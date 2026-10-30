package diag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestRouteSelectorDigest_stableAndBounded(t *testing.T) {
	t.Parallel()
	raw := "SECRETSEL:expensive-model"
	got := diag.RouteSelectorDigest(raw)
	if got == "" || got == raw {
		t.Fatalf("digest=%q must be a non-empty hash, not the raw selector", got)
	}
	if len(got) != 16 {
		t.Fatalf("digest length=%d want 16 hex chars", len(got))
	}
	if diag.RouteSelectorDigest(raw) != got {
		t.Fatal("digest must be stable")
	}
	if diag.RouteSelectorDigest(raw+"x") == got {
		t.Fatal("digest must change when selector changes")
	}
}

func TestLogRouteOverrideMutation_omitsRawSelectorAndALeg(t *testing.T) {
	t.Parallel()
	const (
		aLeg = "SECRET_ALEG_xyz"
		sel  = "SECRETSEL:gpt-4"
	)
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	diag.LogRouteOverrideMutation(context.Background(), log, diag.RouteOverrideMutation{
		Action:        "set",
		Outcome:       "ok",
		Revision:      1,
		ALegID:        aLeg,
		Selector:      sel,
		SelectorBytes: len(sel),
		Active:        true,
	})
	line := buf.String()
	if line == "" {
		t.Fatal("expected mutation log line")
	}
	if strings.Contains(line, aLeg) {
		t.Fatalf("raw A-leg leaked into mutation log: %s", line)
	}
	if strings.Contains(line, sel) || strings.Contains(line, "SECRETSEL") {
		t.Fatalf("raw selector leaked into mutation log: %s", line)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("json: %v body=%s", err, line)
	}
	if m["msg"] != diag.RouteOverrideMutationLogMsg {
		t.Fatalf("msg=%v want %q", m["msg"], diag.RouteOverrideMutationLogMsg)
	}
	if m["action"] != "set" || m["outcome"] != "ok" {
		t.Fatalf("action/outcome: %#v", m)
	}
	if m["a_leg_hash"] != diag.BoundedALegID(aLeg) {
		t.Fatalf("a_leg_hash=%v want %s", m["a_leg_hash"], diag.BoundedALegID(aLeg))
	}
	if m["selector_digest"] != diag.RouteSelectorDigest(sel) {
		t.Fatalf("selector_digest=%v want %s", m["selector_digest"], diag.RouteSelectorDigest(sel))
	}
	if m["selector_bytes"] != float64(len(sel)) {
		t.Fatalf("selector_bytes=%v want %d", m["selector_bytes"], len(sel))
	}
}

func TestLogRouteOverrideMutation_inactiveOmitsDigest(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	diag.LogRouteOverrideMutation(context.Background(), log, diag.RouteOverrideMutation{
		Action:   "clear",
		Outcome:  "ok",
		Revision: 2,
		ALegID:   "a_cleared",
		Selector: "should-not-appear",
		Active:   false,
	})
	line := buf.String()
	if strings.Contains(line, "should-not-appear") || strings.Contains(line, "a_cleared") {
		t.Fatalf("inactive mutation log leaked identity: %s", line)
	}
	if strings.Contains(line, "selector_digest") {
		t.Fatalf("inactive mutation must omit selector_digest: %s", line)
	}
}

func TestLogRouteOverrideMutation_nilLoggerNoPanic(t *testing.T) {
	t.Parallel()
	diag.LogRouteOverrideMutation(context.Background(), nil, diag.RouteOverrideMutation{Action: "noop"})
}
