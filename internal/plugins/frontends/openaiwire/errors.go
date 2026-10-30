package openaiwire

import (
	"encoding/json"
	"net/http"
)

type APIError struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   any     `json:"param"`
		Code    *string `json:"code,omitempty"`
	} `json:"error"`
}

// WriteErrorJSON writes an OpenAI-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we APIError
	we.Error.Message = message
	we.Error.Type = errType
	we.Error.Param = nil
	if code != "" {
		we.Error.Code = &code
	}
	return json.NewEncoder(w).Encode(we)
}
