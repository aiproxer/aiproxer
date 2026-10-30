package openaichat

import (
	"encoding/json"
)

// strictReasoningContentViolation reports whether body violates DeepSeek
// thinking-mode strictness: with a tools parameter present, every assistant
// message must carry the reasoning_content field (empty string counts as
// present). Requests without tools are never violations. A present-but-empty
// field satisfies this presence validator; it is not proof of lossless
// reasoning preservation.
func strictReasoningContentViolation(body []byte) bool {
	var req struct {
		Tools    json.RawMessage   `json:"tools"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	if len(req.Tools) == 0 || string(req.Tools) == "null" {
		return false
	}
	var tools []any
	if err := json.Unmarshal(req.Tools, &tools); err != nil || len(tools) == 0 {
		return false
	}
	for _, raw := range req.Messages {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		var role string
		if r, ok := msg["role"]; ok {
			_ = json.Unmarshal(r, &role)
		}
		if role != "assistant" {
			continue
		}
		if _, ok := msg["reasoning_content"]; !ok {
			return true
		}
	}
	return false
}

const strictReasoningContentError = "{\"error\":{\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"invalid_request_error\"}}"
