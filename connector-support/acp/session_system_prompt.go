package acp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// maxSessionSystemPromptBytes bounds canonical instructions delivered as
// session/new _meta.systemPrompt. Longer inputs fall back to prompt-text
// flattening with a warning rather than failing the turn.
const maxSessionSystemPromptBytes = 128 * 1024

// SessionSystemPromptSpec is the connector opt-in for authoritative
// system-instruction delivery. The returned text is installed as
// session/new _meta.systemPrompt (appended to the agent preset); ok is false
// when the turn carries no deliverable instructions.
type SessionSystemPromptSpec interface {
	SessionSystemPrompt(call *aipapi.Call) (appendText string, ok bool)
}

// SystemPromptSessionProtocol is the protocol-level opt-in consumed by the
// shared subprocess orchestrator. Only protocols whose spec implements
// SessionSystemPromptSpec expose it; other connectors keep legacy behavior.
type SystemPromptSessionProtocol interface {
	HandshakeWithSystemPrompt(ctx context.Context, transport Transport, model, workspace, sysPrompt string) (string, error)
	RotateSessionSystemPrompt(ctx context.Context, transport Transport, model, sysPrompt string) (string, error)
}

// CanonicalSystemPromptText renders deliverable canonical instructions for
// connectors implementing SessionSystemPromptSpec. Length bounding stays
// with the orchestrator so one policy governs all connectors.
func CanonicalSystemPromptText(call *aipapi.Call) (string, bool) {
	text := systemInstructionText(call)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// systemInstructionText renders the canonical instruction sources exactly as
// the prompt mapper consumes them: call Instructions plus system-role message
// text, blank and non-text parts skipped. It mirrors promptBlocksForCall so
// authoritative delivery and flattening never disagree on content.
func systemInstructionText(call *aipapi.Call) string {
	if call == nil {
		return ""
	}
	parts := []string{}
	if t := aipapi.JoinInstructionText(call.Instructions); t != "" {
		parts = append(parts, t)
	}
	for _, m := range call.Messages {
		if m.Role != aipapi.RoleSystem {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// validSessionSystemPrompt reports whether text is deliverable as session
// metadata: non-empty and within the session-new bound.
func validSessionSystemPrompt(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= maxSessionSystemPromptBytes
}

// mergeSessionSystemPrompt installs the append text as _meta.systemPrompt,
// preserving the connector-owned base meta. The object form keeps the
// agent's preset while appending operator instructions; a corrupt base falls
// back to a minimal object rather than failing session creation.
func mergeSessionSystemPrompt(base json.RawMessage, appendText string) json.RawMessage {
	meta := map[string]any{}
	if len(base) > 0 {
		var decoded map[string]any
		// JSON null decodes without error into a nil map. Decoding through a
		// fresh variable keeps the initialized map as the decode target, so the
		// assignment below writes into an owned map instead of panicking on a
		// nil one. A corrupt object still falls back to the minimal object.
		if err := json.Unmarshal(base, &decoded); err == nil && decoded != nil {
			meta = decoded
		}
	}
	meta["systemPrompt"] = map[string]any{"append": appendText}
	merged, err := json.Marshal(meta)
	if err != nil {
		return json.RawMessage(`{"systemPrompt":{"append":` + quoteJSONString(appendText) + `}}`)
	}
	return merged
}

// sessionSystemPromptForCall resolves deliverable instructions for one turn.
// It returns false for protocols whose spec does not opt in, for calls
// without instructions, and for over-bound text: all three keep legacy
// prompt-text behavior without warnings.
func sessionSystemPromptForCall(proto SubprocessProtocol, call *aipapi.Call) (string, bool) {
	p, ok := proto.(interface {
		SessionSystemPrompt(*aipapi.Call) (string, bool)
	})
	if !ok || call == nil {
		return "", false
	}
	text, ok := p.SessionSystemPrompt(call)
	if !ok || !validSessionSystemPrompt(text) {
		return "", false
	}
	return text, true
}

// deliveredPromptCall strips authoritatively delivered instructions from
// prompt text. An instructions-only turn would leave no prompt content, so
// it keeps flattened text with a warning instead of failing a servable turn.
func (b *subprocessBackend) deliveredPromptCall(call *aipapi.Call, userMessage string) *aipapi.Call {
	stripped := stripSystemInstructions(call)
	if _, err := promptBlocksForCall(prepareTranscriptCall(stripped, userMessage)); err != nil {
		b.log.Warn("acp: instructions-only turn keeps prompt-text instructions", "error", truncErrDetail(err, 256))
		return call
	}
	return stripped
}

// stripSystemInstructions returns a copy of the call without the instruction
// sources once they are delivered authoritatively: Instructions cleared and
// system-role messages removed. The original call is never mutated; history
// tracking upstream already consumed it.
func stripSystemInstructions(call *aipapi.Call) *aipapi.Call {
	if call == nil {
		return nil
	}
	stripped := *call
	stripped.Instructions = nil
	if call.Messages != nil {
		kept := make([]aipapi.Message, 0, len(call.Messages))
		for _, m := range call.Messages {
			if m.Role == aipapi.RoleSystem {
				continue
			}
			kept = append(kept, m)
		}
		stripped.Messages = kept
	}
	return &stripped
}
