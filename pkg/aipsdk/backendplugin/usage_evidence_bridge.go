package backendplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const maxBufferedUsageEvidence = 256

// UsageEvidenceBuffer is the connector-local bridge for the negotiated V1
// accounting sideband. It carries only the six presence-aware token counters;
// the host owns B-leg/store binding and any V2 identity or native measure
// lifting. Provider adapters must keep unsupported cost, media, gauge, and
// revision detail out of this bridge rather than coercing it into tokens.
//
// The buffer is safe for a stream reader and the forwarding coordinator to use
// concurrently. Exact replay is ignored; changed payloads remain available as
// separate V1 records so the host can apply its own source-key policy.
type UsageEvidenceBuffer struct {
	mu               sync.Mutex
	items            []AccountingEvidence
	lastFingerprint  map[string]string
	fingerprintOrder []string
	enabled          bool
}

// NewUsageEvidenceBuffer creates a stream-local V1 accounting bridge.
func NewUsageEvidenceBuffer() *UsageEvidenceBuffer {
	return &UsageEvidenceBuffer{lastFingerprint: make(map[string]string), enabled: true}
}

// SetEnabled controls whether this bridge emits V1 sideband records. A
// connector sets it from the negotiated host feature before reading a stream;
// disabling also discards any constructor-seeded records so a legacy host
// cannot receive an unnegotiated host-only frame.
func (b *UsageEvidenceBuffer) SetEnabled(enabled bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.enabled = enabled
	if !enabled {
		b.items = nil
		b.lastFingerprint = make(map[string]string)
		b.fingerprintOrder = nil
	}
	b.mu.Unlock()
}

// AccountingEvidenceEnabled reports whether this bridge is negotiated for the
// current connector session. Producers use it when projecting canonical usage
// events: an enabled sideband owns the durable key, while a disabled bridge
// leaves that key available to the legacy canonical capture path.
func (b *UsageEvidenceBuffer) AccountingEvidenceEnabled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled
}

// AddUsageEvent adds a provider-billable usage event when every represented
// counter has explicit presence. Invalid, client-visible, unknown-source, or
// otherwise unsafe events are omitted; the canonical event remains the source
// of any non-V1 detail and no value is inferred here.
func (b *UsageEvidenceBuffer) AddUsageEvent(event aipapi.Event, fallbackDedupeKey string) {
	if b == nil || event.Kind != aipapi.EventUsageDelta {
		return
	}
	evidence, ok := accountingEvidenceFromUsageEvent(event, fallbackDedupeKey)
	if !ok {
		return
	}
	fingerprint := usageEvidenceFingerprint(evidence)
	if fingerprint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return
	}
	if len(b.items) >= maxBufferedUsageEvidence {
		return
	}
	key := evidence.DedupeKey
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	// Replay suppression is consecutive per provider source. Keep the latest
	// payload across drains so A -> B -> A is retained as a correction, while
	// an immediately repeated A remains a no-op.
	if b.lastFingerprint[key] == fingerprint {
		return
	}
	b.rememberFingerprintLocked(key, fingerprint)
	b.items = append(b.items, evidence)
}

func (b *UsageEvidenceBuffer) rememberFingerprintLocked(key, fingerprint string) {
	if b == nil || key == "" || fingerprint == "" {
		return
	}
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	if _, exists := b.lastFingerprint[key]; !exists {
		if len(b.lastFingerprint) >= maxBufferedUsageEvidence && len(b.fingerprintOrder) > 0 {
			oldest := b.fingerprintOrder[0]
			b.fingerprintOrder = b.fingerprintOrder[1:]
			delete(b.lastFingerprint, oldest)
		}
		b.fingerprintOrder = append(b.fingerprintOrder, key)
	}
	b.lastFingerprint[key] = fingerprint
}

// DrainAccountingEvidence returns immutable copies of pending V1 records.
func (b *UsageEvidenceBuffer) DrainAccountingEvidence() []AccountingEvidence {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil
	}
	out := make([]AccountingEvidence, len(b.items))
	for i := range b.items {
		out[i] = cloneUsageEvidence(b.items[i])
	}
	b.items = nil
	return out
}

func accountingEvidenceFromUsageEvent(event aipapi.Event, fallbackDedupeKey string) (AccountingEvidence, bool) {
	presence := event.UsagePresence
	if !presence.Any() {
		return AccountingEvidence{}, false
	}
	if event.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		return AccountingEvidence{}, false
	}
	source, ok := accountingSourceFromUsageSource(event.Accounting.Source)
	if !ok {
		return AccountingEvidence{}, false
	}
	authority, ok := accountingAuthorityFromUsageAuthority(event.Accounting.Authority)
	if !ok {
		return AccountingEvidence{}, false
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	if key == "" {
		key = strings.TrimSpace(fallbackDedupeKey)
	}
	if key == "" || len(key) > int(DefaultMaxAccountingDedupeKeyBytes) {
		return AccountingEvidence{}, false
	}
	evidence := AccountingEvidence{
		InputTokens:                  usageCounter(event.InputTokens, presence.InputTokens),
		OutputTokens:                 usageCounter(event.OutputTokens, presence.OutputTokens),
		CacheReadTokens:              usageCounter(event.CacheReadTokens, presence.CacheReadTokens),
		CacheWriteTokens:             usageCounter(event.CacheWriteTokens, presence.CacheWriteTokens),
		ReasoningTokens:              usageCounter(event.ReasoningTokens, presence.ReasoningTokens),
		TotalTokens:                  usageCounter(event.TotalTokens, presence.TotalTokens),
		Presence:                     presence,
		Source:                       source,
		Authority:                    authority,
		Plane:                        AccountingPlaneProviderBillable,
		DedupeKey:                    key,
		InputTokensExcludeCacheRead:  event.Accounting.InputTokensExcludeCacheRead,
		InputTokensExcludeCacheWrite: event.Accounting.InputTokensExcludeCacheWrite,
	}
	if err := ValidateAccountingEvidence(evidence); err != nil {
		return AccountingEvidence{}, false
	}
	return evidence, true
}

func usageCounter(value int, present bool) *int64 {
	if !present || value < 0 {
		return nil
	}
	converted := int64(value)
	return &converted
}

func accountingSourceFromUsageSource(source aipapi.UsageSource) (AccountingSource, bool) {
	switch source {
	case aipapi.UsageSourceProviderReported:
		return AccountingSourceProviderReported, true
	case aipapi.UsageSourceProviderCountAPI:
		return AccountingSourceProviderCountAPI, true
	case aipapi.UsageSourceLocalEstimator:
		return AccountingSourceLocalEstimator, true
	case aipapi.UsageSourceLocalTokenizer:
		return AccountingSourceLocalTokenizer, true
	default:
		return AccountingSourceUnknown, false
	}
}

func accountingAuthorityFromUsageAuthority(authority aipapi.UsageAuthority) (AccountingAuthority, bool) {
	switch authority {
	case aipapi.UsageAuthorityAuthoritative:
		return AccountingAuthorityAuthoritative, true
	case aipapi.UsageAuthorityEstimated:
		return AccountingAuthorityEstimated, true
	case aipapi.UsageAuthorityDelegated:
		return AccountingAuthorityDelegated, true
	case aipapi.UsageAuthorityAdvisory:
		return AccountingAuthorityAdvisory, true
	default:
		return AccountingAuthorityUnknown, false
	}
}

func usageEvidenceFingerprint(evidence AccountingEvidence) string {
	data, err := json.Marshal(struct {
		InputTokens                  *int64
		OutputTokens                 *int64
		CacheReadTokens              *int64
		CacheWriteTokens             *int64
		ReasoningTokens              *int64
		TotalTokens                  *int64
		Presence                     UsagePresence
		Source                       AccountingSource
		Authority                    AccountingAuthority
		Plane                        AccountingPlane
		DedupeKey                    string
		InputTokensExcludeCacheRead  bool
		InputTokensExcludeCacheWrite bool
	}{
		evidence.InputTokens, evidence.OutputTokens, evidence.CacheReadTokens,
		evidence.CacheWriteTokens, evidence.ReasoningTokens, evidence.TotalTokens,
		evidence.Presence, evidence.Source, evidence.Authority, evidence.Plane,
		evidence.DedupeKey, evidence.InputTokensExcludeCacheRead,
		evidence.InputTokensExcludeCacheWrite,
	})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func cloneUsageEvidence(in AccountingEvidence) AccountingEvidence {
	out := in
	out.InputTokens = cloneInt64(in.InputTokens)
	out.OutputTokens = cloneInt64(in.OutputTokens)
	out.CacheReadTokens = cloneInt64(in.CacheReadTokens)
	out.CacheWriteTokens = cloneInt64(in.CacheWriteTokens)
	out.ReasoningTokens = cloneInt64(in.ReasoningTokens)
	out.TotalTokens = cloneInt64(in.TotalTokens)
	return out
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
