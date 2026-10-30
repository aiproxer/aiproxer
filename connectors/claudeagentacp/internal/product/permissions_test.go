package product

import (
	"encoding/json"
	"testing"
)

func TestPermissionHandler_OnlyOfferedDeclines(t *testing.T) {
	auth, err := NewAuthEnv(AuthConfig{PermissionPolicy: "deny"}, map[string]string{"api_key": "test-only"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.TransportOptions().RefuseOutOfTurnRequests {
		t.Fatal("lifetime refusal not enabled")
	}
	h := auth.ServerRequestHandler()
	for _, tc := range []struct{ name, params, want string }{
		{"tool", `{"toolCall":{"title":"Bash","rawInput":{"command":"echo ok"}},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}`, `{"outcome":{"optionId":"no","outcome":"selected"}}`},
		{"edit prefer once", `{"toolCall":{"kind":"edit"},"options":[{"optionId":"never","kind":"reject_always"},{"optionId":"always","kind":"allow_always"},{"optionId":"once","kind":"reject_once"}]}`, `{"outcome":{"optionId":"once","outcome":"selected"}}`},
		{"plan exit", `{"toolCall":{"title":"ExitPlanMode"},"options":[{"optionId":"accept","kind":"allow_always"},{"optionId":"decline","kind":"reject_always"}]}`, `{"outcome":{"optionId":"decline","outcome":"selected"}}`},
		{"allow only", `{"options":[{"optionId":"reject_once","kind":"allow_once"},{"optionId":"persist","kind":"allow_always"}]}`, `{"outcome":{"outcome":"cancelled"}}`},
		{"empty", `{"options":[]}`, `{"outcome":{"outcome":"cancelled"}}`},
		{"missing id", `{"options":[{"kind":"reject_once"}]}`, `{"outcome":{"outcome":"cancelled"}}`},
		{"ambiguous id", `{"options":[{"optionId":"same","kind":"allow_always"},{"optionId":"same","kind":"reject_once"}]}`, `{"outcome":{"outcome":"cancelled"}}`},
		{"malformed", `{"options":42}`, `{"outcome":{"outcome":"cancelled"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := h.HandleServerRequest(t.Context(), "session/request_permission", json.RawMessage(`"id"`), json.RawMessage(tc.params))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result)
			if err != nil || string(raw) != tc.want {
				t.Fatalf("result=%s error=%v want=%s", raw, err, tc.want)
			}
		})
	}
	result, err := h.HandleServerRequest(t.Context(), "unknown/secret-shaped-input", nil, nil)
	if result != nil || err == nil || err.Error() != "claudeagentacp: method not found" {
		t.Fatalf("unknown method: %v %v", result, err)
	}
}

func TestPermissionHandler_BypassAutoAcceptsWithoutInputInspection(t *testing.T) {
	for _, policy := range []string{"", "bypass"} {
		auth, err := NewAuthEnv(AuthConfig{PermissionPolicy: policy}, map[string]string{"api_key": "test-only"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ options, want string }{
			{`[{"optionId":"persist","kind":"allow_always"},{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]`, `{"outcome":{"optionId":"yes","outcome":"selected"}}`},
			{`[{"optionId":"persist","kind":"allow_always"},{"optionId":"no","kind":"reject_always"}]`, `{"outcome":{"outcome":"cancelled"}}`},
			{`[{"optionId":"no","kind":"reject_once"}]`, `{"outcome":{"outcome":"cancelled"}}`},
			{`[{"optionId":"same","kind":"allow_once"},{"optionId":"same","kind":"reject_once"}]`, `{"outcome":{"outcome":"cancelled"}}`},
			{`[{"kind":"allow_once"}]`, `{"outcome":{"outcome":"cancelled"}}`},
		} {
			params := json.RawMessage(`{"toolCall":{"name":"Agent","rawInput":{"run_in_background":true}},"options":` + tc.options + `}`)
			result, err := auth.ServerRequestHandler().HandleServerRequest(t.Context(), "session/request_permission", nil, params)
			raw, _ := json.Marshal(result)
			if err != nil || string(raw) != tc.want {
				t.Fatalf("%q: got %s error=%v want=%s", policy, raw, err, tc.want)
			}
		}
	}
}
