package product

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffectiveIdentity_GatewaySourceTupleAccepted(t *testing.T) {
	spec := claudeTestSpecForSource(t, "gateway")
	nonce := strings.Repeat("a", 64)
	first := json.RawMessage(`{"version":1,"sessionId":"s","checkpointId":"` + nonce + `","processGeneration":"` + strings.Repeat("b", 64) + `","queryGeneration":1,"observationSequence":1,"provider":"firstParty","credentialSource":"gateway"}`)
	if err := spec.ValidatePromptAdmission("s", nonce, nil, first); err != nil {
		t.Fatal(err)
	}
	second := json.RawMessage(strings.Replace(string(first), `"observationSequence":1`, `"observationSequence":2`, 1))
	secondNonce := strings.Repeat("d", 64)
	second = json.RawMessage(strings.Replace(string(second), nonce, secondNonce, 1))
	if err := spec.ValidatePromptAdmission("s", secondNonce, first, second); err != nil {
		t.Fatal(err)
	}
	// A first-party ticket under the gateway source must refuse.
	if err := spec.ValidatePromptAdmission("s", nonce, nil, json.RawMessage(strings.Replace(string(first), `"gateway"`, `"api_key"`, 1))); err == nil {
		t.Fatal("accepted first-party ticket under gateway source")
	}
}

func TestEffectiveIdentity_ExactPrivateMonotonicTuple(t *testing.T) {
	spec := claudeTestSpec(t, nil)
	nonce := strings.Repeat("a", 64)
	first := json.RawMessage(`{"version":1,"sessionId":"s","checkpointId":"` + nonce + `","processGeneration":"` + strings.Repeat("b", 64) + `","queryGeneration":1,"observationSequence":1,"provider":"firstParty","credentialSource":"api_key"}`)
	if err := spec.ValidatePromptAdmission("s", nonce, nil, first); err != nil {
		t.Fatal(err)
	}
	second := json.RawMessage(strings.Replace(strings.Replace(string(first), `"observationSequence":1`, `"observationSequence":3`, 1), nonce, strings.Repeat("d", 64), 1))
	nonce = strings.Repeat("d", 64)
	if err := spec.ValidatePromptAdmission("s", nonce, first, second); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []json.RawMessage{
		first,
		json.RawMessage(strings.Replace(string(second), `"observationSequence":3`, `"observationSequence":1`, 1)),
		json.RawMessage(strings.Replace(string(second), `"version":1`, `"version":2`, 1)),
		json.RawMessage(strings.Replace(string(second), `"queryGeneration":1`, `"queryGeneration":2`, 1)),
		json.RawMessage(strings.Replace(string(second), strings.Repeat("b", 64), strings.Repeat("c", 64), 1)),
		json.RawMessage(strings.Replace(string(second), `"sessionId":"s"`, `"sessionId":"other"`, 1)),
		json.RawMessage(strings.Replace(string(second), `"firstParty"`, `"bedrock"`, 1)),
		json.RawMessage(strings.Replace(string(second), `"api_key"`, `"host_login"`, 1)),
		json.RawMessage(strings.Replace(string(second), `"version":1`, `"version":1,"email":"private"`, 1)),
		json.RawMessage(strings.Replace(string(second), `"version":1`, `"version":1,"version":1`, 1)),
		append(append(json.RawMessage(nil), second...), []byte(` {}`)...),
	} {
		if err := spec.ValidatePromptAdmission("s", nonce, first, bad); err == nil {
			t.Fatalf("accepted invalid tuple: %s", bad)
		}
	}
}
