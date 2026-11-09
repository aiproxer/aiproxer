package openairesponses_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
)

// TestProfile_NamespaceToolsDeclineBeforeWire pins the fast-path guard: a raw
// namespace declaration must never be raw-forwarded with the client's body.
func TestProfile_NamespaceToolsDeclineBeforeWire(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","stream":true,"input":"hi","tools":[{"type":"namespace","name":"multi_agent_v1","description":"NS","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}]}`)
	_, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(body, "stub:gpt-4o", nil))
	if !errors.Is(err, openairesponses.ErrNamespaceToolsRequiresCanonicalDecode) {
		t.Fatalf("a raw namespace declaration must decline before wire execution, got %v", err)
	}
}

// TestProfile_NamespaceHistoryDeclinesBeforeWire pins the history guard for a
// small array input and for the large-input path that bypasses item parsing.
func TestProfile_NamespaceHistoryDeclinesBeforeWire(t *testing.T) {
	t.Parallel()
	small := []byte(`{"model":"gpt-4o","stream":true,"input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]}`)
	_, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(small, "stub:gpt-4o", nil))
	if !errors.Is(err, openairesponses.ErrNamespaceHistoryRequiresCanonicalDecode) {
		t.Fatalf("namespaced history must decline before wire execution, got %v", err)
	}

	t.Run("large input", func(t *testing.T) {
		t.Parallel()
		pad := strings.Repeat("x", 4096)
		large := []byte(`{"model":"gpt-4o","stream":true,"tools":[],"input":[{"type":"message","role":"user","content":"` + pad + `"},{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]}`)
		in := defaultProofInput(large, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(in.Ctx, 1024)
		_, err := openairesponses.NewProfile().CompileProof(in.Ctx, in)
		if !errors.Is(err, openairesponses.ErrNamespaceHistoryRequiresCanonicalDecode) {
			t.Fatalf("large namespaced history must decline before wire execution, got %v", err)
		}
	})
}

// TestProfile_UnsupportedControlDeclinesBeforeNamespaceHistory pins the decline
// order: a body carrying both an unsupported control key and a namespaced
// history item keeps the pre-existing control-key error.
func TestProfile_UnsupportedControlDeclinesBeforeNamespaceHistory(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","stream":true,"store":true,"input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]}`)
	_, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(body, "stub:gpt-4o", nil))
	if err == nil || !strings.Contains(err.Error(), "unsupported control key") {
		t.Fatalf("unsupported control must decline before namespaced history, got %v", err)
	}
	if errors.Is(err, openairesponses.ErrNamespaceHistoryRequiresCanonicalDecode) {
		t.Fatalf("namespaced history decline must not take priority, got %v", err)
	}
}

// TestProfile_PlainFunctionToolsStillTakeWirePath is the positive control: the
// new guard must not narrow the wire path for ordinary function tools.
func TestProfile_PlainFunctionToolsStillTakeWirePath(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","stream":true,"input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	if _, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(body, "stub:gpt-4o", nil)); err != nil {
		t.Fatalf("ordinary function tools must keep the wire path: %v", err)
	}
}

// TestProfile_PlainDoubleUnderscoreHistoryStillTakesWirePath is the positive
// control for the flat-identity fix: an ordinary function_call whose name merely
// contains the namespace separator carries no namespace declaration or
// namespaced history item, so it must not be forced onto canonical decode.
func TestProfile_PlainDoubleUnderscoreHistoryStillTakesWirePath(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","stream":true,"input":[{"type":"function_call","call_id":"c1","name":"a__run","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`)
	if _, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(body, "stub:gpt-4o", nil)); err != nil {
		t.Fatalf("plain flat history must keep the wire path: %v", err)
	}
}
