package largebody_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 9.1: Build exact bounded SessionInput
// Requirements: 14, 17.
// Proves:
//  1. Precedence: header/body/session/resume/client-session precedence.
//  2. Body-metadata rejection: initial profiles may reject body-carried AIProxer metadata and support authoritative headers only.
//  3. Resume-token exclusion: resume token never enters backend facts or telemetry.

func TestSessionInput_Precedence(t *testing.T) {
	t.Parallel()

	t.Run("header only extracts authoritative fields", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: "  sess-auth-1  ",
			HeaderResumeToken:            "  tok-resume-1  ",
			HeaderALegID:                 "  aleg-1  ",
			HeaderClientSessionHint:      "  client-hint-1  ",
		}
		in, err := largebody.BuildSessionInput(src, 64*1024)
		if err != nil {
			t.Fatalf("BuildSessionInput error: %v", err)
		}
		if in.AuthoritativeSessionID != "sess-auth-1" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "sess-auth-1")
		}
		if in.ResumeToken.Reveal() != "tok-resume-1" {
			t.Errorf("ResumeToken = %q, want %q", in.ResumeToken.Reveal(), "tok-resume-1")
		}
		if in.ALegID != "aleg-1" {
			t.Errorf("ALegID = %q, want %q", in.ALegID, "aleg-1")
		}
		if in.ClientSessionID != "client-hint-1" {
			t.Errorf("ClientSessionID = %q, want %q", in.ClientSessionID, "client-hint-1")
		}
		if in.NewSessionRequested {
			t.Errorf("NewSessionRequested = true, want false when session ID and token are present")
		}
		if got := in.CorrelationID(); got != "sess-auth-1" {
			t.Errorf("CorrelationID() = %q, want %q", got, "sess-auth-1")
		}
	})

	t.Run("header wins over body metadata when body metadata is permitted", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: "hdr-sess-id",
			HeaderResumeToken:            "hdr-resume-tok",
			HeaderALegID:                 "hdr-aleg-id",
			HeaderClientSessionHint:      "hdr-hint",
			BodyAuthoritativeSessionID:   "body-sess-id",
			BodyResumeToken:              "body-resume-tok",
			HasBodySessionMetadata:       true,
			RejectBodyMetadata:           false,
		}
		in, err := largebody.BuildSessionInput(src, 64*1024)
		if err != nil {
			t.Fatalf("BuildSessionInput error: %v", err)
		}
		if in.AuthoritativeSessionID != "hdr-sess-id" {
			t.Errorf("AuthoritativeSessionID = %q, want %q (header wins)", in.AuthoritativeSessionID, "hdr-sess-id")
		}
		if in.ResumeToken.Reveal() != "hdr-resume-tok" {
			t.Errorf("ResumeToken = %q, want %q (header wins)", in.ResumeToken.Reveal(), "hdr-resume-tok")
		}
		if in.ALegID != "hdr-aleg-id" {
			t.Errorf("ALegID = %q, want %q", in.ALegID, "hdr-aleg-id")
		}
		if in.ClientSessionID != "hdr-hint" {
			t.Errorf("ClientSessionID = %q, want %q", in.ClientSessionID, "hdr-hint")
		}
	})

	t.Run("body metadata used when headers absent and body metadata is permitted", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			BodyAuthoritativeSessionID: "body-sess-id",
			BodyResumeToken:            "body-resume-tok",
			HasBodySessionMetadata:     true,
			RejectBodyMetadata:         false,
		}
		in, err := largebody.BuildSessionInput(src, 64*1024)
		if err != nil {
			t.Fatalf("BuildSessionInput error: %v", err)
		}
		if in.AuthoritativeSessionID != "body-sess-id" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "body-sess-id")
		}
		if in.ResumeToken.Reveal() != "body-resume-tok" {
			t.Errorf("ResumeToken = %q, want %q", in.ResumeToken.Reveal(), "body-resume-tok")
		}
		if in.NewSessionRequested {
			t.Errorf("NewSessionRequested = true, want false when body session is present")
		}
	})

	t.Run("new session requested when no authoritative session ID or resume token", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderClientSessionHint: "client-only-hint",
		}
		in, err := largebody.BuildSessionInput(src, 64*1024)
		if err != nil {
			t.Fatalf("BuildSessionInput error: %v", err)
		}
		if !in.NewSessionRequested {
			t.Errorf("NewSessionRequested = false, want true when no session ID or resume token")
		}
		if in.AuthoritativeSessionID != "" {
			t.Errorf("AuthoritativeSessionID = %q, want empty", in.AuthoritativeSessionID)
		}
		if !in.ResumeToken.IsZero() {
			t.Errorf("ResumeToken should be zero")
		}
		if in.ClientSessionID != "client-only-hint" {
			t.Errorf("ClientSessionID = %q, want %q", in.ClientSessionID, "client-only-hint")
		}
		if got := in.CorrelationID(); got != "client-only-hint" {
			t.Errorf("CorrelationID() = %q, want fallback to client session hint %q", got, "client-only-hint")
		}
	})

	t.Run("resume detected if only session id or only resume token present", func(t *testing.T) {
		t.Parallel()
		in1, err := largebody.BuildSessionInput(largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: "sess-only",
		}, 64*1024)
		if err != nil || in1.NewSessionRequested {
			t.Errorf("in1 NewSessionRequested = %t, want false", in1.NewSessionRequested)
		}

		in2, err := largebody.BuildSessionInput(largebody.SessionInputSource{
			HeaderResumeToken: "tok-only",
		}, 64*1024)
		if err != nil || in2.NewSessionRequested {
			t.Errorf("in2 NewSessionRequested = %t, want false", in2.NewSessionRequested)
		}
	})

	t.Run("bounds enforcement rejects oversized fields", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			src  largebody.SessionInputSource
		}{
			{
				name: "oversized authoritative session id",
				src: largebody.SessionInputSource{
					HeaderAuthoritativeSessionID: strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1),
				},
			},
			{
				name: "oversized resume token",
				src: largebody.SessionInputSource{
					HeaderResumeToken: strings.Repeat("r", aipapi.MaxResumeTokenBytes+1),
				},
			},
			{
				name: "oversized aleg id",
				src: largebody.SessionInputSource{
					HeaderALegID: strings.Repeat("a", aipapi.MaxALegIDBytes+1),
				},
			},
			{
				name: "oversized client session hint",
				src: largebody.SessionInputSource{
					HeaderClientSessionHint: strings.Repeat("c", aipapi.MaxClientSessionIDBytes+1),
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				_, err := largebody.BuildSessionInput(tc.src, 64*1024)
				if err == nil {
					t.Fatal("expected error for oversized field, got nil")
				}
			})
		}
	})

	t.Run("fact budget bounds enforcement", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: strings.Repeat("s", 100),
		}
		// Budget 50 is less than 100 bytes
		_, err := largebody.BuildSessionInput(src, 50)
		if err == nil {
			t.Fatal("expected error when field exceeds maxFactBytes budget")
		}
	})
}

func TestSessionInput_BodyMetadataRejection(t *testing.T) {
	t.Parallel()

	t.Run("rejects body-carried session ID when RejectBodyMetadata is true", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			BodyAuthoritativeSessionID: "body-session-id",
			HasBodySessionMetadata:     true,
			RejectBodyMetadata:         true,
		}
		_, err := largebody.BuildSessionInput(src, 64*1024)
		if err == nil {
			t.Fatal("expected error when body contains session metadata and RejectBodyMetadata is true")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("rejects body-carried resume token when RejectBodyMetadata is true", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			BodyResumeToken:        "body-resume-token",
			HasBodySessionMetadata: true,
			RejectBodyMetadata:     true,
		}
		_, err := largebody.BuildSessionInput(src, 64*1024)
		if err == nil {
			t.Fatal("expected error when body contains resume token and RejectBodyMetadata is true")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("rejects body-carried session metadata even if headers are also present", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: "hdr-session-id",
			BodyAuthoritativeSessionID:   "body-session-id",
			HasBodySessionMetadata:       true,
			RejectBodyMetadata:           true,
		}
		_, err := largebody.BuildSessionInput(src, 64*1024)
		if err == nil {
			t.Fatal("expected error when body contains session metadata even if header is present")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("accepts authoritative headers when body metadata is absent", func(t *testing.T) {
		t.Parallel()
		src := largebody.SessionInputSource{
			HeaderAuthoritativeSessionID: "hdr-session-id",
			HeaderResumeToken:            "hdr-resume-token",
			RejectBodyMetadata:           true,
		}
		in, err := largebody.BuildSessionInput(src, 64*1024)
		if err != nil {
			t.Fatalf("expected success for header-only with RejectBodyMetadata=true, got %v", err)
		}
		if in.AuthoritativeSessionID != "hdr-session-id" {
			t.Errorf("AuthoritativeSessionID = %q, want %q", in.AuthoritativeSessionID, "hdr-session-id")
		}
		if in.ResumeToken.Reveal() != "hdr-resume-token" {
			t.Errorf("ResumeToken = %q, want %q", in.ResumeToken.Reveal(), "hdr-resume-token")
		}
	})
}

func TestSessionInput_ResumeTokenNeverEntersBackendFactsOrTelemetry(t *testing.T) {
	t.Parallel()

	const secretToken = "super-secret-resume-token-material-999"
	src := largebody.SessionInputSource{
		HeaderAuthoritativeSessionID: "sess-abc",
		HeaderResumeToken:            secretToken,
		HeaderALegID:                 "aleg-123",
		HeaderClientSessionHint:      "hint-456",
	}
	in, err := largebody.BuildSessionInput(src, 64*1024)
	if err != nil {
		t.Fatalf("BuildSessionInput failed: %v", err)
	}

	t.Run("telemetry format verbs never leak resume token", func(t *testing.T) {
		t.Parallel()
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			rendered := fmt.Sprintf(verb, in)
			if strings.Contains(rendered, secretToken) {
				t.Fatalf("SessionInput formatted with %s leaked secret token: %q", verb, rendered)
			}
		}
		if strings.Contains(in.String(), secretToken) {
			t.Fatalf("SessionInput.String() leaked secret token: %q", in.String())
		}
		if strings.Contains(in.GoString(), secretToken) {
			t.Fatalf("SessionInput.GoString() leaked secret token: %q", in.GoString())
		}
	})

	t.Run("JSON marshal never leaks resume token", func(t *testing.T) {
		t.Parallel()
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		if strings.Contains(string(raw), secretToken) {
			t.Fatalf("json.Marshal(SessionInput) leaked secret token: %s", string(raw))
		}
	})

	t.Run("slog handlers never leak resume token", func(t *testing.T) {
		t.Parallel()
		for _, format := range []string{"json", "text"} {
			var buf bytes.Buffer
			var handler slog.Handler
			if format == "json" {
				handler = slog.NewJSONHandler(&buf, nil)
			} else {
				handler = slog.NewTextHandler(&buf, nil)
			}
			logger := slog.New(handler)
			logger.Info("session-input-audit", slog.Any("session", in))
			if strings.Contains(buf.String(), secretToken) {
				t.Fatalf("slog %s handler leaked secret token: %s", format, buf.String())
			}
		}
	})

	t.Run("backend WireRequestFacts and WireDomainFacts do not contain session or token fields", func(t *testing.T) {
		t.Parallel()
		// Reflect over WireRequestFacts to ensure no SessionInput or resume token fields exist
		reqType := reflect.TypeFor[largebody.WireRequestFacts]()
		for f := range reqType.Fields() {
			name := strings.ToLower(f.Name)
			if strings.Contains(name, "session") || strings.Contains(name, "resumetoken") || strings.Contains(name, "resume") {
				t.Fatalf("WireRequestFacts contains forbidden field %q (resume tokens/session input must not enter backend facts)", f.Name)
			}
			if f.Type == reflect.TypeFor[largebody.SessionInput]() || f.Type == reflect.TypeFor[largebody.SensitiveString]() {
				t.Fatalf("WireRequestFacts field %q has session/secret type %v", f.Name, f.Type)
			}
		}

		// Reflect over WireDomainFacts to ensure no SessionInput or resume token fields exist
		domType := reflect.TypeFor[largebody.WireDomainFacts]()
		for f := range domType.Fields() {
			name := strings.ToLower(f.Name)
			if strings.Contains(name, "session") || strings.Contains(name, "resumetoken") || strings.Contains(name, "resume") {
				t.Fatalf("WireDomainFacts contains forbidden field %q (resume tokens/session input must not enter backend facts)", f.Name)
			}
			if f.Type == reflect.TypeFor[largebody.SessionInput]() || f.Type == reflect.TypeFor[largebody.SensitiveString]() {
				t.Fatalf("WireDomainFacts field %q has session/secret type %v", f.Name, f.Type)
			}
		}
	})
}

func TestSessionInput_ProvesFreshALeg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   largebody.SessionInput
		want bool
	}{
		{
			name: "fresh new session without any id or token",
			in: largebody.SessionInput{
				NewSessionRequested: true,
			},
			want: true,
		},
		{
			name: "fresh new session with client session id only (hint)",
			in: largebody.SessionInput{
				NewSessionRequested: true,
				ClientSessionID:     "client-hint-123",
			},
			want: true,
		},
		{
			name: "fresh new session with whitespace-only fields",
			in: largebody.SessionInput{
				NewSessionRequested:    true,
				AuthoritativeSessionID: "   ",
				ALegID:                 " \t ",
				ResumeToken:            largebody.NewSensitiveString("   "),
			},
			want: true,
		},
		{
			name: "new session false",
			in: largebody.SessionInput{
				NewSessionRequested: false,
			},
			want: false,
		},
		{
			name: "authoritative session id present",
			in: largebody.SessionInput{
				NewSessionRequested:    true,
				AuthoritativeSessionID: "sess-auth-existing",
			},
			want: false,
		},
		{
			name: "aleg id present",
			in: largebody.SessionInput{
				NewSessionRequested: true,
				ALegID:              "aleg-existing",
			},
			want: false,
		},
		{
			name: "resume token present",
			in: largebody.SessionInput{
				NewSessionRequested: true,
				ResumeToken:         largebody.NewSensitiveString("secret-resume-token"),
			},
			want: false,
		},
		{
			name: "all fields present",
			in: largebody.SessionInput{
				NewSessionRequested:    true,
				AuthoritativeSessionID: "sess-auth",
				ALegID:                 "aleg-1",
				ResumeToken:            largebody.NewSensitiveString("resume-tok"),
				ClientSessionID:        "client-1",
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.in.ProvesFreshALeg()
			if got != tc.want {
				t.Errorf("ProvesFreshALeg() = %v, want %v for %+v", got, tc.want, tc.in)
			}
		})
	}
}
