package frontendpipe

// PR #1064 review findings F3+F4 (RED, internal access):
// - F3: if an error-bearing stream's Close() panics, the promised original
// execution error is never returned (the panic escapes first). Both the
// canonical and wire seams must contain the disposal panic and still return
// the original error.
// - F4: logStreamCleanup emits the raw adapter Close() error (which may carry
// a filesystem path or request-derived material) into production logs.
// Cleanup diagnostics must carry a fixed reason/category only; CANARY path
// and payload strings must never appear. The fixed reason/category MUST
// appear (non-vacuity: logging the raw error fails this test).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type pr1064PanicCloseStream struct {
	closed int
}

func (s *pr1064PanicCloseStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1064PanicCloseStream) Close() error {
	s.closed++
	panic("pr1064 adapter close panic")
}

type pr1064CanaryPanicStream struct {
	material string
	closed   int
}

func (s *pr1064CanaryPanicStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1064CanaryPanicStream) Close() error {
	s.closed++
	panic(errors.New(s.material))
}

func pr1064F3Recover(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("disposal panic escaped to the caller instead of being contained: %v", r)
	}
}

func TestPR1064_F3_ClosePanicPreservesOriginalError(t *testing.T) {
	t.Parallel()

	for _, wire := range []bool{false, true} {
		name := "canonical"
		if wire {
			name = "wire"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer pr1064F3Recover(t)
			boom := errors.New("pr1064 f3 original failure")
			stream := &pr1064PanicCloseStream{}
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			if wire {
				cfg := ownershipTestConfig(nil, log, aipsdk.FrontendKeepaliveConfig{})
				got, err := cfg.executeLargeBody(context.Background(), nil,
					&ownershipWireExecutor{
						fn: func(context.Context, largebody.Assessment, largebody.Source) (largebody.ExecutionResult, error) {
							return largebody.ExecutionResult{Stream: stream}, boom
						},
					}, largebody.Assessment{}, nil, false)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want original %v", err, boom)
				}
				if got.Stream != nil {
					t.Fatalf("error-bearing wire stream was handed to the caller: %v", got.Stream)
				}
			} else {
				cfg := ownershipTestConfig(&ownershipExecutor{
					fn: func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
						return stream, boom
					},
				}, log, aipsdk.FrontendKeepaliveConfig{})
				got, err := cfg.execute(context.Background(), nil, &aipapi.Call{}, false)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want original %v", err, boom)
				}
				if got != nil {
					t.Fatalf("error-bearing stream was handed to the caller: %v", got)
				}
			}
			if stream.closed != 1 {
				t.Fatalf("disposal attempts=%d want exactly 1", stream.closed)
			}
		})
	}
}

func TestPR1064_F4_StreamCleanupDiagnosticsSanitized(t *testing.T) {
	t.Parallel()
	const canaryPath = "/pr1064_f4_canary_spool/secret-spill-0001.tmp"
	const canaryPayload = "PR1064_F4_CANARY_PROMPT_MATERIAL_ALPHA"
	// A realistic spool-cleanup failure: *os.PathError embeds the filesystem
	// path, and the wrapped cause carries request-derived material.
	canaryErr := &os.PathError{Op: "remove", Path: canaryPath, Err: errors.New(canaryPayload)}
	if !strings.Contains(canaryErr.Error(), canaryPath) || !strings.Contains(canaryErr.Error(), canaryPayload) {
		t.Fatal("canary error does not carry both canaries: assertion would be vacuous")
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := ownershipTestConfig(nil, log, aipsdk.FrontendKeepaliveConfig{})
	cfg.logStreamCleanup(context.Background(), "holdalive abandoned wire stream close failed", canaryErr)
	out := buf.String()
	if out == "" {
		t.Fatal("no cleanup diagnostic emitted: want the fixed sanitized signal")
	}
	if strings.Contains(out, canaryPath) {
		t.Fatalf("cleanup diagnostic leaks filesystem path %q; log=%q", canaryPath, out)
	}
	if strings.Contains(out, canaryPayload) {
		t.Fatalf("cleanup diagnostic leaks request-derived material %q; log=%q", canaryPayload, out)
	}
	if strings.Contains(out, canaryErr.Error()) {
		t.Fatalf("cleanup diagnostic emits the raw error string; log=%q", out)
	}
	if !strings.Contains(out, "close failed") {
		t.Fatalf("fixed cleanup message missing; log=%q", out)
	}
	if !strings.Contains(out, "stream_cleanup_failed") {
		t.Fatalf("fixed cleanup reason missing; log=%q", out)
	}
	if !strings.Contains(out, "path_error") {
		t.Fatalf("bounded error category missing; log=%q", out)
	}
}

func TestPR1064_F4_StreamCleanupPanicDiagnosticsSanitized(t *testing.T) {
	t.Parallel()
	const canaryPayload = "PR1064_F4_CANARY_PANIC_MATERIAL_BETA"
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := ownershipTestConfig(nil, log, aipsdk.FrontendKeepaliveConfig{})
	stream := &pr1064CanaryPanicStream{material: canaryPayload}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("disposal panic escaped to the caller instead of being contained: %v", r)
			}
		}()
		cfg.closeErrorBearingWire(context.Background(), largebody.ExecutionResult{Stream: stream})
	}()
	out := buf.String()
	if out == "" {
		t.Fatal("no panic diagnostic emitted: contained panics must remain visible")
	}
	if strings.Contains(out, canaryPayload) {
		t.Fatalf("panic diagnostic leaks raw panic material %q; log=%q", canaryPayload, out)
	}
	if !strings.Contains(out, "stream_cleanup_panic") {
		t.Fatalf("fixed panic reason missing; log=%q", out)
	}
}
