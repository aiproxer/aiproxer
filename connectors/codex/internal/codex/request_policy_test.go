package codex

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPayloadForCall_nativeReasoningRequestPolicy(t *testing.T) {
	t.Parallel()
	cat, err := catalog.Parse([]byte(`{"models":[
		{"slug":"catalog-model","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"low-only","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"low"}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name             string
		callEffort       string
		configuredEffort string
		model            string
		marker           bool
		cfg              *NativeContextConfig
		internal         bool
		wantReasoning    bool
		wantEffort       string
		wantSummary      string
	}{
		{
			name:          "eligible marker creates valid reasoning object without effort",
			marker:        true,
			cfg:           &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning: true,
			wantSummary:   "auto",
		},
		{
			name:             "explicit caller effort wins",
			callEffort:       "low",
			configuredEffort: "high",
			marker:           true,
			cfg:              &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning:    true,
			wantEffort:       "low",
			wantSummary:      "auto",
		},
		{
			name:             "configured effort wins over catalog default",
			configuredEffort: "low",
			model:            "catalog-model",
			marker:           true,
			cfg:              &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning:    true,
			wantEffort:       "low",
			wantSummary:      "auto",
		},
		{
			name:          "catalog default is used when configured effort is absent",
			model:         "catalog-model",
			marker:        true,
			cfg:           &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning: true,
			wantEffort:    "high",
			wantSummary:   "auto",
		},
		{
			name:             "unsupported configured effort is omitted",
			configuredEffort: "high",
			model:            "low-only",
			marker:           true,
			cfg:              &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning:    true,
			wantSummary:      "auto",
		},
		{
			name:             "unknown catalog model does not receive unverified configured effort",
			configuredEffort: "high",
			model:            "unknown-model",
			marker:           true,
			cfg:              &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			wantReasoning:    true,
			wantSummary:      "auto",
		},
		{
			name:          "disabled native context keeps existing payload shape",
			marker:        true,
			cfg:           &NativeContextConfig{Enabled: false, RequestEncryptedReasoning: true},
			wantReasoning: false,
		},
		{
			name:          "request flag false keeps existing payload shape",
			marker:        true,
			cfg:           &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: false},
			wantReasoning: false,
		},
		{
			name:          "internal compaction request always requests encrypted reasoning",
			marker:        true,
			cfg:           &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
			internal:      true,
			wantReasoning: true,
			wantSummary:   "auto",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := tc.model
			if model == "" {
				model = "uncataloged-model"
			}
			call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
			call.Options.ReasoningEffort = tc.callEffort
			if tc.marker {
				call.Extensions = map[string]json.RawMessage{nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue)}
			}
			cfg := Config{DefaultReasoningEffort: tc.configuredEffort, NativeContext: tc.cfg, ModelCatalog: cat}
			var payload Payload
			var err error
			if tc.internal {
				payload, err = payloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}}, cfg)
			} else {
				payload, err = PayloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}}, cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := payload.Reasoning != nil; got != tc.wantReasoning {
				t.Fatalf("reasoning present = %v, want %v (payload=%+v)", got, tc.wantReasoning, payload)
			}
			if !tc.wantReasoning {
				return
			}
			if payload.Reasoning.Effort != tc.wantEffort {
				t.Fatalf("effort = %q, want %q", payload.Reasoning.Effort, tc.wantEffort)
			}
			if payload.Reasoning.Summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", payload.Reasoning.Summary, tc.wantSummary)
			}
			if len(payload.Include) != 1 || payload.Include[0] != "reasoning.encrypted_content" {
				t.Fatalf("include = %v", payload.Include)
			}
		})
	}
}

func TestPayloadForCall_requestPolicyConsumesMarkerWithoutMutatingCall(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Extensions: map[string]json.RawMessage{
			nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue),
		},
	}
	cfg := Config{NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true}}
	payload, err := PayloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: "model"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil {
		t.Fatal("eligible marker must create reasoning request")
	}
	if _, ok := call.Extensions[nativeContinuityMarkerKey]; !ok {
		t.Fatal("payload construction mutated caller-owned extensions")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || containsBytes(body, []byte(nativeContinuityMarkerKey)) {
		t.Fatalf("provider payload contains internal marker: %s", body)
	}
}

func TestPayloadForCall_defaultReasoningOmitsEmptyEffort(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Extensions: map[string]json.RawMessage{
			nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue),
		},
	}
	payload, err := PayloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: "gpt-5.3-codex-spark"}}, Config{
		NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "" {
		t.Fatalf("reasoning = %#v", payload.Reasoning)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if containsBytes(raw, []byte(`"effort"`)) {
		t.Fatalf("empty reasoning effort was sent: %s", raw)
	}
}

func TestPayloadForCall_internalCompactionPolicySeam(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("history")}}},
		Extensions: map[string]json.RawMessage{nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue)},
	}
	cfg := Config{NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true}}
	payload, err := payloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: "model"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil || len(payload.Include) != 1 || payload.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("compaction request policy = %+v", payload)
	}
}

func TestPayloadForCall_internalCompactionPreservesExplicitReasoningDisable(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("history")}}}}
	cfg := Config{NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: false}}
	payload, err := payloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: "model"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning != nil || len(payload.Include) != 0 {
		t.Fatalf("explicit request_encrypted_reasoning:false was overridden: %+v", payload)
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
