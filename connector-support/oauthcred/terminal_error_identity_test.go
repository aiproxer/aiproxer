package oauthcred_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// statusOnlyError implements the auxiliary status interface this package
// classifies (StatusCode) without an HTTPError message that carries a status.
type statusOnlyError struct{ code int }

func (e statusOnlyError) Error() string   { return "upstream rejected the refresh" }
func (e statusOnlyError) StatusCode() int { return e.code }

// A Refresher is an interface external connectors implement and decorate, so a
// status-coded error can arrive wrapped. Classification must follow the wrapped
// cause: a plain type assertion falls through to the message-text fallback,
// which only recognizes 400/401/403, and a terminal failure is replayable.
func TestIsTerminalError_ClassifiesWrappedStatusByType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not terminal", nil, false},
		{"wrapped 404", fmt.Errorf("refresh decorator: %w", &oauthcred.HTTPError{StatusCode: http.StatusNotFound, Body: "gone"}), true},
		{"joined 404", errors.Join(errors.New("first attempt"), &oauthcred.HTTPError{StatusCode: http.StatusNotFound, Body: "gone"}), true},
		{"wrapped status coder 401", fmt.Errorf("refresh decorator: %w", statusOnlyError{code: http.StatusUnauthorized}), true},
		{"wrapped status coder 408 stays transient", fmt.Errorf("refresh decorator: %w", statusOnlyError{code: http.StatusRequestTimeout}), false},
		{"wrapped status coder 429 stays transient", fmt.Errorf("refresh decorator: %w", statusOnlyError{code: http.StatusTooManyRequests}), false},
		{"plain invalid_grant stays terminal", errors.New("oauth: server returned invalid_grant"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := oauthcred.IsTerminalError(tc.err); got != tc.want {
				t.Fatalf("IsTerminalError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A wrapped terminal status must still quarantine: otherwise the session keeps
// replaying a dead refresh token on every request.
func TestSession_WrappedTerminalStatus_QuarantinesNoReplay(t *testing.T) {
	t.Parallel()

	store := oauthcred.NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err := store.Save(oauthcred.TokenRecord{
		AccessToken:  "expired",
		RefreshToken: "ref",
		Expiry:       time.Now().Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	var calls atomic.Int32
	refresher := oauthcred.RefresherFunc(func(context.Context, string) (string, string, time.Time, error) {
		calls.Add(1)
		return "", "", time.Time{}, fmt.Errorf("connector refresh wrapper: %w",
			&oauthcred.HTTPError{StatusCode: http.StatusNotFound, Body: "gone"})
	})
	sess := oauthcred.NewSession(store, refresher)

	_, err := sess.Token(context.Background())
	if !errors.Is(err, oauthcred.ErrTerminalRefresh) {
		t.Fatalf("wrapped 404 must be terminal, got %v", err)
	}
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !rec.Quarantined {
		t.Fatal("wrapped terminal status must quarantine the record")
	}
	if _, err := sess.Token(context.Background()); !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("second call = %v, want ErrQuarantined", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresher replayed a dead credential: %d calls, want 1", got)
	}
}
