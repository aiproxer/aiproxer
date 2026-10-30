package service

// Accounting evidence for one completed provider stream.
//
// The V1 sideband contract (backendplugin.AccountingEvidence) says DedupeKey
// identifies ONE provider charge. These tests apply the connector's records
// through that contract and prove its two directions:
//
//   - two completed provider responses are two charges and must not merge, and
//   - a redelivered provider response is one charge and must not double-count.
//
// Nothing here prices the subscription: the records carry provider-reported
// token counts only, the provider correlation identity that names the charge
// (taken before the diagnostic display bound), and the non-secret local profile
// id that attributes it.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	// firstChargeRequestID is the provider request id of the first scripted
	// answer. It names one provider charge, not the account.
	firstChargeRequestID = "req_charge_a"
	// secondChargeRequestID is the provider request id of a distinct answer.
	secondChargeRequestID = "req_charge_b"
)

// completedAttempt runs one completed attempt and returns the single V1
// accounting record it emitted.
func completedAttempt(t *testing.T, inst *instance, attemptID, requestID string) backendplugin.AccountingEvidence {
	t.Helper()
	inv := invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{})
	inv.AttemptID = attemptID
	inv.RequestID = requestID
	frames, err := runExecute(context.Background(), inst, inv)
	if err != nil {
		t.Fatalf("Execute attempt %q: %v", attemptID, err)
	}
	records := accountingRecords(t, frames)
	if len(records) != 1 {
		t.Fatalf("one completed provider stream emitted %d accounting records, want exactly 1", len(records))
	}
	return records[0]
}

// accountingRecords returns every V1 record a completed stream sent.
func accountingRecords(t *testing.T, frames []backendplugin.ServerFrame) []backendplugin.AccountingEvidence {
	t.Helper()
	var records []backendplugin.AccountingEvidence
	for _, frame := range frames {
		if frame.Kind != backendplugin.ServerFrameAccountingEvidence {
			continue
		}
		if frame.Accounting == nil {
			t.Fatal("an accounting evidence frame carried no V1 record")
		}
		records = append(records, *frame.Accounting)
	}
	return records
}

// chargeLedger applies records the way the accounting contract does: the
// dedupe key identifies one provider charge, so an exact redelivery is
// idempotent and a key that carries two different payloads is a contract
// violation rather than a silent overwrite.
type chargeLedger struct {
	records map[string]backendplugin.AccountingEvidence
	order   []string
}

func newChargeLedger() *chargeLedger {
	return &chargeLedger{records: make(map[string]backendplugin.AccountingEvidence)}
}

func (l *chargeLedger) apply(t *testing.T, record backendplugin.AccountingEvidence) {
	t.Helper()
	if prior, ok := l.records[record.DedupeKey]; ok {
		if !sameUsageCounts(prior, record) {
			t.Fatalf("dedupe key %q carries two different provider charges; one key must identify one provider charge", record.DedupeKey)
		}
		return
	}
	l.records[record.DedupeKey] = record
	l.order = append(l.order, record.DedupeKey)
}

// charges is the number of distinct provider charges the contract admitted.
func (l *chargeLedger) charges() int { return len(l.order) }

// counters returns the provider-reported count vector of one admitted charge.
func (l *chargeLedger) counters(t *testing.T, key string) usageCounts {
	t.Helper()
	record, ok := l.records[key]
	if !ok {
		t.Fatalf("the ledger holds no charge under key %q", key)
	}
	return usageCountsOf(t, record)
}

// usageCounts is the provider-reported count vector under test.
type usageCounts struct{ Input, Output, Total int64 }

func usageCountsOf(t *testing.T, record backendplugin.AccountingEvidence) usageCounts {
	t.Helper()
	if record.InputTokens == nil || record.OutputTokens == nil || record.TotalTokens == nil {
		t.Fatalf("accounting record = %+v, want provider-reported input/output/total counts", record)
	}
	return usageCounts{Input: *record.InputTokens, Output: *record.OutputTokens, Total: *record.TotalTokens}
}

func sameUsageCounts(left, right backendplugin.AccountingEvidence) bool {
	return sameInt64(left.InputTokens, right.InputTokens) &&
		sameInt64(left.OutputTokens, right.OutputTokens) &&
		sameInt64(left.CacheReadTokens, right.CacheReadTokens) &&
		sameInt64(left.CacheWriteTokens, right.CacheWriteTokens) &&
		sameInt64(left.ReasoningTokens, right.ReasoningTokens) &&
		sameInt64(left.TotalTokens, right.TotalTokens)
}

func sameInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// assertChargeProvenance pins the non-secret provenance of one charge: the
// local profile and the provider request id, never credential material, all
// inside the accounting contract's key bound.
func assertChargeProvenance(t *testing.T, record backendplugin.AccountingEvidence, profileID, providerRequestID string) {
	t.Helper()
	if !strings.Contains(record.DedupeKey, profileID) {
		t.Fatalf("dedupe key %q must name the non-secret local profile %q", record.DedupeKey, profileID)
	}
	if !strings.Contains(record.DedupeKey, providerRequestID) {
		t.Fatalf("dedupe key %q must carry the provider request id %q that identifies the charge", record.DedupeKey, providerRequestID)
	}
	if strings.Contains(record.DedupeKey, referenceAccessToken) || strings.Contains(record.DedupeKey, referenceRefreshToken) {
		t.Fatal("the accounting evidence key must not carry credential material")
	}
	if len(record.DedupeKey) > int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) {
		t.Fatalf("dedupe key is %d bytes, over the accounting contract bound of %d", len(record.DedupeKey), backendplugin.DefaultMaxAccountingDedupeKeyBytes)
	}
	if record.Source != backendplugin.AccountingSourceProviderReported ||
		record.Authority != backendplugin.AccountingAuthorityAuthoritative ||
		record.Plane != backendplugin.AccountingPlaneProviderBillable {
		t.Fatalf("usage provenance = %s/%s/%s, want provider-reported/authoritative/provider-billable",
			record.Source, record.Authority, record.Plane)
	}
}

// TestAccountingEvidence_TwoCompletedStreamsAreTwoProviderCharges is the
// two-attempt contract proof: two completed provider responses are two charges,
// each identified by its own provider request id and attributed to the one
// local profile that authorized it. A key that names the profile instead of the
// charge would merge them.
func TestAccountingEvidence_TwoCompletedStreamsAreTwoProviderCharges(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	provider.requestIDs = []string{firstChargeRequestID, secondChargeRequestID}
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	first := completedAttempt(t, inst, "att-charge-a", "req-charge-a")
	second := completedAttempt(t, inst, "att-charge-b", "req-charge-b")

	if first.DedupeKey == second.DedupeKey {
		t.Fatalf("two provider responses share one charge identity %q; the key names the account, not the provider charge", first.DedupeKey)
	}

	ledger := newChargeLedger()
	ledger.apply(t, first)
	ledger.apply(t, second)
	if got := ledger.charges(); got != 2 {
		t.Fatalf("two completed provider responses were admitted as %d provider charge(s), want 2", got)
	}

	assertChargeProvenance(t, first, record.ID, firstChargeRequestID)
	assertChargeProvenance(t, second, record.ID, secondChargeRequestID)
	want := usageCounts{Input: 10, Output: 5, Total: 15}
	for _, key := range ledger.order {
		if got := ledger.counters(t, key); got != want {
			t.Fatalf("charge %q carries %+v, want the provider-reported %+v", key, got, want)
		}
	}
}

// TestAccountingEvidence_RedeliveredProviderResponseStaysOneCharge proves the
// other direction of the same contract: the SAME provider response delivered
// twice - same provider request id, same counts, different host attempt
// identities - is ONE charge. Keying by the delivery or the attempt instead of
// the provider response would double-count it.
func TestAccountingEvidence_RedeliveredProviderResponseStaysOneCharge(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	provider.requestIDs = []string{firstChargeRequestID, firstChargeRequestID}
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	original := completedAttempt(t, inst, "att-delivery-a", "req-delivery-a")
	redelivery := completedAttempt(t, inst, "att-delivery-b", "req-delivery-b")

	if original.DedupeKey != redelivery.DedupeKey {
		t.Fatalf("a redelivered provider response changed its charge identity (%q then %q); a delivery is not a charge",
			original.DedupeKey, redelivery.DedupeKey)
	}

	ledger := newChargeLedger()
	ledger.apply(t, original)
	ledger.apply(t, redelivery)
	if got := ledger.charges(); got != 1 {
		t.Fatalf("one redelivered provider response was admitted as %d provider charges, want 1", got)
	}

	assertChargeProvenance(t, original, record.ID, firstChargeRequestID)
	assertChargeProvenance(t, redelivery, record.ID, firstChargeRequestID)
	want := usageCounts{Input: 10, Output: 5, Total: 15}
	if got := ledger.counters(t, original.DedupeKey); got != want {
		t.Fatalf("the redelivered charge carries %+v, want the provider-reported %+v counted once", got, want)
	}
}

// TestAccountingEvidence_BoundsTheProviderChargeIdentity proves a provider
// request id that does not fit the accounting key bound still identifies its
// own charge: the record is emitted, stays within the bound, and two distinct
// long identities stay distinct. An over-bound key would be silently dropped by
// the sideband, which is a hidden loss of provider evidence.
func TestAccountingEvidence_BoundsTheProviderChargeIdentity(t *testing.T) {
	t.Parallel()
	firstIdentity := strings.Repeat("\U0001F600", 100) + "-a"
	secondIdentity := strings.Repeat("\U0001F600", 100) + "-b"
	provider := newPlanReference(t, completeScript)
	provider.requestIDs = []string{firstIdentity, secondIdentity}
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	first := completedAttempt(t, inst, "att-long-a", "req-long-a")
	second := completedAttempt(t, inst, "att-long-b", "req-long-b")

	for _, emitted := range []backendplugin.AccountingEvidence{first, second} {
		if !strings.Contains(emitted.DedupeKey, record.ID) {
			t.Fatalf("dedupe key %q must name the non-secret local profile %q", emitted.DedupeKey, record.ID)
		}
		if len(emitted.DedupeKey) > int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) {
			t.Fatalf("dedupe key is %d bytes, over the accounting contract bound of %d",
				len(emitted.DedupeKey), backendplugin.DefaultMaxAccountingDedupeKeyBytes)
		}
		if strings.Contains(emitted.DedupeKey, strings.Repeat("\U0001F600", 8)) {
			t.Fatalf("dedupe key %q embeds an unbounded provider identity", emitted.DedupeKey)
		}
	}
	if first.DedupeKey == second.DedupeKey {
		t.Fatalf("two distinct long provider identities share one charge identity %q", first.DedupeKey)
	}
}

// TestAccountingEvidence_FallsBackToTheAttemptWithoutAProviderIdentity covers
// the provider that omits its request id: the HOST attempt identity is the only
// per-charge identity left, so two actual attempts must still stay two charges
// and the local profile must remain the attribution. The host adapter/executor
// proof that those IDs are distinct per attempt lives in the backend-plugin
// bridge tests; this proof consumes two distinct host identities.
func TestAccountingEvidence_FallsBackToTheAttemptWithoutAProviderIdentity(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	provider.omitRequestID = true
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	first := completedAttempt(t, inst, "att-fallback-a", "req-fallback-a")
	second := completedAttempt(t, inst, "att-fallback-b", "req-fallback-b")

	if first.DedupeKey == second.DedupeKey {
		t.Fatalf("two attempts without a provider request id share one charge identity %q; distinct charges must not merge",
			first.DedupeKey)
	}
	for i, emitted := range []backendplugin.AccountingEvidence{first, second} {
		if !strings.Contains(emitted.DedupeKey, record.ID) {
			t.Fatalf("dedupe key %q must name the non-secret local profile %q", emitted.DedupeKey, record.ID)
		}
		if strings.Contains(emitted.DedupeKey, referenceAccessToken) || strings.Contains(emitted.DedupeKey, referenceRefreshToken) {
			t.Fatalf("record %d carries credential material in its key", i)
		}
		if len(emitted.DedupeKey) > int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) {
			t.Fatalf("dedupe key is %d bytes, over the accounting contract bound of %d",
				len(emitted.DedupeKey), backendplugin.DefaultMaxAccountingDedupeKeyBytes)
		}
	}
}

// TestManagedStream_AggregatesEveryUsageDeltaIntoOneRecord is the COLLECT
// proof at the emission boundary: a completed stream emits ONE record built
// from EVERY usage delta it carried, at completion. Sampling the first delta, or
// emitting per delta, would lose or duplicate provider-reported counts.
func TestManagedStream_AggregatesEveryUsageDeltaIntoOneRecord(t *testing.T) {
	t.Parallel()
	stream := &managedStream{
		usage:          backendplugin.NewUsageEvidenceBuffer(),
		dedupeKey:      usageTestKey,
		identitySource: identitySourceProvider,
	}

	stream.captureUsage(context.Background(), aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  4,
		OutputTokens: 2,
		UsagePresence: aipapi.UsagePresence{
			InputTokens:  true,
			OutputTokens: true,
		},
	})
	if got := stream.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("usage before the completed terminal emitted %d record(s); the record belongs to the COMPLETE stream, not to the first usage delta", len(got))
	}

	stream.captureUsage(context.Background(), aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  6,
		OutputTokens: 3,
		TotalTokens:  15,
		UsagePresence: aipapi.UsagePresence{
			InputTokens:  true,
			OutputTokens: true,
			TotalTokens:  true,
		},
	})
	stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: responseStatusCompleted})

	records := stream.DrainAccountingEvidence()
	if len(records) != 1 {
		t.Fatalf("one completed stream emitted %d accounting records, want exactly 1", len(records))
	}
	if got, want := usageCountsOf(t, records[0]), (usageCounts{Input: 10, Output: 5, Total: 15}); got != want {
		t.Fatalf("aggregated usage = %+v, want every usage delta summed to %+v", got, want)
	}
	if !records[0].Presence.InputTokens || !records[0].Presence.OutputTokens || !records[0].Presence.TotalTokens {
		t.Fatalf("aggregated presence = %+v, want every reported counter present", records[0].Presence)
	}
	if again := stream.DrainAccountingEvidence(); len(again) != 0 {
		t.Fatalf("the completed stream emitted %d record(s) on a second drain, want none", len(again))
	}

	// A repeated completion cannot turn a late delta into a second charge: the
	// record was emitted once, at the completed terminal.
	stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: responseStatusCompleted})
	stream.captureUsage(context.Background(), aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   1,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
	})
	stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: responseStatusCompleted})
	if late := stream.DrainAccountingEvidence(); len(late) != 0 {
		t.Fatalf("a repeated completion emitted %d extra accounting record(s); one completed stream carries one record", len(late))
	}
}

// usageTestKey is a test-only accounting key; the aggregation proof is about
// the payload, not about key composition.
const usageTestKey = "openai-chatgpt-plan.responses:acct-test:" + firstChargeRequestID

// chargeSegment returns the charge-identity segment of one emitted key: the
// part after the connector prefix and the local profile id.
func chargeSegment(t *testing.T, key, profileID string) string {
	t.Helper()
	prefix := usageDedupeKeyPrefix + profileID + ":"
	if !strings.HasPrefix(key, prefix) {
		t.Fatalf("dedupe key %q does not start with the connector/profile prefix %q", key, prefix)
	}
	segment := strings.TrimPrefix(key, prefix)
	if segment == "" {
		t.Fatalf("dedupe key %q carries no charge identity", key)
	}
	return segment
}

// TestAccountingEvidence_SamePrefixCorrelationIDsAreDistinctCharges is the J1
// regression: the diagnostic request id is deliberately truncated for display,
// so two provider headers that share the first 160 characters must still be two
// charges. An accounting key built from the DISPLAY string would collapse them
// before any commitment could see the distinguishing suffix.
func TestAccountingEvidence_SamePrefixCorrelationIDsAreDistinctCharges(t *testing.T) {
	t.Parallel()
	shared := strings.Repeat("x", 160)
	provider := newPlanReference(t, completeScript)
	provider.requestIDs = []string{shared + "a", shared + "b"}
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	first := completedAttempt(t, inst, "att-prefix-a", "req-prefix-a")
	second := completedAttempt(t, inst, "att-prefix-b", "req-prefix-b")

	for _, emitted := range []backendplugin.AccountingEvidence{first, second} {
		if len(emitted.DedupeKey) > int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) {
			t.Fatalf("dedupe key is %d bytes, over the accounting contract bound of %d",
				len(emitted.DedupeKey), backendplugin.DefaultMaxAccountingDedupeKeyBytes)
		}
	}
	if first.DedupeKey == second.DedupeKey {
		t.Fatalf("two provider headers that differ only after the display bound share one charge identity %q", first.DedupeKey)
	}
	if got := chargeSegment(t, first.DedupeKey, record.ID); got == chargeSegment(t, second.DedupeKey, record.ID) {
		t.Fatalf("the two charges carry the same identity segment %q", got)
	}

	ledger := newChargeLedger()
	ledger.apply(t, first)
	ledger.apply(t, second)
	if got := ledger.charges(); got != 2 {
		t.Fatalf("two distinct provider responses were admitted as %d provider charge(s), want 2", got)
	}
}

// TestAccountingEvidence_CommittedIdentityCannotAliasAProviderIdentity is the
// first J2 regression: a provider id that literally repeats the identity the
// connector COMMITTED for a different, over-bound id must not inherit that
// charge. The competing encodings are the demonstration, so the literal is read
// back from the emitted key instead of being hard-coded.
func TestAccountingEvidence_CommittedIdentityCannotAliasAProviderIdentity(t *testing.T) {
	t.Parallel()
	longIdentity := strings.Repeat("\U0001F600", 100) + "-committed"
	provider := newPlanReference(t, completeScript)
	provider.requestIDs = []string{longIdentity}
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	committed := completedAttempt(t, inst, "att-commit-a", "req-commit-a")
	segment := chargeSegment(t, committed.DedupeKey, record.ID)
	if strings.Contains(segment, longIdentity) {
		t.Fatalf("the over-bound charge identity %q was embedded verbatim, so this regression proves nothing", segment)
	}
	if len(segment) >= len(longIdentity) {
		t.Fatalf("the committed segment %q is not bounded", segment)
	}

	// The second answer's provider id is EXACTLY the committed segment. It is a
	// different provider identity and therefore a different charge.
	provider.requestIDs = []string{longIdentity, segment}
	impostor := completedAttempt(t, inst, "att-commit-b", "req-commit-b")

	if committed.DedupeKey == impostor.DedupeKey {
		t.Fatalf("a provider id equal to the committed identity %q inherited the committed charge's key %q", segment, committed.DedupeKey)
	}
	if len(impostor.DedupeKey) > int(backendplugin.DefaultMaxAccountingDedupeKeyBytes) {
		t.Fatalf("dedupe key is %d bytes, over the accounting contract bound of %d",
			len(impostor.DedupeKey), backendplugin.DefaultMaxAccountingDedupeKeyBytes)
	}
}

// TestAccountingEvidence_ProviderIdentityCannotAliasTheAttemptFallback is the
// second J2 regression: a provider id that literally spells the fallback
// encoding must not collide with the unrelated attempt that has no provider
// identity at all. Both are ordinary ASCII identities.
func TestAccountingEvidence_ProviderIdentityCannotAliasTheAttemptFallback(t *testing.T) {
	t.Parallel()
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)

	correlatedProvider := newPlanReference(t, completeScript)
	correlatedProvider.requestIDs = []string{"attempt:att-fallback-b"}
	correlatedInst := configureForExecution(t, correlatedProvider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})
	correlated := completedAttempt(t, correlatedInst, "att-correlated", "req-correlated")

	fallbackProvider := newPlanReference(t, completeScript)
	fallbackProvider.omitRequestID = true
	fallbackInst := configureForExecution(t, fallbackProvider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})
	fallback := completedAttempt(t, fallbackInst, "att-fallback-b", "req-fallback-b")

	if correlated.DedupeKey == fallback.DedupeKey {
		t.Fatalf("a provider id spelling the attempt fallback inherited the no-identity attempt's key %q", correlated.DedupeKey)
	}
	assertChargeProvenance(t, fallback, record.ID, "att-fallback-b")
}

// TestManagedStream_NonCompletedFinishEmitsNoEvidence pins the completed-only
// emission rule: the connector charges one COMPLETED stream. A finish shape that
// is not the completed terminal - an incomplete answer, or a producer that left
// the status unset - must not emit provider usage.
func TestManagedStream_NonCompletedFinishEmitsNoEvidence(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"incomplete", "failed", ""} {
		t.Run("status="+status, func(t *testing.T) {
			t.Parallel()
			stream := &managedStream{
				usage:          backendplugin.NewUsageEvidenceBuffer(),
				dedupeKey:      usageTestKey,
				identitySource: identitySourceProvider,
			}
			stream.captureUsage(context.Background(), aipapi.Event{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   4,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
			})
			stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: status})
			if got := stream.DrainAccountingEvidence(); len(got) != 0 {
				t.Fatalf("a response_finished with status %q emitted %d accounting record(s); only the completed terminal is a provider charge", status, len(got))
			}
		})
	}
}

// TestManagedStream_AccumulatesPartialAndAbsentUsage retains the partial and
// absent counter shapes: an empty usage delta is not usage, explicit zeroes stay
// present and distinct from absence, and a stream that reported no usage emits
// no record.
func TestManagedStream_AccumulatesPartialAndAbsentUsage(t *testing.T) {
	t.Parallel()

	t.Run("no usage at all emits no record", func(t *testing.T) {
		t.Parallel()
		stream := &managedStream{
			usage:          backendplugin.NewUsageEvidenceBuffer(),
			dedupeKey:      usageTestKey,
			identitySource: identitySourceProvider,
		}
		// A usage event with no present counter is not provider usage.
		stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventUsageDelta})
		stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: responseStatusCompleted})
		if got := stream.DrainAccountingEvidence(); len(got) != 0 {
			t.Fatalf("a stream without provider usage emitted %d record(s), want none", len(got))
		}
	})

	t.Run("partial deltas accumulate with explicit presence", func(t *testing.T) {
		t.Parallel()
		stream := &managedStream{
			usage:          backendplugin.NewUsageEvidenceBuffer(),
			dedupeKey:      usageTestKey,
			identitySource: identitySourceProvider,
		}
		// First delta: input only. Total, reasoning and cache counters are absent.
		stream.captureUsage(context.Background(), aipapi.Event{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   4,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
		})
		// Second delta: cache read plus an EXPLICIT zero output. Zero is a
		// reported value, not an absence.
		stream.captureUsage(context.Background(), aipapi.Event{
			Kind:            aipapi.EventUsageDelta,
			CacheReadTokens: 2,
			OutputTokens:    0,
			UsagePresence: aipapi.UsagePresence{
				CacheReadTokens: true,
				OutputTokens:    true,
			},
		})
		stream.captureUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: responseStatusCompleted})

		records := stream.DrainAccountingEvidence()
		if len(records) != 1 {
			t.Fatalf("one completed stream emitted %d accounting records, want exactly 1", len(records))
		}
		record := records[0]
		requireCounter(t, "input", record.InputTokens, 4, true)
		requireCounter(t, "output", record.OutputTokens, 0, true)
		requireCounter(t, "cache_read", record.CacheReadTokens, 2, true)
		// Counters the provider never reported stay ABSENT instead of becoming
		// a fabricated zero.
		requireCounter(t, "cache_write", record.CacheWriteTokens, 0, false)
		requireCounter(t, "reasoning", record.ReasoningTokens, 0, false)
		requireCounter(t, "total", record.TotalTokens, 0, false)
		if record.Presence.CacheWriteTokens || record.Presence.ReasoningTokens || record.Presence.TotalTokens {
			t.Fatalf("aggregated presence = %+v, want the unreported counters absent", record.Presence)
		}
	})
}

// requireCounter asserts one accounting counter's presence and value.
func requireCounter(t *testing.T, name string, counter *int64, want int64, present bool) {
	t.Helper()
	if !present {
		if counter != nil {
			t.Fatalf("%s = %d, want absent", name, *counter)
		}
		return
	}
	if counter == nil {
		t.Fatalf("%s is absent, want the provider-reported %d", name, want)
	}
	if *counter != want {
		t.Fatalf("%s = %d, want the provider-reported %d", name, *counter, want)
	}
}

// TestExecute_RecordsTheIdentitySourceOfTheCharge is the S2 proof: the attempt
// fallback is a real limitation of correlation, so it is recorded for the
// operator instead of living only in a comment. The marker is non-secret and
// stays out of the dedupe key.
//
// The test is deliberately NOT parallel: it installs a process logger.
func TestExecute_RecordsTheIdentitySourceOfTheCharge(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)

	fallbackProvider := newPlanReference(t, completeScript)
	fallbackProvider.omitRequestID = true
	fallbackInst := configureForExecution(t, fallbackProvider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})
	fallback := completedAttempt(t, fallbackInst, "att-log-fallback", "req-log-fallback")
	if !strings.Contains(fallback.DedupeKey, chargeAttemptNamespace) {
		t.Fatalf("the fallback key %q must keep the attempt encoding", fallback.DedupeKey)
	}

	fallbackLogs := logs.String()
	if !strings.Contains(fallbackLogs, `"identity_source":"attempt"`) {
		t.Fatalf("the attempt fallback was not recorded for the operator; log=%s", fallbackLogs)
	}
	if strings.Contains(fallbackLogs, referenceAccessToken) || strings.Contains(fallbackLogs, referenceRefreshToken) {
		t.Fatal("the accounting diagnostic must not carry credential material")
	}

	logs.Reset()
	correlatedProvider := newPlanReference(t, completeScript)
	correlatedInst := configureForExecution(t, correlatedProvider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})
	completedAttempt(t, correlatedInst, "att-log-provider", "req-log-provider")
	correlatedLogs := logs.String()
	if !strings.Contains(correlatedLogs, `"identity_source":"provider"`) {
		t.Fatalf("a correlated charge did not record its provider identity source; log=%s", correlatedLogs)
	}
	if !strings.Contains(correlatedLogs, `"profile_id":"`+record.ID+`"`) {
		t.Fatalf("the accounting diagnostic must attribute the record to the authorized profile; log=%s", correlatedLogs)
	}
}
