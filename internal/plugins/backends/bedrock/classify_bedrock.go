package bedrock

import (
	"context"
	"errors"
	"net/http"
	"strings"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
)

// failureKind classifies a ConverseStream open failure for pre-output failover.
// The bedrock adapter has no local credential pool (the AWS SDK owns the credential
// chain), so every non-none kind maps to a recoverable pre-output failure at Open,
// matching the pool-exhausted end state of pooled backends.
type failureKind int

const (
	failureNone failureKind = iota
	failureThrottling
	failureAuthInvalid
	failureRetryableUpstream
)

// classifyBedrockError inspects err (including wrapped smithy operation errors) for
// throttling, invalid/expired credentials, and transient upstream or transport failures.
// Caller-side context cancellation is never an upstream failure.
func classifyBedrockError(err error) failureKind {
	if err == nil {
		return failureNone
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return failureNone
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		if kind := classifyBedrockCode(apiErr.ErrorCode()); kind != failureNone {
			return kind
		}
	}
	if status, ok := bedrockHTTPStatus(err); ok {
		switch {
		case status == http.StatusTooManyRequests:
			return failureThrottling
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return failureAuthInvalid
		case status == http.StatusRequestTimeout || status >= 500:
			return failureRetryableUpstream
		}
	}
	if transporterr.IsRetryable(err) {
		return failureRetryableUpstream
	}
	return failureNone
}

func classifyBedrockCode(code string) failureKind {
	switch {
	case strings.HasPrefix(code, "Throttling"),
		code == "ThrottledException",
		code == "TooManyRequestsException":
		return failureThrottling
	case strings.HasPrefix(code, "ExpiredToken"),
		code == "UnrecognizedClientException",
		code == "InvalidSignatureException",
		code == "InvalidTokenException",
		code == "AccessDeniedException",
		code == "AuthFailure",
		code == "MissingAuthenticationToken":
		return failureAuthInvalid
	case code == "InternalServerException",
		code == "InternalError",
		code == "InternalFailure",
		code == "ServiceUnavailable",
		code == "ServiceUnavailableException",
		code == "RequestTimeout",
		code == "RequestTimeoutException",
		code == "ModelTimeoutException":
		return failureRetryableUpstream
	default:
		return failureNone
	}
}

// bedrockHTTPStatus extracts the HTTP status from an AWS HTTP response error, guarding
// the doubly-nested response pointers that the SDK sets on real wire failures.
func bedrockHTTPStatus(err error) (int, bool) {
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) || respErr == nil || respErr.ResponseError == nil {
		return 0, false
	}
	res := respErr.HTTPResponse()
	if res == nil || res.Response == nil {
		return 0, false
	}
	return res.StatusCode, true
}
