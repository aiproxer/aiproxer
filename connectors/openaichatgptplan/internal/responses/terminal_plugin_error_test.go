package responses_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestClassifiedError_TerminalPluginErrorMapsTheRecoveryClass proves the local
// taxonomy is not hidden behind the generic terminal: each class reaches the
// host with a stable code and safe retryability.
func TestClassifiedError_TerminalPluginErrorMapsTheRecoveryClass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		err       *responses.ClassifiedError
		wantCode  backendplugin.ErrorCode
		retryable bool
	}{
		{
			name:      "temporary",
			err:       &responses.ClassifiedError{Class: responses.ErrorClassTemporaryUnavailable, Retryable: true},
			wantCode:  backendplugin.ErrorCodeProviderTransient,
			retryable: true,
		},
		{
			name:     "temporary but not retryable",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassTemporaryUnavailable},
			wantCode: backendplugin.ErrorCodeProviderTransient,
		},
		{
			name:     "usage exhausted",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassUsageExhausted, Retryable: false},
			wantCode: backendplugin.ErrorCodeResourceExhausted,
		},
		{
			name:     "invalid context 401",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassInvalidUserContext, Status: 401},
			wantCode: backendplugin.ErrorCodeUnauthenticated,
		},
		{
			name:     "invalid context 403",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassInvalidUserContext, Status: 403},
			wantCode: backendplugin.ErrorCodePermissionDenied,
		},
		{
			name:     "refresh terminal",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassRefreshTerminal},
			wantCode: backendplugin.ErrorCodeUnauthenticated,
		},
		{
			name:     "protocol",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassProtocolViolation},
			wantCode: backendplugin.ErrorCodeProtocolViolation,
		},
		{
			name:     "provider failure",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassProviderFailure},
			wantCode: backendplugin.ErrorCodeProviderTerminal,
		},
		{
			name:     "unsupported",
			err:      &responses.ClassifiedError{Class: responses.ErrorClassUnsupportedCapability},
			wantCode: backendplugin.ErrorCodeFailedPrecondition,
		},
		{
			name:     "unknown",
			err:      &responses.ClassifiedError{Class: responses.ErrorClass("unknown")},
			wantCode: backendplugin.ErrorCodeInternal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.err.TerminalPluginError()
			if got == nil {
				t.Fatal("no terminal mapping")
			}
			if got.Code != tc.wantCode || got.Retryable != tc.retryable {
				t.Fatalf("terminal=%+v, want code=%q retryable=%v", got, tc.wantCode, tc.retryable)
			}
		})
	}
}

// TestClassifiedError_TerminalMessageCarriesNoProviderOrProfileText proves the
// host-visible terminal is built from constants, never from the classified
// provider text, code, request id or local profile.
func TestClassifiedError_TerminalMessageCarriesNoProviderOrProfileText(t *testing.T) {
	t.Parallel()
	err := &responses.ClassifiedError{
		Class:      responses.ErrorClassTemporaryUnavailable,
		Retryable:  true,
		Status:     503,
		Code:       "provider-secret-code",
		RequestID:  "provider-request-id",
		Diagnostic: "provider diagnostic text",
		ProfileID:  "acct-local-profile",
	}
	got := err.TerminalPluginError()
	if got == nil {
		t.Fatal("no terminal mapping")
	}
	for _, marker := range []string{"provider-secret-code", "provider-request-id", "provider diagnostic text", "acct-local-profile"} {
		if strings.Contains(got.Message, marker) {
			t.Fatalf("terminal message %q carries %q", got.Message, marker)
		}
	}
}
