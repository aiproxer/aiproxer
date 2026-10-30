package credpool_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)}
}

func (m *manualClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *manualClock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

func newTwoKeyCoordinator(t *testing.T, clock *manualClock, threshold time.Duration) *credpool.Coordinator {
	t.Helper()
	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "secret-one"},
		{ID: "k2", Secret: "secret-two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coord, err := credpool.NewCoordinator(pool, threshold, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return coord
}

func rateLimitedWithHint(hint string) func(error) (credpool.FailureClass, string) {
	return func(err error) (credpool.FailureClass, string) {
		if err == nil {
			return credpool.FailureNeutral, ""
		}
		return credpool.FailureRateLimited, hint
	}
}

func TestCoordinator_LongHintAdvancesPreOutput(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	attempts := 0
	got, err := credpool.Do(context.Background(), coord, rateLimitedWithHint("7200"),
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("429 with long retry-after")
			}
			return cred.Secret, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || got != "secret-two" {
		t.Fatalf("want advance to k2 after long 429: attempts=%d got=%q", attempts, got)
	}
	snap := coord.Snapshot()
	if len(snap) != 2 || snap[0].State != credpool.StateCooldown || snap[1].State != credpool.StateUsable {
		t.Fatalf("k1 must quota-cool, k2 usable: %+v", snap)
	}
	if want := clock.Now().Add(2 * time.Hour); !snap[0].CooldownUntil.Equal(want) {
		t.Fatalf("cooldown until=%v want %v", snap[0].CooldownUntil, want)
	}
}

func TestCoordinator_NoRotationHintsStayOnPrimary(t *testing.T) {
	t.Parallel()
	past := newManualClock().Now().Add(-time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	for _, hint := range []string{"", "   ", "bogus", "0", "-5", "30", "3600", past, "20000000000"} {
		t.Run("hint_"+strings.ReplaceAll(strings.TrimSpace(hint), " ", "_"), func(t *testing.T) {
			t.Parallel()
			clock := newManualClock()
			coord := newTwoKeyCoordinator(t, clock, time.Hour)
			attempts := 0
			_, err := credpool.Do(context.Background(), coord, rateLimitedWithHint(hint),
				func(ctx context.Context, cred credpool.Credential) (string, error) {
					attempts++
					if cred.ID != "k1" {
						t.Fatalf("must stay on primary, got %q", cred.ID)
					}
					return "", errors.New("429")
				})
			if !errors.Is(err, credpool.ErrTransientThrottle) {
				t.Fatalf("hint %q: want ErrTransientThrottle, got %v", hint, err)
			}
			if attempts != 1 {
				t.Fatalf("hint %q: must not consume secondary: attempts=%d", hint, attempts)
			}
			snap := coord.Snapshot()
			if snap[0].State != credpool.StateUsable || snap[1].State != credpool.StateUsable {
				t.Fatalf("hint %q: no state change allowed: %+v", hint, snap)
			}
		})
	}
}

func TestCoordinator_HTTPDateLongHintAdvances(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	future := clock.Now().Add(2 * time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	attempts := 0
	_, err := credpool.Do(context.Background(), coord, rateLimitedWithHint(future),
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("429 with http-date hint")
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("future http-date must advance: attempts=%d", attempts)
	}
}

func TestCoordinator_AuthInvalidAdvancesAndSticks(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	classify := func(err error) (credpool.FailureClass, string) {
		return credpool.FailureAuthInvalid, ""
	}
	attempts := 0
	got, err := credpool.Do(context.Background(), coord, classify,
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("401")
			}
			return cred.ID, nil
		})
	if err != nil || got != "k2" || attempts != 2 {
		t.Fatalf("want advance to k2: got=%q attempts=%d err=%v", got, attempts, err)
	}
	clock.Advance(3 * time.Hour)
	snap := coord.Snapshot()
	if snap[0].State != credpool.StateAuthInvalid || snap[1].State != credpool.StateUsable {
		t.Fatalf("invalid must stick after cooldown horizon: %+v", snap)
	}
}

func TestCoordinator_QuotaResetRestoresFirstKeyPreference(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	calls := 0
	_, err := credpool.Do(context.Background(), coord, rateLimitedWithHint("7200"),
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("429 long")
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2*time.Hour + time.Second)
	got, err := credpool.Do(context.Background(), coord,
		func(err error) (credpool.FailureClass, string) { return credpool.FailureNeutral, "" },
		func(ctx context.Context, cred credpool.Credential) (string, error) { return cred.ID, nil })
	if err != nil || got != "k1" {
		t.Fatalf("after reset want k1 first: got=%q err=%v", got, err)
	}
}

func TestCoordinator_NeutralAndRetryablePassThrough(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		class credpool.FailureClass
		want  error
	}{
		{name: "neutral_returns_original", class: credpool.FailureNeutral},
		{name: "retryable_joins_sentinel", class: credpool.FailureRetryable, want: credpool.ErrUpstreamRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := newManualClock()
			coord := newTwoKeyCoordinator(t, clock, time.Hour)
			boom := errors.New("provider 503")
			attempts := 0
			_, err := credpool.Do(context.Background(), coord,
				func(err error) (credpool.FailureClass, string) { return tc.class, "" },
				func(ctx context.Context, cred credpool.Credential) (string, error) {
					attempts++
					return "", boom
				})
			if attempts != 1 {
				t.Fatalf("no rotation on neutral/retryable: attempts=%d", attempts)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) || !errors.Is(err, boom) {
					t.Fatalf("want joined sentinel+cause, got %v", err)
				}
				return
			}
			if !errors.Is(err, boom) || errors.Is(err, credpool.ErrTransientThrottle) {
				t.Fatalf("neutral must return original: %v", err)
			}
			if snap := coord.Snapshot(); snap[0].State != credpool.StateUsable {
				t.Fatalf("neutral must not mutate pool: %+v", snap)
			}
		})
	}
}

func TestCoordinator_BoundedAttemptsThenQuotaExhausted(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	pool, err := credpool.New([]credpool.Credential{
		{ID: "a", Secret: "s-a"}, {ID: "b", Secret: "s-b"}, {ID: "c", Secret: "s-c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coord, err := credpool.NewCoordinator(pool, time.Hour, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err = credpool.Do(context.Background(), coord,
		func(err error) (credpool.FailureClass, string) { return credpool.FailureAuthInvalid, "" },
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			attempts++
			return "", errors.New("401")
		})
	if !errors.Is(err, credpool.ErrQuotaExhausted) {
		t.Fatalf("want ErrQuotaExhausted, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("bounded attempts <= credentials: attempts=%d", attempts)
	}
}

func TestCoordinator_StartsExhaustedReturnsNoUsable(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	pool, err := credpool.New([]credpool.Credential{{ID: "only", Secret: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkAuthInvalid("only")
	coord, err := credpool.NewCoordinator(pool, time.Hour, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = credpool.Do(context.Background(), coord,
		func(err error) (credpool.FailureClass, string) { return credpool.FailureAuthInvalid, "" },
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			called = true
			return "", nil
		})
	if !errors.Is(err, credpool.ErrNoUsableCredential) || called {
		t.Fatalf("exhausted pool must not attempt: err=%v called=%v", err, called)
	}
}

func TestCoordinator_CancellationSafe(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := credpool.Do(ctx, coord, rateLimitedWithHint("7200"),
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			called = true
			return "", nil
		})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled ctx must not attempt: err=%v called=%v", err, called)
	}

	// Cancel races a 401: the attempt error wins, state is untouched, no retry.
	ctx2, cancel2 := context.WithCancel(context.Background())
	coord2 := newTwoKeyCoordinator(t, clock, time.Hour)
	attempts := 0
	_, err = credpool.Do(ctx2, coord2,
		func(err error) (credpool.FailureClass, string) { return credpool.FailureAuthInvalid, "" },
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			attempts++
			cancel2()
			return "", errors.New("401")
		})
	if attempts != 1 || err == nil {
		t.Fatalf("mid-flight cancel: attempts=%d err=%v", attempts, err)
	}
	if snap := coord2.Snapshot(); snap[0].State != credpool.StateUsable {
		t.Fatalf("canceled attempt must not mutate pool: %+v", snap)
	}
}

func TestCoordinator_ConcurrentShortThrottleNeverTouchesSecondary(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	var primary, secondary atomic.Int64
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = credpool.Do(context.Background(), coord, rateLimitedWithHint("30"),
				func(ctx context.Context, cred credpool.Credential) (string, error) {
					switch cred.ID {
					case "k1":
						primary.Add(1)
					case "k2":
						secondary.Add(1)
					}
					return "", errors.New("429 short")
				})
		})
	}
	wg.Wait()
	for _, err := range errs {
		if !errors.Is(err, credpool.ErrTransientThrottle) {
			t.Fatalf("want throttle for all, got %v", err)
		}
	}
	if primary.Load() != 16 || secondary.Load() != 0 {
		t.Fatalf("concurrent short throttle must pin primary: k1=%d k2=%d", primary.Load(), secondary.Load())
	}
}

func TestCoordinator_ErrorsAndSnapshotsNeverCarrySecrets(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coord := newTwoKeyCoordinator(t, clock, time.Hour)
	_, throttleErr := credpool.Do(context.Background(), coord, rateLimitedWithHint("30"),
		func(ctx context.Context, cred credpool.Credential) (string, error) {
			return "", errors.New("429")
		})
	for _, err := range []error{throttleErr, credpool.ErrTransientThrottle, credpool.ErrQuotaExhausted, credpool.ErrNoUsableCredential} {
		if strings.Contains(err.Error(), "secret-one") || strings.Contains(err.Error(), "secret-two") {
			t.Fatalf("error carries secret: %v", err)
		}
	}
	joined := fmt.Sprintf("%+v", coord.Snapshot())
	if strings.Contains(joined, "secret-one") || strings.Contains(joined, "secret-two") {
		t.Fatalf("snapshot carries secrets: %s", joined)
	}
}

func TestCoordinator_RejectsNilPool(t *testing.T) {
	t.Parallel()
	if _, err := credpool.NewCoordinator(nil, time.Hour, nil); err == nil {
		t.Fatal("nil pool must be rejected")
	}
	var nilCoord *credpool.Coordinator
	if _, err := credpool.Do(context.Background(), nilCoord,
		func(err error) (credpool.FailureClass, string) { return credpool.FailureNeutral, "" },
		func(ctx context.Context, cred credpool.Credential) (string, error) { return "", nil }); !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("nil coordinator must fail closed: %v", err)
	}
}
