package gemini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		status         int
		message        string
		expectedStatus string
	}{
		{
			name:           "bad request",
			status:         http.StatusBadRequest,
			message:        "bad request message",
			expectedStatus: "INVALID_ARGUMENT",
		},
		{
			name:           "unauthorized",
			status:         http.StatusUnauthorized,
			message:        "unauthorized message",
			expectedStatus: "UNAUTHENTICATED",
		},
		{
			name:           "forbidden",
			status:         http.StatusForbidden,
			message:        "forbidden message",
			expectedStatus: "PERMISSION_DENIED",
		},
		{
			name:           "service unavailable",
			status:         http.StatusServiceUnavailable,
			message:        "unavailable message",
			expectedStatus: "UNAVAILABLE",
		},
		{
			name:           "internal server error",
			status:         http.StatusInternalServerError,
			message:        "internal error message",
			expectedStatus: "INTERNAL",
		},
		{
			name:           "not found (default)",
			status:         http.StatusNotFound,
			message:        "not found message",
			expectedStatus: "UNKNOWN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			err := WriteErrorJSON(w, tt.status, tt.message)
			if err != nil {
				t.Fatalf("WriteErrorJSON returned an unexpected error: %v", err)
			}

			res := w.Result()
			defer func() { _ = res.Body.Close() }()

			if res.StatusCode != tt.status {
				t.Errorf("expected status code %d, got %d", tt.status, res.StatusCode)
			}

			if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", contentType)
			}

			var got wireAPIError
			if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
				t.Fatalf("failed to decode JSON response: %v", err)
			}

			if got.Error.Code != tt.status {
				t.Errorf("expected JSON code %d, got %d", tt.status, got.Error.Code)
			}
			if got.Error.Message != tt.message {
				t.Errorf("expected JSON message %q, got %q", tt.message, got.Error.Message)
			}
			if got.Error.Status != tt.expectedStatus {
				t.Errorf("expected JSON status %q, got %q", tt.expectedStatus, got.Error.Status)
			}
		})
	}
}
