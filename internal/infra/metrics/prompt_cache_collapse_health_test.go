package metrics

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	ssessiondiag "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

func healthDetector(now *time.Time) *PromptCacheCollapse {
	reg := prometheus.NewRegistry()
	return NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), func() time.Time { return *now })
}

func healthEvent(session, leg string, input, cacheRead int) usage.Event {
	return usage.Event{
		SessionID: session, ALegID: "a-leg", BLegID: leg,
		BackendID: "backend", Model: "model",
		InputTokens: input, CacheReadTokens: cacheRead,
	}
}

func collapseSession(t *testing.T, d *PromptCacheCollapse, session string) {
	t.Helper()
	observeCache(t, d, healthEvent(session, "warm", 8000, 8000))
	for i := range 5 {
		observeCache(t, d, healthEvent(session, fmt.Sprint("cold-", i), 8000, 0))
	}
}

func TestPromptCacheCollapseHealth_Snapshot(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)

	t.Run("unknown when never observed", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		if got := d.SessionCacheHealth("absent"); got.Status != ssessiondiag.CacheHealthUnknown || got.Stale {
			t.Fatalf("got %+v want unknown fresh", got)
		}
		if got := d.SessionCacheHealth(""); got.Status != ssessiondiag.CacheHealthUnknown {
			t.Fatalf("empty session got %+v want unknown", got)
		}
	})

	t.Run("nil detector reports unknown", func(t *testing.T) {
		t.Parallel()
		var d *PromptCacheCollapse
		if got := d.SessionCacheHealth("session"); got.Status != ssessiondiag.CacheHealthUnknown || got.Stale {
			t.Fatalf("got %+v want unknown", got)
		}
		if got := (&PromptCacheCollapse{}).SessionCacheHealth("session"); got.Status != ssessiondiag.CacheHealthUnknown {
			t.Fatalf("zero value got %+v want unknown", got)
		}
	})

	t.Run("implements diagnostics reader port", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		var r ssessiondiag.CacheHealthReader = d
		observeCache(t, d, healthEvent("sess", "warm", 8000, 8000))
		if got := r.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthHealthy || got.Stale {
			t.Fatalf("got %+v want healthy fresh", got)
		}
	})

	t.Run("healthy after warm usage", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		observeCache(t, d, healthEvent("sess", "warm", 8000, 8000))
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthHealthy || got.Stale {
			t.Fatalf("got %+v want healthy fresh", got)
		}
		if got := d.SessionCacheHealth("other"); got.Status != ssessiondiag.CacheHealthUnknown {
			t.Fatalf("other session got %+v want unknown", got)
		}
	})

	t.Run("collapsed after episode", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		collapseSession(t, d, "sess")
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthCollapsed || got.Stale {
			t.Fatalf("got %+v want collapsed fresh", got)
		}
	})

	t.Run("recovery returns to healthy", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		collapseSession(t, d, "sess")
		observeCache(t, d, healthEvent("sess", "warm-again", 8000, 8000))
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthHealthy || got.Stale {
			t.Fatalf("got %+v want healthy after recovery", got)
		}
	})

	t.Run("cold without healthy history stays unknown", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		for i := range 5 {
			observeCache(t, d, healthEvent("sess", fmt.Sprint("cold-", i), 8000, 0))
		}
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthUnknown || got.Stale {
			t.Fatalf("got %+v want unknown fresh", got)
		}
	})

	t.Run("worst key wins across backends", func(t *testing.T) {
		t.Parallel()
		d := healthDetector(&now)
		warm := healthEvent("sess", "warm", 8000, 8000)
		warm.BackendID = "other-backend"
		observeCache(t, d, warm)
		collapseSession(t, d, "sess")
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthCollapsed {
			t.Fatalf("got %+v want collapsed", got)
		}
	})

	t.Run("idle observation reads stale", func(t *testing.T) {
		t.Parallel()
		localNow := time.Unix(200, 0)
		d := healthDetector(&localNow)
		collapseSession(t, d, "sess")
		localNow = localNow.Add(2 * time.Hour)
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthCollapsed || !got.Stale {
			t.Fatalf("got %+v want collapsed stale", got)
		}
	})

	t.Run("fresh key dominates idle siblings", func(t *testing.T) {
		t.Parallel()
		localNow := time.Unix(400, 0)
		d := healthDetector(&localNow)
		warm := healthEvent("sess", "warm", 8000, 8000)
		warm.BackendID = "other-backend"
		observeCache(t, d, warm)
		localNow = localNow.Add(2 * time.Hour)
		observeCache(t, d, healthEvent("sess", "warm-fresh", 8000, 8000))
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthHealthy || got.Stale {
			t.Fatalf("got %+v want healthy fresh", got)
		}
	})

	t.Run("evicted observation reads unknown", func(t *testing.T) {
		t.Parallel()
		localNow := time.Unix(300, 0)
		d := healthDetector(&localNow)
		observeCache(t, d, healthEvent("target", "warm", 8000, 8000))
		localNow = localNow.Add(2 * time.Hour)
		// The next observation lazily reclaims the idle key; the snapshot
		// must not resurrect it.
		observeCache(t, d, healthEvent("other", "warm", 8000, 8000))
		if got := d.SessionCacheHealth("target"); got.Status != ssessiondiag.CacheHealthUnknown {
			t.Fatalf("got %+v want unknown after eviction", got)
		}
	})

	t.Run("snapshot does not consume legs or trip episodes", func(t *testing.T) {
		t.Parallel()
		reg := prometheus.NewRegistry()
		d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), nil)
		observeCache(t, d, healthEvent("sess", "warm", 8000, 8000))
		for i := range 10 {
			_ = d.SessionCacheHealth("sess")
			if i < 5 {
				observeCache(t, d, healthEvent("sess", fmt.Sprint("cold-", i), 8000, 0))
			}
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1: snapshot must stay read-only", got)
		}
		if got := d.SessionCacheHealth("sess"); got.Status != ssessiondiag.CacheHealthCollapsed {
			t.Fatalf("got %+v want collapsed", got)
		}
	})
}
