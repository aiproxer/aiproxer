package execctx_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestViewsFromSubmit_sessionHintsAndAuthoritative(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	aLeg := b2bua.ALegRecord{
		ALegID:        "aleg-1",
		ContinuityKey: "ck",
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	t.Run("client hint only", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{
			Session: aipapi.SessionRef{ClientSessionID: "client-sess"},
		}
		v := execctx.ViewsFromSubmit("trace-1", aLeg, call, map[string]string{"k": "v"})
		if v.Session.AuthoritativeSessionID != "" {
			t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
		}
		if v.Session.ClientSessionHint != "client-sess" {
			t.Fatalf("hint: %q", v.Session.ClientSessionHint)
		}
		if v.Session.ALegID != "aleg-1" {
			t.Fatalf("aleg: %q", v.Session.ALegID)
		}
		if !v.Session.IsNew {
			t.Fatal("want IsNew when CreatedAt equals LastSeenAt")
		}
		if v.Session.AuthoritativeFirstTurn {
			t.Fatal("timestamp-derived IsNew must not establish secure first-turn authority")
		}
		if v.Session.PartitionKey() != "client-sess" {
			t.Fatalf("partition: %q", v.Session.PartitionKey())
		}
		if v.Attempt.TraceID != "trace-1" {
			t.Fatalf("attempt trace: %q", v.Attempt.TraceID)
		}
		if v.Annotations["k"] != "v" {
			t.Fatalf("annotations: %v", v.Annotations)
		}
	})
	t.Run("authoritative from call", func(t *testing.T) {
		t.Parallel()
		secret := "RAW_RESUME_BEARER_SHOULD_NOT_APPEAR_IN_VIEW"
		call := aipapi.Call{
			Session: aipapi.SessionRef{
				ClientSessionID:        "client-sess",
				AuthoritativeSessionID: "proxy-auth-sess",
				ResumeToken:            secret,
			},
		}
		v := execctx.ViewsFromSubmit("trace-2", aLeg, call, nil)
		if v.Session.AuthoritativeSessionID != "proxy-auth-sess" {
			t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
		}
		if v.Session.ClientSessionHint != "client-sess" {
			t.Fatalf("hint: %q", v.Session.ClientSessionHint)
		}
		if strings.Contains(fmt.Sprintf("%+v", v.Session), secret) {
			t.Fatal("resume token leaked into stringified view")
		}
		if v.Session.PartitionKey() != "proxy-auth-sess" {
			t.Fatalf("partition: %q", v.Session.PartitionKey())
		}
	})
}

func TestViewsFromSubmit_sameClientHintKeepsDistinctALegAuthority(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "shared-client-hint"}}
	first := execctx.ViewsFromSubmit("trace-1", b2bua.ALegRecord{ALegID: "proxy-a-leg-1"}, call, nil)
	second := execctx.ViewsFromSubmit("trace-2", b2bua.ALegRecord{ALegID: "proxy-a-leg-2"}, call, nil)

	for _, view := range []execctx.Views{first, second} {
		if view.Session.AuthoritativeSessionID != "" {
			t.Fatalf("client hint became proxy session authority: %+v", view.Session)
		}
		if view.Session.ClientSessionHint != "shared-client-hint" {
			t.Fatalf("client hint was not preserved as a separate field: %+v", view.Session)
		}
	}
	if first.Session.ALegID == second.Session.ALegID {
		t.Fatalf("distinct proxy A-legs collapsed under a shared client hint: first=%+v second=%+v", first.Session, second.Session)
	}
}

func TestViewsFromSecureSubmit_authoritativeTurnAndPolicyLabels(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	aLeg := b2bua.ALegRecord{
		ALegID:        "aleg-sec",
		ContinuityKey: "",
		CreatedAt:     now,
		LastSeenAt:    now.Add(time.Minute),
	}
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "client-hint",
			AuthoritativeSessionID: "wrong-client-authority",
			ResumeToken:            "raw-should-not-leak",
		},
	}
	v := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                "tr-sec",
		ALeg:                   aLeg,
		Call:                   call,
		AuthoritativeSessionID: "proxy-owned-sid",
		TurnID:                 "turn-zz",
		ResumeEligible:         true,
		AuthoritativeFirstTurn: true,
		PolicyLabels:           map[string]string{"effective_treatment": "strict"},
	})
	if v.Session.AuthoritativeSessionID != "proxy-owned-sid" {
		t.Fatalf("authoritative: %q", v.Session.AuthoritativeSessionID)
	}
	if v.Session.TurnID != "turn-zz" {
		t.Fatalf("turn: %q", v.Session.TurnID)
	}
	if !v.Session.ResumeEligible {
		t.Fatal("expected resume eligible")
	}
	if !v.Session.AuthoritativeFirstTurn {
		t.Fatal("expected authoritative first-turn metadata")
	}
	if v.Session.Labels["effective_treatment"] != "strict" {
		t.Fatalf("labels: %v", v.Session.Labels)
	}
	if strings.Contains(fmt.Sprintf("%+v", v), "raw-should-not-leak") {
		t.Fatal("resume token leaked into view dump")
	}
}

// TestViewsFromSecureSubmit_projectsSessionClassification proves the core
// view-copy helper carries the bounded classification scalar into every later
// same-turn view instead of leaving each consumer to re-derive it
// (requirements 1.6, 4.2, 9.5).
func TestViewsFromSecureSubmit_projectsSessionClassification(t *testing.T) {
	t.Parallel()

	aLeg := b2bua.ALegRecord{ALegID: "aleg-classified"}
	positive := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "view.client_identity",
		Revision:   4,
	}

	got := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                "tr-classified",
		ALeg:                   aLeg,
		Call:                   aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "client-hint"}},
		AuthoritativeSessionID: "proxy-owned-sid",
		TurnID:                 "turn-classified",
		ResumeEligible:         true,
		PolicyLabels:           map[string]string{"effective_treatment": "strict"},
		Classification:         positive,
	})
	if got.Session.Classification != positive {
		t.Fatalf("projected classification = %+v, want %+v", got.Session.Classification, positive)
	}
	if !got.Session.Classification.IsCodingAgent() {
		t.Fatal("projected classification cannot gate a first-turn positive consumer")
	}

	// An absent classification stays the conservative unknown zero value and
	// never becomes a partially populated snapshot.
	unknown := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{ALeg: aLeg})
	if unknown.Session.Classification != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown when none was decided", unknown.Session.Classification)
	}
}

// TestViewsFromSubmit_leavesClassificationUndecided proves the pre-secure-session
// projection cannot invent a classification: only the secure-submit projection
// that carries the classified turn may set it.
func TestViewsFromSubmit_leavesClassificationUndecided(t *testing.T) {
	t.Parallel()

	v := execctx.ViewsFromSubmit("trace-unclassified", b2bua.ALegRecord{ALegID: "aleg-1"}, aipapi.Call{}, nil)
	if v.Session.Classification != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown before the secure-session bind", v.Session.Classification)
	}
}
