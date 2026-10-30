package openairesponsestream

import "encoding/json"

func CallIDFromRawJSON(rawJSON string) string {
	if rawJSON == "" {
		return ""
	}
	var probe struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &probe); err != nil {
		return ""
	}
	return probe.CallID
}

// FunctionNameFromRawJSON reads the function name carried by
// response.function_call_arguments.done payloads. The typed event does not
// expose the field, but compatible backends still send it.
func FunctionNameFromRawJSON(rawJSON string) string {
	if rawJSON == "" {
		return ""
	}
	var probe struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &probe); err != nil {
		return ""
	}
	return probe.Name
}

func ToolCallIDFromRaw(itemID, rawJSON string) string {
	if itemID != "" {
		return itemID
	}
	return CallIDFromRawJSON(rawJSON)
}
