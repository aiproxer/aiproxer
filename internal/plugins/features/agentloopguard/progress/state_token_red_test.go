package progress

import (
	"errors"
	"strings"
	"testing"
)

func TestStateTokenRoundTripIsBoundedAndOpaque(t *testing.T) {
	t.Parallel()

	want := State{
		LastFingerprint:       "sha256:0123456789abcdef",
		HasBaseline:           true,
		TotalAttempts:         7,
		ConsecutiveNoProgress: 1,
		NoProgressTripped:     false,
		BudgetExhausted:       false,
		Terminal:              false,
	}
	token, err := EncodeState(want)
	if err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	if token == "" || len(token) > MaxStateTokenBytes || strings.Contains(token, want.LastFingerprint) {
		t.Fatalf("state token=%q is not bounded opaque state", token)
	}
	got, err := DecodeState(token)
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}
	if got != want {
		t.Fatalf("decoded state=%+v, want %+v", got, want)
	}
}

func TestStateTokenRejectsMalformedAndUnboundedState(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"", "alg-state-v1.bad", "alg-state-v2.bad", strings.Repeat("x", MaxStateTokenBytes+1)} {
		if _, err := DecodeState(token); !errors.Is(err, ErrInvalidStateToken) {
			t.Fatalf("DecodeState(%q) error=%v, want ErrInvalidStateToken", token, err)
		}
	}
	if _, err := EncodeState(State{LastFingerprint: strings.Repeat("x", MaxStateFingerprintBytes+1)}); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("oversized fingerprint error=%v, want ErrInvalidStateToken", err)
	}
	if _, err := EncodeState(State{TotalAttempts: -1}); !errors.Is(err, ErrInvalidStateToken) {
		t.Fatalf("negative attempts error=%v, want ErrInvalidStateToken", err)
	}
}
