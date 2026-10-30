package sessionwire_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestHasSessionMetadata(t *testing.T) {
	t.Parallel()

	if sessionwire.HasSessionMetadata(nil) {
		t.Error("HasSessionMetadata(nil) = true, want false")
	}
	if sessionwire.HasSessionMetadata(map[string]string{}) {
		t.Error("HasSessionMetadata(empty) = true, want false")
	}
	if sessionwire.HasSessionMetadata(map[string]string{"user_id": "u1", "env": "prod"}) {
		t.Error("HasSessionMetadata(non-AIP) = true, want false")
	}
	if !sessionwire.HasSessionMetadata(map[string]string{sessionwire.MetaKeyAuthoritativeSessionID: "sess-1"}) {
		t.Error("HasSessionMetadata(aip_session_id) = false, want true")
	}
	if !sessionwire.HasSessionMetadata(map[string]string{sessionwire.MetaKeyResumeToken: "tok-1"}) {
		t.Error("HasSessionMetadata(aip_resume_token) = false, want true")
	}
}

func TestBuildSessionInput_SessionWire(t *testing.T) {
	t.Parallel()

	t.Run("headers extract and precedence over body metadata", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
		h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
		h.Set(sessionwire.HeaderALegID, "hdr-aleg")
		h.Set(sessionwire.HeaderSessionHint, "hdr-hint")

		meta := map[string]string{
			sessionwire.MetaKeyAuthoritativeSessionID: "body-sid",
			sessionwire.MetaKeyResumeToken:            "body-tok",
		}

		in, err := sessionwire.BuildSessionInput(h, meta, sessionwire.SessionInputOptions{
			RejectBodyMetadata: false,
		})
		if err != nil {
			t.Fatalf("BuildSessionInput failed: %v", err)
		}

		if in.AuthoritativeSessionID != "hdr-sid" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "hdr-sid")
		}
		if in.ResumeToken.Reveal() != "hdr-tok" {
			t.Errorf("ResumeToken = %q, want %q", in.ResumeToken.Reveal(), "hdr-tok")
		}
		if in.ALegID != "hdr-aleg" {
			t.Errorf("ALegID = %q, want %q", in.ALegID, "hdr-aleg")
		}
		if in.ClientSessionID != "hdr-hint" {
			t.Errorf("ClientSessionID = %q, want %q", in.ClientSessionID, "hdr-hint")
		}
		if in.NewSessionRequested {
			t.Errorf("NewSessionRequested = true, want false")
		}
	})

	t.Run("body metadata rejection when RejectBodyMetadata is true", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
		meta := map[string]string{
			sessionwire.MetaKeyAuthoritativeSessionID: "body-sid",
		}

		_, err := sessionwire.BuildSessionInput(h, meta, sessionwire.SessionInputOptions{
			RejectBodyMetadata: true,
		})
		if err == nil {
			t.Fatal("expected error when RejectBodyMetadata is true and body has session metadata")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("non-AIP body metadata is not rejected when RejectBodyMetadata is true", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
		h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
		meta := map[string]string{
			"custom_project_id": "proj-123",
		}

		in, err := sessionwire.BuildSessionInput(h, meta, sessionwire.SessionInputOptions{
			RejectBodyMetadata: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if in.AuthoritativeSessionID != "hdr-sid" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "hdr-sid")
		}
	})

	t.Run("header aliases support", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		h.Set("X-Custom-Session", "alias-sid")
		h.Set("X-Custom-Resume", "alias-tok")

		aliases := aipsdk.HTTPHeaders{
			SessionID:   []string{"X-Custom-Session"},
			ResumeToken: []string{"X-Custom-Resume"},
		}

		in, err := sessionwire.BuildSessionInput(h, nil, sessionwire.SessionInputOptions{
			HeaderAliases: aliases,
		})
		if err != nil {
			t.Fatalf("BuildSessionInput error: %v", err)
		}
		if in.AuthoritativeSessionID != "alias-sid" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "alias-sid")
		}
		if in.ResumeToken.Reveal() != "alias-tok" {
			t.Errorf("ResumeToken = %q, want %q", in.ResumeToken.Reveal(), "alias-tok")
		}
	})

	t.Run("bounds validation on metadata when permitted", func(t *testing.T) {
		t.Parallel()
		meta := map[string]string{
			sessionwire.MetaKeyAuthoritativeSessionID: strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1),
		}
		_, err := sessionwire.BuildSessionInput(nil, meta, sessionwire.SessionInputOptions{
			RejectBodyMetadata: false,
		})
		if err == nil {
			t.Fatal("expected error for oversized metadata session id")
		}
	})
}
