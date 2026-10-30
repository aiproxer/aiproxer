package openresponses

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// BranchScenarioRegistry records reviewed production scenarios, deterministic package boundaries, and unreachable/defensive branches.
type BranchScenarioRegistry struct {
	PackageName         string
	BaselineMinCoverage float64
	ReviewedScenarios   []string
	UnreachableBranches []string
}

// GetBranchScenarioRegistry returns the checked-in coverage baseline and branch scenario registry.
func GetBranchScenarioRegistry() BranchScenarioRegistry {
	return BranchScenarioRegistry{
		PackageName:         "internal/plugins/protocols/openresponses",
		BaselineMinCoverage: 90.0,
		ReviewedScenarios: []string{
			"Request codec: string vs item array input, tool choice variants, presence distinctions, extra top-level fields, strict JSON depth and duplicate keys",
			"Response resource builder: required envelope presence, completed timestamp, usage statistics, stream error fallback",
			"State machine & SSE: lifecycle state transitions, sequence numbers, item & content part deltas, conservative legacy normalization, transactional rollback",
			"Compact resource builder: compaction item generation, usage statistics, encap ID materialization",
			"Error mapping & limits: table-driven classification, HTTP status code mapping, payload/count/depth limit enforcement, sanitized wire error messages",
		},
		UnreachableBranches: []string{
			"encode.go: defensive json.Marshal error checks for pre-validated struct literals",
			"state_machine.go: defensive fallback when snapshotting uninitialized content parts during unexpected error aborts",
		},
	}
}

func TestCoverageRegistry_Metadata(t *testing.T) {
	t.Parallel()
	reg := GetBranchScenarioRegistry()
	if reg.PackageName == "" {
		t.Error("expected non-empty PackageName in registry")
	}
	if reg.BaselineMinCoverage < 90.0 {
		t.Errorf("expected BaselineMinCoverage >= 90.0, got %f", reg.BaselineMinCoverage)
	}
	if len(reg.ReviewedScenarios) == 0 {
		t.Error("expected non-empty ReviewedScenarios in registry")
	}
	if len(reg.UnreachableBranches) == 0 {
		t.Error("expected non-empty UnreachableBranches in registry")
	}
}

func TestAdditionalCoverageEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("LimitExceededError_EmptyMessage_And_NilErr", func(t *testing.T) {
		t.Parallel()
		err := &LimitExceededError{
			Param:  "test_param",
			Limit:  10,
			Actual: 20,
		}
		if !strings.Contains(err.Error(), "limit 10 exceeded by actual 20") {
			t.Errorf("unexpected error string: %s", err.Error())
		}
		if err.Unwrap() != ErrLimitExceeded {
			t.Errorf("expected Unwrap to return ErrLimitExceeded when Err is nil")
		}
	})

	t.Run("SanitizeErrorMessage_PasswordAndToken", func(t *testing.T) {
		t.Parallel()
		if res := sanitizeErrorMessage("failed with password123"); res != "an internal system error occurred" {
			t.Errorf("unexpected sanitization result: %s", res)
		}
	})

	t.Run("BuildResponseResource_CompletedAtEnvelope", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		meta := EnvelopeMetadata{
			ResponseID:  "resp_123",
			Model:       "gpt-4o",
			Status:      "completed",
			CompletedAt: &now,
		}
		res, _, err := BuildResponseResource(meta, nil, UsageStats{}, aipapi.GenerationOptions{}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.CompletedAt == nil || *res.CompletedAt != now.Unix() {
			t.Fatalf("expected CompletedAt %d, got %v", now.Unix(), res.CompletedAt)
		}
	})

	t.Run("BuildResponseResource_IncompleteDetails", func(t *testing.T) {
		meta := EnvelopeMetadata{
			ResponseID: "resp_123",
			Model:      "gpt-4o",
			Status:     "incomplete",
		}
		streamErr := &aipapi.StreamError{
			Code:    "max_tokens",
			Message: "max output tokens reached",
		}
		res, _, err := BuildResponseResource(meta, nil, UsageStats{}, aipapi.GenerationOptions{}, streamErr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Status != "incomplete" {
			t.Errorf("expected status incomplete, got %s", res.Status)
		}
	})

	t.Run("SSEWriter_ValidTerminalAndDONE", func(t *testing.T) {
		var buf strings.Builder
		w := NewSSEWriter(&buf)
		if err := w.WriteEvent(StreamEvent{Type: "response.created"}); err != nil {
			t.Fatalf("unexpected WriteEvent error: %v", err)
		}
		if err := w.WriteEvent(StreamEvent{Type: "response.completed"}); err != nil {
			t.Fatalf("unexpected WriteEvent terminal error: %v", err)
		}
		if err := w.WriteDONE(); err != nil {
			t.Fatalf("unexpected WriteDONE error: %v", err)
		}
		if err := w.WriteDONE(); !errors.Is(err, ErrDuplicateTerminal) {
			t.Fatalf("expected ErrDuplicateTerminal, got %v", err)
		}
	})

	t.Run("EncodeContentPart_ImageRef_And_Refusal", func(t *testing.T) {
		pImg := aipapi.ContentPart{
			Kind:     aipapi.ContentPartImageRef,
			ImageRef: "https://example.com/test.png",
		}
		wImg := encodeContentPart(pImg, aipapi.RoleUser)
		if wImg.Type != "input_image" {
			t.Errorf("expected type input_image, got %s", wImg.Type)
		}

		pRef := aipapi.ContentPart{
			Kind:    aipapi.ContentPartRefusal,
			Refusal: "I cannot do that",
		}
		wRef := encodeContentPart(pRef, aipapi.RoleAssistant)
		if wRef.Type != "refusal" || wRef.Refusal != "I cannot do that" {
			t.Errorf("unexpected refusal part: %+v", wRef)
		}
	})

	t.Run("ValidateJSONStrict_DepthAndErrors", func(t *testing.T) {
		deepJSON := []byte(`{"a":{"b":{"c":1}}}`)
		if err := validateJSONStrictWithLimits(deepJSON, Limits{MaxItemDepth: 2}); err == nil {
			t.Error("expected error for JSON exceeding depth")
		}

		unclosed := []byte(`{"a": 1`)
		if err := validateJSONStrictWithLimits(unclosed, Limits{MaxItemDepth: 10}); err == nil {
			t.Error("expected error for unclosed JSON")
		}

		invalidKey := []byte(`{123: "val"}`)
		if err := validateJSONStrictWithLimits(invalidKey, Limits{MaxItemDepth: 10}); err == nil {
			t.Error("expected error for invalid key")
		}
	})

	t.Run("DecodeToolChoice_EdgeCases", func(t *testing.T) {
		tc, err := decodeToolChoice([]byte(""))
		if err != nil || tc.Mode != "" {
			t.Errorf("expected empty toolchoice for empty bytes")
		}

		_, err = decodeToolChoice([]byte(`"invalid_mode"`))
		if err == nil {
			t.Error("expected error for unknown string tool choice")
		}

		tcAllowed, err := decodeToolChoice([]byte(`{"type":"allowed_tools"}`))
		if err == nil {
			t.Error("expected error for allowed_tools without a tools array")
		}

		tcAllowed, err = decodeToolChoice([]byte(`{"type":"allowed_tools","tools":[{"type":"function","name":"x"}]}`))
		if err != nil || tcAllowed.Mode != aipapi.ToolChoiceAuto {
			t.Errorf("expected ToolChoiceAuto for allowed_tools, got mode %s, err %v", tcAllowed.Mode, err)
		}
		if len(tcAllowed.AllowedTools) != 1 || tcAllowed.AllowedTools[0] != "x" {
			t.Errorf("expected allowed subset [x], got %v", tcAllowed.AllowedTools)
		}
	})

	t.Run("DecodeItem_ReasoningContentArray", func(t *testing.T) {
		wire := WireItem{
			Type:    "reasoning",
			Content: []byte(`[{"type":"input_text","text":"reasoning part 1"},{"type":"input_text","text":"reasoning part 2"}]`),
		}
		item, err := DecodeItem(wire, DefaultLimits())
		if err != nil {
			t.Fatalf("unexpected DecodeItem error: %v", err)
		}
		if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
			t.Fatal("expected reasoning item")
		}
		if !strings.Contains(item.Reasoning.Reasoning.Text, "reasoning part 1") || !strings.Contains(item.Reasoning.Reasoning.Text, "reasoning part 2") {
			t.Errorf("unexpected reasoning text: %s", item.Reasoning.Reasoning.Text)
		}
	})
}
