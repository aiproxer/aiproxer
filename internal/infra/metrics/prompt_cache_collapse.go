package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	collapseMinPrompt  = 8000
	collapseHealthy    = 0.5
	collapseCold       = 0.1
	collapseRun        = 5
	collapseMaxKeys    = 1024
	collapseLegHistory = 64
	collapseIdle       = 2 * time.Hour
	// collapseMaxItems caps each retained per-item digest vector.
	// Worst case per fed key is 2*collapseMaxItems SHA-256 hashes (8 KiB at
	// the default bound), or 8 MiB process-wide at full occupancy with the
	// default key capacity; the ceiling scales with configured
	// MaxTrackedKeys. Vectors are allocated lazily on the first digest
	// feed so unfed keys cost nothing.
	collapseMaxItems = 128
)

// PromptCacheCollapseSettings carries the resolved collapse thresholds owned
// by the process constructor. The zero value selects the historical defaults
// via resolveCollapseSettings, so existing call sites keep default behavior.
type PromptCacheCollapseSettings struct {
	MinPromptTokens int
	HealthyRatio    float64
	ColdRatio       float64
	ConsecutiveCold int
	MaxTrackedKeys  int
	IdleExpiry      time.Duration
}

// DefaultPromptCacheCollapseSettings returns the historical thresholds.
func DefaultPromptCacheCollapseSettings() PromptCacheCollapseSettings {
	return PromptCacheCollapseSettings{
		MinPromptTokens: collapseMinPrompt,
		HealthyRatio:    collapseHealthy,
		ColdRatio:       collapseCold,
		ConsecutiveCold: collapseRun,
		MaxTrackedKeys:  collapseMaxKeys,
		IdleExpiry:      collapseIdle,
	}
}

func resolveCollapseSettings(in PromptCacheCollapseSettings) PromptCacheCollapseSettings {
	def := DefaultPromptCacheCollapseSettings()
	if in == (PromptCacheCollapseSettings{}) {
		return def
	}
	if in.MinPromptTokens <= 0 {
		in.MinPromptTokens = def.MinPromptTokens
	}
	if in.HealthyRatio <= 0 {
		in.HealthyRatio = def.HealthyRatio
	}
	if in.ColdRatio < 0 {
		in.ColdRatio = def.ColdRatio
	}
	if in.ConsecutiveCold <= 0 {
		in.ConsecutiveCold = def.ConsecutiveCold
	}
	if in.MaxTrackedKeys <= 0 {
		in.MaxTrackedKeys = def.MaxTrackedKeys
	}
	if in.IdleExpiry <= 0 {
		in.IdleExpiry = def.IdleExpiry
	}
	return in
}

type cacheCollapseKey struct{ session, backend, model string }

// CollapseCause is the bounded structural attribution for a collapse episode.
// Values are fixed log labels only; observers never attach content, session or
// credential material to them.
type CollapseCause string

const (
	CollapseCauseUnexplained      CollapseCause = "unexplained"
	CollapseCauseKeepWarmExpiry   CollapseCause = "keep-warm-expiry"
	CollapseCauseAffinitySwitch   CollapseCause = "affinity-switch"
	CollapseCauseCredentialSwitch CollapseCause = "credential-switch"
	CollapseCauseCompaction       CollapseCause = "compaction"
	CollapseCauseConfigGeneration CollapseCause = "config-generation"
	CollapseCauseSystemPrompt     CollapseCause = "system-prompt"
)

// publishableCollapseCause reports whether an authority may publish cause.
// The published set is closed, so a label that reaches a log line or a metric
// is always one of the known causes. Derived causes are never published:
// unexplained and config-generation are derived by the detector, and
// keep-warm-expiry is derived from the keep-warm idle horizon, whose win
// conditions are fixed below a configuration generation and every published
// cause. No authority may publish them.
func publishableCollapseCause(cause CollapseCause) bool {
	switch cause {
	case CollapseCauseAffinitySwitch, CollapseCauseCredentialSwitch,
		CollapseCauseCompaction, CollapseCauseSystemPrompt:
		return true
	default:
		return false
	}
}

// switchIdentityEvidence is the content-free switch evidence carried by one
// usage sample. Components are independently optional so the diagnostic never
// treats an unreported adapter target, open credential, or provider account as
// a known value and never combines unrelated identifiers.
type switchIdentityEvidence struct {
	core            [32]byte
	cacheTarget     [32]byte
	openCredential  [32]byte
	providerAccount [32]byte
}

func switchIdentityEvidenceFromEvent(ev usage.Event) switchIdentityEvidence {
	return switchIdentityEvidence{
		core:            ev.CacheIdentityDigest,
		cacheTarget:     ev.CacheTargetDigest,
		openCredential:  ev.CredentialIdentityDigest,
		providerAccount: ev.ProviderAccountDigest,
	}
}

func (e switchIdentityEvidence) equal(other switchIdentityEvidence) bool {
	return e.core == other.core &&
		e.cacheTarget == other.cacheTarget &&
		e.openCredential == other.openCredential &&
		e.providerAccount == other.providerAccount
}

type cacheCollapseState struct {
	key                cacheCollapseKey
	sawHealthy, warned bool
	lowRun             int
	lastBLeg           string
	// B-leg history handles interleaved requests even when their A-leg sequences
	// restart. Rotating a full history also discards healthy/episode history:
	// eviction cannot carry healthy evidence into a new deduplication epoch.
	legs         [collapseLegHistory]string
	legCount     int
	lastSeen     time.Time
	healthyRatio float64
	// healthyAt/healthyGen anchor structural attribution to the latest healthy
	// sample. note carries one authority-published cause recorded after that
	// sample; a later healthy sample clears it as stale.
	healthyAt  time.Time
	healthyGen uint64
	note       CollapseCause
	// healthySystemPrompt is the content-free instruction identity observed on
	// the latest healthy sample in launch order. Attribution compares it with
	// the collapse-triggering leg's identity only when the trigger's launch
	// ordinal is strictly newer: a slow older request completing late is not
	// evidence that the triggering request carries a new instruction prefix.
	// Zero means the latest healthy sample carried no identity (or the epoch
	// has no baseline), while healthySystemPromptSeq still records that
	// sample's launch order so an even older completion cannot become the
	// baseline.
	//
	// healthySystemPromptSeq is the process-owned launch ordinal of the healthy
	// sample that produced the identity. Zero means no chronological evidence
	// is available; the detector then falls back to the conservative ambiguity
	// latch instead of ordering by arrival.
	//
	// healthySystemPromptAmbiguous records a conflict that cannot be ordered
	// because launch chronology is unavailable, or because two legs of the same
	// request carried different identities. The epoch keeps no comparison
	// baseline until rotation or eviction.
	healthySystemPrompt          [32]byte
	healthySystemPromptSeq       uint64
	healthySystemPromptAmbiguous bool
	// healthySwitch is the keyed, content-free switch evidence observed on
	// the latest accepted healthy sample in launch order. Attribution
	// compares it with the collapse-triggering leg, never with an intervening
	// leg: a temporary target or credential change that reverts before the
	// trigger leaves the trigger equal to the healthy reference and is not
	// blamed. The comparison runs after the B-leg deduplication boundary, so
	// a rejected duplicate observation cannot shift the reference.
	//
	// Each component is optional and is compared only when both the healthy
	// reference and the trigger carry it. The core component is the
	// configured backend plus resolved native model and is always present
	// when the identity feed is armed; the adapter target and the open-time
	// credential are present only when the backend reported them, and the
	// provider account is present only on usage samples that carried one.
	// Unknown is not a known value, so unknown-to-known and known-to-unknown
	// transitions are not switches by themselves.
	healthySwitch switchIdentityEvidence
	// healthySwitchSeq is the request launch ordinal of the healthy sample
	// that established the switch reference. A late older healthy completion
	// must not move it backwards; a sample without an ordinal must not
	// overwrite or inherit a known ordinal. Zero means the epoch has no
	// ordered reference, and switch attribution is suppressed.
	healthySwitchSeq uint64
	// healthySwitchAmbiguous records conflicting switch evidence from two
	// legs of the same request (the same launch ordinal). A cross-request
	// switch cannot be established from that conflict; the epoch keeps no
	// switch reference until a strictly newer healthy request replaces it.
	healthySwitchAmbiguous bool
	prev, next             *cacheCollapseState
	// Seam attribution retains content-free per-item digests for two legs
	// only: the last eligible healthy leg (the reference the cold era is
	// compared against) and the most recent fed leg. Slices are allocated
	// lazily on the first digest feed and owned under the detector mutex;
	// they are dropped with the key on idle/capacity eviction and on
	// history rotation. The detector never receives item content:
	// ObserveSeamDigests and usage.Event.SeamDigests accept fixed-size
	// hashes, so retaining or logging content through this seam is
	// structurally impossible.
	//
	// The reference/triggering pair is authoritative: a collapse reports
	// the first index where the triggering cold vector differs from the
	// healthy reference. A triggering leg without digests (feed disabled,
	// or a path with no canonical call such as wire/large-body), or a
	// missing/invalidated healthy reference, reports unknown rather than
	// a stale comparison between two unrelated cold legs. B-leg ownership
	// (seamHealthyLeg/seamLeg) ties each vector to the leg that fed it.
	seamHealthy, seamLast                   [][32]byte
	seamHealthyTruncated, seamLastTruncated bool
	seamHealthyLeg, seamLeg                 string
	seamHealthyValid                        bool
}

// PromptCacheCollapse is a bounded, process-owned usage diagnostic. It neither
// changes events nor participates in routing, cache control, or accounting.
// One mutex owns the map, intrusive LRU and episode/attempt state. No timers or
// goroutines are started; existing-key observation allocates only on a trip.
type PromptCacheCollapse struct {
	mu             sync.Mutex
	keys           map[cacheCollapseKey]*cacheCollapseState
	oldest, newest *cacheCollapseState
	// gen counts applied config generations. The process build establishes the
	// baseline; each reloaded candidate increments it via NoteConfigGeneration.
	gen  uint64
	now  func() time.Time
	log  *slog.Logger
	prom *PromptCacheCollapseProm
	cfg  PromptCacheCollapseSettings
}

// NewPromptCacheCollapse constructs process-lifetime state alongside its metrics.
// A nil clock uses time.Now; a nil logger disables warning output.
func NewPromptCacheCollapse(prom *PromptCacheCollapseProm, log *slog.Logger, now func() time.Time) *PromptCacheCollapse {
	return NewPromptCacheCollapseWithSettings(prom, log, now, PromptCacheCollapseSettings{})
}

// NewPromptCacheCollapseWithSettings constructs process-lifetime state with
// resolved operator thresholds. Validation belongs to the config layer; a
// zero-valued field selects the historical default here.
func NewPromptCacheCollapseWithSettings(prom *PromptCacheCollapseProm, log *slog.Logger, now func() time.Time, cfg PromptCacheCollapseSettings) *PromptCacheCollapse {
	if now == nil {
		now = time.Now
	}
	cfg = resolveCollapseSettings(cfg)
	return &PromptCacheCollapse{
		keys: make(map[cacheCollapseKey]*cacheCollapseState, cfg.MaxTrackedKeys),
		now:  now,
		log:  log,
		prom: prom,
		cfg:  cfg,
	}
}

// NoteStructuralCause records one authority-published structural cause for the
// key's current healthy epoch. Only the known published causes are accepted;
// empty, unexplained, config-generation and arbitrary values are ignored so
// labels stay bounded. Notes for unknown keys are dropped: without healthy
// history no episode can fire. A later healthy sample clears the note as
// stale. It never changes requests, streams, routing, or billing.
func (d *PromptCacheCollapse) NoteStructuralCause(session, backend, model string, cause CollapseCause) {
	if !publishableCollapseCause(cause) {
		return
	}
	if d == nil || session == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.keys[cacheCollapseKey{session, backend, model}]; s != nil {
		s.note = cause
	}
}

// NoteConfigGeneration records that a reloaded config generation was applied.
// A later collapse whose healthy sample predates the reload is attributed to
// the generation change. It never changes requests, streams, routing, or billing.
func (d *PromptCacheCollapse) NoteConfigGeneration() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gen++
}

// OnUsage always returns nil. Only the first positive input/cache-read sample
// per B-leg is considered. Missing session/leg identity and malformed counters
// are ignored. Input may include or exclude cache reads, so the denominator
// deliberately includes both: inclusive usage underestimates the hit ratio.
func (d *PromptCacheCollapse) OnUsage(ctx context.Context, ev usage.Event) error {
	if d == nil || ev.SessionID == "" || ev.BLegID == "" || ev.InputTokens < 0 || ev.CacheReadTokens < 0 {
		return nil
	}
	// Convert before addition to avoid overflow in provider-supplied int counts.
	prompt := float64(ev.InputTokens) + float64(ev.CacheReadTokens)
	if prompt == 0 {
		return nil
	}
	trip, prior, seam, cause := d.observe(ev, prompt)
	if trip {
		d.prom.Observe(ev.BackendID, ev.Model)
		if d.log != nil {
			// Hash only on a trip; never retain/log raw payloads or B-leg IDs.
			hash := sha256.Sum256([]byte(ev.SessionID))
			attrs := []slog.Attr{
				slog.String("backend", ev.BackendID), slog.String("model", ev.Model),
				slog.Int("consecutive_cold", d.cfg.ConsecutiveCold),
				slog.Int("input_tokens", ev.InputTokens), slog.Int("cache_read_tokens", ev.CacheReadTokens),
				slog.Float64("prior_healthy_ratio", prior), slog.String("session_hash", hex.EncodeToString(hash[:6])),
			}
			if seam >= 0 {
				attrs = append(attrs, slog.Int("seam_item_index", seam))
			}
			attrs = append(attrs, slog.String("cause", string(cause)))
			d.log.LogAttrs(ctx, slog.LevelWarn, "prompt-cache collapse", attrs...)
		}
	}
	return nil
}

// ObserveSeamDigests records content-free per-item digests (for example
// SHA-256 over each normalized request item, computed by the caller) for one
// B-leg. It is advisory only: it never changes request, stream, routing, or
// billing behavior, and a key that never feeds digests trips exactly as
// before.
//
// Production publishes the same vectors on the leg's own usage sample
// (usage.Event.SeamDigests); this entry point remains for direct feeds and
// tests. Both paths share the first-wins rule: each B-leg is recorded at
// most once regardless of feed/usage ordering (duplicate deliveries are
// ignored). The direct feed updates only the latest-leg vector; the healthy
// reference is owned by the usage path, which is the only path that knows
// whether a leg was healthy.
//
// Ownership and bounds: vectors are copied under the detector mutex and
// truncated to the first collapseMaxItems digests; only the healthy
// reference and the most recent leg are kept. Each B-leg is recorded at
// most once regardless of feed/usage ordering (duplicate deliveries are
// ignored), and vectors are dropped with the key on idle/capacity eviction
// and on history rotation.
//
// Attribution compares the healthy reference with the collapse-triggering
// cold vector at trip time and reports the first divergent item index into
// the normalized item trajectory (instructions first, then messages for
// legacy calls; canonical items for item-authoritative calls). Byte offsets
// are deliberately unsupported: a digest-only representation cannot locate
// bytes without content or per-item lengths, so no offset is ever reported.
func (d *PromptCacheCollapse) ObserveSeamDigests(session, backend, model, bLegID string, digests [][32]byte) {
	if d == nil || session == "" || bLegID == "" || len(digests) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.keyState(cacheCollapseKey{session, backend, model}, d.now())
	if bLegID == s.seamLeg || bLegID == s.lastBLeg {
		return
	}
	if slices.Contains(s.legs[:s.legCount], bLegID) {
		return
	}
	storeSeamLast(s, bLegID, digests)
}

// storeSeamLast copies and retains one B-leg digest vector as the most
// recent leg, truncating to the first collapseMaxItems entries. Callers hold
// the detector mutex and have already applied their path's deduplication
// rule. It never touches the healthy reference.
func storeSeamLast(s *cacheCollapseState, bLegID string, digests [][32]byte) {
	n, truncated := len(digests), false
	if n > collapseMaxItems {
		n, truncated = collapseMaxItems, true
	}
	owned := make([][32]byte, n)
	copy(owned, digests[:n])
	s.seamLast, s.seamLastTruncated = owned, truncated
	s.seamLeg = bLegID
}

// storeSeamHealthy copies and retains one eligible healthy leg digest vector
// as the attribution reference, truncating to the first collapseMaxItems
// entries. Callers hold the detector mutex. A healthy leg without digests
// invalidates the reference instead: the last known healthy prompt may no
// longer describe the baseline, so a later collapse must report unknown
// rather than compare against a stale vector.
func storeSeamHealthy(s *cacheCollapseState, bLegID string, digests [][32]byte) {
	if len(digests) == 0 {
		s.seamHealthy, s.seamHealthyTruncated = nil, false
		s.seamHealthyLeg, s.seamHealthyValid = "", false
		return
	}
	n, truncated := len(digests), false
	if n > collapseMaxItems {
		n, truncated = collapseMaxItems, true
	}
	owned := make([][32]byte, n)
	copy(owned, digests[:n])
	s.seamHealthy, s.seamHealthyTruncated = owned, truncated
	s.seamHealthyLeg, s.seamHealthyValid = bLegID, true
}

// applySeamDigests records the content-free per-item digests the leg's own
// usage sample carries. It runs only for an eligible accepted sample (at or
// above the minimum prompt size), after the duplicate-B-leg checks, so a
// repeated usage delta can neither apply digests nor shift the retained
// vectors. Vector ownership is first-wins per B-leg: a leg that already fed
// digests through ObserveSeamDigests keeps its vector regardless of
// feed/usage ordering.
//
// Healthy promotion is independent of vector ownership: an accepted healthy
// usage sample always (re)arms the healthy reference. When the direct path
// already owns the leg's vector, the existing vector is promoted without
// being overwritten, preserving its truncation flag — this covers a healthy
// leg whose digests arrived only through the direct feed. An eligible
// healthy leg with usable evidence from neither source invalidates the
// reference instead. Legs without digests (feed disabled, or a path with no canonical call such as
// wire/large-body) otherwise leave the retained vectors unchanged, so a
// collapse whose triggering leg fed no digests, or whose healthy baseline
// is unknown, reports unknown instead of a stale comparison between two
// unrelated cold legs.
func applySeamDigests(s *cacheCollapseState, ev usage.Event, isHealthy bool) {
	if len(ev.SeamDigests) == 0 {
		if isHealthy {
			// A healthy leg without usage-riding digests may still own a
			// direct-fed vector: promote it instead of invalidating. Only
			// invalidate when neither source supplied usable evidence.
			if ev.BLegID != "" && ev.BLegID == s.seamLeg {
				promoteSeamHealthy(s, ev.BLegID)
			} else {
				storeSeamHealthy(s, "", nil)
			}
		}
		return
	}
	if ev.BLegID == "" {
		return
	}
	if ev.BLegID == s.seamLeg {
		if isHealthy {
			promoteSeamHealthy(s, ev.BLegID)
		}
		return
	}
	storeSeamLast(s, ev.BLegID, ev.SeamDigests)
	if isHealthy {
		storeSeamHealthy(s, ev.BLegID, ev.SeamDigests)
	}
}

// promoteSeamHealthy arms the healthy reference from the latest vector when
// the direct feed already owns the accepted healthy leg. The existing vector
// is copied (never aliased) with its truncation flag preserved; when no
// owned vector exists the reference is invalidated rather than left stale.
func promoteSeamHealthy(s *cacheCollapseState, bLegID string) {
	if bLegID == "" || s.seamLeg != bLegID || len(s.seamLast) == 0 {
		storeSeamHealthy(s, "", nil)
		return
	}
	owned := append([][32]byte(nil), s.seamLast...)
	s.seamHealthy, s.seamHealthyTruncated = owned, s.seamLastTruncated
	s.seamHealthyLeg, s.seamHealthyValid = bLegID, true
}

// seamDivergence reports the first divergent item index between the healthy
// reference and the collapse-triggering cold vector, or -1 when attribution
// is unknown: either vector missing, identical vectors, or a divergence
// hidden beyond the retention bound (a truncated prefix match never
// fabricates a seam at the truncation point). The index addresses the
// normalized item trajectory (instructions first, then messages for legacy
// calls; canonical items for item-authoritative calls), not a wire byte
// offset.
func seamDivergence(healthy, last [][32]byte, healthyTruncated, lastTruncated bool) int {
	if len(healthy) == 0 || len(last) == 0 {
		return -1
	}
	n := min(len(last), len(healthy))
	for i := range n {
		if healthy[i] != last[i] {
			return i
		}
	}
	if len(healthy) != len(last) && !healthyTruncated && !lastTruncated {
		return n
	}
	return -1
}

// seamTripIndex resolves the seam index for a collapse trip on s triggered by
// triggerBLegID. It requires the triggering leg to own the latest vector and
// a valid healthy reference; any missing or untied evidence reports unknown
// (-1) rather than a stale comparison between two unrelated cold legs.
func seamTripIndex(s *cacheCollapseState, triggerBLegID string) int {
	if s == nil || triggerBLegID == "" {
		return -1
	}
	if !s.seamHealthyValid || len(s.seamHealthy) == 0 || len(s.seamLast) == 0 {
		return -1
	}
	if s.seamLeg != triggerBLegID {
		return -1
	}
	if s.seamHealthyLeg == "" || s.seamHealthyLeg == triggerBLegID {
		return -1
	}
	return seamDivergence(s.seamHealthy, s.seamLast, s.seamHealthyTruncated, s.seamLastTruncated)
}

func (d *PromptCacheCollapse) observe(ev usage.Event, prompt float64) (bool, float64, int, CollapseCause) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	s := d.keyState(cacheCollapseKey{ev.SessionID, ev.BackendID, ev.Model}, now)
	if ev.BLegID == s.lastBLeg {
		return false, 0, -1, CollapseCauseUnexplained
	}
	if slices.Contains(s.legs[:s.legCount], ev.BLegID) {
		return false, 0, -1, CollapseCauseUnexplained
	}
	if s.legCount == collapseLegHistory {
		clear(s.legs[:])
		s.legCount, s.lowRun = 0, 0
		s.sawHealthy, s.warned, s.healthyRatio = false, false, 0
		// A rotated history starts a new deduplication epoch: digests from
		// the old epoch cannot attribute seams in the new one.
		s.seamHealthy, s.seamLast = nil, nil
		s.seamHealthyTruncated, s.seamLastTruncated = false, false
		s.seamHealthyLeg, s.seamLeg = "", ""
		s.seamHealthyValid = false
		s.note = ""
		s.healthySystemPrompt = [32]byte{}
		s.healthySystemPromptSeq = 0
		s.healthySystemPromptAmbiguous = false
		s.healthySwitch = switchIdentityEvidence{}
		s.healthySwitchSeq = 0
		s.healthySwitchAmbiguous = false
		s.healthyGen = d.gen
	}
	s.legs[s.legCount] = ev.BLegID
	s.legCount++
	s.lastBLeg = ev.BLegID
	// The sample is accepted: only now may its cause change the diagnostic
	// state. A duplicate or already-recorded leg never mutates the state, so
	// a repeated usage delta can neither apply a cause nor revive one that a
	// healthy sample already proved stale. Digest vectors are handled below,
	// after prompt eligibility and health are known.
	d.applyStructuralCause(s, ev)
	if prompt < float64(d.cfg.MinPromptTokens) {
		return false, 0, -1, CollapseCauseUnexplained
	}
	hit := float64(ev.CacheReadTokens) / prompt
	// Digest evidence rides eligibility: only prompts that can advance an
	// episode may become the healthy reference or the triggering vector, so
	// sub-threshold samples can neither arm a reference nor shift the pair.
	applySeamDigests(s, ev, hit >= d.cfg.HealthyRatio)
	switch {
	case hit >= d.cfg.HealthyRatio:
		s.sawHealthy, s.warned, s.lowRun, s.healthyRatio = true, false, 0, hit
		s.healthyAt, s.healthyGen, s.note = now, d.gen, ""
		d.observeHealthySwitchIdentity(s, switchIdentityEvidenceFromEvent(ev), ev.PromptCacheLaunchSeq)
		d.observeHealthySystemPrompt(s, ev.SystemPromptDigest, ev.PromptCacheLaunchSeq)
	case hit > d.cfg.ColdRatio:
		s.lowRun = 0
	case s.sawHealthy && !s.warned:
		s.lowRun++
		if s.lowRun == d.cfg.ConsecutiveCold {
			s.warned = true
			return true, s.healthyRatio, seamTripIndex(s, ev.BLegID),
				d.attribute(s, now, ev.SystemPromptDigest, ev.PromptCacheLaunchSeq, switchIdentityEvidenceFromEvent(ev))
		}
	}
	return false, 0, -1, CollapseCauseUnexplained
}

// applyStructuralCause records the structural cause the proxy published for the
// request this usage sample belongs to. The cause rides the leg's own sample,
// so a request can never be attributed a cause published for another one.
//
// The cause is published by the request, not by the leg: the proxy publishes
// one structural cause at most once per request and cache key, so a later leg
// of the same request carries no cause and cannot re-arm one that a healthy
// sample already proved stale. The detector therefore applies every cause it
// receives, and the deduplication boundary above decides which samples may
// change the state at all.
//
// Only the known published causes are accepted; an empty, unknown or derived
// label is ignored so labels stay bounded. The label carries no content.
func (d *PromptCacheCollapse) applyStructuralCause(s *cacheCollapseState, ev usage.Event) {
	cause := CollapseCause(strings.TrimSpace(ev.StructuralCause))
	if !publishableCollapseCause(cause) {
		return
	}
	s.note = cause
}

// observeHealthySwitchIdentity stores the switch reference from a healthy
// sample. A sample with a launch ordinal older than the stored reference is
// ignored, and a sample without a launch ordinal never creates, overwrites, or
// inherits a numbered reference: it cannot be ordered against one.
//
// An unsequenced sample that confirms the numbered reference is harmless, but
// one that differs (including one carrying only incomplete evidence) proves
// that the numbered reference is no longer the latest healthy observation.
// Retaining it would let a later cold leg that matches the unsequenced sample
// be falsely attributed to a switch. The epoch therefore clears the comparison
// and marks it ambiguous while keeping the last trusted ordinal, so a late
// older numbered completion cannot re-establish the baseline. Only a strictly
// newer numbered healthy request establishes a fresh reference.
//
// Two healthy legs of the same request (the same non-zero launch ordinal) with
// different evidence likewise cannot establish a cross-request switch
// reference; the epoch marks the reference ambiguous and keeps no comparison
// baseline until a strictly newer healthy request replaces it.
func (d *PromptCacheCollapse) observeHealthySwitchIdentity(s *cacheCollapseState, evidence switchIdentityEvidence, launchSeq uint64) {
	if s == nil {
		return
	}
	if launchSeq == 0 {
		if s.healthySwitchSeq != 0 {
			// The observation cannot be ordered against the numbered
			// reference. Confirmation is safe; replacement is not. Any
			// difference, including missing adapter components, invalidates
			// the comparison but keeps the chronology watermark.
			if !s.healthySwitch.equal(evidence) {
				s.healthySwitch = switchIdentityEvidence{}
				s.healthySwitchAmbiguous = true
			}
			return
		}
		// No chronology is available yet. Retain the evidence for a later
		// ordered sample to replace, but do not invent an ordinal.
		s.healthySwitch = evidence
		return
	}
	switch {
	case s.healthySwitchSeq == 0:
		s.healthySwitchSeq = launchSeq
		s.healthySwitch = evidence
		s.healthySwitchAmbiguous = false
	case launchSeq < s.healthySwitchSeq:
		// A late older healthy completion cannot replace or clear a newer
		// reference.
		return
	case launchSeq == s.healthySwitchSeq:
		if s.healthySwitchAmbiguous {
			// Ambiguity established for this launch ordinal survives every
			// further leg of the same request. Only a strictly newer healthy
			// request can establish a fresh reference below.
			s.healthySwitch = switchIdentityEvidence{}
			return
		}
		if !s.healthySwitch.equal(evidence) {
			s.healthySwitch = switchIdentityEvidence{}
			s.healthySwitchAmbiguous = true
		}
	default:
		// A strictly newer healthy request is the new reference, even when
		// one of its optional components is absent. The absent component has
		// no comparison baseline and cannot fabricate a switch.
		s.healthySwitchSeq = launchSeq
		s.healthySwitch = evidence
		s.healthySwitchAmbiguous = false
	}
}

// observeHealthySystemPrompt selects the instruction identity that anchors
// attribution for the healthy sample that just arrived.
//
// Launch order is trusted when the producer supplied a process-owned ordinal.
// A sample older than the recorded reference is ignored: it cannot replace or
// clear a baseline established by a request that started later. A sample from
// the same request may establish the baseline or confirm it, but two differing
// identities from one request prove nothing about a cross-turn change, so the
// epoch keeps no baseline. A newer sample replaces the reference even when its
// identity is missing; the ordinal still advances so an even older completion
// cannot become the baseline, and the absent identity leaves no comparison
// baseline.
//
// Without a launch ordinal the detector cannot order healthy samples against a
// numbered reference. An unsequenced observation may confirm that reference's
// identity, but it can never supply it: the retained ordinal must keep naming
// the observation that produced it. A differing or missing unsequenced identity
// therefore clears the comparison while the numbered watermark stays in place,
// so an older numbered completion still cannot become the baseline. When the
// epoch has no numbered reference at all, the previous conservative latch
// applies: a differing identity suppresses the comparison until rotation or
// eviction, and a missing identity clears the baseline.
func (d *PromptCacheCollapse) observeHealthySystemPrompt(s *cacheCollapseState, identity [32]byte, launchSeq uint64) {
	if s == nil {
		return
	}
	var none [32]byte
	if launchSeq == 0 {
		if s.healthySystemPromptSeq != 0 {
			// Never retain a numbered identity that this unnumbered
			// observation introduced. Confirmation is safe; replacement is
			// not, because the two observations cannot be ordered.
			if identity != s.healthySystemPrompt {
				s.healthySystemPrompt = none
			}
			return
		}
		switch {
		case identity == none:
			s.healthySystemPrompt = none
		case s.healthySystemPromptAmbiguous:
			s.healthySystemPrompt = none
		case s.healthySystemPrompt == none:
			s.healthySystemPrompt = identity
		case s.healthySystemPrompt == identity:
			// Same effective instructions: keep the established baseline.
		default:
			s.healthySystemPrompt = none
			s.healthySystemPromptAmbiguous = true
		}
		return
	}
	if s.healthySystemPromptSeq != 0 && launchSeq < s.healthySystemPromptSeq {
		// An older request completing healthily after a newer one cannot
		// replace or clear the newer baseline.
		return
	}
	if launchSeq == s.healthySystemPromptSeq {
		if s.healthySystemPromptAmbiguous {
			// Ambiguity established for this launch ordinal survives every
			// further leg of the same request. Only a strictly newer healthy
			// request can establish a fresh reference below.
			s.healthySystemPrompt = none
			return
		}
		switch {
		case s.healthySystemPrompt == none:
			s.healthySystemPrompt = identity
		case identity == none:
			// A leg of the same request without an observation does not erase
			// a known identity from another leg of that request.
		case s.healthySystemPrompt == identity:
			// Same effective instructions: keep the established baseline.
		default:
			s.healthySystemPrompt = none
			s.healthySystemPromptAmbiguous = true
		}
		return
	}
	// A strictly newer healthy request sets the reference. A missing identity
	// still advances the ordinal, so older completions stay unable to set a
	// baseline.
	s.healthySystemPromptSeq = launchSeq
	s.healthySystemPromptAmbiguous = false
	s.healthySystemPrompt = identity
}

// attribute resolves the trip cause. An authority-published note recorded after
// the latest healthy sample wins; then a demonstrated cross-turn instruction
// change, proven by comparing the collapse-triggering leg's content-free
// instruction identity with the healthy sample's identity; then a reload
// generation boundary; then a demonstrated downstream cache-target or
// credential change, proven by comparing the triggering leg's switch identity
// with the healthy sample's identity; then a gap beyond the keep-warm idle
// horizon, which reuses the keep-warm authority's DefaultMaxIdleDuration
// rather than inventing a TTL.
//
// Switch attribution is detector-owned and anchored to the healthy reference,
// never to an intervening observation: a temporary target/credential change
// that reverts before the trigger is not blamed, and duplicate B-leg samples
// rejected by the deduplication boundary never move the reference. A reload
// generation outranks the switch because a reload can itself change the
// target.
//
// The switch evidence is component-wise. Core identity (configured backend and
// resolved native model) is comparable whenever both sides carry it. The
// optional adapter target, open-time credential, and usage provider account are
// compared only when both the healthy reference and the trigger carry that
// component: an unknown value is never treated as a known one, so an adapter
// beginning or stopping target/account reporting does not fabricate a switch.
//
// The identity comparison is deliberately anchored to the healthy reference and
// the triggering leg, never to an intervening leg. Usage observations can
// arrive out of request-start order (a slow older request completing after a
// newer healthy one), and a temporary change can revert before the trigger; in
// both cases the triggering request carries the same identity as the healthy
// one, so attributing it to system-prompt or a switch would fabricate a
// change. The trigger must also have started strictly after the healthy
// reference; a delayed older request that happens to be the fifth cold leg
// cannot prove a cross-turn change.
// An ambiguous reference is never usable: conflicting identities from the same
// request (or an unorderable mixed-chronology observation) suppress attribution
// until a strictly newer healthy request establishes a fresh reference.
// A missing identity or launch ordinal on either side leaves the comparison
// impossible and the episode unexplained rather than attributed from incomplete
// evidence.
func (d *PromptCacheCollapse) attribute(s *cacheCollapseState, now time.Time, triggerSystemPrompt [32]byte, triggerLaunchSeq uint64, triggerSwitch switchIdentityEvidence) CollapseCause {
	var none [32]byte
	ordered := !s.healthySwitchAmbiguous &&
		s.healthySwitchSeq != 0 &&
		triggerLaunchSeq != 0 &&
		triggerLaunchSeq > s.healthySwitchSeq
	known := func(value [32]byte) bool { return value != none }
	coreChanged := ordered && known(s.healthySwitch.core) && known(triggerSwitch.core) &&
		s.healthySwitch.core != triggerSwitch.core
	targetChanged := ordered && known(s.healthySwitch.cacheTarget) && known(triggerSwitch.cacheTarget) &&
		s.healthySwitch.cacheTarget != triggerSwitch.cacheTarget
	openCredentialChanged := ordered && known(s.healthySwitch.openCredential) && known(triggerSwitch.openCredential) &&
		s.healthySwitch.openCredential != triggerSwitch.openCredential
	accountChanged := ordered && known(s.healthySwitch.providerAccount) && known(triggerSwitch.providerAccount) &&
		s.healthySwitch.providerAccount != triggerSwitch.providerAccount
	affinityChanged := coreChanged || targetChanged
	credentialChanged := openCredentialChanged || accountChanged
	switch {
	case s.note != "":
		return s.note
	case !s.healthySystemPromptAmbiguous &&
		s.healthySystemPrompt != none && triggerSystemPrompt != none &&
		s.healthySystemPrompt != triggerSystemPrompt &&
		s.healthySystemPromptSeq != 0 && triggerLaunchSeq > s.healthySystemPromptSeq:
		return CollapseCauseSystemPrompt
	case s.healthyGen != d.gen:
		return CollapseCauseConfigGeneration
	case affinityChanged:
		return CollapseCauseAffinitySwitch
	case credentialChanged:
		return CollapseCauseCredentialSwitch
	case now.Sub(s.healthyAt) >= keepwarm.DefaultMaxIdleDuration:
		return CollapseCauseKeepWarmExpiry
	default:
		return CollapseCauseUnexplained
	}
}

// keyState returns the LRU-touched state for key, reclaiming at most one
// unrelated idle entry per access (O(1)) and evicting idle or over-capacity
// keys with all their digest state. Callers must hold d.mu.
func (d *PromptCacheCollapse) keyState(key cacheCollapseKey, now time.Time) *cacheCollapseState {
	// Reclaim at most one unrelated idle key per access, keeping cost O(1).
	if d.oldest != nil && now.Sub(d.oldest.lastSeen) >= d.cfg.IdleExpiry {
		d.remove(d.oldest)
	}
	s := d.keys[key]
	if s != nil && now.Sub(s.lastSeen) >= d.cfg.IdleExpiry {
		d.remove(s)
		s = nil
	}
	if s == nil {
		if len(d.keys) == d.cfg.MaxTrackedKeys {
			s = d.oldest
			d.remove(s)
			*s = cacheCollapseState{}
		} else {
			s = &cacheCollapseState{}
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
	return s
}

func (d *PromptCacheCollapse) unlink(s *cacheCollapseState) {
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

func (d *PromptCacheCollapse) remove(s *cacheCollapseState) {
	d.unlink(s)
	delete(d.keys, s.key)
}

// PromptCacheCollapseProm counts diagnostic episodes with configured backend
// instance IDs and the existing two-valued model label, never session IDs.
type PromptCacheCollapseProm struct{ trips *prometheus.CounterVec }

// RegisterPromptCacheCollapseProm registers the process-owned collapse counter.
func RegisterPromptCacheCollapseProm(reg prometheus.Registerer) *PromptCacheCollapseProm {
	m := &PromptCacheCollapseProm{trips: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "prompt_cache_collapse_total",
		Help: "Prompt-cache collapse episodes after healthy usage (configured backend and bounded model labels).",
	}, []string{"backend", "model"})}
	reg.MustRegister(m.trips)
	return m
}

// Observe counts one episode. It is called only after the episode latch is set.
func (m *PromptCacheCollapseProm) Observe(backend, model string) {
	if m != nil {
		m.trips.WithLabelValues(labelOrUnknown(backend), modelLabel(model)).Inc()
	}
}
