package execctx

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestWithSecureSessionTurnNilParent(t *testing.T) {
	t.Parallel()
	st := SecureSessionTurn{
		SessionID: domain.SessionID("s"),
		TurnID:    domain.TurnID("u"),
	}
	ctx := WithSecureSessionTurn(nil, st) //nolint:staticcheck // SA1012: exercise nil-parent → TODO() branch
	if ctx == nil {
		t.Fatal("expected non-nil context when parent is nil (nil parent uses context.TODO)")
	}
	got, ok := SecureSessionTurnFromContext(ctx)
	if !ok {
		t.Fatal("expected turn binding in context")
	}
	if got != st {
		t.Fatalf("turn: got %+v want %+v", got, st)
	}
}

func TestWithSecureSessionTurnProjectsContentFreeSDKPolicy(t *testing.T) {
	t.Parallel()

	ctx := WithSecureSessionTurn(
		nil, //nolint:staticcheck // SA1012: exercise nil-parent → TODO() branch
		SecureSessionTurn{
			SessionID: domain.SessionID("s"),
			TurnID:    domain.TurnID("t"),
			Policy:    domain.PolicyMetadata{TranscriptEnabled: true},
		})
	got, ok := session.SecureTurnPolicyFromContext(ctx)
	if !ok || !got.TranscriptEnabled {
		t.Fatalf("public secure-turn policy = %+v ok=%v", got, ok)
	}
}

func TestWithDetachedSessionMasksInheritedSecureTurnPolicy(t *testing.T) {
	t.Parallel()

	ctx := session.WithSecureTurnPolicy(context.Background(), session.SecureTurnPolicyView{TranscriptEnabled: true})
	ctx = WithDetachedSession(ctx, DetachedSession{ParentSessionID: "parent-session"})
	if _, ok := session.SecureTurnPolicyFromContext(ctx); ok {
		t.Fatal("detached context inherited primary secure-turn policy")
	}
}
