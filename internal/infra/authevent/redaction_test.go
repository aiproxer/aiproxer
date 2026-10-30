package authevent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestSlogEventSink_challengeSummary_redactsCredentialLikeText(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sink, err := NewSlogEventSink(log)
	if err != nil {
		t.Fatal(err)
	}
	ev := sdkauth.AuthDecisionEvent{
		Time:              time.Unix(10, 0).UTC(),
		TraceID:           "trace-chal",
		Outcome:           sdkauth.OutcomeChallenge,
		ChallengeKind:     sdkauth.ChallengeSSORequired,
		ChallengeSummary:  "Use access_token=leaked-value please",
		PrincipalID:       "p1",
		DeviceFingerprint: "fp",
	}
	if err := sink.OnAuthDecision(context.Background(), ev); err != nil {
		t.Fatalf("OnAuthDecision: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "leaked-value") {
		t.Fatalf("challenge summary leaked token material: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected [redacted] challenge_summary in log: %s", out)
	}
}

func TestSlogEventSink_redaction_fixtureSecretsAbsent(t *testing.T) {
	t.Parallel()
	for i, secret := range testkit.AuthLeakFixtureSecrets() {
		t.Run(fmt.Sprintf("case_%02d", i), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			sink, err := NewSlogEventSink(log)
			if err != nil {
				t.Fatal(err)
			}
			ev := sdkauth.AuthDecisionEvent{
				Time:                 time.Unix(10, 0).UTC(),
				TraceID:              "trace-redact",
				Outcome:              sdkauth.OutcomeAllow,
				PrincipalID:          "p1",
				PrincipalDisplayName: "Pat",
				// Misconfigured "safe" map carrying a secret in the value — must not reach logs.
				PrincipalSafeClaims: map[string]string{"note": secret},
				DeviceFingerprint:   "fp-redacted-ok",
				ChallengeSummary:    "challenge ok",
			}
			if err := sink.OnAuthDecision(context.Background(), ev); err != nil {
				t.Fatalf("OnAuthDecision: %v", err)
			}
			sess := sdkauth.SessionStartEvent{
				Time:                 time.Unix(11, 0).UTC(),
				TraceID:              "trace-redact-s",
				SessionID:            "sess-x",
				PrincipalID:          "p1",
				PrincipalDisplayName: "Pat",
			}
			if err := sink.OnSessionStart(context.Background(), sess); err != nil {
				t.Fatalf("OnSessionStart: %v", err)
			}
			out := buf.String()
			assertNoFixtureLeak(t, out)
		})
	}
}

func assertNoFixtureLeak(t *testing.T, out string) {
	t.Helper()
	for _, s := range testkit.AuthLeakFixtureSecrets() {
		if strings.Contains(out, s) {
			t.Fatalf("fixture secret leaked: %q\noutput=%s", s, out)
		}
	}
}
