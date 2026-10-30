package httpauth_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func TestAuthenticationResult_EffectiveStatus_defaults401(t *testing.T) {
	t.Parallel()
	r := httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: 0}
	if got := r.EffectiveStatus(); got != http.StatusUnauthorized {
		t.Fatalf("got %d", got)
	}
	r2 := httpauth.AuthenticationResult{Type: httpauth.TypeChallenge, HTTPStatus: 0}
	if got := r2.EffectiveStatus(); got != http.StatusUnauthorized {
		t.Fatalf("got %d", got)
	}
}

func TestAuthenticationResult_EffectiveStatus_explicit(t *testing.T) {
	t.Parallel()
	r := httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusForbidden}
	if got := r.EffectiveStatus(); got != http.StatusForbidden {
		t.Fatalf("got %d", got)
	}
}
