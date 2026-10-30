package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TranscriptHistoryCoordinator is the transcript-aware history coordinator for
// ACP-style backends: serializes the conversation as Markdown with a SHA-256
// prefix hash for divergence detection. Callers that
// own per-session process state (the ACP CLI subprocess runner and the Codex
// app-server runner) feed the runtime's historyState into
// [TranscriptHistoryCoordinator.ComputeHistoryAndUserMessage] and use the
// returned userMessage as the single prompt input, resetting the agent
// process when resetNeeded is true.
type TranscriptHistoryCoordinator struct{}

// TranscriptResult is the output of history computation: the user message to
// send to the agent, the new history state to commit, and whether the agent
// process must be reset (killed/respawned) before the prompt is sent because
// the conversation diverged from the agent's known state. The HistoryState
// field is an opaque value obtained from [SubprocessRuntime.HistoryState] and
// committed via [SubprocessRuntime.SetHistoryState]; cross-package callers
// pass it through without inspecting it.
type TranscriptResult struct {
	UserMessage  string
	HistoryState historyState
	ResetNeeded  bool
}

// ComputeHistoryAndUserMessage determines what text to send to the ACP agent
// based on the current conversation history and the agent's known state.
//
// History selection rules:
//  1. If the agent has no prior history state (fresh process), serialize the
//     full transcript.
//  2. If the message prefix hash diverged from the agent's known state, the
//     history was edited or truncated — reset the agent (caller kills/respawns)
//     and serialize the full transcript (ResetNeeded is true).
//  3. If the message count matches the agent's known state (same messages as
//     last turn, e.g. client retry), extract just the last user message.
//  4. Otherwise (append-only), serialize the tail from the agent's known
//     message count to the end.
func (h *TranscriptHistoryCoordinator) ComputeHistoryAndUserMessage(
	messages []aipapi.Message,
	state historyState,
) TranscriptResult {
	n := state.messageCount

	// Fresh process: no prior history.
	if n == 0 || state.prefixHash == "" {
		return TranscriptResult{
			UserMessage:  serializeTranscript(messages),
			HistoryState: newHistoryState(messages),
		}
	}

	// Check for divergence: hash the first n messages and compare.
	currentPrefixHash := hashMessagesPrefix(messages, min(n, len(messages)))
	if currentPrefixHash != state.prefixHash || len(messages) < n {
		// History diverged or shrank — caller must reset the agent process.
		// We return the full transcript; the caller is responsible for
		// killing/respawning the process before sending it.
		return TranscriptResult{
			UserMessage:  serializeTranscript(messages),
			HistoryState: newHistoryState(messages),
			ResetNeeded:  true,
		}
	}

	// Same message list as last turn (e.g. client retry).
	if len(messages) == n {
		return TranscriptResult{
			UserMessage:  extractLastUserMessage(messages),
			HistoryState: state,
		}
	}

	// Append-only: serialize from n to end.
	tail := serializeTranscriptTail(messages, n)
	if strings.TrimSpace(tail) == "" {
		tail = extractLastUserMessage(messages)
	}
	return TranscriptResult{
		UserMessage:  tail,
		HistoryState: newHistoryState(messages),
	}
}

// prepareTranscriptCall returns a shallow copy of orig with its messages
// replaced by a single user text message containing the serialized transcript
// produced by ComputeHistoryAndUserMessage. When userMessage is empty (e.g. no
// user message in the call), the original messages are preserved so the
// downstream prompt mapper surfaces its usual empty-prompt error. Returns nil
// only when orig is nil.
func prepareTranscriptCall(orig *aipapi.Call, userMessage string) *aipapi.Call {
	if orig == nil {
		return nil
	}
	c := *orig
	if strings.TrimSpace(userMessage) == "" {
		return &c
	}
	c.Messages = []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart(userMessage)},
	}}
	return &c
}

// serializeTranscript converts the full message history into a Markdown string
// for the ACP agent. The last user message is the "current" prompt; preceding
// messages are "Previous Context."
func serializeTranscript(messages []aipapi.Message) string {
	if len(messages) == 0 {
		return ""
	}
	// Find the last user message index.
	lastUserIdx := -1
	for i, message := range slices.Backward(messages) {
		if message.Role == aipapi.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		// No user message — serialize everything as context.
		var b strings.Builder
		for _, m := range messages {
			appendSerializedMessage(&b, m)
		}
		return strings.TrimSpace(b.String())
	}

	// Serialize history before the last user message as "Previous Context."
	var b strings.Builder
	if lastUserIdx > 0 {
		b.WriteString("Previous Context:\n\n")
		for i := range lastUserIdx {
			appendSerializedMessage(&b, messages[i])
		}
		b.WriteString("\n---\n\n")
	}

	// Append the last user message as the current prompt.
	appendSerializedMessage(&b, messages[lastUserIdx])
	// Append any messages after the last user message (e.g. assistant responses
	// in a multi-turn conversation that the agent needs to see).
	for i := lastUserIdx + 1; i < len(messages); i++ {
		appendSerializedMessage(&b, messages[i])
	}
	return strings.TrimSpace(b.String())
}

// serializeTranscriptTail serializes messages from startIdx to the end, for
// incremental append when the agent already saw messages[:startIdx].
func serializeTranscriptTail(messages []aipapi.Message, startIdx int) string {
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(messages) {
		return ""
	}
	var b strings.Builder
	for i := startIdx; i < len(messages); i++ {
		appendSerializedMessage(&b, messages[i])
	}
	return strings.TrimSpace(b.String())
}

// appendSerializedMessage appends one message to the builder in Markdown format.
func appendSerializedMessage(b *strings.Builder, m aipapi.Message) {
	role := roleLabel(m.Role)
	for _, p := range m.Parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			fmt.Fprintf(b, "**%s:** %s\n\n", role, p.Text)
		case aipapi.PartImageRef:
			fmt.Fprintf(b, "**%s:** [image: %s]\n\n", role, p.ImageRef)
		case aipapi.PartFileRef:
			name := p.FileName
			if name == "" {
				name = p.FileRef
			}
			fmt.Fprintf(b, "**%s:** [file: %s]\n\n", role, name)
		}
	}
}

// roleLabel returns a human-readable role label for the transcript.
func roleLabel(role aipapi.Role) string {
	switch role {
	case aipapi.RoleUser:
		return "User"
	case aipapi.RoleAssistant:
		return "Assistant"
	case aipapi.RoleSystem:
		return "System"
	case aipapi.RoleTool:
		return "Tool"
	default:
		return string(role)
	}
}

// extractLastUserMessage extracts the text of the last user message.
func extractLastUserMessage(messages []aipapi.Message) string {
	for _, message := range slices.Backward(messages) {
		if message.Role == aipapi.RoleUser {
			var texts []string
			for _, p := range message.Parts {
				if p.Kind == aipapi.PartText && strings.TrimSpace(p.Text) != "" {
					texts = append(texts, p.Text)
				}
			}
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

// hashMessagesPrefix computes a stable SHA-256 hash of the first endExclusive
// messages, used for conversation-prefix divergence detection.
func hashMessagesPrefix(messages []aipapi.Message, endExclusive int) string {
	if endExclusive <= 0 || endExclusive > len(messages) {
		endExclusive = len(messages)
	}
	h := sha256.New()
	for i := range endExclusive {
		m := messages[i]
		_, _ = fmt.Fprintf(h, "%s|", m.Role)
		for _, p := range m.Parts {
			switch p.Kind {
			case aipapi.PartText:
				_, _ = fmt.Fprintf(h, "text:%s|", p.Text)
			case aipapi.PartImageRef:
				_, _ = fmt.Fprintf(h, "img:%s|", p.ImageRef)
			case aipapi.PartFileRef:
				_, _ = fmt.Fprintf(h, "file:%s:%s|", p.FileRef, p.FileName)
			}
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newHistoryState creates a history state from the full message list.
func newHistoryState(messages []aipapi.Message) historyState {
	return historyState{
		messageCount: len(messages),
		prefixHash:   hashMessagesPrefix(messages, len(messages)),
	}
}
