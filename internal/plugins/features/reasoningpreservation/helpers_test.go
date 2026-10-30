package reasoningpreservation_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func mustYAML(tb testing.TB, s string) yaml.Node {
	tb.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		tb.Fatalf("mustYAML: %v", err)
	}
	return n
}

func reasoningPart(dialect aipapi.ReasoningDialect, text, signature string, opaque json.RawMessage) aipapi.Part {
	var op json.RawMessage
	if len(opaque) > 0 {
		op = append(json.RawMessage(nil), opaque...)
	}
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:   dialect,
			Text:      text,
			Signature: signature,
			Opaque:    op,
		},
	}
}

func assistantMsg(parts ...aipapi.Part) aipapi.Message {
	return aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: append([]aipapi.Part(nil), parts...),
	}
}

func redNotImplemented(t *testing.T, err error, msg string) {
	t.Helper()
	if errors.Is(err, reasoningpreservation.ErrNotImplemented) {
		t.Fatalf("RED: %s", msg)
	}
}

func newTestClock(t0 time.Time) (now func() time.Time, advance func(time.Duration)) {
	var mu sync.Mutex
	cur := t0
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return cur
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			cur = cur.Add(d)
		}
}

func mustOpaqueJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	b := json.RawMessage(raw)
	if !json.Valid(b) {
		t.Fatalf("invalid JSON opaque: %q", raw)
	}
	return append(json.RawMessage(nil), b...)
}

func jsonPart(content string) aipapi.Part {
	return aipapi.Part{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(content),
	}
}

func boolPtr(v bool) *bool {
	return new(v)
}

func turnArtifact(id string, anchor [32]byte, reasoning ...reasoningpreservation.PlacedReasoning) reasoningpreservation.TurnArtifact {
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         anchor,
		SourceBackend:  "src-backend",
		SourceModel:    "src-model",
		Reasoning:      append([]reasoningpreservation.PlacedReasoning(nil), reasoning...),
		CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		ReasoningBytes: 128,
	}
}

func placedReasoning(before int, part aipapi.Part) reasoningpreservation.PlacedReasoning {
	return reasoningpreservation.PlacedReasoning{
		BeforeNonReasoningPart: before,
		Part:                   part,
	}
}

func decodeValidConfig(tb testing.TB, yamlBody string) reasoningpreservation.Config {
	tb.Helper()
	cfg, err := reasoningpreservation.DecodeConfig(mustYAML(tb, yamlBody))
	if err != nil {
		tb.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}

func decodeConfigExpectError(t *testing.T, yamlBody string) error {
	t.Helper()
	_, err := reasoningpreservation.DecodeConfig(mustYAML(t, yamlBody))
	redNotImplemented(t, err, "DecodeConfig validation must be implemented")
	return err
}

func newMemoryStore(t *testing.T, opts reasoningpreservation.StoreOptions) reasoningpreservation.TurnStore {
	t.Helper()
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	redNotImplemented(t, err, "NewMemoryTurnStore must be implemented")
	if err != nil {
		t.Fatalf("NewMemoryTurnStore: %v", err)
	}
	return st
}
