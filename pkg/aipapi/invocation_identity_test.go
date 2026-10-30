package aipapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestInvocation_ClientUserAgentNotSerializedOnCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "c1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			DeliveryMode:    aipapi.DeliveryModeStreaming,
			ClientUserAgent: "SecretAgent/9.9",
		},
	}
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "SecretAgent") {
		t.Fatalf("ClientUserAgent leaked into JSON: %s", s)
	}
	if strings.Contains(s, "ClientUserAgent") || strings.Contains(s, "user_agent") || strings.Contains(s, "User-Agent") {
		t.Fatalf("identity metadata keys leaked into JSON: %s", s)
	}
}

func TestInvocation_ClientUserAgentNotSerializedDirectly(t *testing.T) {
	t.Parallel()
	inv := aipapi.Invocation{
		Operation:       aipapi.OperationOpenAIChatCompletions,
		ClientUserAgent: "DirectAgent/1.0",
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "DirectAgent") {
		t.Fatalf("ClientUserAgent leaked from Invocation JSON: %s", raw)
	}
}
