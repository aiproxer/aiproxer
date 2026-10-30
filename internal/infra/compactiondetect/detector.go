package compactiondetect

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

const (
	// HeuristicRuleID is the generic local-compaction rule id used for
	// completion-only history-heuristic observations.
	HeuristicRuleID = "local.compaction_heuristic.v1"

	defaultMaxLegs = 4096
	defaultIdleTTL = 24 * time.Hour

	// DefaultSweepInterval bounds how often the full-map inactivity sweep runs.
	// The release path calls the detector for every canonical event, so the
	// O(maxLegs) TTL scan is amortized to once per interval instead of once per
	// event (F3). TTL expiry is therefore approximate within one interval —
	// acceptable for lazy hygiene, since the hard max-entry cap is enforced
	// separately at leg creation (O(1) check on every call).
	DefaultSweepInterval = time.Minute
	defaultSweepInterval = DefaultSweepInterval

	// releaseTextWindow bounds the rolling window of released response text
	// retained per response for cross-delta post-marker matching (requirement
	// 7.3: source text is discarded after matching). Markers are short; the
	// window is matched together with each new chunk before trimming, so a
	// marker split across deltas is always fully present when matched.
	releaseTextWindow = 512
)

// Config tunes detector bounds. Zero values use documented defaults.
type Config struct {
	// MaxLegs bounds the number of concurrently tracked authoritative A-legs.
	// Zero uses 4096. Entries beyond the bound are evicted lazily (least
	// recently seen), with no background worker.
	MaxLegs int
	// IdleTTL expires tracked A-legs (and their transactions) after inactivity.
	// Zero uses 24h. Eviction is lazy and time-sampled: the full-map inactivity
	// sweep runs at most once per minute, so expiry is approximate within one
	// interval; the hard max-entry cap is enforced separately at leg creation.
	IdleTTL time.Duration
	// Now overrides the clock for deterministic tests. Nil uses time.Now.
	Now func() time.Time
}

// correlation is the shared A-leg/B-leg/trace correlation carried by both
// request-open and response-release observations. It maps directly to
// compaction.PreservationMeta without conversion.
type correlation = compaction.PreservationMeta

// PreviewKind and the preview shapes are owned by the public compaction SDK
// seam so detector output can be passed directly to preservers. Aliases keep
// recognition and fingerprint authority in this package without introducing
// a conversion layer or a second contract.
type PreviewKind = compaction.PreviewKind

const (
	PreviewNone                = compaction.PreviewNone
	PreviewStartCandidate      = compaction.PreviewStartCandidate
	PreviewCompletionCandidate = compaction.PreviewCompletionCandidate
)

type (
	RequestPreview  = compaction.RequestPreview
	ResponsePreview = compaction.ResponsePreview
)

// transactionState is the bounded per-rule transaction for one logical
// compaction on one A-leg (requirement 6).
type transactionState struct {
	id        string
	ruleID    string
	mode      ruleMode
	firstAt   time.Time
	lastAt    time.Time
	completed bool
}

// legState is the bounded per-A-leg detector state (requirement 7.3): one
// previous fingerprint, one active transaction, timestamps, and a bounded
// rolling window of the current response's released text (content is
// discarded after matching and at completion).
type legState struct {
	lastFP                        requestFingerprint
	active                        *transactionState
	lastSeen                      time.Time
	lastFingerprintStrictComplete bool
	// releaseText is the per-response rolling window of released text (folded
	// to lowercase at write time) used to match signature post markers that
	// arrive split across streamed deltas (F1; requirement 4.7). It never
	// grows beyond releaseTextWindow plus one event chunk and is reset on
	// trace change and after any completion.
	releaseText      strings.Builder
	releaseTextTrace string
}

// Detector is a concrete, race-safe, process-owned compaction detector. One
// instance is shared by all runtime generations; it is not safe to copy.
// Callers must never invoke observers while holding the internal lock — the
// detector never does: RequestOpened and ResponseReleased return derived
// events for the caller to dispatch after the call returns.
type Detector struct {
	mu      sync.Mutex
	legs    map[string]*legState
	now     func() time.Time
	maxLegs int
	idleTTL time.Duration
	// lastSweep is the wall-clock time of the most recent full-map inactivity
	// sweep; the sweep runs at most once per defaultSweepInterval.
	lastSweep time.Time
}

// New constructs a Detector with the given bounds. A zero Config applies the
// documented defaults.
func New(cfg Config) *Detector {
	maxLegs := cfg.MaxLegs
	if maxLegs <= 0 {
		maxLegs = defaultMaxLegs
	}
	ttl := cfg.IdleTTL
	if ttl <= 0 {
		ttl = defaultIdleTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Detector{
		legs:    make(map[string]*legState),
		now:     now,
		maxLegs: maxLegs,
		idleTTL: ttl,
	}
}

func historyCandidate(ls *legState, fp requestFingerprint, curHashes [][32]byte) bool {
	return ls != nil && !ls.lastFingerprintStrictComplete && ls.lastFP.ItemCount > 0 && heuristicMatch(ls.lastFP, fp, curHashes)
}

// PreviewRequestFacts returns the request-side candidate that the shared detector
// would recognize from exact facts without recording a fingerprint, changing a
// transaction, or emitting an event.
func (d *Detector) PreviewRequestFacts(meta compaction.PreservationMeta, facts compactionfacts.RequestFacts) RequestPreview {
	if d == nil || strings.TrimSpace(meta.ALegID) == "" {
		return RequestPreview{Kind: PreviewNone}
	}
	fp := requestFingerprint{
		TraceID:         meta.TraceID,
		EstimatedTokens: facts.EstimatedTokens,
		ItemCount:       facts.ItemCount,
		TailHashes:      facts.TailHashes,
		TailLen:         facts.TailLen,
		PrefixHash:      facts.PrefixHash,
		PrefixItems:     facts.PrefixItems,
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	ls := d.legs[meta.ALegID]
	if facts.StartRuleMatched && facts.StartRuleMode != compactionfacts.RuleModeCompletionOnly {
		preview := RequestPreview{
			Evidence: facts.StartRuleEvidence,
			RuleID:   facts.StartRuleID,
			Kind:     PreviewStartCandidate,
		}
		if ls != nil && ls.active != nil && !ls.active.completed && ls.active.ruleID == facts.StartRuleID {
			preview.TransactionID = ls.active.id
		}
		return preview
	}
	if !historyCandidate(ls, fp, facts.ItemHashes) {
		return RequestPreview{Kind: PreviewNone}
	}
	if ls.active != nil && !ls.active.completed && ls.active.mode == modeSingle {
		// Committed detection closes an unfinished single-rule transaction
		// silently on this transition, so it is not a completion candidate.
		return RequestPreview{Kind: PreviewNone}
	}
	preview := RequestPreview{
		Evidence: compaction.EvidenceHistoryHeuristic,
		RuleID:   HeuristicRuleID,
		Kind:     PreviewCompletionCandidate,
	}
	if ls.active != nil && !ls.active.completed {
		preview.TransactionID = ls.active.id
	}
	if preview.TransactionID == "" {
		preview.BoundaryFingerprint = boundaryFingerprint(meta.ALegID, fp)
	}
	return preview
}

// PreviewRequest returns the request-side candidate that the shared detector
// would recognize without recording a fingerprint, changing a transaction, or
// emitting an event. It is safe to call before upstream Open. A strict start
// takes precedence over the history heuristic, matching committed detection.
func (d *Detector) PreviewRequest(meta compaction.PreservationMeta, call aipapi.Call) RequestPreview {
	return d.PreviewRequestFacts(meta, ExactFactsFromCall(call))
}

// PreviewResponse returns the response-side completion candidate without
// changing the rolling release-text window, transaction state, or observer
// output. It is intended to run before a separate preservation finalization;
// the committed ResponseReleased call must run afterwards on the final event.
func (d *Detector) PreviewResponse(meta compaction.PreservationMeta, ev aipapi.Event) ResponsePreview {
	if d == nil || strings.TrimSpace(meta.ALegID) == "" {
		return ResponsePreview{Kind: PreviewNone}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ls := d.legs[meta.ALegID]
	if isCompactionItemRelease(ev) {
		return d.previewCompletionLocked(meta, ls, protocolRule)
	}
	if r, ok := responseTextCandidate(ls, meta.TraceID, ev); ok {
		return d.previewCompletionLocked(meta, ls, r)
	}
	if ev.Kind == aipapi.EventResponseFinished && terminalIsSuccessful(ev) &&
		ls != nil && ls.active != nil && ls.active.ruleID == protocolRule.id && !ls.active.completed {
		return d.previewCompletionLocked(meta, ls, protocolRule)
	}
	return ResponsePreview{Kind: PreviewNone}
}

func (d *Detector) previewCompletionLocked(meta compaction.PreservationMeta, ls *legState, r rule) ResponsePreview {
	if ls != nil && ls.active != nil && ls.active.ruleID == r.id && ls.active.completed {
		return ResponsePreview{Kind: PreviewNone}
	}
	tx := ""
	if ls != nil && ls.active != nil && !ls.active.completed && ls.active.ruleID == r.id {
		tx = ls.active.id
	}
	if tx == "" {
		tx = txID(meta.ALegID, r.id, meta.TraceID)
	}
	return ResponsePreview{
		Evidence:      r.evidence,
		RuleID:        r.id,
		Kind:          PreviewCompletionCandidate,
		TransactionID: tx,
	}
}

// responseTextCandidate is the shared pure response-marker authority used by
// both PreviewResponse and the committed release path. The caller decides
// when to append the released text; this helper only evaluates the bounded
// existing window plus the candidate event.
func responseTextCandidate(ls *legState, traceID string, ev aipapi.Event) (rule, bool) {
	text := releasedText(ev)
	if text == "" {
		return rule{}, false
	}
	window := ""
	if ls != nil && ls.releaseTextTrace == traceID {
		window = ls.releaseText.String()
	}
	activeRuleID := ""
	if ls != nil && ls.active != nil && !ls.active.completed {
		activeRuleID = ls.active.ruleID
	}
	return matchResponseText(window+strings.ToLower(text), activeRuleID)
}

func matchResponseText(window, activeRuleID string) (rule, bool) {
	return matchCompleteRule(window, activeRuleID)
}

func boundaryFingerprint(aLegID string, fp requestFingerprint) string {
	h := sha256.New()
	_, _ = h.Write([]byte(aLegID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fp.TraceID))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(fp.EstimatedTokens))
	_, _ = h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], uint64(fp.ItemCount))
	_, _ = h.Write(buf[:])
	for i := 0; i < fp.TailLen; i++ {
		_, _ = h.Write(fp.TailHashes[i][:])
	}
	_, _ = h.Write(fp.PrefixHash[:])
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// RequestOpenedFacts observes one logical request using exact bounded semantic facts.
// It records the bounded fingerprint, runs the conservative same-A-leg history check,
// and matches strict start rules. Returns derived events in emission order.
func (d *Detector) RequestOpenedFacts(meta compaction.PreservationMeta, facts compactionfacts.RequestFacts) []compaction.Event {
	if d == nil || strings.TrimSpace(meta.ALegID) == "" {
		return nil
	}
	now := d.now()
	fp := requestFingerprint{
		TraceID:         meta.TraceID,
		EstimatedTokens: facts.EstimatedTokens,
		ItemCount:       facts.ItemCount,
		TailHashes:      facts.TailHashes,
		TailLen:         facts.TailLen,
		PrefixHash:      facts.PrefixHash,
		PrefixItems:     facts.PrefixItems,
		SeenAt:          now,
	}

	var events []compaction.Event

	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	ls := d.leg(meta.ALegID, now)

	// Conservative history heuristic (completion-only; strict post evidence
	// suppresses a duplicate, requirement 5.6).
	if historyCandidate(ls, fp, facts.ItemHashes) {
		if ev, ok := d.heuristicCompletionLocked(meta, ls, fp, facts.ItemHashes, now); ok {
			events = append(events, ev)
		}
	}

	// Stale active transactions expire so they cannot suppress later
	// compactions indefinitely (requirement 6.7). Expiry emits nothing.
	if ls.active != nil && now.Sub(ls.active.lastAt) > d.idleTTL {
		ls.active = nil
	}

	if facts.StartRuleMatched {
		r := rule{
			id:       facts.StartRuleID,
			mode:     ruleMode(facts.StartRuleMode),
			evidence: facts.StartRuleEvidence,
		}
		switch r.mode {
		case modeCompletionOnly:
			// A completion-only rule never emits a start.
		case modeSeries:
			if ls.active != nil && !ls.active.completed && ls.active.ruleID == r.id {
				// Reuse one active transaction across matching utility
				// subcalls and suppress repeated starts (requirement 6.2).
				ls.active.lastAt = now
			} else {
				ls.active = newTransaction(meta.ALegID, r, meta.TraceID, now)
				events = append(events, startedEvent(meta, r, ls.active.id, now))
			}
		case modeSingle:
			// A fresh start-rule match is a new logical compaction; the prior
			// transaction (never proven completed) closes silently. A new
			// transaction can only start after that, preserving R6.6 ordering
			// for the provable-completion case handled on the release side.
			ls.active = newTransaction(meta.ALegID, r, meta.TraceID, now)
			events = append(events, startedEvent(meta, r, ls.active.id, now))
		}
	} else if ls.active != nil && !ls.active.completed {
		// An ordinary request may close an unprovable transaction silently;
		// completion is never fabricated (requirements 6.5, 1.6).
		ls.active = nil
	}

	// Record the new fingerprint; the next request compares against it. The
	// strict-completion suppression flag resets for the new baseline.
	ls.lastFP = fp
	ls.lastFingerprintStrictComplete = false
	ls.lastSeen = now

	return events
}

// RequestOpened observes one logical request after its upstream B-leg opened
// successfully. It delegates to RequestOpenedFacts using ExactFactsFromCall.
func (d *Detector) RequestOpened(meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
	return d.RequestOpenedFacts(meta, ExactFactsFromCall(call))
}

// ExactFactsFromCall extracts exact bounded RequestFacts from a canonical Call.
func ExactFactsFromCall(call aipapi.Call) compactionfacts.RequestFacts {
	return compactionfacts.ExtractFactsFromCall(call)
}

// ResponseReleased observes one canonical event actually released by the retry
// stream (live, gated, tool-finalizer, and recovery drains — exactly once per
// release). It runs protocol-strict and signature post completions; signature
// post markers are matched against the accumulated visible text of the
// response so markers split across streamed deltas are still recognized.
// Returns derived events for fail-open dispatch; observation never alters the
// event.
func (d *Detector) ResponseReleased(meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
	if d == nil || strings.TrimSpace(meta.ALegID) == "" {
		return nil
	}
	now := d.now()
	var events []compaction.Event

	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	ls := d.leg(meta.ALegID, now)

	// Reset the per-response rolling text window when a new logical response
	// begins (trace changed); content is discarded at that boundary.
	if ls.releaseTextTrace != meta.TraceID {
		ls.releaseText.Reset()
		ls.releaseTextTrace = meta.TraceID
	}

	// Protocol-strict: a released compaction item completes once (R3.3).
	if isCompactionItemRelease(ev) {
		if out, ok := d.completeByRuleLocked(meta, ls, protocolRule, now); ok {
			events = append(events, out)
			ls.releaseText.Reset()
		}
	}

	// Signature-strict post markers on released response text (R4.7). The
	// marker is matched against the accumulated visible text of the response
	// — the retained window plus the current chunk — so a marker split across
	// streamed deltas is still recognized. The window is folded to lowercase
	// once per chunk at write time, so matching below is plain
	// allocation-free strings.Contains over the folded window; the window is
	// bounded and discarded after matching/completion (requirement 7.3).
	if text := releasedText(ev); text != "" {
		activeRuleID := ""
		if ls.active != nil && !ls.active.completed {
			activeRuleID = ls.active.ruleID
		}
		ls.releaseText.WriteString(strings.ToLower(text))
		if r, ok := matchResponseText(ls.releaseText.String(), activeRuleID); ok {
			if out, ok := d.completeByRuleLocked(meta, ls, r, now); ok {
				events = append(events, out)
				ls.releaseText.Reset()
			}
		}
		trimReleaseWindow(&ls.releaseText)
	}

	// Protocol-strict: a successful terminal of an explicit compact operation
	// may complete if no earlier compaction item did so (R3.4). An incomplete
	// or aborted terminal never fabricates a completion.
	if ev.Kind == aipapi.EventResponseFinished &&
		terminalIsSuccessful(ev) &&
		ls.active != nil && ls.active.ruleID == protocolRule.id && !ls.active.completed {
		ls.active.completed = true
		ls.active.lastAt = now
		events = append(events, completedEvent(meta, ls.active.id, protocolRule.id, compaction.EvidenceProtocolStrict, now))
	}

	if len(events) > 0 && meta.TraceID != "" && ls.lastFP.TraceID == meta.TraceID {
		// Strict post evidence suppresses a duplicate heuristic completion for
		// the same baseline (requirement 5.6).
		ls.lastFingerprintStrictComplete = true
	}
	ls.lastSeen = now

	return events
}

// heuristicCompletionLocked emits the completion-only heuristic observation
// when the conservative history match holds (requirements 5.1-5.7).
func (d *Detector) heuristicCompletionLocked(meta compaction.PreservationMeta, ls *legState, fp requestFingerprint, curHashes [][32]byte, now time.Time) (compaction.Event, bool) {
	if !heuristicMatch(ls.lastFP, fp, curHashes) {
		return compaction.Event{}, false
	}
	c := meta
	if ls.active != nil && !ls.active.completed {
		if ls.active.mode == modeSingle {
			// A single-rule transaction closes only via strict evidence or a
			// silent transition; the heuristic emits no separate completion.
			ls.active.completed = true
			return compaction.Event{}, false
		}
		// Series and completion-only transactions close on the first
		// strict/heuristic completion (design transaction-state rules).
		ls.active.completed = true
		ls.active.lastAt = now
		return completedEvent(c, ls.active.id, ls.active.ruleID, compaction.EvidenceHistoryHeuristic, now), true
	}
	// No active transaction: the heuristic is its own completion-only
	// transaction; it never invents a retroactive start (requirement 5.5).
	ls.active = &transactionState{
		id:        txID(meta.ALegID, HeuristicRuleID, meta.TraceID),
		ruleID:    HeuristicRuleID,
		mode:      modeCompletionOnly,
		firstAt:   now,
		lastAt:    now,
		completed: true,
	}
	return completedEvent(c, ls.active.id, HeuristicRuleID, compaction.EvidenceHistoryHeuristic, now), true
}

// completeByRuleLocked completes the matching active transaction once, or
// creates a completion-only transaction when no transaction exists — a post
// marker or released compaction item never invents a historical start
// (requirements 1.5, 4.7, 6.4).
func (d *Detector) completeByRuleLocked(c correlation, ls *legState, r rule, now time.Time) (compaction.Event, bool) {
	if ls.active != nil && ls.active.ruleID == r.id {
		if ls.active.completed {
			// One completion per transaction: repeat strict/post signals for
			// the same rule emit once (requirement 6.4). A completed
			// transaction is closed or replaced by the next request open, so
			// this cannot suppress a later, distinct compaction.
			return compaction.Event{}, false
		}
		ls.active.completed = true
		ls.active.lastAt = now
		return completedEvent(c, ls.active.id, r.id, r.evidence, now), true
	}
	ls.active = &transactionState{
		id:        txID(c.ALegID, r.id, c.TraceID),
		ruleID:    r.id,
		mode:      modeCompletionOnly,
		firstAt:   now,
		lastAt:    now,
		completed: true,
	}
	return completedEvent(c, ls.active.id, r.id, r.evidence, now), true
}

// leg returns (creating if needed) the per-A-leg state. Caller holds the lock.
// Creation enforces the max-entry bound: when a new A-leg pushes the map over
// the cap, the least-recently-seen leg is evicted once (ties on lastSeen break
// deterministically on the lexicographically smaller A-leg id). The
// O(maxLegs) eviction scan therefore runs only on new A-leg creation at
// capacity — never on the steady-state release path, which only touches
// already-tracked legs (F3).
func (d *Detector) leg(aLegID string, at time.Time) *legState {
	ls, ok := d.legs[aLegID]
	if ok {
		return ls
	}
	ls = &legState{lastSeen: at}
	d.legs[aLegID] = ls
	if len(d.legs) > d.maxLegs {
		d.evictOldestLocked(aLegID)
	}
	return ls
}

// evictOldestLocked removes the least-recently-seen leg. Caller holds the
// lock. Ties on lastSeen are broken by lexicographically smaller A-leg id so
// eviction is deterministic.
func (d *Detector) evictOldestLocked(excludeID string) {
	oldestID := ""
	var oldest time.Time
	for id, ls := range d.legs {
		if id == excludeID {
			continue
		}
		if oldestID == "" || ls.lastSeen.Before(oldest) || (ls.lastSeen.Equal(oldest) && id < oldestID) {
			oldestID, oldest = id, ls.lastSeen
		}
	}
	if oldestID != "" {
		delete(d.legs, oldestID)
	}
}

// sweepLocked applies the inactivity TTL lazily. Caller holds the lock. There
// is no background worker or ticker (requirement 7.4).
//
// The full-map inactivity sweep is time-sampled to at most once per
// defaultSweepInterval: ResponseReleased runs on every released event, so a
// per-event O(maxLegs) scan would dominate the streaming hot path (F3). TTL
// expiry is therefore approximate within one interval — acceptable for lazy
// hygiene, since the hard max-entry cap is enforced separately at leg
// creation. The cap check itself is O(1) on every call.
func (d *Detector) sweepLocked(now time.Time) {
	if now.Sub(d.lastSweep) < defaultSweepInterval {
		return
	}
	d.lastSweep = now
	for id, ls := range d.legs {
		if now.Sub(ls.lastSeen) > d.idleTTL {
			delete(d.legs, id)
		}
	}
}

// newTransaction allocates a deterministic transaction (no random global
// mutable state, requirement 6.3).
func newTransaction(aLegID string, r rule, triggerID string, now time.Time) *transactionState {
	return &transactionState{
		id:      txID(aLegID, r.id, triggerID),
		ruleID:  r.id,
		mode:    r.mode,
		firstAt: now,
		lastAt:  now,
	}
}

// txID is a deterministic opaque hash of the A-leg plus the first triggering
// trace/request identity and the rule (requirement 6.3).
func txID(aLegID, ruleID, triggerID string) string {
	h := sha256.Sum256([]byte(aLegID + "\x00" + ruleID + "\x00" + triggerID))
	return hex.EncodeToString(h[:8])
}

func isCompactionItemRelease(ev aipapi.Event) bool {
	return ev.Kind == aipapi.EventItem && ev.Item != nil && ev.Item.Kind == aipapi.ItemKindCompaction
}

// trimReleaseWindow bounds the retained release-text window to the last
// releaseTextWindow characters, dropping the old backing buffer so memory
// stays bounded (requirement 7.3). The caller has already matched markers
// against the full window (old tail plus the new chunk), so trimming cannot
// hide a marker: any marker whose final bytes are older than the window would
// have matched when those bytes arrived.
func trimReleaseWindow(b *strings.Builder) {
	if b.Len() <= releaseTextWindow {
		return
	}
	keep := b.String()[b.Len()-releaseTextWindow:]
	*b = strings.Builder{}
	b.WriteString(keep)
}

// terminalIsSuccessful reports whether a released response_finished proves a
// successful terminal of an explicit compact operation (R3.4). ResponseStatus
// is authoritative when set ("completed" or "incomplete"); producers that
// know the upstream status set it so downstream logic never infers
// incompleteness solely from FinishReason, which is ambiguous (a completed
// response may legitimately carry finish_reason "content_filter"). When
// ResponseStatus is empty the producer left terminal semantics to legacy
// inference, and the detector fails closed: only the canonical successful
// finish reasons ("stop", "end_turn" — the same taxonomy the continuation
// recorder uses) complete the transaction. Unknown, empty, truncated,
// cancelled, or recovery-artifact reasons never fabricate a completion; an
// uncompleted transaction is instead closed silently by the next request
// open (requirements 6.5, 1.6).
func terminalIsSuccessful(ev aipapi.Event) bool {
	if ev.ResponseStatus != "" {
		return ev.ResponseStatus == "completed"
	}
	switch strings.ToLower(strings.TrimSpace(ev.FinishReason)) {
	case "stop", "end_turn":
		return true
	default:
		return false
	}
}

// releasedText extracts the canonical text of one released event (assistant
// content parts only; tool-result output is not inspected to keep post-marker
// matching precise).
func releasedText(ev aipapi.Event) string {
	switch ev.Kind {
	case aipapi.EventTextDelta:
		return ev.Delta
	case aipapi.EventItem:
		if ev.Item == nil {
			return ""
		}
		var sb strings.Builder
		for _, cp := range ev.Item.Content {
			sb.WriteString(cp.Text)
			sb.WriteString(cp.Refusal)
			sb.WriteString(cp.Summary)
		}
		return sb.String()
	default:
		return ""
	}
}

func startedEvent(c correlation, r rule, txID string, at time.Time) compaction.Event {
	return compaction.Event{
		Phase:         compaction.PhaseStarted,
		Evidence:      r.evidence,
		RuleID:        r.id,
		TransactionID: txID,
		TraceID:       c.TraceID,
		ALegID:        c.ALegID,
		BLegID:        c.BLegID,
		AttemptSeq:    c.AttemptSeq,
		SessionID:     c.SessionID,
		OccurredAt:    at,
	}
}

func completedEvent(c correlation, txID, ruleID string, evidence compaction.Evidence, at time.Time) compaction.Event {
	return compaction.Event{
		Phase:         compaction.PhaseCompleted,
		Evidence:      evidence,
		RuleID:        ruleID,
		TransactionID: txID,
		TraceID:       c.TraceID,
		ALegID:        c.ALegID,
		BLegID:        c.BLegID,
		AttemptSeq:    c.AttemptSeq,
		SessionID:     c.SessionID,
		OccurredAt:    at,
	}
}
