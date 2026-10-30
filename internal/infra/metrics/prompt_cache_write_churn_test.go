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

// churnInclusive is an OpenAI/Bedrock-style inclusive-input rewrite leg: the
// input total already contains the rewritten prefix, so the write fraction is
// write/(input+read).
func churnInclusive() usage.Event {
	return usage.Event{InputTokens: 10000, CacheReadTokens: 0, CacheWriteTokens: 8000}
}

// churnExclusive is an Anthropic-style exclusive-input rewrite leg: input
// excludes cache quantities, so the same rewrite presents a write fraction
// above one. Both conventions must trip the detector identically.
func churnExclusive() usage.Event {
	return usage.Event{InputTokens: 2000, CacheReadTokens: 0, CacheWriteTokens: 8000}
}

func healthyLeg() usage.Event { return usage.Event{InputTokens: 8000, CacheReadTokens: 8000} }

func TestPromptCacheWriteChurn_StateMachine(t *testing.T) {
	inclusive, exclusive := churnInclusive(), churnExclusive()
	healthy := healthyLeg()
	coldNoCache := usage.Event{InputTokens: 8000}
	middle := usage.Event{InputTokens: 8000, CacheReadTokens: 2000}
	small := usage.Event{InputTokens: 7999, CacheWriteTokens: 7000}
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name    string
		samples []usage.Event
		want    float64
	}{
		{"rewrite churn trips", []usage.Event{inclusive, inclusive, inclusive}, 1},
		{"exclusive-input churn trips", []usage.Event{exclusive, exclusive, exclusive}, 1},
		{"mixed conventions trip", []usage.Event{inclusive, exclusive, inclusive}, 1},
		{"single cold enrollment silent", []usage.Event{inclusive, healthy, healthy}, 0},
		{"two legs silent", []usage.Event{inclusive, inclusive}, 0},
		{"no-cache cold silent", []usage.Event{coldNoCache, coldNoCache, coldNoCache, coldNoCache}, 0},
		{"healthy resets", []usage.Event{inclusive, inclusive, healthy, inclusive, inclusive}, 0},
		{"middle resets", []usage.Event{inclusive, inclusive, middle, inclusive, inclusive}, 0},
		{"heavy write with warm cache reads resets", []usage.Event{inclusive, inclusive, {InputTokens: 10000, CacheReadTokens: 3000, CacheWriteTokens: 8000}, inclusive, inclusive}, 0},
		{"latched churn does not re-fire without recovery", []usage.Event{inclusive, inclusive, inclusive, middle, inclusive, inclusive, inclusive}, 1},
		{"cold without write resets", []usage.Event{inclusive, inclusive, coldNoCache, inclusive, inclusive}, 0},
		{"small ignored", []usage.Event{small, small, small}, 0},
		{"small does not reset", []usage.Event{inclusive, inclusive, small, inclusive}, 1},
		{"recovery rearms", []usage.Event{inclusive, inclusive, inclusive, healthy, inclusive, inclusive, inclusive}, 2},
		{"zero then churn", []usage.Event{inclusive, {}, inclusive, inclusive}, 1},
		{"negative rejected", []usage.Event{inclusive, {InputTokens: 8000, CacheWriteTokens: -1}, inclusive}, 0},
		{"overflow safe", []usage.Event{{InputTokens: maxInt, CacheWriteTokens: maxInt}, inclusive, inclusive}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			d := NewPromptCacheWriteChurn(RegisterPromptCacheWriteChurnProm(reg), slog.New(slog.DiscardHandler), nil)
			for i, ev := range tc.samples {
				ev.SessionID, ev.BackendID, ev.Model = "session", "backend", "model"
				ev.ALegID = "a-leg"
				ev.BLegID, ev.AttemptSeq = fmt.Sprint(i), i+1
				empty := ev
				empty.InputTokens, empty.CacheReadTokens, empty.CacheWriteTokens = 0, 0, 0
				observeChurn(t, d, empty) // output-only/empty usage must not consume the leg
				observeChurn(t, d, ev)
				observeChurn(t, d, ev) // cumulative usage for the same B-leg cannot count twice
			}
			if got := churnCount(t, reg); got != tc.want {
				t.Fatalf("trips=%v want %v", got, tc.want)
			}
		})
	}
}

func TestPromptCacheWriteChurn_BoundsAndIdentity(t *testing.T) {
	for _, mode := range []string{"intact", "capacity", "idle", "empty session", "backend switch", "model switch", "late duplicate", "interleaved A-legs", "history rotation"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Unix(1, 0)
			reg := prometheus.NewRegistry()
			d := NewPromptCacheWriteChurn(RegisterPromptCacheWriteChurnProm(reg), slog.New(slog.DiscardHandler), func() time.Time { return now })
			newChurn := func(leg string, seq int) usage.Event {
				return usage.Event{SessionID: "target", ALegID: "a-leg", BackendID: "backend", Model: "model", BLegID: leg, AttemptSeq: seq, InputTokens: 10000, CacheWriteTokens: 8000}
			}
			sess := "target"
			ev := newChurn("first", 1)
			if mode == "empty session" {
				sess = ""
				ev.SessionID = ""
			}
			observeChurn(t, d, ev)
			second := newChurn("second", 2)
			second.SessionID = sess
			observeChurn(t, d, second)
			switch mode {
			case "capacity":
				for i := range 1024 {
					filler := healthyLeg()
					filler.SessionID = fmt.Sprint(i)
					filler.ALegID, filler.BackendID, filler.Model, filler.BLegID = "a-leg", "backend", "model", "leg"
					observeChurn(t, d, filler)
				}
			case "idle":
				now = now.Add(2 * time.Hour)
			case "backend switch":
				ev.BackendID = "new-backend"
			case "model switch":
				ev.Model = "new-model"
			case "history rotation":
				// Sub-prompt legs consume B-leg history without touching the
				// run, so rotation discards the partial run of two and the
				// next churn leg restarts at one.
				for i := range 63 {
					small := usage.Event{SessionID: sess, ALegID: "a-leg", BackendID: "backend", Model: "model", BLegID: fmt.Sprint("history-", i), AttemptSeq: i + 3, InputTokens: 7999}
					observeChurn(t, d, small)
				}
				after := newChurn("after-rotation", 100)
				after.SessionID = sess
				observeChurn(t, d, after)
				if got := churnCount(t, reg); got != 0 {
					t.Fatalf("trips=%v after %s", got, mode)
				}
				return
			}
			// Two more churn legs: intact state completes the run of three and
			// trips, while any disruption above leaves at most a partial run.
			third := newChurn("third", 3)
			third.SessionID, third.BackendID, third.Model = ev.SessionID, ev.BackendID, ev.Model
			if mode == "late duplicate" || mode == "interleaved A-legs" {
				third.BLegID, third.AttemptSeq = "second", 2
			}
			observeChurn(t, d, third)
			if mode == "interleaved A-legs" {
				other := third
				other.ALegID, other.BLegID = "second", "first"
				observeChurn(t, d, other)
			}
			fourth := newChurn("fourth", 4)
			fourth.SessionID, fourth.BackendID, fourth.Model = ev.SessionID, ev.BackendID, ev.Model
			if mode == "late duplicate" || mode == "interleaved A-legs" {
				fourth.BLegID, fourth.AttemptSeq = "second", 2
			}
			observeChurn(t, d, fourth)
			want := 0.0
			if mode == "intact" {
				want = 1
			}
			if got := churnCount(t, reg); got != want {
				t.Fatalf("trips=%v after %s, want %v", got, mode, want)
			}
		})
	}
}

func TestPromptCacheWriteChurn_Concurrent(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := NewPromptCacheWriteChurn(RegisterPromptCacheWriteChurnProm(reg), slog.New(slog.DiscardHandler), nil)
	var wg sync.WaitGroup
	var errors atomic.Int64
	for key := range 8 {
		wg.Go(func() {
			ev := usage.Event{SessionID: fmt.Sprint(key), BackendID: "backend", Model: "model", BLegID: "first", AttemptSeq: 1, InputTokens: 10000, CacheWriteTokens: 8000}
			if d.OnUsage(context.Background(), ev) != nil {
				errors.Add(1)
			}
			for i := range 2 {
				ev.BLegID, ev.AttemptSeq = fmt.Sprint(i), i+2
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
	if got := churnCount(t, reg); got != 8 {
		t.Fatalf("trips=%v want 8", got)
	}
}

func TestPromptCacheWriteChurn_PrivateWarning(t *testing.T) {
	var logs bytes.Buffer
	reg := prometheus.NewRegistry()
	d := NewPromptCacheWriteChurn(RegisterPromptCacheWriteChurnProm(reg), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	ev := usage.Event{SessionID: "PRIVATE_SESSION", BLegID: "PRIVATE_BLEG", BackendID: "backend", Model: "model", InputTokens: 10000, CacheWriteTokens: 8000, RawUsageJSON: "PRIVATE_CONTENT"}
	observeChurn(t, d, ev)
	for i := range 2 {
		// The tripping leg carries the private B-leg marker too, so a warning
		// that logged ev.BLegID would leak it.
		ev.BLegID = fmt.Sprint("PRIVATE_BLEG-", i)
		observeChurn(t, d, ev)
	}
	text := logs.String()
	for _, secret := range []string{"PRIVATE_SESSION", "PRIVATE_BLEG", "PRIVATE_CONTENT"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaks %s", secret)
		}
	}
	for _, field := range []string{`"level":"WARN"`, `"msg":"prompt-cache write churn"`, `"consecutive_churn":3`, `"input_tokens":10000`, `"cache_read_tokens":0`, `"cache_write_tokens":8000`, `"session_hash":`} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s in %s", field, text)
		}
	}
	if strings.Count(text, "prompt-cache write churn") != 1 {
		t.Fatalf("warnings=%s", text)
	}
}

func TestPromptCacheWriteChurn_ModelLabelBoundedCardinality(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := RegisterPromptCacheWriteChurnProm(reg)
	for i := range 500 {
		m.Observe("configured-backend", fmt.Sprintf("hostile-model-%d-SECRET", i))
	}
	m.Observe("configured-backend", "")
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "aip_prompt_cache_write_churn_total" {
			continue
		}
		if len(f.GetMetric()) != 2 {
			t.Fatalf("series=%d want 2", len(f.GetMetric()))
		}
		for _, metric := range f.GetMetric() {
			if len(metric.GetLabel()) != 2 {
				t.Fatalf("unexpected labels: %v", metric.GetLabel())
			}
			for _, label := range metric.GetLabel() {
				if label.GetName() == "model" && label.GetValue() != "specified" && label.GetValue() != "unknown" {
					t.Fatalf("raw model: %s", label.GetValue())
				}
				if label.GetName() != "model" && label.GetName() != "backend" {
					t.Fatalf("unbounded label: %s", label.GetName())
				}
			}
		}
		return
	}
	t.Fatal("missing churn counter")
}

func observeChurn(t *testing.T, d *PromptCacheWriteChurn, ev usage.Event) {
	t.Helper()
	if err := d.OnUsage(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
}

func churnCount(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var count float64
	for _, family := range families {
		if family.GetName() == "aip_prompt_cache_write_churn_total" {
			for _, metric := range family.GetMetric() {
				count += metric.GetCounter().GetValue()
			}
		}
	}
	return count
}
