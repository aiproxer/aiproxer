package streamdebug

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEnabledUsesDiagGate(t *testing.T) {
	t.Parallel()
	if Enabled() != diag.DebugTurnsEnabled() {
		t.Fatalf("Enabled() = %v, want diag gate %v", Enabled(), diag.DebugTurnsEnabled())
	}
}

func TestWrapFollowsDebugGate(t *testing.T) {
	t.Parallel()
	inner := &testStream{}
	wrapped := Wrap(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test", &aipapi.Call{ID: "call"}, inner, time.Now())
	if diag.DebugTurnsEnabled() {
		if wrapped == inner {
			t.Fatal("Wrap enabled returned original stream, want debug wrapper")
		}
		return
	}
	if wrapped != inner {
		t.Fatalf("Wrap disabled returned %T, want original stream", wrapped)
	}
}

type testStream struct{}

func (*testStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (*testStream) Close() error {
	return nil
}
