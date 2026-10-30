package acp

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func messageIDForCall(call *aipapi.Call) string {
	if call == nil || call.Extensions == nil {
		return "msg_" + stableCallToken(call)
	}
	raw, ok := call.Extensions[extMessageIDJSONKey]
	if !ok || len(raw) == 0 {
		return "msg_" + stableCallToken(call)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
		return "msg_" + stableCallToken(call)
	}
	return strings.TrimSpace(s)
}

func buildPromptParams(sessionID string, blocks []map[string]any, messageID string) map[string]any {
	p := map[string]any{
		"sessionId": sessionID,
		"prompt":    blocks,
	}
	if strings.TrimSpace(messageID) != "" {
		p["messageId"] = messageID
	}
	return p
}
