package service

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestExecute_nilInstanceReturnsError(t *testing.T) {
	t.Parallel()
	var inst instance
	err := inst.Execute(newExecuteNilTestStream(t))
	if err == nil {
		t.Fatal("expected error for unconfigured instance")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err=%v", err)
	}
}

type executeNilTestStream struct {
	ctx context.Context
}

func newExecuteNilTestStream(t *testing.T) *executeNilTestStream {
	t.Helper()
	return &executeNilTestStream{ctx: context.Background()}
}

func (s *executeNilTestStream) Context() context.Context { return s.ctx }

func (s *executeNilTestStream) Recv() (backendplugin.ClientFrame, error) {
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m1",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}, nil
}

func (s *executeNilTestStream) Send(backendplugin.ServerFrame) error { return nil }
