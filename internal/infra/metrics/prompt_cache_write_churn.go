package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// churnMinPrompt matches the collapse diagnostic so sub-prompt noise is
	// ignored identically by both cache-economics signals.
	churnMinPrompt = 8000
	// churnHealthyRead matches the collapse healthy threshold: a leg that
	// hits cache this well proves the prefix survived and rearms the episode.
	churnHealthyRead = 0.5
	// churnColdRead matches the collapse cold threshold: only legs at or
	// below it count as low-read candidates for a rewrite pattern.
	churnColdRead = 0.1
	// churnWriteFraction is the share of the prompt rewritten in one leg
	// that counts as a heavy write. A full prefix rewrite approaches 1 on
	// inclusive-input families and exceeds 1 on exclusive-input families,
	// so one threshold catches both conventions.
	churnWriteFraction = 0.5
	// churnRun is the consecutive heavy-write/low-read run that trips one
	// episode. A single cold-enrollment write never trips; only a repeated
	// rewrite pattern does.
	churnRun        = 3
	churnMaxKeys    = 1024
	churnLegHistory = 64
	churnIdle       = 2 * time.Hour
)

type cacheChurnKey struct{ session, backend, model string }

type cacheChurnState struct {
	key        cacheChurnKey
	churnRun   int
	warned     bool
	lastBLeg   string
	legs       [churnLegHistory]string
	legCount   int
	lastSeen   time.Time
	prev, next *cacheChurnState
}

// PromptCacheWriteChurn is a bounded, process-owned usage diagnostic. It
// neither changes events nor participates in routing, cache control,
// accounting, billing, affinity or keep-warm decisions. One mutex owns the
// map, intrusive LRU and episode state. No timers or goroutines are started;
// existing-key observation allocates only on a trip.
type PromptCacheWriteChurn struct {
	mu             sync.Mutex
	keys           map[cacheChurnKey]*cacheChurnState
	oldest, newest *cacheChurnState
	now            func() time.Time
	log            *slog.Logger
	prom           *PromptCacheWriteChurnProm
}

// NewPromptCacheWriteChurn constructs process-lifetime state alongside its metrics.
// A nil clock uses time.Now; a nil logger disables warning output.
func NewPromptCacheWriteChurn(prom *PromptCacheWriteChurnProm, log *slog.Logger, now func() time.Time) *PromptCacheWriteChurn {
	if now == nil {
		now = time.Now
	}
	return &PromptCacheWriteChurn{keys: make(map[cacheChurnKey]*cacheChurnState, churnMaxKeys), now: now, log: log, prom: prom}
}

// OnUsage always returns nil. Only the first positive input/cache sample per
// B-leg is considered. Missing session/leg identity and malformed counters
// are ignored.
//
// Essential-backend accounting reconciliation: Anthropic reports input
// excluding cache reads and cache writes (exclusive, annotated with
// InputTokensExcludeCacheRead since #1087), while the OpenAI, Gemini and
// Bedrock families report input totals that already contain the cached and
// rewritten subsets (inclusive). The usage seam carries no inclusion flag,
// so the denominator deliberately uses input plus cache reads, exactly like
// the collapse diagnostic: inclusive usage slightly underestimates both
// fractions (conservative), exclusive usage reports the write fraction above
// one on a full rewrite. Either convention trips on a repeated rewrite and
// stays silent on a single cold-enrollment write.
func (d *PromptCacheWriteChurn) OnUsage(ctx context.Context, ev usage.Event) error {
	if d == nil || ev.SessionID == "" || ev.BLegID == "" || ev.InputTokens < 0 || ev.CacheReadTokens < 0 || ev.CacheWriteTokens < 0 {
		return nil
	}
	// Convert before addition to avoid overflow in provider-supplied int counts.
	prompt := float64(ev.InputTokens) + float64(ev.CacheReadTokens)
	if prompt == 0 {
		return nil
	}
	if d.observe(ev, prompt) {
		d.prom.Observe(ev.BackendID, ev.Model)
		if d.log != nil {
			// Hash only on a trip; never retain/log raw payloads or B-leg IDs.
			hash := sha256.Sum256([]byte(ev.SessionID))
			d.log.LogAttrs(ctx, slog.LevelWarn, "prompt-cache write churn",
				slog.String("backend", ev.BackendID), slog.String("model", ev.Model),
				slog.Int("consecutive_churn", churnRun),
				slog.Int("input_tokens", ev.InputTokens), slog.Int("cache_read_tokens", ev.CacheReadTokens),
				slog.Int("cache_write_tokens", ev.CacheWriteTokens),
				slog.String("session_hash", hex.EncodeToString(hash[:6])))
		}
	}
	return nil
}

func (d *PromptCacheWriteChurn) observe(ev usage.Event, prompt float64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	// Reclaim at most one unrelated idle key per access, keeping cost O(1).
	if d.oldest != nil && now.Sub(d.oldest.lastSeen) >= churnIdle {
		d.remove(d.oldest)
	}
	key := cacheChurnKey{ev.SessionID, ev.BackendID, ev.Model}
	s := d.keys[key]
	if s != nil && now.Sub(s.lastSeen) >= churnIdle {
		d.remove(s)
		s = nil
	}
	if s == nil {
		if len(d.keys) == churnMaxKeys {
			s = d.oldest
			d.remove(s)
			*s = cacheChurnState{}
		} else {
			s = &cacheChurnState{}
		}
		s.key = key
		d.keys[key] = s
	} else {
		d.unlink(s)
	}
	s.lastSeen = now
	s.prev = d.newest
	if d.newest != nil {
		d.newest.next = s
	} else {
		d.oldest = s
	}
	d.newest = s
	if ev.BLegID == s.lastBLeg {
		return false
	}
	if slices.Contains(s.legs[:s.legCount], ev.BLegID) {
		return false
	}
	if s.legCount == churnLegHistory {
		clear(s.legs[:])
		s.legCount, s.churnRun = 0, 0
		s.warned = false
	}
	s.legs[s.legCount] = ev.BLegID
	s.legCount++
	s.lastBLeg = ev.BLegID
	// Size gate: a leg matters when its reconciled prompt reaches the
	// threshold or its rewrite volume alone does. On inclusive-input
	// families the write term is a subset of input, so the gate coincides
	// with the collapse gate exactly; on exclusive-input families (where a
	// full rewrite dwarfs the uncached input) the write term admits the leg.
	if prompt < churnMinPrompt && ev.CacheWriteTokens < churnMinPrompt {
		return false
	}
	read := float64(ev.CacheReadTokens) / prompt
	write := float64(ev.CacheWriteTokens) / prompt
	switch {
	case read >= churnHealthyRead:
		s.churnRun, s.warned = 0, false
	case write >= churnWriteFraction && read <= churnColdRead && !s.warned:
		s.churnRun++
		if s.churnRun == churnRun {
			s.warned = true
			return true
		}
	default:
		s.churnRun = 0
	}
	return false
}

func (d *PromptCacheWriteChurn) unlink(s *cacheChurnState) {
	if s.prev != nil {
		s.prev.next = s.next
	} else {
		d.oldest = s.next
	}
	if s.next != nil {
		s.next.prev = s.prev
	} else {
		d.newest = s.prev
	}
	s.prev, s.next = nil, nil
}

func (d *PromptCacheWriteChurn) remove(s *cacheChurnState) {
	d.unlink(s)
	delete(d.keys, s.key)
}

// PromptCacheWriteChurnProm counts diagnostic episodes with configured
// backend instance IDs and the existing two-valued model label, never
// session IDs.
type PromptCacheWriteChurnProm struct{ trips *prometheus.CounterVec }

// RegisterPromptCacheWriteChurnProm registers the process-owned churn counter.
func RegisterPromptCacheWriteChurnProm(reg prometheus.Registerer) *PromptCacheWriteChurnProm {
	m := &PromptCacheWriteChurnProm{trips: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "prompt_cache_write_churn_total",
		Help: "Prompt-cache rewrite-churn episodes after repeated heavy cache writes with low cache reads (configured backend and bounded model labels).",
	}, []string{"backend", "model"})}
	reg.MustRegister(m.trips)
	return m
}

// Observe counts one episode. It is called only after the episode latch is set.
func (m *PromptCacheWriteChurnProm) Observe(backend, model string) {
	if m != nil {
		m.trips.WithLabelValues(labelOrUnknown(backend), modelLabel(model)).Inc()
	}
}
