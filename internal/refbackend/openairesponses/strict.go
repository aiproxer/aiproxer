package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"
)

// strictAdjacencyViolation reports whether input contains a non-call,
// non-output item between a function_call and its matching function_call_output.
// It mirrors strict upstream adjacency: outputs must follow their calls with no
// interleaving message/reasoning items. Duplicate or missing call_ids are not
// violations here; the upstream answers those itself.
func strictAdjacencyViolation(body []byte) (string, bool) {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", false
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		return "", false
	}
	// String input is a single user message; never a violation.
	if strings.HasPrefix(strings.TrimSpace(string(req.Input)), `"`) {
		return "", false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return "", false
	}
	type itemKind int
	const (
		kindOther itemKind = iota
		kindCall
		kindOutput
	)
	kinds := make([]itemKind, len(items))
	callIDs := make([]string, len(items))
	for i, it := range items {
		var typ string
		if t, ok := it["type"]; ok {
			_ = json.Unmarshal(t, &typ)
		} else if _, ok := it["role"]; ok {
			typ = "message"
		}
		switch strings.TrimSpace(typ) {
		case "function_call":
			kinds[i] = kindCall
			if c, ok := it["call_id"]; ok {
				_ = json.Unmarshal(c, &callIDs[i])
				callIDs[i] = strings.TrimSpace(callIDs[i])
			}
		case "function_call_output":
			kinds[i] = kindOutput
			if c, ok := it["call_id"]; ok {
				_ = json.Unmarshal(c, &callIDs[i])
				callIDs[i] = strings.TrimSpace(callIDs[i])
			}
		default:
			kinds[i] = kindOther
		}
	}
	// For each call with a unique, present call_id that has a later matching
	// output, any other-kind item strictly between them is a violation.
	seenCall := map[string]int{}
	for i, k := range kinds {
		if k != kindCall || callIDs[i] == "" {
			continue
		}
		if _, dup := seenCall[callIDs[i]]; dup {
			continue
		}
		seenCall[callIDs[i]] = i
	}
	// Skip groups with duplicate call_ids entirely.
	callCount := map[string]int{}
	for i, k := range kinds {
		if k == kindCall && callIDs[i] != "" {
			callCount[callIDs[i]]++
		}
	}
	for callID, idx := range seenCall {
		if callCount[callID] != 1 {
			continue
		}
		outIdx := -1
		for j := idx + 1; j < len(items); j++ {
			if kinds[j] == kindOutput && callIDs[j] == callID {
				outIdx = j
				break
			}
		}
		if outIdx < 0 {
			continue
		}
		for k := idx + 1; k < outIdx; k++ {
			if kinds[k] == kindOther {
				return callID, true
			}
		}
	}
	return "", false
}

func strictAdjacencyError(callID string) string {
	return fmt.Sprintf(`{"error":{"message":"No tool output found for tool call %s","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, callID)
}
