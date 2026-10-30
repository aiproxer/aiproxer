package adapter

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// attemptCaptureSession records the invocation of each Execute call.
type attemptCaptureSession struct {
	invs chan backendplugin.Invocation
}

func (s *attemptCaptureSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (s *attemptCaptureSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (s *attemptCaptureSession) Execute(stream backendplugin.ExecuteStream) error {
	frame, err := stream.Recv()
	if err != nil {
		return err
	}
	if frame.Invocation != nil {
		s.invs <- *frame.Invocation
	}
	return nil
}

func (s *attemptCaptureSession) Close(context.Context) error { return nil }

// TestOpenStream_UsesTheHostAttemptIdentity proves two actual attempts for the
// same canonical request reach the connector with distinct host attempt IDs,
// while the deterministic fallback remains for callers without attempt
// authority.
func TestOpenStream_UsesTheHostAttemptIdentity(t *testing.T) {
	t.Parallel()
	session := &attemptCaptureSession{invs: make(chan backendplugin.Invocation, 3)}
	call := aipapi.Call{
		ID:       "req-1",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "m"},
		Key:     "fake:m",
	}
	open := func(ctx context.Context) backendplugin.Invocation {
		t.Helper()
		stream, err := openStream(ctx, session, Options{InstanceID: "i", MaxPendingEvents: 4}, call, cand)
		if err != nil {
			t.Fatalf("openStream: %v", err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		return <-session.invs
	}

	first := open(execbackend.WithAttemptMeta(context.Background(), execbackend.AttemptMeta{ID: "b-leg-1", Seq: 1}))
	second := open(execbackend.WithAttemptMeta(context.Background(), execbackend.AttemptMeta{ID: "b-leg-2", Seq: 2}))
	if first.AttemptID != "b-leg-1" || second.AttemptID != "b-leg-2" {
		t.Fatalf("attempt identities = %q and %q, want the distinct host B-leg ids", first.AttemptID, second.AttemptID)
	}
	if first.AttemptID == second.AttemptID {
		t.Fatal("two actual attempts for the same request share an attempt identity")
	}

	fallback := open(context.Background())
	if fallback.AttemptID != "req-1-attempt" {
		t.Fatalf("fallback attempt id=%q, want the deterministic request-scoped value", fallback.AttemptID)
	}
}
