package gemini

import (
	"encoding/json"
	"net/http"
)

type wireAPIError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// WriteErrorJSON writes a Google-style JSON error for generateContent failures.
// Status strings are derived from the HTTP status code.
func WriteErrorJSON(w http.ResponseWriter, status int, message string) error {
	return WriteErrorJSONWithStatus(w, status, message, googleStatusFromHTTP(status))
}

// WriteErrorJSONWithStatus writes a Google-style JSON error with an explicit RPC status string.
func WriteErrorJSONWithStatus(w http.ResponseWriter, status int, message, googleStatus string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Error.Code = status
	we.Error.Message = message
	if googleStatus == "" {
		googleStatus = googleStatusFromHTTP(status)
	}
	we.Error.Status = googleStatus
	return json.NewEncoder(w).Encode(we)
}

func googleStatusFromHTTP(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case status == http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case status == http.StatusForbidden:
		return "PERMISSION_DENIED"
	case status == http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case status >= 500:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}
