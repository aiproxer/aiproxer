package sessionwire_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestApplyAuthoritativeHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "  sid-1  ")
	h.Set(sessionwire.HeaderResumeToken, " tok-secret ")
	var ref aipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeaders(&ref, h)
	if ref.AuthoritativeSessionID != "sid-1" {
		t.Fatalf("AuthoritativeSessionID: got %q", ref.AuthoritativeSessionID)
	}
	if ref.ResumeToken != "tok-secret" {
		t.Fatalf("ResumeToken: got %q", ref.ResumeToken)
	}
}

func TestApplyAuthoritativeHeaders_nilSafe(t *testing.T) {
	t.Parallel()
	sessionwire.ApplyAuthoritativeHeaders(nil, http.Header{})
	var ref aipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeaders(&ref, nil)
}

func TestApplyAuthoritativeHeadersNamed_aliasAfterDefault(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-Custom-Session", "alias-sid")
	var ref aipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeadersNamed(&ref, h,
		[]string{sessionwire.HeaderAuthoritativeSessionID, "X-Custom-Session"},
		[]string{sessionwire.HeaderResumeToken},
	)
	if ref.AuthoritativeSessionID != "alias-sid" {
		t.Fatalf("got %q", ref.AuthoritativeSessionID)
	}
}

func TestWriteResponseCarriers_includesALegID(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	sessionwire.WriteResponseCarriers(rr, &aipapi.Call{Session: aipapi.SessionRef{
		ALegID:                 "a-leg-1",
		AuthoritativeSessionID: "sid-1",
		ResumeToken:            "resume-1",
	}})
	if got := rr.Header().Get(sessionwire.HeaderALegID); got != "a-leg-1" {
		t.Fatalf("A-leg header: got %q", got)
	}
}

func TestHTTPStatusForSessionDenial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code aipapi.SessionDenialCode
		want int
	}{
		{"missing_principal", aipapi.SessionDeniedMissingPrincipal, 401},
		{"invalid_authority", aipapi.SessionDeniedInvalidAuthority, 400},
		{"owner_mismatch", aipapi.SessionDeniedOwnerMismatch, 400},
		{"resume_expired", aipapi.SessionDeniedResumeExpired, 400},
		{"workspace", aipapi.SessionDeniedWorkspace, 400},
		{"policy_unavailable", aipapi.SessionDeniedPolicyUnavailable, 503},
		{"storage_unavailable", aipapi.SessionDeniedStorageUnavailable, 503},
		{"mandatory_audit", aipapi.SessionDeniedMandatoryAuditFailure, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sessionwire.HTTPStatusForSessionDenial(tc.code); got != tc.want {
				t.Fatalf("code %v: got %d want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestApplyMetadata(t *testing.T) {
	t.Parallel()
	var ref aipapi.SessionRef
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: "  m-sid  ",
		sessionwire.MetaKeyResumeToken:            " m-tok\t",
	}
	sessionwire.ApplyMetadata(&ref, meta)
	if ref.AuthoritativeSessionID != "m-sid" || ref.ResumeToken != "m-tok" {
		t.Fatalf("got %+v", ref)
	}
}

func TestApplyMetadataThenHeaders_headersWin(t *testing.T) {
	t.Parallel()
	var ref aipapi.SessionRef
	sessionwire.ApplyMetadata(&ref, map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: "meta-sid",
		sessionwire.MetaKeyResumeToken:            "meta-tok",
	})
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, " hdr-sid ")
	h.Set(sessionwire.HeaderResumeToken, " hdr-tok ")
	sessionwire.ApplyAuthoritativeHeaders(&ref, h)
	if ref.AuthoritativeSessionID != "hdr-sid" || ref.ResumeToken != "hdr-tok" {
		t.Fatalf("session: %+v", ref)
	}
}

func TestValidateMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		meta map[string]string
	}{
		{
			name: "oversized session id",
			meta: map[string]string{sessionwire.MetaKeyAuthoritativeSessionID: strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1)},
		},
		{
			name: "oversized resume token",
			meta: map[string]string{sessionwire.MetaKeyResumeToken: strings.Repeat("r", aipapi.MaxResumeTokenBytes+1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := sessionwire.ValidateMetadata(tt.meta); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateMetadata_allowsTrimmedCaps(t *testing.T) {
	t.Parallel()
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: " " + strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes) + " ",
		sessionwire.MetaKeyResumeToken:            "\t" + strings.Repeat("r", aipapi.MaxResumeTokenBytes) + "\n",
	}
	if err := sessionwire.ValidateMetadata(meta); err != nil {
		t.Fatal(err)
	}
}

func TestWithoutSensitiveToken(t *testing.T) {
	t.Parallel()
	const tok = "raw-resume-bearer"
	msg := "failed resume for " + tok + " and " + tok + " on trace"
	got := sessionwire.WithoutSensitiveToken(msg, tok)
	if got == msg {
		t.Fatal("expected redaction")
	}
	if strings.Contains(got, tok) {
		t.Fatalf("token still present: %q", got)
	}
	if strings.Count(got, "[REDACTED]") != 2 {
		t.Fatalf("redactions: %q", got)
	}
}

func TestWriteSessionResponseCarrier_ExactHeaderParityWithCanonical(t *testing.T) {
	t.Parallel()

	t.Run("new session with token matches canonical WriteResponseCarriers", func(t *testing.T) {
		t.Parallel()
		const (
			sid    = "sid-parity-new"
			alegID = "aleg-parity-new"
			tok    = "token-parity-new"
		)

		// Canonical writer output
		canonicalRec := httptest.NewRecorder()
		sessionwire.WriteResponseCarriers(canonicalRec, &aipapi.Call{
			Session: aipapi.SessionRef{
				AuthoritativeSessionID: sid,
				ALegID:                 alegID,
				ResumeToken:            tok,
			},
		})

		// Wire response carrier writer output
		wireRec := httptest.NewRecorder()
		carrier := largebody.SessionResponseCarrier{
			AuthoritativeSessionID: sid,
			ALegID:                 alegID,
			ResumeToken:            largebody.NewSensitiveString(tok),
		}
		sessionwire.WriteSessionResponseCarrier(wireRec, carrier)

		// Assert exact header key and value parity
		for _, hdr := range []string{
			sessionwire.HeaderAuthoritativeSessionID,
			sessionwire.HeaderALegID,
			sessionwire.HeaderResumeToken,
		} {
			cVal := canonicalRec.Header().Get(hdr)
			wVal := wireRec.Header().Get(hdr)
			if cVal == "" {
				t.Fatalf("canonical header %q was empty", hdr)
			}
			if wVal != cVal {
				t.Fatalf("header %q mismatch: canonical=%q wire=%q", hdr, cVal, wVal)
			}
		}
	})

	t.Run("resumed session without new token leaves resume header absent", func(t *testing.T) {
		t.Parallel()
		const (
			sid    = "sid-parity-resumed"
			alegID = "aleg-parity-resumed"
		)

		// Canonical writer output on resume
		canonicalRec := httptest.NewRecorder()
		sessionwire.WriteResponseCarriers(canonicalRec, &aipapi.Call{
			Session: aipapi.SessionRef{
				AuthoritativeSessionID: sid,
				ALegID:                 alegID,
				ResumeToken:            "",
			},
		})

		// Wire response carrier on resume (zero/empty SensitiveString)
		wireRec := httptest.NewRecorder()
		carrier := largebody.SessionResponseCarrier{
			AuthoritativeSessionID: sid,
			ALegID:                 alegID,
			ResumeToken:            largebody.NewSensitiveString(""),
		}
		sessionwire.WriteSessionResponseCarrier(wireRec, carrier)

		for _, hdr := range []string{
			sessionwire.HeaderAuthoritativeSessionID,
			sessionwire.HeaderALegID,
		} {
			cVal := canonicalRec.Header().Get(hdr)
			wVal := wireRec.Header().Get(hdr)
			if wVal != cVal {
				t.Fatalf("header %q mismatch: canonical=%q wire=%q", hdr, cVal, wVal)
			}
		}
		if got := wireRec.Header().Get(sessionwire.HeaderResumeToken); got != "" {
			t.Fatalf("resumed turn must not emit resume header, got %q", got)
		}
		if got := canonicalRec.Header().Get(sessionwire.HeaderResumeToken); got != "" {
			t.Fatalf("canonical resumed turn must not emit resume header, got %q", got)
		}
	})

	t.Run("WriteSessionResponseCarrierHeaders directly into http.Header", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		carrier := largebody.SessionResponseCarrier{
			AuthoritativeSessionID: "sid-direct",
			ALegID:                 "aleg-direct",
			ResumeToken:            largebody.NewSensitiveString("tok-direct"),
		}
		sessionwire.WriteSessionResponseCarrierHeaders(h, carrier)
		if h.Get(sessionwire.HeaderAuthoritativeSessionID) != "sid-direct" {
			t.Fatalf("session id: got %q", h.Get(sessionwire.HeaderAuthoritativeSessionID))
		}
		if h.Get(sessionwire.HeaderALegID) != "aleg-direct" {
			t.Fatalf("aleg id: got %q", h.Get(sessionwire.HeaderALegID))
		}
		if h.Get(sessionwire.HeaderResumeToken) != "tok-direct" {
			t.Fatalf("resume token: got %q", h.Get(sessionwire.HeaderResumeToken))
		}
	})

	t.Run("nil safety and zero values", func(t *testing.T) {
		t.Parallel()
		// Must not panic
		sessionwire.WriteSessionResponseCarrier(nil, largebody.SessionResponseCarrier{})
		sessionwire.WriteSessionResponseCarrierHeaders(nil, largebody.SessionResponseCarrier{})

		h := http.Header{}
		sessionwire.WriteSessionResponseCarrierHeaders(h, largebody.SessionResponseCarrier{})
		if len(h) != 0 {
			t.Fatalf("zero carrier should not set any headers, got %+v", h)
		}
	})
}
