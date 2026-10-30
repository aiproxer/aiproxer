package metrics

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPromptCacheCollapse_StateMachine(t *testing.T) {
	warm := usage.Event{InputTokens: 8000, CacheReadTokens: 8000}
	cold := usage.Event{InputTokens: 8000}
	middle := usage.Event{InputTokens: 8000, CacheReadTokens: 2000}
	small := usage.Event{InputTokens: 7999}
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name    string
		samples []usage.Event
		want    float64
	}{
		{"one episode", []usage.Event{warm, cold, cold, cold, cold, cold, cold}, 1},
		{"fresh A-legs", []usage.Event{warm, cold, cold, cold, cold, cold}, 1},
		{"never healthy", []usage.Event{cold, cold, cold, cold, cold, cold}, 0},
		{"small ignored", []usage.Event{warm, small, small, small, small, small}, 0},
		{"small does not reset", []usage.Event{warm, cold, cold, small, cold, cold, cold}, 1},
		{"middle resets", []usage.Event{warm, cold, cold, cold, cold, middle, cold}, 0},
		{"recovery rearms", []usage.Event{warm, cold, cold, cold, cold, cold, warm, cold, cold, cold, cold, cold}, 2},
		{"latched episode does not re-fire after a mid-band reset", []usage.Event{warm, cold, cold, cold, cold, cold, middle, cold, cold, cold, cold, cold}, 1},
		{"inclusive warm", []usage.Event{warm, {InputTokens: 10000, CacheReadTokens: 8000}, {InputTokens: 10000, CacheReadTokens: 8000}, {InputTokens: 10000, CacheReadTokens: 8000}, {InputTokens: 10000, CacheReadTokens: 8000}, {InputTokens: 10000, CacheReadTokens: 8000}}, 0},
		{"exclusive warm", []usage.Event{warm, {InputTokens: 2000, CacheReadTokens: 8000}, {InputTokens: 2000, CacheReadTokens: 8000}, {InputTokens: 2000, CacheReadTokens: 8000}, {InputTokens: 2000, CacheReadTokens: 8000}, {InputTokens: 2000, CacheReadTokens: 8000}}, 0},
		{"zero then input", []usage.Event{warm, {}, cold, cold, cold, cold, cold}, 1},
		{"negative rejected", []usage.Event{warm, {InputTokens: -1, CacheReadTokens: 8001}, cold, cold, cold, cold}, 0},
		{"overflow safe", []usage.Event{{InputTokens: maxInt, CacheReadTokens: maxInt}, cold, cold, cold, cold, cold}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), nil)
			for i, ev := range tc.samples {
				ev.SessionID, ev.BackendID, ev.Model = "session", "backend", "model"
				ev.ALegID = "a-leg"
				ev.BLegID, ev.AttemptSeq = fmt.Sprint(i), i+1
				if tc.name == "fresh A-legs" {
					ev.ALegID, ev.AttemptSeq = fmt.Sprint(i), 1
				}
				empty := ev
				empty.InputTokens, empty.CacheReadTokens = 0, 0
				observeCache(t, d, empty) // output-only/empty usage must not consume the leg
				observeCache(t, d, ev)
				observeCache(t, d, ev) // cumulative usage for the same B-leg cannot count twice
			}
			if got := collapseCount(t, reg); got != tc.want {
				t.Fatalf("trips=%v want %v", got, tc.want)
			}
		})
	}
}

func TestPromptCacheCollapse_BoundsAndIdentity(t *testing.T) {
	for _, mode := range []string{"capacity", "idle", "empty session", "backend switch", "model switch", "late duplicate", "interleaved A-legs", "history rotation"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Unix(1, 0)
			reg := prometheus.NewRegistry()
			d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), func() time.Time { return now })
			ev := usage.Event{SessionID: "target", ALegID: "a-leg", BackendID: "backend", Model: "model", BLegID: "healthy", AttemptSeq: 1, InputTokens: 8000, CacheReadTokens: 8000}
			if mode == "empty session" {
				ev.SessionID = ""
			}
			observeCache(t, d, ev)
			switch mode {
			case "capacity":
				for i := range 1024 {
					ev.SessionID = fmt.Sprint(i)
					observeCache(t, d, ev)
				}
				ev.SessionID = "target"
			case "idle":
				now = now.Add(2 * time.Hour)
			case "backend switch":
				ev.BackendID = "new-backend"
			case "model switch":
				ev.Model = "new-model"
			case "history rotation":
				for i := range 63 {
					ev.BLegID = fmt.Sprint("history-", i)
					observeCache(t, d, ev)
				}
			}
			ev.CacheReadTokens = 0
			for i := range 5 {
				ev.BLegID, ev.AttemptSeq = fmt.Sprint(i), i+2
				if mode == "late duplicate" || mode == "interleaved A-legs" {
					ev.BLegID, ev.AttemptSeq = "same", 2
				}
				if mode == "interleaved A-legs" {
					ev.ALegID = "first"
					ev.AttemptSeq = 1
				}
				observeCache(t, d, ev)
				if mode == "interleaved A-legs" {
					other := ev
					other.ALegID, other.BLegID = "second", "other"
					observeCache(t, d, other)
				}
				if mode == "late duplicate" {
					old := ev
					old.BLegID, old.AttemptSeq = "healthy", 1
					observeCache(t, d, old)
				}
			}
			if got := collapseCount(t, reg); got != 0 {
				t.Fatalf("trips=%v after %s", got, mode)
			}
		})
	}
}

func TestPromptCacheCollapse_Concurrent(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.DiscardHandler), nil)
	var wg sync.WaitGroup
	var errors atomic.Int64
	for key := range 8 {
		wg.Go(func() {
			ev := usage.Event{SessionID: fmt.Sprint(key), BackendID: "backend", Model: "model", BLegID: "warm", AttemptSeq: 1, InputTokens: 8000, CacheReadTokens: 8000}
			if d.OnUsage(context.Background(), ev) != nil {
				errors.Add(1)
			}
			for i := range 5 {
				ev.CacheReadTokens, ev.BLegID, ev.AttemptSeq = 0, fmt.Sprint(i), i+2
				var same sync.WaitGroup
				for range 8 {
					same.Go(func() {
						if d.OnUsage(context.Background(), ev) != nil {
							errors.Add(1)
						}
					})
				}
				same.Wait()
			}
		})
	}
	wg.Wait()
	if errors.Load() != 0 {
		t.Fatalf("observer returned %d errors", errors.Load())
	}
	if got := collapseCount(t, reg); got != 8 {
		t.Fatalf("trips=%v want 8", got)
	}
}

func TestPromptCacheCollapse_PrivateWarning(t *testing.T) {
	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	ev := usage.Event{SessionID: "PRIVATE_SESSION", BLegID: "PRIVATE_BLEG", BackendID: "backend", Model: "model", InputTokens: 8000, CacheReadTokens: 8000, RawUsageJSON: "PRIVATE_CONTENT"}
	observeCache(t, d, ev)
	for i := range 6 {
		// The tripping leg carries the private B-leg marker too, so a warning
		// that logged ev.BLegID would leak it.
		ev.BLegID = fmt.Sprint("PRIVATE_BLEG-", i)
		ev.CacheReadTokens = 0
		observeCache(t, d, ev)
	}
	text := logs.String()
	for _, secret := range []string{"PRIVATE_SESSION", "PRIVATE_BLEG", "PRIVATE_CONTENT"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaks %s", secret)
		}
	}
	for _, field := range []string{`"level":"WARN"`, `"msg":"prompt-cache collapse"`, `"consecutive_cold":5`, `"input_tokens":8000`, `"cache_read_tokens":0`, `"prior_healthy_ratio":0.5`, `"session_hash":`} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s in %s", field, text)
		}
	}
	if strings.Count(text, "prompt-cache collapse") != 1 {
		t.Fatalf("warnings=%s", text)
	}
}

func observeCache(t *testing.T, d *PromptCacheCollapse, ev usage.Event) {
	t.Helper()
	if err := d.OnUsage(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
}

func collapseCount(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var count float64
	for _, family := range families {
		if family.GetName() == "aip_prompt_cache_collapse_total" {
			for _, metric := range family.GetMetric() {
				count += metric.GetCounter().GetValue()
			}
		}
	}
	return count
}

func TestPromptCacheCollapse_StructuralAttribution(t *testing.T) {
	noteCauses := []struct {
		name  string
		cause CollapseCause
	}{
		{"affinity switch", CollapseCauseAffinitySwitch},
		{"credential switch", CollapseCauseCredentialSwitch},
		{"compaction", CollapseCauseCompaction},
		{"system prompt", CollapseCauseSystemPrompt},
	}
	cases := []struct {
		name   string
		cause  CollapseCause
		signal func(d *PromptCacheCollapse, now *time.Time)
	}{
		{"unexplained", CollapseCauseUnexplained, nil},
		{"keep-warm expiry", CollapseCauseKeepWarmExpiry, func(_ *PromptCacheCollapse, now *time.Time) {
			*now = now.Add(61 * time.Minute)
		}},
		{"config generation", CollapseCauseConfigGeneration, func(d *PromptCacheCollapse, _ *time.Time) {
			d.NoteConfigGeneration()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertAttributedCause(t, tc.cause, tc.signal)
		})
	}
	for _, nc := range noteCauses {
		t.Run(nc.name, func(t *testing.T) {
			cause := nc.cause
			assertAttributedCause(t, cause, func(d *PromptCacheCollapse, _ *time.Time) {
				d.NoteStructuralCause("session", "backend", "model", cause)
			})
		})
	}
}

func assertAttributedCause(t *testing.T, want CollapseCause, signal func(d *PromptCacheCollapse, now *time.Time)) {
	t.Helper()
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	reg := prometheus.NewRegistry()
	d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.NewJSONHandler(&logs, nil)), func() time.Time { return now })
	observeCache(t, d, usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: "warm", AttemptSeq: 1, InputTokens: 8000, CacheReadTokens: 8000})
	if signal != nil {
		signal(d, &now)
	}
	for i := range 5 {
		observeCache(t, d, usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: fmt.Sprint("cold-", i), AttemptSeq: i + 2, InputTokens: 8000})
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if strings.Count(text, "prompt-cache collapse") != 1 {
		t.Fatalf("warnings in %s", text)
	}
	if !strings.Contains(text, `"cause":"`+string(want)+`"`) {
		t.Fatalf("missing cause %q in %s", want, text)
	}
}

func TestPromptCacheCollapse_StructuralAttributionNotes(t *testing.T) {
	newDetector := func(logs *bytes.Buffer, now *time.Time) (*PromptCacheCollapse, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		return NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.NewJSONHandler(logs, nil)), func() time.Time { return *now }), reg
	}
	healthy := usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: "warm", AttemptSeq: 1, InputTokens: 8000, CacheReadTokens: 8000}
	cold := func(i int) usage.Event {
		return usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: fmt.Sprint("cold-", i), AttemptSeq: i + 2, InputTokens: 8000}
	}
	t.Run("note cleared by a later healthy sample", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		observeCache(t, d, healthy)
		d.NoteStructuralCause("session", "backend", "model", CollapseCauseCompaction)
		// A later healthy sample proves the recorded cause is stale.
		healthyAgain := healthy
		healthyAgain.BLegID = "warm-2"
		observeCache(t, d, healthyAgain)
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"unexplained"`) {
			t.Fatalf("stale note applied in %s", logs.String())
		}
	})
	t.Run("note for unknown key is dropped", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		d.NoteStructuralCause("absent", "backend", "model", CollapseCauseCompaction)
		observeCache(t, d, healthy)
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"unexplained"`) {
			t.Fatalf("dropped note applied in %s", logs.String())
		}
	})
	t.Run("derived keep-warm cause cannot be published", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		observeCache(t, d, healthy)
		d.NoteStructuralCause("session", "backend", "model", CollapseCauseKeepWarmExpiry)
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"unexplained"`) {
			t.Fatalf("derived keep-warm note applied in %s", logs.String())
		}
	})
	t.Run("unbounded cause is rejected", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		observeCache(t, d, healthy)
		for _, hostile := range []CollapseCause{"", "unexplained", "hostile-cause-SECRET", CollapseCause("backend")} {
			d.NoteStructuralCause("session", "backend", "model", hostile)
		}
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"unexplained"`) {
			t.Fatalf("hostile cause applied in %s", logs.String())
		}
	})
	t.Run("explicit note wins over generation and gap", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		observeCache(t, d, healthy)
		d.NoteConfigGeneration()
		now = now.Add(61 * time.Minute)
		d.NoteStructuralCause("session", "backend", "model", CollapseCauseAffinitySwitch)
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"affinity-switch"`) {
			t.Fatalf("precedence violated in %s", logs.String())
		}
	})
	t.Run("generation wins over gap", func(t *testing.T) {
		var logs bytes.Buffer
		now := time.Unix(1000, 0)
		d, reg := newDetector(&logs, &now)
		observeCache(t, d, healthy)
		d.NoteConfigGeneration()
		now = now.Add(61 * time.Minute)
		for i := range 5 {
			observeCache(t, d, cold(i))
		}
		if got := collapseCount(t, reg); got != 1 {
			t.Fatalf("trips=%v want 1", got)
		}
		if !strings.Contains(logs.String(), `"cause":"config-generation"`) {
			t.Fatalf("precedence violated in %s", logs.String())
		}
	})
}

func BenchmarkPromptCacheCollapse_UsageChain(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprint(enabled), func(b *testing.B) {
			var observer usage.Observer = usage.NoopObserver{}
			if enabled {
				d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), nil)
				observer = usage.ChainObservers(observer, d)
			}
			ev := usage.Event{SessionID: "session", BackendID: "backend", Model: "model", InputTokens: 8000, CacheReadTokens: 8000}
			var legs [128]string
			for i := range legs {
				legs[i] = fmt.Sprint(i)
			}
			b.ReportAllocs()
			for b.Loop() {
				ev.AttemptSeq++
				ev.BLegID = legs[ev.AttemptSeq%len(legs)]
				if err := observer.OnUsage(b.Context(), ev); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
