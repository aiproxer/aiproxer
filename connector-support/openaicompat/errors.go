package openaicompat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPError is a bounded upstream HTTP failure.
type HTTPError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "openaicompat: http error"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("openaicompat: http %d (%s): %s", e.Status, e.Code, msg)
	}
	return fmt.Sprintf("openaicompat: http %d: %s", e.Status, msg)
}

func readHTTPError(resp *http.Response, maxBody int64) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	err := &HTTPError{
		Status:     resp.StatusCode,
		RetryAfter: resp.Header.Get("Retry-After"),
		Message:    strings.TrimSpace(string(raw)),
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		if payload.Error.Message != "" {
			err.Message = payload.Error.Message
		}
		switch v := payload.Error.Code.(type) {
		case string:
			err.Code = v
		case float64:
			err.Code = fmt.Sprintf("%g", v)
		}
		if err.Code == "" {
			err.Code = payload.Error.Type
		}
	}
	return err
}
