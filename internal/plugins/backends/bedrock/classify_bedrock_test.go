package bedrock

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func httpResponseError(status int, inner error) error {
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      inner,
		},
	}
}

func TestClassifyBedrockError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want failureKind
	}{
		{"nil", nil, failureNone},
		{"typed_throttling", &types.ThrottlingException{}, failureThrottling},
		{"typed_access_denied", &types.AccessDeniedException{}, failureAuthInvalid},
		{"typed_internal_server", &types.InternalServerException{}, failureRetryableUpstream},
		{"typed_service_unavailable", &types.ServiceUnavailableException{}, failureRetryableUpstream},
		{"typed_validation", &types.ValidationException{}, failureNone},
		{"code_throttling", &smithy.GenericAPIError{Code: "Throttling"}, failureThrottling},
		{"code_throttled", &smithy.GenericAPIError{Code: "ThrottledException"}, failureThrottling},
		{"code_too_many_requests", &smithy.GenericAPIError{Code: "TooManyRequestsException"}, failureThrottling},
		{"code_expired_token", &smithy.GenericAPIError{Code: "ExpiredToken"}, failureAuthInvalid},
		{"code_expired_token_exception", &smithy.GenericAPIError{Code: "ExpiredTokenException"}, failureAuthInvalid},
		{"code_unrecognized_client", &smithy.GenericAPIError{Code: "UnrecognizedClientException"}, failureAuthInvalid},
		{"code_invalid_signature", &smithy.GenericAPIError{Code: "InvalidSignatureException"}, failureAuthInvalid},
		{"code_internal_error", &smithy.GenericAPIError{Code: "InternalError"}, failureRetryableUpstream},
		{"code_service_unavailable", &smithy.GenericAPIError{Code: "ServiceUnavailable"}, failureRetryableUpstream},
		{"code_request_timeout", &smithy.GenericAPIError{Code: "RequestTimeoutException"}, failureRetryableUpstream},
		{"code_model_timeout", &smithy.GenericAPIError{Code: "ModelTimeoutException"}, failureRetryableUpstream},
		{"code_resource_not_found", &smithy.GenericAPIError{Code: "ResourceNotFoundException"}, failureNone},
		{
			"operation_error_wraps_typed",
			&smithy.OperationError{ServiceID: "Bedrock Runtime", OperationName: "ConverseStream", Err: &types.ThrottlingException{}},
			failureThrottling,
		},
		{
			"fmt_wrapped_api_error",
			fmt.Errorf("outer: %w", &smithy.GenericAPIError{Code: "ThrottlingException"}),
			failureThrottling,
		},
		{"http_500", httpResponseError(http.StatusInternalServerError, errors.New("boom")), failureRetryableUpstream},
		{"http_503", httpResponseError(http.StatusServiceUnavailable, errors.New("boom")), failureRetryableUpstream},
		{"http_408", httpResponseError(http.StatusRequestTimeout, errors.New("boom")), failureRetryableUpstream},
		{"http_429", httpResponseError(http.StatusTooManyRequests, errors.New("boom")), failureThrottling},
		{"http_401", httpResponseError(http.StatusUnauthorized, errors.New("boom")), failureAuthInvalid},
		{"http_403", httpResponseError(http.StatusForbidden, errors.New("boom")), failureAuthInvalid},
		{"http_400_plain", httpResponseError(http.StatusBadRequest, errors.New("boom")), failureNone},
		{
			"net_timeout",
			&url.Error{Op: "Post", URL: "http://x", Err: timeoutNetError{}},
			failureRetryableUpstream,
		},
		{
			"conn_reset",
			&url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
			failureRetryableUpstream,
		},
		{
			"conn_refused",
			&url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
			failureRetryableUpstream,
		},
		{
			"dns_not_found",
			&url.Error{Op: "Post", URL: "http://x", Err: &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}},
			failureRetryableUpstream,
		},
		{"context_canceled", context.Canceled, failureNone},
		{
			"wrapped_deadline_exceeded",
			&url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded},
			failureNone,
		},
		{"plain_error", errors.New("plain"), failureNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyBedrockError(tt.err); got != tt.want {
				t.Fatalf("classifyBedrockError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func openAgainstErrorServer(t *testing.T, status int, body string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	defer cancel()
	b := NewWithContext(ctx, Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
		HTTPClient:      srv.Client(),
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: ID, Model: "m"}}
	openCtx, openCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer openCancel()
	_, err := b.Open(openCtx, call, cand)
	if err == nil {
		t.Fatal("expected Open error")
	}
	return err
}

func TestBackendOpen_throttlingIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	err := openAgainstErrorServer(t, http.StatusTooManyRequests, `{"__type":"ThrottlingException","message":"slow down"}`)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bedrock: ConverseStream:") {
		t.Fatalf("expected backend-attributed wrap, got: %v", err)
	}
}

func TestBackendOpen_internalServerErrorIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	err := openAgainstErrorServer(t, http.StatusInternalServerError, `{"__type":"InternalServerException","message":"boom"}`)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
}

func TestBackendOpen_accessDeniedIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	err := openAgainstErrorServer(t, http.StatusBadRequest, `{"__type":"AccessDeniedException","message":"denied"}`)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
}

func TestBackendOpen_validationErrorStaysNonRecoverable(t *testing.T) {
	t.Parallel()
	err := openAgainstErrorServer(t, http.StatusBadRequest, `{"__type":"ValidationException","message":"bad input"}`)
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("did not expect recoverable pre-output, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bedrock: ConverseStream:") {
		t.Fatalf("expected backend-attributed wrap, got: %v", err)
	}
}
