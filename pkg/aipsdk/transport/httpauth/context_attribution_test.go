package httpauth_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type stubCredMatcher struct{}

func (stubCredMatcher) ScanBytes(context.Context, []byte) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubCredMatcher) ScanString(context.Context, string) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubCredMatcher) RedactBytes(context.Context, []byte) ([]byte, []secretguard.Finding, error) {
	return nil, nil, nil
}

func (stubCredMatcher) RedactString(context.Context, string) (string, []secretguard.Finding, error) {
	return "", nil, nil
}

func TestIngressAttributionContext_roundTrip(t *testing.T) {
	t.Parallel()
	want := httpauth.IngressAttribution{
		PeerIP:              "203.0.113.9",
		FrontendID:          "openai_compatible",
		Operation:           "chat",
		UserAgentDigest:     "ua-digest",
		AgentIdentityDigest: "agent-digest",
		DeviceID:            "dev-1",
		KeyID:               "key-1",
		Fingerprint:         "fp-1",
	}
	ctx := httpauth.WithIngressAttribution(context.Background(), want)
	got, ok := httpauth.IngressAttributionFromContext(ctx)
	if !ok {
		t.Fatal("expected attribution")
	}
	if got != want {
		t.Fatalf("attribution mismatch: %+v vs %+v", got, want)
	}
}

func TestIngressAttributionFromContext_missing(t *testing.T) {
	t.Parallel()
	got, ok := httpauth.IngressAttributionFromContext(context.Background())
	if ok || got != (httpauth.IngressAttribution{}) {
		t.Fatalf("expected absent attribution, got ok=%v %+v", ok, got)
	}
	got, ok = httpauth.IngressAttributionFromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok || got != (httpauth.IngressAttribution{}) {
		t.Fatal("nil context must report absent attribution")
	}
}

func TestWithIngressAttribution_nilParent_usesTODO(t *testing.T) {
	t.Parallel()
	want := httpauth.IngressAttribution{PeerIP: "127.0.0.1"}
	ctx := httpauth.WithIngressAttribution(
		nil, //nolint:staticcheck // SA1012: intentional nil parent contract
		want,
	)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := httpauth.IngressAttributionFromContext(ctx)
	if !ok || got.PeerIP != want.PeerIP {
		t.Fatalf("attribution %+v ok=%v", got, ok)
	}
}

func TestCredentialMatcherContext_delegatesToSecretguard(t *testing.T) {
	t.Parallel()
	m := stubCredMatcher{}
	ctx := httpauth.WithCredentialMatcher(context.Background(), m)
	gotHTTP, okHTTP := httpauth.CredentialMatcherFromContext(ctx)
	if !okHTTP || gotHTTP == nil {
		t.Fatalf("httpauth read: ok=%v got=%v", okHTTP, gotHTTP)
	}
	gotSG, okSG := secretguard.RequestMatcherFromContext(ctx)
	if !okSG || gotSG == nil {
		t.Fatalf("secretguard read: ok=%v got=%v", okSG, gotSG)
	}
	ctxSG := secretguard.WithRequestMatcher(context.Background(), m)
	gotViaHTTP, okViaHTTP := httpauth.CredentialMatcherFromContext(ctxSG)
	if !okViaHTTP || gotViaHTTP == nil {
		t.Fatalf("secretguard set, httpauth read: ok=%v got=%v", okViaHTTP, gotViaHTTP)
	}
}

func TestCredentialMatcherFromContext_missing(t *testing.T) {
	t.Parallel()
	got, ok := httpauth.CredentialMatcherFromContext(context.Background())
	if ok || got != nil {
		t.Fatalf("expected absent matcher, got ok=%v matcher=%v", ok, got)
	}
}

func TestAuthenticationResult_doesNotExposeCredentialMatcherField(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[httpauth.AuthenticationResult]()
	if _, ok := rt.FieldByName("CredentialMatcher"); ok {
		t.Fatal("AuthenticationResult must not expose CredentialMatcher")
	}
}

func TestAuthenticationResult_zeroAttributionAndNilMatcherMeanAbsent(t *testing.T) {
	t.Parallel()
	r := httpauth.AuthenticationResult{Type: httpauth.TypePrincipal}
	if r.IngressAttribution != (httpauth.IngressAttribution{}) {
		t.Fatalf("expected zero attribution, got %+v", r.IngressAttribution)
	}
}
