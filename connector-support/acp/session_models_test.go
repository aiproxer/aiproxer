package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestParseSessionModels_Full(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sessionId":"s1","models":{"availableModels":[{"modelId":"gemini-2.5-pro","name":"Gemini 2.5 Pro","description":"pro"},{"modelId":"","name":"empty-skipped"}],"currentModelId":"gemini-2.5-pro"}}`)
	got := parseSessionModels(raw)
	if got.CurrentID != "gemini-2.5-pro" {
		t.Fatalf("CurrentID = %q", got.CurrentID)
	}
	if len(got.Available) != 1 {
		t.Fatalf("Available len = %d, want 1 (empty modelId skipped)", len(got.Available))
	}
	m := got.Available[0]
	if m.ModelID != "gemini-2.5-pro" || m.Name != "Gemini 2.5 Pro" || m.Description != "pro" {
		t.Fatalf("Available[0] = %+v", m)
	}
}

func TestParseSessionModels_MissingAndMalformed(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]json.RawMessage{
		"absent":   json.RawMessage(`{"sessionId":"s1"}`),
		"null":     json.RawMessage(`{"sessionId":"s1","models":null}`),
		"empty":    json.RawMessage(``),
		"garbage":  json.RawMessage(`{not json`),
		"no-array": json.RawMessage(`{"sessionId":"s1","models":{"availableModels":{}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := parseSessionModels(raw)
			if got.CurrentID != "" {
				t.Fatalf("CurrentID = %q, want empty", got.CurrentID)
			}
			if len(got.Available) != 0 {
				t.Fatalf("Available = %+v, want empty", got.Available)
			}
		})
	}
}

func TestDiscoverSessionModels_Scripted(t *testing.T) {
	t.Parallel()

	starter := &ScriptedStarter{
		SessionModels: []ModelInfo{
			{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
			{ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Description: "fast"},
		},
		SessionCurrentModel: "gemini-2.5-flash",
	}
	got, err := DiscoverSessionModels(context.Background(), []string{"gemini", "--acp"}, t.TempDir(), nil, starter, HandshakeProfile{
		ProtocolVersion:  1,
		SkipAuthenticate: true,
	})
	if err != nil {
		t.Fatalf("DiscoverSessionModels failed: %v", err)
	}
	if got.CurrentID != "gemini-2.5-flash" {
		t.Fatalf("CurrentID = %q", got.CurrentID)
	}
	if len(got.Available) != 2 || got.Available[0].ModelID != "gemini-2.5-pro" || got.Available[1].ModelID != "gemini-2.5-flash" {
		t.Fatalf("Available = %+v", got.Available)
	}
}

func TestDiscoverSessionModels_NoModelsAdvertised(t *testing.T) {
	t.Parallel()

	got, err := DiscoverSessionModels(context.Background(), []string{"gemini", "--acp"}, t.TempDir(), nil, &ScriptedStarter{}, HandshakeProfile{
		ProtocolVersion:  1,
		SkipAuthenticate: true,
	})
	if err != nil {
		t.Fatalf("DiscoverSessionModels failed: %v", err)
	}
	if len(got.Available) != 0 {
		t.Fatalf("Available = %+v, want empty", got.Available)
	}
}

func TestDiscoverSessionModels_StarterError(t *testing.T) {
	t.Parallel()

	_, err := DiscoverSessionModels(context.Background(), []string{"gemini"}, "", nil, &failStarter{err: errors.New("spawn denied")}, HandshakeProfile{})
	if err == nil {
		t.Fatal("expected spawn error, got nil")
	}
}

func TestDiscoverSessionModels_EmptyCommand(t *testing.T) {
	t.Parallel()

	_, err := DiscoverSessionModels(context.Background(), nil, "", nil, &ScriptedStarter{}, HandshakeProfile{})
	if err == nil {
		t.Fatal("expected empty command error, got nil")
	}
}

func TestDiscoverSessionModels_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := DiscoverSessionModels(ctx, []string{"gemini"}, "", nil, &ScriptedStarter{}, HandshakeProfile{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
}

type failStarter struct{ err error }

func (s *failStarter) Start([]string, string, []string) (Process, error) {
	return nil, s.err
}
