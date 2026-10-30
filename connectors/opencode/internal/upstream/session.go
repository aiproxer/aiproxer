package upstream

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const HeaderOpenCodeSession = "x-opencode-session"

func resolveSessionID(call aipapi.Call) string {
	if s := strings.TrimSpace(call.Session.CorrelationID()); s != "" {
		return s
	}
	if call.Session.Metadata != nil {
		for _, k := range []string{HeaderOpenCodeSession, "session_id", "client_session_id"} {
			if s := strings.TrimSpace(call.Session.Metadata[k]); s != "" {
				return s
			}
		}
	}
	if s := strings.TrimSpace(call.ID); s != "" {
		return s
	}
	return mintFallbackSessionID()
}

func mintFallbackSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "aip-session"
	}
	return "aip-" + hex.EncodeToString(b[:])
}

func sanitizeOpenAIPayload(body map[string]any, model string) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	if normalized == "omen-alpha" || strings.HasPrefix(normalized, "glm-") {
		delete(body, "reasoning")
		delete(body, "reasoning_effort")
		delete(body, "thinking")
	}
}
