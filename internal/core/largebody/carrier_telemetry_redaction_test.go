package largebody_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestSensitiveCarrier_NeverFormatsSecrets locks Requirement 14/22 telemetry
// safety before wire behavior exists: the sensitive session-response carrier
// and its bearer token must never render into normal diagnostics through fmt
// verbs, JSON marshal, or slog handlers. Session/response IDs travel in the
// same carrier struct, so the carrier exposes presence signals only.
func TestSensitiveCarrier_NeverFormatsSecrets(t *testing.T) {
	t.Parallel()
	const token = "aip-resume-token-telemetry-probe"
	const sessionID = "sess-telemetry-probe"
	const aLegID = "a-leg-telemetry-probe"
	secrets := []string{token, sessionID, aLegID}

	sensitive := largebody.NewSensitiveString(token)
	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: sessionID,
		ALegID:                 aLegID,
		ResumeToken:            largebody.NewSensitiveString(token),
	}

	t.Run("sensitive verbs", func(t *testing.T) {
		t.Parallel()
		for _, verb := range []string{"%v", "%+v", "%q", "%s"} {
			rendered := fmt.Sprintf(verb, sensitive)
			for _, secret := range secrets[:1] {
				if strings.Contains(rendered, secret) {
					t.Fatalf("SensitiveString %s leaks the bearer token: %q", verb, rendered)
				}
			}
			if !strings.Contains(rendered, "redacted") {
				t.Fatalf("SensitiveString %s = %q, want explicit redaction marker", verb, rendered)
			}
		}
	})

	t.Run("carrier verbs", func(t *testing.T) {
		t.Parallel()
		renderings := map[string]string{
			"%v":       fmt.Sprintf("%v", carrier),
			"%+v":      fmt.Sprintf("%+v", carrier),
			"%q":       fmt.Sprintf("%q", carrier),
			"%s":       fmt.Sprintf("%s", carrier),
			"String":   carrier.String(),
			"GoString": carrier.GoString(),
		}
		for name, rendered := range renderings {
			for _, secret := range secrets {
				if strings.Contains(rendered, secret) {
					t.Fatalf("carrier %s leaks %q: %q", name, secret, rendered)
				}
			}
			if !strings.Contains(rendered, "has_resume_token") {
				t.Fatalf("carrier %s = %q, want presence-only signal", name, rendered)
			}
		}
	})

	t.Run("carrier json", func(t *testing.T) {
		t.Parallel()
		raw, err := json.Marshal(carrier)
		if err != nil {
			t.Fatalf("carrier MarshalJSON error = %v", err)
		}
		for _, secret := range secrets {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("carrier JSON leaks %q: %s", secret, raw)
			}
		}
		if !strings.Contains(string(raw), "has_resume_token") {
			t.Fatalf("carrier JSON = %s, want presence-only signal", raw)
		}
	})

	t.Run("sensitive json", func(t *testing.T) {
		t.Parallel()
		raw, err := json.Marshal(sensitive)
		if err != nil {
			t.Fatalf("SensitiveString MarshalJSON error = %v", err)
		}
		if strings.Contains(string(raw), token) {
			t.Fatalf("SensitiveString JSON leaks the bearer token: %s", raw)
		}
	})

	t.Run("slog handlers", func(t *testing.T) {
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
			logger.Info(
				"large-body-wire-event",
				slog.Any("carrier", carrier),
				slog.Any("resume_token", sensitive),
			)
			out := buf.String()
			for _, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("slog %s handler leaks %q: %s", format, secret, out)
				}
			}
		}
	})
}

// TestSessionInput_TokenNeverFormatsSecrets locks the SessionInput half of the
// same contract: profile-derived session IDs may travel as bounded facts, but
// the resume bearer token must stay redacted under every fmt verb, JSON
// marshal, and slog handler.
func TestSessionInput_TokenNeverFormatsSecrets(t *testing.T) {
	t.Parallel()
	const token = "aip-session-input-token-probe"
	input := largebody.SessionInput{
		AuthoritativeSessionID: "sess-input-probe",
		ResumeToken:            largebody.NewSensitiveString(token),
		NewSessionRequested:    true,
	}

	for _, verb := range []string{"%v", "%+v", "%q", "%s"} {
		rendered := fmt.Sprintf(verb, input)
		if strings.Contains(rendered, token) {
			t.Fatalf("SessionInput %s leaks the resume token: %q", verb, rendered)
		}
	}

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("SessionInput MarshalJSON error = %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("SessionInput JSON leaks the resume token: %s", raw)
	}

	for _, format := range []string{"json", "text"} {
		var buf bytes.Buffer
		var handler slog.Handler
		if format == "json" {
			handler = slog.NewJSONHandler(&buf, nil)
		} else {
			handler = slog.NewTextHandler(&buf, nil)
		}
		slog.New(handler).Info("large-body-session-input", slog.Any("session", input))
		if strings.Contains(buf.String(), token) {
			t.Fatalf("slog %s handler leaks the resume token: %s", format, buf.String())
		}
	}
}
