package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func testGeminiSpec(t *testing.T) *geminiSpec {
	t.Helper()
	// BuildCommand resolves the configured executable; point it at a fake file
	// so enumeration tests exercise the scripted starter, not PATH lookup.
	fakeBin := filepath.Join(t.TempDir(), "gemini")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &geminiSpec{cfg: Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: fakeBin, Model: "gemini-2.5-flash"},
	}}
}

func TestSessionModelsProvider_EnumeratesAdvertisedModels(t *testing.T) {
	t.Parallel()

	p := sessionModelsProvider{
		starter: &acp.ScriptedStarter{
			SessionModels: []acp.ModelInfo{
				{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
				{ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Description: "fast"},
				{ModelID: "", Name: "empty-skipped"},
			},
			SessionCurrentModel: "gemini-2.5-flash",
		},
		spec:      testGeminiSpec(t),
		workspace: t.TempDir(),
		timeout:   10 * time.Second,
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels failed: %v", err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want remote", snap.Source)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("Models len = %d, want 2 (empty modelId skipped): %+v", len(snap.Models), snap.Models)
	}
	m := snap.Models[0]
	if m.CanonicalID != "google/gemini-2.5-pro" || m.NativeID != "gemini-2.5-pro" || m.DisplayName != "Gemini 2.5 Pro" {
		t.Fatalf("Models[0] = %+v", m)
	}
	if snap.Models[1].DisplayName != "Gemini 2.5 Flash" {
		t.Fatalf("Models[1] = %+v", snap.Models[1])
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("Warnings = %+v, want empty", snap.Warnings)
	}
}

func TestSessionModelsProvider_EmptyAdvertisementReturnsEmpty(t *testing.T) {
	t.Parallel()

	p := sessionModelsProvider{
		starter:   &acp.ScriptedStarter{},
		spec:      testGeminiSpec(t),
		workspace: t.TempDir(),
		timeout:   10 * time.Second,
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels failed: %v", err)
	}
	if len(snap.Models) != 0 {
		t.Fatalf("Models = %+v, want empty for successful empty enumeration", snap.Models)
	}
}

func TestSessionModelsProvider_StarterErrorIsOperational(t *testing.T) {
	t.Parallel()

	p := sessionModelsProvider{
		starter:   &failProcessStarter{err: errors.New("spawn denied")},
		spec:      testGeminiSpec(t),
		workspace: t.TempDir(),
		timeout:   10 * time.Second,
	}
	_, err := p.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("error = %v, want OperationalError unavailable", err)
	}
}

func TestSessionModelsProvider_NilContext(t *testing.T) {
	t.Parallel()

	p := sessionModelsProvider{spec: testGeminiSpec(t)}
	//nolint:staticcheck // defensive nil-context handling is part of the provider contract
	if _, err := p.LoadModels(nil); !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("error = %v, want ErrNilContext", err)
	}
}

func TestSessionModelsProvider_ContextCanceled(t *testing.T) {
	t.Parallel()

	p := sessionModelsProvider{
		starter:   &acp.ScriptedStarter{},
		spec:      testGeminiSpec(t),
		workspace: t.TempDir(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.LoadModels(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
}

func TestDiscoveryContext_HonorsCallerDeadline(t *testing.T) {
	t.Parallel()

	t.Run("long caller deadline is unchanged", func(t *testing.T) {
		t.Parallel()
		want := time.Now().Add(2 * time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), want)
		defer cancel()
		got, gotCancel := discoveryContext(ctx, 0)
		defer gotCancel()
		deadline, ok := got.Deadline()
		if !ok || !deadline.Equal(want) {
			t.Fatalf("deadline = %v (ok=%v), want exactly %v", deadline, ok, want)
		}
	})

	t.Run("short caller deadline is unchanged", func(t *testing.T) {
		t.Parallel()
		want := time.Now().Add(250 * time.Millisecond)
		ctx, cancel := context.WithDeadline(context.Background(), want)
		defer cancel()
		got, gotCancel := discoveryContext(ctx, time.Minute)
		defer gotCancel()
		deadline, ok := got.Deadline()
		if !ok || !deadline.Equal(want) {
			t.Fatalf("deadline = %v (ok=%v), want exactly %v", deadline, ok, want)
		}
	})

	t.Run("undated context gets the 30 second fallback", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		got, gotCancel := discoveryContext(context.Background(), 0)
		defer gotCancel()
		deadline, ok := got.Deadline()
		if !ok {
			t.Fatal("undated context received no fallback deadline")
		}
		if earliest := before.Add(defaultDiscoveryTimeout - time.Second); deadline.Before(earliest) {
			t.Fatalf("deadline %v earlier than %v", deadline, earliest)
		}
		if latest := time.Now().Add(defaultDiscoveryTimeout + time.Second); deadline.After(latest) {
			t.Fatalf("deadline %v later than %v", deadline, latest)
		}
	})

	t.Run("positive explicit fallback wins without a caller deadline", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		got, gotCancel := discoveryContext(context.Background(), 2*time.Minute)
		defer gotCancel()
		deadline, ok := got.Deadline()
		if !ok {
			t.Fatal("undated context received no fallback deadline")
		}
		if earliest := before.Add(2*time.Minute - time.Second); deadline.Before(earliest) {
			t.Fatalf("deadline %v earlier than %v", deadline, earliest)
		}
		if latest := time.Now().Add(2*time.Minute + time.Second); deadline.After(latest) {
			t.Fatalf("deadline %v later than %v", deadline, latest)
		}
	})
}

type failProcessStarter struct{ err error }

func (s *failProcessStarter) Start([]string, string, []string) (acp.Process, error) {
	return nil, s.err
}
