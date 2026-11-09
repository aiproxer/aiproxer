package adapter

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	testSIWCOmissionMarker = "siwc_preview_omission_v1"
	testOperatorWarning    = "openai-chatgpt-plan: request degraded under the SIWC preview policy"
)

// TestManagedStream_ConsumesExactSIWCOmissionMarkerAndLogsOnce pins the host
// half of the F3 paired fix:
//
//   - the exact private marker is never forwarded to the canonical client stream;
//   - the operator logger records one fixed, content-free warning per attempt;
//   - a duplicate marker cannot flood the log;
//   - ordinary canonical warnings, including a near-miss marker, pass unchanged
//     and are never copied into the operator log.
func TestManagedStream_ConsumesExactSIWCOmissionMarkerAndLogsOnce(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s := &managedStream{
		ctx:      context.Background(),
		opt:      Options{InstanceID: "plan-instance"},
		events:   make(chan aipapi.Event, 4),
		done:     make(chan struct{}),
		maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes),
	}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatal(err)
	}
	warningFrame := func(sequence uint64, text string) backendplugin.ServerFrame {
		value := text
		return backendplugin.ServerFrame{
			Kind:     backendplugin.ServerFrameEvent,
			Sequence: sequence,
			Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventWarning, Warning: &value},
		}
	}

	if err := s.onPluginFrame(warningFrame(1, testSIWCOmissionMarker)); err != nil {
		t.Fatal(err)
	}
	if err := s.onPluginFrame(warningFrame(2, testSIWCOmissionMarker)); err != nil {
		t.Fatal(err)
	}
	nearMiss := testSIWCOmissionMarker + "_ordinary"
	if err := s.onPluginFrame(warningFrame(3, nearMiss)); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-s.events:
		if ev.Kind != aipapi.EventWarning || ev.WarningMessage != nearMiss {
			t.Fatalf("forwarded event = %+v, want the unchanged ordinary warning %q", ev, nearMiss)
		}
	default:
		t.Fatal("the ordinary canonical warning was not forwarded")
	}
	select {
	case ev := <-s.events:
		t.Fatalf("a private omission marker leaked into the client stream: %+v", ev)
	default:
	}

	recorded := logs.String()
	if got := strings.Count(recorded, testOperatorWarning); got != 1 {
		t.Fatalf("operator omission warnings = %d, want exactly one; log=%s", got, recorded)
	}
	if !strings.Contains(recorded, "plan-instance") {
		t.Fatalf("operator warning must identify the backend instance; log=%s", recorded)
	}
	for _, forbidden := range []string{testSIWCOmissionMarker, nearMiss} {
		if strings.Contains(recorded, forbidden) {
			t.Fatalf("operator warning must not copy canonical warning text %q; log=%s", forbidden, recorded)
		}
	}
}
