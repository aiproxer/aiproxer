package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	codexContinuationTTL        = time.Hour
	codexContinuationMaxEntries = 1024
)

type wsContinuationStore struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[wsContinuationKey]wsContinuationEntry
	order      []wsContinuationKey
	now        func() time.Time
}

type wsContinuationKey struct {
	sessionID      string
	model          string
	accountID      string
	promptCacheKey string
	clientFamily   string
}

type wsContinuationEntry struct {
	responseID              string
	inputFingerprints       []string
	outputItemFingerprints  []string
	instructionsFingerprint string
	toolsFingerprint        string
	inFlight                bool
	expiresAt               time.Time
}

func newWSContinuationStore(ttl time.Duration, maxEntries int) *wsContinuationStore {
	if ttl <= 0 {
		ttl = codexContinuationTTL
	}
	if maxEntries <= 0 {
		maxEntries = codexContinuationMaxEntries
	}
	return &wsContinuationStore{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[wsContinuationKey]wsContinuationEntry),
		now:        time.Now,
	}
}

func (s *wsContinuationStore) prepare(ctx context.Context, cfg *Config, call aipapi.Call, payload *Payload) bool {
	if payload == nil {
		return false
	}
	return s.prepareWithFingerprints(ctx, cfg, call, payload, fingerprintInputItems(payload.Input))
}

func (s *wsContinuationStore) prepareWithFingerprints(ctx context.Context, cfg *Config, call aipapi.Call, payload *Payload, inputFingerprints []string) bool {
	if s == nil || payload == nil {
		return false
	}
	if strings.TrimSpace(call.Session.AuthoritativeSessionID) == "" {
		return false
	}
	key := continuationKeyWithFingerprints(cfg, call, payload, inputFingerprints)
	instructionsFingerprint := fingerprintJSON(payload.Instructions)
	toolsFingerprint := fingerprintJSON(payload.Tools)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[key]
	if !ok {
		return false
	}
	if entry.inFlight {
		logWSContinuation(ctx, call, payload.Model, "in_flight", len(payload.Input), len(payload.Input), "")
		return false
	}
	s.touchLocked(key)
	if entry.instructionsFingerprint != instructionsFingerprint ||
		entry.toolsFingerprint != toolsFingerprint {
		delete(s.entries, key)
		logWSContinuation(ctx, call, payload.Model, "static_fingerprint_changed", 0, len(payload.Input), "")
		return false
	}
	baseline := append([]string(nil), entry.inputFingerprints...)
	baseline = append(baseline, entry.outputItemFingerprints...)
	sliced, ok := sliceInputForContinuation(baseline, payload.Input, inputFingerprints)
	mode := "delta_applied"
	if !ok {
		sliced, ok = sliceInputAfterReplayedOutputItems(entry.inputFingerprints, len(entry.outputItemFingerprints), payload.Input, inputFingerprints)
		mode = "delta_applied_replayed_output"
	}
	if !ok {
		delete(s.entries, key)
		logWSContinuation(ctx, call, payload.Model, "input_drift", 0, len(payload.Input), "")
		return false
	}
	before := len(payload.Input)
	payload.PreviousResponseID = entry.responseID
	payload.Input = sliced
	entry.inFlight = true
	s.entries[key] = entry
	logWSContinuation(ctx, call, payload.Model, mode, before, len(payload.Input), entry.responseID)
	return true
}

func (s *wsContinuationStore) record(cfg *Config, call aipapi.Call, payload Payload, responseID string, outputItems ...inputItem) {
	s.recordWithFingerprints(cfg, call, payload, fingerprintInputItems(payload.Input), responseID, outputItems...)
}

func (s *wsContinuationStore) recordWithFingerprints(cfg *Config, call aipapi.Call, payload Payload, inputFingerprints []string, responseID string, outputItems ...inputItem) {
	if s == nil {
		return
	}
	if strings.TrimSpace(call.Session.AuthoritativeSessionID) == "" {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	key := continuationKeyWithFingerprints(cfg, call, &payload, inputFingerprints)
	outputItemFingerprints := fingerprintInputItems(outputItems)
	instructionsFingerprint := fingerprintJSON(payload.Instructions)
	toolsFingerprint := fingerprintJSON(payload.Tools)
	expiresAt := s.now().Add(s.ttl)
	entry := wsContinuationEntry{
		responseID:              responseID,
		inputFingerprints:       append([]string(nil), inputFingerprints...),
		outputItemFingerprints:  outputItemFingerprints,
		instructionsFingerprint: instructionsFingerprint,
		toolsFingerprint:        toolsFingerprint,
		expiresAt:               expiresAt,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.entries[key] = entry
	s.touchLocked(key)
	for len(s.entries) > s.maxEntries && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
}

func (s *wsContinuationStore) invalidate(cfg *Config, call aipapi.Call, payload *Payload) {
	if payload == nil {
		return
	}
	s.invalidateWithFingerprints(cfg, call, payload, fingerprintInputItems(payload.Input))
}

func (s *wsContinuationStore) invalidateWithFingerprints(cfg *Config, call aipapi.Call, payload *Payload, inputFingerprints []string) {
	if s == nil || payload == nil {
		return
	}
	key := continuationKeyWithFingerprints(cfg, call, payload, inputFingerprints)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	out := s.order[:0]
	for _, existing := range s.order {
		if existing != key {
			out = append(out, existing)
		}
	}
	s.order = out
}

// invalidateLineage removes every turn-scoped response-id optimization for the
// selected account/session lineage. A newly installed checkpoint changes the
// upstream history authority, so the next normal request must send full exact
// input and establish a fresh baseline only after successful completion.
func (s *wsContinuationStore) invalidateLineage(cfg *Config, call aipapi.Call, payload *Payload) {
	if s == nil || payload == nil {
		return
	}
	key := continuationKeyWithFingerprints(cfg, call, payload, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	for existing := range s.entries {
		if existing.sessionID == key.sessionID && existing.model == key.model && existing.accountID == key.accountID && existing.promptCacheKey == key.promptCacheKey && existing.clientFamily == key.clientFamily {
			delete(s.entries, existing)
		}
	}
	filtered := s.order[:0]
	for _, existing := range s.order {
		if _, ok := s.entries[existing]; ok {
			filtered = append(filtered, existing)
		}
	}
	s.order = filtered
}

func (s *wsContinuationStore) purgeExpiredLocked() {
	now := s.now()
	out := s.order[:0]
	for _, key := range s.order {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
			continue
		}
		out = append(out, key)
	}
	s.order = out
}

func (s *wsContinuationStore) touchLocked(key wsContinuationKey) {
	out := s.order[:0]
	for _, existing := range s.order {
		if existing != key {
			out = append(out, existing)
		}
	}
	out = append(out, key)
	s.order = out
}

func continuationKeyWithFingerprints(cfg *Config, call aipapi.Call, payload *Payload, inputFingerprints []string) wsContinuationKey {
	accountID := ""
	if cfg != nil {
		accountID = strings.TrimSpace(cfg.AccountID)
	}
	model := ""
	promptCacheKey := ""
	if payload != nil {
		model = strings.TrimSpace(payload.Model)
		promptCacheKey = strings.TrimSpace(payload.PromptCacheKey)
	}
	// WebSocket response IDs are only an optimization. Like checkpoint state,
	// they must be partitioned by proxy-owned authority; client continuity hints
	// are insufficient to prevent cross-session reuse.
	sessionID := strings.TrimSpace(call.Session.AuthoritativeSessionID)
	if sessionID == "" && payload != nil && len(payload.Input) > 0 {
		sessionID = "input:" + firstInputFingerprint(payload.Input, inputFingerprints)
	}
	return wsContinuationKey{
		sessionID:      sessionID,
		model:          model,
		accountID:      accountID,
		promptCacheKey: promptCacheKey,
		clientFamily:   continuationClientFamily(call),
	}
}

func continuationClientFamily(call aipapi.Call) string {
	for _, key := range []string{"agent", "openai_codex.agent", "user_agent"} {
		if raw, ok := call.Extensions[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) == nil {
				if family := normalizeContinuationFamily(value); family != "generic" {
					return family
				}
			}
		}
	}
	if raw, ok := call.Extensions["headers"]; ok {
		var headers map[string]string
		if json.Unmarshal(raw, &headers) == nil {
			for _, key := range []string{"user-agent", "User-Agent"} {
				if family := normalizeContinuationFamily(headers[key]); family != "generic" {
					return family
				}
			}
		}
	}
	return "generic"
}

func normalizeContinuationFamily(candidate string) string {
	lowered := strings.ToLower(strings.TrimSpace(candidate))
	switch {
	case strings.Contains(lowered, "opencode"):
		return "opencode"
	case strings.Contains(lowered, "factory-cli"), strings.Contains(lowered, "factory_cli"), strings.Contains(lowered, "factorydroid"):
		return "droid"
	default:
		return "generic"
	}
}

func sliceInputForContinuation(prior []string, current []inputItem, currentFP []string) ([]inputItem, bool) {
	if len(prior) == 0 || len(current) == 0 {
		return nil, false
	}
	if len(currentFP) == 0 {
		currentFP = fingerprintInputItems(current)
	}
	common := 0
	for common < len(prior) && common < len(currentFP) && prior[common] == currentFP[common] {
		common++
	}
	if common < len(prior) || common <= 0 || common >= len(current) {
		return nil, false
	}
	return append([]inputItem(nil), current[common:]...), true
}

func sliceInputAfterReplayedOutputItems(prior []string, outputItems int, current []inputItem, currentFP []string) ([]inputItem, bool) {
	if len(prior) == 0 || outputItems <= 0 || len(current) == 0 {
		return nil, false
	}
	if len(currentFP) == 0 {
		currentFP = fingerprintInputItems(current)
	}
	if len(currentFP) <= len(prior) {
		return nil, false
	}
	for i := range prior {
		if prior[i] != currentFP[i] {
			return nil, false
		}
	}
	idx := len(prior)
	skipped := 0
	for idx < len(current) && skipped < outputItems {
		if !isReplayableOutputItem(current[idx]) {
			break
		}
		idx++
		skipped++
	}
	if skipped == 0 || idx >= len(current) {
		return nil, false
	}
	if _, ok := current[idx].(functionCallOutputItem); !ok {
		return nil, false
	}
	return append([]inputItem(nil), current[idx:]...), true
}

func isReplayableOutputItem(item inputItem) bool {
	switch item.(type) {
	case functionCallItem, opaqueResponseItem:
		return true
	default:
		return false
	}
}

func fingerprintInputItems(items []inputItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fingerprintJSON(item))
	}
	return out
}

func fingerprintJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
