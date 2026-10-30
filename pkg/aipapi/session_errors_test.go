package aipapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const distinctiveFakeResumeToken = "DISTINCTIVE_FAKE_RESUME_TOKEN_XYZ_12345"

func TestSessionDenialError_codesAndUnwrap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want aipapi.SessionDenialCode
	}{
		{"missing_principal", aipapi.NewSessionDenialMissingPrincipal(""), aipapi.SessionDeniedMissingPrincipal},
		{"invalid_authority", aipapi.NewSessionDenialInvalidAuthority(""), aipapi.SessionDeniedInvalidAuthority},
		{"owner_mismatch", aipapi.NewSessionDenialOwnerMismatch(""), aipapi.SessionDeniedOwnerMismatch},
		{"expired_resume", aipapi.NewSessionDenialResumeExpired(""), aipapi.SessionDeniedResumeExpired},
		{"quarantined", aipapi.NewSessionDenialQuarantined(""), aipapi.SessionDeniedQuarantined},
		{"workspace", aipapi.NewSessionDenialWorkspace(""), aipapi.SessionDeniedWorkspace},
		{"policy_unavailable", aipapi.NewSessionDenialPolicyUnavailable(""), aipapi.SessionDeniedPolicyUnavailable},
		{"storage_unavailable", aipapi.NewSessionDenialStorageUnavailable(""), aipapi.SessionDeniedStorageUnavailable},
		{"mandatory_audit", aipapi.NewSessionDenialMandatoryAuditFailure(""), aipapi.SessionDeniedMandatoryAuditFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sd *aipapi.SessionDenialError
			if !errors.As(tc.err, &sd) {
				t.Fatalf("expected SessionDenialError, got %T", tc.err)
			}
			if sd.Code() != tc.want {
				t.Fatalf("Code: got %q want %q", sd.Code(), tc.want)
			}
			if !errors.Is(tc.err, aipapi.ErrSessionDenial) {
				t.Fatalf("expected errors.Is(_, ErrSessionDenial)")
			}
			if aipapi.SessionDenialPublicCode(tc.err) == "" {
				t.Fatal("expected non-empty public code")
			}
		})
	}
}

func TestSessionDenialError_Error_doesNotLeakInternalReasonOrResumeToken(t *testing.T) {
	t.Parallel()
	internal := "probe: " + distinctiveFakeResumeToken + " detail"
	err := aipapi.NewSessionDenialInvalidAuthority(internal)
	msg := err.Error()
	if strings.Contains(msg, distinctiveFakeResumeToken) {
		t.Fatalf("Error() leaked token: %q", msg)
	}
	var sd *aipapi.SessionDenialError
	if !errors.As(err, &sd) {
		t.Fatal("expected SessionDenialError")
	}
	if sd.InternalReason() != internal {
		t.Fatalf("InternalReason not preserved")
	}
	// fmt path should still avoid token in default formatting of error chain
	joined := fmt.Errorf("wrap: %w", err)
	if strings.Contains(joined.Error(), distinctiveFakeResumeToken) {
		t.Fatalf("wrapped Error leaked token: %q", joined.Error())
	}
}

func TestSessionRef_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	ref := aipapi.SessionRef{
		ClientSessionID:        "client-hint",
		ContinuityKey:          "ck",
		ALegID:                 "aleg",
		AuthoritativeSessionID: "proxy-sess",
		ResumeToken:            "bearer-secret",
	}
	b, jerr := json.Marshal(ref)
	if jerr != nil {
		t.Fatal(jerr)
	}
	var got aipapi.SessionRef
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, aipapi.SessionRef{
		ClientSessionID:        ref.ClientSessionID,
		ContinuityKey:          ref.ContinuityKey,
		ALegID:                 ref.ALegID,
		AuthoritativeSessionID: ref.AuthoritativeSessionID,
		ResumeToken:            ref.ResumeToken,
	}) {
		t.Fatalf("round trip: %+v vs %+v", got, ref)
	}
	if got.Metadata != nil {
		t.Fatalf("session metadata must not be serialized: %#v", got.Metadata)
	}
}
