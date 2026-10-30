package service

// Accounting evidence for one completed provider stream.
//
// # One record per provider charge
//
// A completed Responses stream is one provider charge. The connector emits that
// charge ONCE through the negotiated V1 accounting sideband, when the stream
// reports completion: the record carries the provider-reported token counts of
// the WHOLE stream and a dedupe key that identifies the charge itself.
//
// The V1 contract defines DedupeKey as the identity of ONE provider charge. The
// local profile alone is not that identity: every charge of one subscription
// would share it, so two attempts would merge into one charge while a
// redelivered response could not be recognised as the same one. The key
// therefore combines
//
//   - the non-secret local profile id that authorized the attempt (requirement
//     8.8), and
//   - the provider correlation identity of the answer (the 5.2 transport
//     envelope), which is per-response and therefore unchanged when the same
//     response is delivered again.
//
// The identity comes from the transport's FULL correlation value, before the
// diagnostic display bound is applied: a bounded display string would merge two
// answers that differ only after the bound, and a charge identity cannot be
// recovered once that distinction is lost.
//
// The identity is namespaced by its source, so a provider identity, an attempt
// fallback and a bounded commitment are three pairwise disjoint encodings: no
// provider id - however it is spelled - can collide with another charge's
// committed or fallback form.
//
// A provider that reports no request id leaves the host attempt id as the only
// per-charge identity; it keeps distinct attempts distinct and is never a
// credential. That fallback is a real limitation of correlation, so it is
// recorded for the operator when the record is emitted, not just commented.
//
// Nothing here prices the subscription. The appendix-A subscription is
// account-funded rather than charged per response, so the evidence carries
// provider-reported token COUNTS only: no price, rating, currency or other
// money notion is manufactured for it.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// usageDedupeKeyPrefix names the accounting evidence source. The identity of
// the one provider charge follows it.
const usageDedupeKeyPrefix = "openai-chatgpt-plan.responses:"

const (
	// chargeProviderNamespace marks a provider correlation identity.
	chargeProviderNamespace = "req:"
	// chargeCommitmentNamespace marks a bounded commitment to a charge identity
	// that does not fit the accounting key bound.
	chargeCommitmentNamespace = "hash:"
	// chargeAttemptNamespace marks the host-attempt fallback identity.
	chargeAttemptNamespace = "attempt:"

	// identitySourceProvider records that the charge identity came from the
	// provider's own correlation id.
	identitySourceProvider = "provider"
	// identitySourceAttempt records that the provider reported no correlation
	// id, so the host attempt identity stands in.
	identitySourceAttempt = "attempt"

	// responseStatusCompleted is the ONE terminal status this connector charges.
	responseStatusCompleted = "completed"
)

// usageTotals is the accumulated provider-reported usage of one stream.
//
// Every usage delta of the stream is collected, and the stream's ONE evidence
// record is built from the total when the stream completes. aipapi usage deltas
// are additive counters (the canonical aipapi.Collected.AccumulateUsage
// contract), except the reported total, which is a running total and keeps its
// last non-zero value instead of being summed.
type usageTotals struct {
	input      int
	output     int
	cacheRead  int
	cacheWrite int
	reasoning  int
	total      int
	presence   aipapi.UsagePresence
}

// add accumulates one provider-reported usage delta.
//
// Presence is unioned, never inferred: a counter the provider did not report
// stays absent in the aggregate instead of becoming a fabricated zero.
func (t *usageTotals) add(event aipapi.Event) {
	t.input += event.InputTokens
	t.output += event.OutputTokens
	t.cacheRead += event.CacheReadTokens
	t.cacheWrite += event.CacheWriteTokens
	t.reasoning += event.ReasoningTokens
	if event.TotalTokens != 0 {
		t.total = event.TotalTokens
	}
	t.presence = t.presence.Union(event.UsagePresence)
}

// event returns the one usage event carrying every collected delta.
func (t usageTotals) event() (aipapi.Event, bool) {
	if !t.presence.Any() {
		return aipapi.Event{}, false
	}
	return aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      t.input,
		OutputTokens:     t.output,
		CacheReadTokens:  t.cacheRead,
		CacheWriteTokens: t.cacheWrite,
		ReasoningTokens:  t.reasoning,
		TotalTokens:      t.total,
		UsagePresence:    t.presence,
	}, true
}

// chargeIdentity names the provider charge one completed stream represents.
//
// The provider correlation identity is the per-response identity: a redelivery
// of the same response keeps it, and a distinct response never shares it. Only
// when the provider reported none does the host attempt id stand in, because a
// delivery that cannot be correlated with a provider response cannot be proven
// to be a redelivery. The ABI requires a non-empty AttemptID for a valid
// invocation, so the fallback is never empty in a real session.
//
// The identity is NAMESPACED by its source, so the encodings cannot alias each
// other: a provider id that literally spells an attempt fallback stays a
// provider identity. The returned source names that provenance for the
// operator; the caller must not derive it again from the string.
func chargeIdentity(requestIdentity, attemptID string) (identity, source string) {
	if identity := strings.TrimSpace(requestIdentity); identity != "" {
		return chargeProviderNamespace + identity, identitySourceProvider
	}
	return chargeAttemptNamespace + strings.TrimSpace(attemptID), identitySourceAttempt
}

// usageDedupeKey names one provider charge and the local profile that
// authorized it.
//
// The namespaced identity is embedded verbatim while it fits the accounting
// contract's key bound. An over-bound charge is committed to with SHA-256 under
// its OWN namespace instead of being truncated or dropped: truncation could
// merge two distinct charges, and an over-bound key is silently discarded by the
// sideband, which would hide the provider's usage. The commitment is over the
// NAMESPACED segment, so a digest of a provider identity can never equal a
// digest of an attempt fallback. Verbatim and committed forms are disjoint
// prefixes, so no provider id - however it is spelled - can occupy the same
// key as another charge's committed form.
//
// The profile id is a validated SIWC local id (at most 64 lowercase ASCII
// characters), so the committed form always fits.
func usageDedupeKey(profileID, charge string) string {
	prefix := usageDedupeKeyPrefix + profileID + ":"
	if room := int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) - len(prefix); len(charge) <= room {
		return prefix + charge
	}
	sum := sha256.Sum256([]byte(charge))
	return prefix + chargeCommitmentNamespace + hex.EncodeToString(sum[:])
}
