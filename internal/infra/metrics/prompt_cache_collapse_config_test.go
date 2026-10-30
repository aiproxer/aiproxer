package metrics

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPromptCacheCollapse_SettingsHonored(t *testing.T) {
	newDetector := func(cfg PromptCacheCollapseSettings) (*PromptCacheCollapse, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		now := time.Unix(1, 0)
		d := NewPromptCacheCollapseWithSettings(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), func() time.Time { return now }, cfg)
		return d, reg
	}
	ev := func(session, leg string, input, cached int) usage.Event {
		return usage.Event{SessionID: session, BackendID: "backend", Model: "model", BLegID: leg, InputTokens: input, CacheReadTokens: cached}
	}
	t.Run("lower minimum admits small prompts", func(t *testing.T) {
		d, reg := newDetector(PromptCacheCollapseSettings{MinPromptTokens: 100})
		observeCache(t, d, ev("s", "warm", 100, 100))
		for i := range 5 {
			observeCache(t, d, ev("s", fmt.Sprint(i), 100, 0))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
	})
	t.Run("shorter run trips sooner", func(t *testing.T) {
		d, reg := newDetector(PromptCacheCollapseSettings{ConsecutiveCold: 2})
		observeCache(t, d, ev("s", "warm", 8000, 8000))
		observeCache(t, d, ev("s", "c0", 8000, 0))
		if got := collapseCount(t, reg); got != 0 {
			t.Fatalf("trips=%v want 0 before run completes", got)
		}
		observeCache(t, d, ev("s", "c1", 8000, 0))
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
	})
	t.Run("custom ratios reshape bands", func(t *testing.T) {
		d, reg := newDetector(PromptCacheCollapseSettings{HealthyRatio: 0.9, ColdRatio: 0.5, ConsecutiveCold: 1})
		// 0.6 hit is neither healthy (>=0.9) nor cold (<=0.5): must not arm or trip.
		observeCache(t, d, ev("s", "mid", 4000, 6000))
		observeCache(t, d, ev("s", "c0", 8000, 0))
		if got := collapseCount(t, reg); got != 0 {
			t.Fatalf("trips=%v want 0 without prior healthy", got)
		}
		observeCache(t, d, ev("s", "warm", 1000, 9000))
		observeCache(t, d, ev("s", "c1", 8000, 0))
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
	})
	t.Run("small capacity evicts", func(t *testing.T) {
		d, reg := newDetector(PromptCacheCollapseSettings{MaxTrackedKeys: 1})
		observeCache(t, d, ev("target", "warm", 8000, 8000))
		observeCache(t, d, ev("other", "warm", 8000, 8000))
		for i := range 5 {
			observeCache(t, d, ev("target", fmt.Sprint(i), 8000, 0))
		}
		if got := collapseCount(t, reg); got != 0 {
			t.Fatalf("trips=%v want 0 after eviction", got)
		}
	})
	t.Run("short idle expiry reclaims", func(t *testing.T) {
		now := time.Unix(1, 0)
		reg := prometheus.NewRegistry()
		d := NewPromptCacheCollapseWithSettings(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), func() time.Time { return now }, PromptCacheCollapseSettings{IdleExpiry: time.Minute})
		observeCache(t, d, ev("s", "warm", 8000, 8000))
		now = now.Add(time.Minute)
		for i := range 5 {
			observeCache(t, d, ev("s", fmt.Sprint(i), 8000, 0))
		}
		if got := collapseCount(t, reg); got != 0 {
			t.Fatalf("trips=%v want 0 after idle reclaim", got)
		}
	})
	t.Run("zero settings keep the default five-cold run", func(t *testing.T) {
		d, reg := newDetector(PromptCacheCollapseSettings{})
		observeCache(t, d, ev("s", "warm", 8000, 8000))
		for i := range 4 {
			observeCache(t, d, ev("s", fmt.Sprint("cold-", i), 8000, 0))
		}
		if got := collapseCount(t, reg); got != 0 {
			t.Fatalf("trips=%v want 0 before the default five-cold run completes", got)
		}
		observeCache(t, d, ev("s", "cold-4", 8000, 0))
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1 at the default five-cold run", got)
		}
	})
}
