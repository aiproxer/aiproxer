package anthropic

import (
	"strings"
	"testing"
)

// Claim (#1511): hybrid-only inline system support does not relax ordinary
// Messages decoding. Mutant: accept system roles without the private option.
func TestDecodeMessage_OrdinaryRejectsInlineSystem(t *testing.T) {
	_, err := DecodeMessageRequest([]byte(`{"model":"external:model","max_tokens":64,"messages":[{"role":"user","content":"question"},{"role":"system","content":"worker instructions"}]}`), DecodeOptions{RouteSelector: "external:model"})
	if err == nil || !strings.Contains(err.Error(), `unsupported message role "system"`) {
		t.Fatalf("ordinary decoder must reject inline system: %v", err)
	}
}
