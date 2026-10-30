package anthropicmessages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"

	asdk "github.com/anthropics/anthropic-sdk-go"
)

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestClassifyAnthropicAPIError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		makeErr  func() error
		wantKind apiFailureKind
		wantRA   string
	}{
		{
			name:    "nil",
			makeErr: func() error { return nil },
		},
		{
			name: "unauthorized",
			makeErr: func() error {
				return &asdk.Error{StatusCode: http.StatusUnauthorized, Response: &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}}
			},
			wantKind: apiFailureAuthInvalid,
		},
		{
			name: "rate_limit_with_retry_after",
			makeErr: func() error {
				rec := httptest.NewRecorder()
				rec.Header().Set("Retry-After", "30")
				return &asdk.Error{StatusCode: http.StatusTooManyRequests, Response: rec.Result()}
			},
			wantKind: apiFailureRateLimited,
			wantRA:   "30",
		},
		{
			name: "bad_request",
			makeErr: func() error {
				return &asdk.Error{StatusCode: http.StatusBadRequest, Response: &http.Response{StatusCode: http.StatusBadRequest}}
			},
			wantKind: apiFailureNone,
		},
		{
			name: "internal_server_error",
			makeErr: func() error {
				return &asdk.Error{StatusCode: http.StatusInternalServerError, Response: &http.Response{StatusCode: http.StatusInternalServerError}}
			},
			wantKind: apiFailureRetryable,
		},
		{
			name: "bad_gateway_wrapped",
			makeErr: func() error {
				return errors.Join(errors.New("wrap"), &asdk.Error{StatusCode: http.StatusBadGateway, Response: &http.Response{StatusCode: http.StatusBadGateway}})
			},
			wantKind: apiFailureRetryable,
		},
		{
			name: "request_timeout_status",
			makeErr: func() error {
				return &asdk.Error{StatusCode: http.StatusRequestTimeout, Response: &http.Response{StatusCode: http.StatusRequestTimeout}}
			},
			wantKind: apiFailureRetryable,
		},
		{
			name: "net_timeout",
			makeErr: func() error {
				return &url.Error{Op: "Post", URL: "http://x", Err: timeoutNetError{}}
			},
			wantKind: apiFailureRetryable,
		},
		{
			name: "conn_reset",
			makeErr: func() error {
				return &url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}
			},
			wantKind: apiFailureRetryable,
		},
		{
			name: "context_deadline_not_transport",
			makeErr: func() error {
				return context.DeadlineExceeded
			},
			wantKind: apiFailureNone,
		},
		{
			name: "plain_error",
			makeErr: func() error {
				return errors.New("plain")
			},
			wantKind: apiFailureNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, ra := classifyAnthropicAPIError(tt.makeErr())
			if kind != tt.wantKind || ra != tt.wantRA {
				t.Fatalf("got kind=%v ra=%q want kind=%v ra=%q", kind, ra, tt.wantKind, tt.wantRA)
			}
		})
	}
}
