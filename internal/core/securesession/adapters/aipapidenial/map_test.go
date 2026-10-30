package aipapidenial

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMapToSessionDenial_Nil(t *testing.T) {
	t.Parallel()
	if MapToSessionDenial(nil) != nil {
		t.Fatalf("expected nil")
	}
}

func TestMapToSessionDenial_ClassifyWithoutStringMatching(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   error
		want aipapi.SessionDenialCode
	}{
		{"missing_principal", domain.ErrMissingPrincipal, aipapi.SessionDeniedMissingPrincipal},
		{"not_found", domain.ErrSessionNotFound, aipapi.SessionDeniedInvalidAuthority},
		{"invalid_token", domain.ErrInvalidResumeToken, aipapi.SessionDeniedInvalidAuthority},
		{"owner", domain.ErrOwnerMismatch, aipapi.SessionDeniedOwnerMismatch},
		{"expired", domain.ErrResumeExpired, aipapi.SessionDeniedResumeExpired},
		{"quarantined", domain.ErrSessionQuarantined, aipapi.SessionDeniedQuarantined},
		{"workspace", domain.ErrWorkspaceDenied, aipapi.SessionDeniedWorkspace},
		{"policy", domain.ErrPolicyUnavailable, aipapi.SessionDeniedPolicyUnavailable},
		{"workspace_unresolved", domain.ErrWorkspaceUnresolved, aipapi.SessionDeniedWorkspace},
		{"storage", domain.ErrStorageUnavailable, aipapi.SessionDeniedStorageUnavailable},
		{"audit", domain.ErrMandatoryAuditFailure, aipapi.SessionDeniedMandatoryAuditFailure},
		{"dup_session", domain.ErrDuplicateSessionID, aipapi.SessionDeniedInvalidAuthority},
		{"dup_fingerprint", domain.ErrDuplicateFingerprint, aipapi.SessionDeniedInvalidAuthority},
		{"transcript_off", domain.ErrTranscriptDisabled, aipapi.SessionDeniedInvalidAuthority},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := MapToSessionDenial(tc.in)
			var sd *aipapi.SessionDenialError
			if !errors.As(out, &sd) {
				t.Fatalf("expected SessionDenialError")
			}
			if got := aipapi.SessionDenialPublicCode(out); string(tc.want) != got {
				t.Fatalf("code: got %q want %q", got, tc.want)
			}
			if sd.Code() != tc.want {
				t.Fatalf("Code(): got %v want %v", sd.Code(), tc.want)
			}
		})
	}
}

func TestMapToSessionDenial_NonEnumeratingPublicMessage(t *testing.T) {
	t.Parallel()

	// Unknown session, bad token, and wrong owner must not give clients different public messages
	// that reveal which case occurred (same safe surface for invalid vs not-found).
	a := MapToSessionDenial(domain.ErrSessionNotFound)
	b := MapToSessionDenial(domain.ErrInvalidResumeToken)
	c := MapToSessionDenial(domain.ErrOwnerMismatch)

	var sa, sb, sc *aipapi.SessionDenialError
	if !errors.As(a, &sa) || !errors.As(b, &sb) || !errors.As(c, &sc) {
		t.Fatalf("expected session denial errors")
	}
	if sa.PublicMessage() != sb.PublicMessage() {
		t.Fatalf("not_found vs invalid public message differ: %q vs %q", sa.PublicMessage(), sb.PublicMessage())
	}
	// Owner mismatch uses the same client-safe phrase as invalid authority in aipapi.
	if sb.PublicMessage() != sc.PublicMessage() {
		t.Fatalf("invalid vs owner public message differ: %q vs %q", sb.PublicMessage(), sc.PublicMessage())
	}
	if sa.InternalReason() == sb.InternalReason() {
		t.Fatalf("internal reasons should differ for operator diagnostics")
	}
	if sb.InternalReason() == sc.InternalReason() {
		t.Fatalf("internal reasons should differ between invalid token and owner mismatch")
	}
}

func TestMapToSessionDenial_UnknownErrorMapsToInvalidAuthority(t *testing.T) {
	t.Parallel()
	in := errors.New("some opaque failure")
	out := MapToSessionDenial(in)
	if aipapi.SessionDenialPublicCode(out) != string(aipapi.SessionDeniedInvalidAuthority) {
		t.Fatalf("expected invalid authority code")
	}
}
