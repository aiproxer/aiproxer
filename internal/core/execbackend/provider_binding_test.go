package execbackend

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func hostBindingTestTime() time.Time {
	return time.Unix(1700000000, 0).UTC()
}

// hostBindingTrusted returns the trusted host facts of one attempt.
func hostBindingTrusted() ProviderBindingIdentity {
	return ProviderBindingIdentity{
		StoreID:               "store-1",
		TenantID:              "tenant-1",
		RequestID:             "request-1",
		CallID:                "call-1",
		BillingCallID:         "call-1",
		ALegID:                "aleg-1",
		BLegID:                "bleg-1",
		AttemptID:             "bleg-1",
		AttemptSeq:            3,
		ConnectorLegPseudonym: "bleg-pseudonym",
		AccountID:             "acct-host",
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectService,
			PrincipalID: scope.Known("principal-1"),
		},
		ScopePresent: true,
		ReceivedAt:   hostBindingTestTime(),
	}
}

// hostBindingClaim builds one connector claim exactly as a connector would
// send it: a placeholder store, the transport B-leg pseudonym, a connector-local
// observation id and the connector's own source event key.
func hostBindingClaim(t *testing.T, localID, sourceEventKey string) metering.Observation {
	t.Helper()
	now := hostBindingTestTime()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "provider.media.v2",
	}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	observation := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             localID,
		SourceEventKey: sourceEventKey,
		Revision:       1,
		StreamID:       "connector-stream",
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:    metering.SubjectBLeg,
			StoreID: "connector-placeholder-store",
			BLegID:  "bleg-pseudonym",
		},
		Correlation: metering.CorrelationV2{
			StoreID:            "connector-placeholder-store",
			BLegID:             "bleg-pseudonym",
			ProviderRequestID:  "provider-request-1",
			ProviderAccountKey: "provider-account-1",
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "provider-schema-v2",
		Measures:   []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("connector claim is not a valid observation: %v", err)
	}
	return observation
}

func TestSourceReferenceMap_BoundObservationUsesHostNamespaceAndOverwritesHostFacts(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	refs := NewSourceReferenceMap(0)
	outcome := refs.Bind(claim, trusted)
	if outcome.Reason != "" {
		t.Fatalf("bind reason = %q, want admitted", outcome.Reason)
	}
	bound := outcome.Bound.Observation
	if !strings.HasPrefix(bound.ID, HostBoundObservationIDPrefix) {
		t.Fatalf("bound id = %q, want the host namespace prefix", bound.ID)
	}
	if bound.Subject.StoreID != trusted.StoreID || bound.Correlation.StoreID != trusted.StoreID {
		t.Fatalf("connector store survived the host stamp: %+v", bound.Subject)
	}
	if bound.Subject.AccountID != trusted.AccountID {
		t.Fatalf("account = %q, want the frozen account %q", bound.Subject.AccountID, trusted.AccountID)
	}
	if bound.Subject.Kind != metering.SubjectBLeg {
		t.Fatalf("subject kind = %q, want b_leg", bound.Subject.Kind)
	}
	if bound.Subject.BLegID != trusted.BLegID {
		t.Fatalf("b_leg = %q, want the sealed b_leg %q", bound.Subject.BLegID, trusted.BLegID)
	}
	if bound.SourceEventKey != claim.SourceEventKey || bound.StreamID != claim.StreamID {
		t.Fatalf("connector-owned identity was not preserved: %+v", bound)
	}
	if bound.Correlation.ProviderRequestID != "provider-request-1" {
		t.Fatalf("provider-owned identity was not preserved: %+v", bound.Correlation)
	}
	if bound.ReceivedAt != trusted.ReceivedAt {
		t.Fatalf("received_at = %v, want the host receipt %v", bound.ReceivedAt, trusted.ReceivedAt)
	}
}

func TestSourceReferenceMap_HostOverwrittenDimensionsDoNotChangeIdentity(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	first := hostBindingClaim(t, "local-1", "source-event-1")
	second := hostBindingClaim(t, "local-1", "source-event-1")
	// A connector that claims a different store, tenant or account gets the
	// same host identity: the host overwrites them.
	second.Subject.StoreID = "other-placeholder-store"
	second.Correlation.StoreID = "other-placeholder-store"
	second.Subject.TenantID = "other-tenant"
	second.Subject.AccountID = "other-account"

	refs := NewSourceReferenceMap(0)
	firstOutcome := refs.Bind(first, trusted)
	secondOutcome := refs.Bind(second, trusted)
	if firstOutcome.Reason != "" || secondOutcome.Reason != "" {
		t.Fatalf("bind reasons = %q/%q, want admitted", firstOutcome.Reason, secondOutcome.Reason)
	}
	if firstOutcome.Bound.HostObservationID != secondOutcome.Bound.HostObservationID {
		t.Fatalf("host overwritten dimensions changed the host id: %q vs %q",
			firstOutcome.Bound.HostObservationID, secondOutcome.Bound.HostObservationID)
	}
}

func TestSourceReferenceMap_PreservedProviderDimensionChangesIdentity(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	first := hostBindingClaim(t, "local-1", "source-event-1")
	second := hostBindingClaim(t, "local-2", "source-event-2")
	second.Correlation.ProviderRequestID = "provider-request-2"
	second.Subject.ProviderRequestID = "provider-request-2"

	refs := NewSourceReferenceMap(0)
	firstOutcome := refs.Bind(first, trusted)
	secondOutcome := refs.Bind(second, trusted)
	if firstOutcome.Reason != "" || secondOutcome.Reason != "" {
		t.Fatalf("bind reasons = %q/%q, want admitted", firstOutcome.Reason, secondOutcome.Reason)
	}
	if firstOutcome.Bound.HostObservationID == secondOutcome.Bound.HostObservationID {
		t.Fatal("a preserved provider-owned dimension did not change the host id")
	}
}

func TestSourceReferenceMap_AbsentScopeDiffersFromPresentAllUnknownScope(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	absent := trusted
	absent.Scope = scope.PrincipalScopeView{}
	absent.ScopePresent = false
	present := trusted
	present.Scope = scope.PrincipalScopeView{}
	present.ScopePresent = true

	claim := hostBindingClaim(t, "local-1", "source-event-1")
	absentRefs := NewSourceReferenceMap(0)
	presentRefs := NewSourceReferenceMap(0)
	absentOutcome := absentRefs.Bind(claim, absent)
	presentOutcome := presentRefs.Bind(claim, present)
	if absentOutcome.Reason != "" || presentOutcome.Reason != "" {
		t.Fatalf("bind reasons = %q/%q, want admitted", absentOutcome.Reason, presentOutcome.Reason)
	}
	if absentOutcome.Bound.HostObservationID == presentOutcome.Bound.HostObservationID {
		t.Fatal("an absent scope and a present all-unknown scope produced the same host id")
	}
}

func TestSourceReferenceMap_ReplayReusesFirstBoundObservation(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	refs := NewSourceReferenceMap(0)
	first := refs.Bind(claim, trusted)
	if first.Reason != "" {
		t.Fatalf("first bind reason = %q", first.Reason)
	}
	// A later delivery of the same source event is an exact replay: the first
	// bound observation and its first receipt are reused.
	replayTrusted := trusted
	replayTrusted.ReceivedAt = trusted.ReceivedAt.Add(time.Hour)
	replay := refs.Bind(claim, replayTrusted)
	if replay.Reason != "" {
		t.Fatalf("replay reason = %q, want admitted", replay.Reason)
	}
	if !replay.Replay {
		t.Fatal("the second delivery was not reported as a replay")
	}
	if replay.Bound.Observation.ReceivedAt != first.Bound.Observation.ReceivedAt {
		t.Fatalf("replay receipt = %v, want the first receipt %v",
			replay.Bound.Observation.ReceivedAt, first.Bound.Observation.ReceivedAt)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want one", refs.Len())
	}
}

func TestSourceReferenceMap_AbsentSequenceReceivesStableOrdinalAcrossReplay(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	claim.Sequence = 0
	refs := NewSourceReferenceMap(0)
	first := refs.Bind(claim, trusted)
	if first.Reason != "" {
		t.Fatalf("first bind reason = %q", first.Reason)
	}
	if first.Bound.Observation.Sequence == 0 {
		t.Fatal("a claim without a sequence did not receive a host receipt ordinal")
	}
	replay := refs.Bind(claim, trusted)
	if replay.Bound.Observation.Sequence != first.Bound.Observation.Sequence {
		t.Fatalf("replay ordinal = %d, want the first ordinal %d",
			replay.Bound.Observation.Sequence, first.Bound.Observation.Sequence)
	}
}

func TestSourceReferenceMap_SuppliedSequenceAndRevisionArePreserved(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	claim.Sequence = 77
	claim.Revision = 5
	refs := NewSourceReferenceMap(0)
	outcome := refs.Bind(claim, trusted)
	if outcome.Reason != "" {
		t.Fatalf("bind reason = %q", outcome.Reason)
	}
	if outcome.Bound.Observation.Sequence != 77 {
		t.Fatalf("sequence = %d, want the supplied 77", outcome.Bound.Observation.Sequence)
	}
	if outcome.Bound.Observation.Revision != 5 {
		t.Fatalf("revision = %d, want the supplied 5", outcome.Bound.Observation.Revision)
	}
}

func TestSourceReferenceMap_PayloadConflictPreservesFirstBinding(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	changed := claim.Clone()
	changed.Measures = append([]metering.Measure(nil), claim.Measures...)
	value := metering.Decimal{Coefficient: "3", Scale: 0}
	changed.Measures[0].Value = &value

	refs := NewSourceReferenceMap(0)
	first := refs.Bind(claim, trusted)
	conflict := refs.Bind(changed, trusted)
	if conflict.Reason != ConnectorEvidenceRejectedPayloadConflict {
		t.Fatalf("conflict reason = %q, want %q", conflict.Reason, ConnectorEvidenceRejectedPayloadConflict)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want the first binding only", refs.Len())
	}
	if first.Bound.Observation.Measures[0].Value.Coefficient != "2" {
		t.Fatal("the first binding was not preserved")
	}
}

func TestSourceReferenceMap_LocalIDConflictRejectsLaterClaim(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	first := hostBindingClaim(t, "local-1", "source-event-1")
	second := hostBindingClaim(t, "local-1", "source-event-2")
	refs := NewSourceReferenceMap(0)
	if outcome := refs.Bind(first, trusted); outcome.Reason != "" {
		t.Fatalf("first bind reason = %q", outcome.Reason)
	}
	conflict := refs.Bind(second, trusted)
	if conflict.Reason != ConnectorEvidenceRejectedLocalIDConflict {
		t.Fatalf("conflict reason = %q, want %q", conflict.Reason, ConnectorEvidenceRejectedLocalIDConflict)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want the first binding only", refs.Len())
	}
}

func TestSourceReferenceMap_RejectedClaimConsumesNoSlot(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	refs := NewSourceReferenceMap(0)
	rejected := refs.Bind(claim, trusted)
	if rejected.Reason != "" {
		t.Fatalf("bind reason = %q", rejected.Reason)
	}
	// A conflicting attempt identity is rejected and must not consume an id,
	// an ordinal or a reference slot.
	conflicting := hostBindingClaim(t, "local-2", "source-event-2")
	conflicting.Subject.ALegID = "foreign-aleg"
	outcome := refs.Bind(conflicting, trusted)
	if outcome.Reason != ConnectorEvidenceRejectedTrustAttemptIdentityConflict {
		t.Fatalf("conflict reason = %q, want %q", outcome.Reason, ConnectorEvidenceRejectedTrustAttemptIdentityConflict)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want the rejected claim to consume no slot", refs.Len())
	}
	// The next admitted claim receives the next ordinal, so the rejected claim
	// consumed no ordinal either.
	next := hostBindingClaim(t, "local-3", "source-event-3")
	admitted := refs.Bind(next, trusted)
	if admitted.Reason != "" {
		t.Fatalf("next bind reason = %q", admitted.Reason)
	}
	if admitted.Bound.Observation.Sequence != 2 {
		t.Fatalf("next ordinal = %d, want 2 (the rejected claim consumed none)", admitted.Bound.Observation.Sequence)
	}
}

func TestSourceReferenceMap_CapacityRejectsNewIdentityButKeepsReplayAndConflict(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	refs := NewSourceReferenceMap(1)
	first := hostBindingClaim(t, "local-1", "source-event-1")
	if outcome := refs.Bind(first, trusted); outcome.Reason != "" {
		t.Fatalf("first bind reason = %q", outcome.Reason)
	}
	// At the cap an exact replay stays a no-op.
	replay := refs.Bind(first, trusted)
	if replay.Reason != "" || !replay.Replay {
		t.Fatalf("replay at capacity = %+v, want a replay no-op", replay)
	}
	// A conflicting reuse preserves the first binding at the cap.
	changed := first.Clone()
	changed.Measures = append([]metering.Measure(nil), first.Measures...)
	value := metering.Decimal{Coefficient: "9", Scale: 0}
	changed.Measures[0].Value = &value
	conflict := refs.Bind(changed, trusted)
	if conflict.Reason != ConnectorEvidenceRejectedPayloadConflict {
		t.Fatalf("conflict at capacity = %q, want %q", conflict.Reason, ConnectorEvidenceRejectedPayloadConflict)
	}
	// A genuinely new source identity takes the overflow path.
	overflow := refs.Bind(hostBindingClaim(t, "local-2", "source-event-2"), trusted)
	if overflow.Reason != ConnectorEvidenceRejectedCapacity {
		t.Fatalf("overflow reason = %q, want %q", overflow.Reason, ConnectorEvidenceRejectedCapacity)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want one (no eviction)", refs.Len())
	}
}

func TestSourceReferenceMap_VerifiedReferenceIsSubstitutedWithHostReference(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	predecessor := hostBindingClaim(t, "local-1", "source-event-1")
	successor := hostBindingClaim(t, "local-2", "source-event-2")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       "connector-placeholder-store",
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   predecessor.Fingerprint(),
	}}

	refs := NewSourceReferenceMap(0)
	priorOutcome := refs.Bind(predecessor, trusted)
	if priorOutcome.Reason != "" {
		t.Fatalf("predecessor bind reason = %q", priorOutcome.Reason)
	}
	successorOutcome := refs.Bind(successor, trusted)
	if successorOutcome.Reason != "" {
		t.Fatalf("successor bind reason = %q, want admitted", successorOutcome.Reason)
	}
	bound := successorOutcome.Bound.Observation
	if len(bound.Supersedes) != 1 {
		t.Fatalf("supersedes = %+v, want one host reference", bound.Supersedes)
	}
	ref := bound.Supersedes[0]
	if ref.StoreID != trusted.StoreID {
		t.Fatalf("reference store = %q, want the trusted store %q", ref.StoreID, trusted.StoreID)
	}
	if ref.ObservationID != priorOutcome.Bound.HostObservationID {
		t.Fatalf("reference id = %q, want the host identity %q", ref.ObservationID, priorOutcome.Bound.HostObservationID)
	}
	if ref.PayloadHash == predecessor.Fingerprint() {
		t.Fatal("the connector's source hash was published as the host reference hash")
	}
}

func TestSourceReferenceMap_WrongReferenceHashRejectsWholeClaim(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	predecessor := hostBindingClaim(t, "local-1", "source-event-1")
	successor := hostBindingClaim(t, "local-2", "source-event-2")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       "connector-placeholder-store",
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   "0000000000000000000000000000000000000000000000000000000000000000",
	}}

	refs := NewSourceReferenceMap(0)
	if outcome := refs.Bind(predecessor, trusted); outcome.Reason != "" {
		t.Fatalf("predecessor bind reason = %q", outcome.Reason)
	}
	outcome := refs.Bind(successor, trusted)
	if outcome.Reason != ConnectorEvidenceRejectedSourceFingerprintMismatch {
		t.Fatalf("reason = %q, want %q", outcome.Reason, ConnectorEvidenceRejectedSourceFingerprintMismatch)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want the rejected claim to consume no slot", refs.Len())
	}
}

func TestSourceReferenceMap_HeldClaimResolvesWhenPredecessorArrives(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	predecessor := hostBindingClaim(t, "local-1", "source-event-1")
	successor := hostBindingClaim(t, "local-2", "source-event-2")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       "connector-placeholder-store",
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   predecessor.Fingerprint(),
	}}

	refs := NewSourceReferenceMap(0)
	held := refs.Bind(successor, trusted)
	if held.Reason != "" || !held.Held {
		t.Fatalf("forward reference outcome = %+v, want a held claim", held)
	}
	released := refs.Bind(predecessor, trusted)
	if released.Reason != "" {
		t.Fatalf("predecessor bind reason = %q", released.Reason)
	}
	if len(released.Released) != 1 {
		t.Fatalf("released claims = %d, want the held claim released immediately", len(released.Released))
	}
	if len(released.Released[0].Observation.Supersedes) != 1 {
		t.Fatal("the released claim carries no verified host reference")
	}
}

func TestSourceReferenceMap_TerminalRejectsUnresolvedHeldClaim(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	successor := hostBindingClaim(t, "local-2", "source-event-2")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       "connector-placeholder-store",
		ObservationID: "never-arrives",
		Revision:      1,
		PayloadHash:   strings.Repeat("a", 64),
	}}
	refs := NewSourceReferenceMap(0)
	held := refs.Bind(successor, trusted)
	if !held.Held {
		t.Fatal("the unresolved claim was not held")
	}
	outcomes := refs.ReleaseUnresolvedHeld()
	if len(outcomes) != 1 {
		t.Fatalf("terminal rejections = %d, want one", len(outcomes))
	}
	if outcomes[0].Reason != ConnectorEvidenceRejectedPredecessorAbsent {
		t.Fatalf("terminal reason = %q, want %q", outcomes[0].Reason, ConnectorEvidenceRejectedPredecessorAbsent)
	}
	if refs.Len() != 0 {
		t.Fatalf("logical items = %d, want the rejected claim removed", refs.Len())
	}
}

func TestConnectorEvidenceRejectionCategoryIsTotalOverTheClosedSet(t *testing.T) {
	t.Parallel()
	reasons := []ConnectorEvidenceRejection{
		ConnectorEvidenceRejectedPeerNotHostBound,
		ConnectorEvidenceRejectedTrustAttemptIdentityConflict,
		ConnectorEvidenceRejectedSourceFingerprintMismatch,
		ConnectorEvidenceRejectedScopeConflict,
		ConnectorEvidenceRejectedPredecessorAbsent,
		ConnectorEvidenceRejectedLocalIDConflict,
		ConnectorEvidenceRejectedPayloadConflict,
		ConnectorEvidenceRejectedCapacity,
	}
	seen := map[string]bool{}
	for _, reason := range reasons {
		category, ok := ConnectorEvidenceRejectionCategoryOf(reason)
		if !ok {
			t.Fatalf("reason %q has no category", reason)
		}
		if seen[category] && category == ConnectorEvidenceCategoryP1 {
			t.Fatal("duplicate P1 category")
		}
		seen[category] = true
	}
	if _, ok := ConnectorEvidenceRejectionCategoryOf("not-a-reason"); ok {
		t.Fatal("an unknown reason was classified")
	}
	if !seen[ConnectorEvidenceCategoryP1] || !seen[ConnectorEvidenceCategoryP2] || !seen[ConnectorEvidenceCategoryP3] {
		t.Fatalf("categories = %v, want every category reachable", seen)
	}
}

func TestParseConnectorEconomicEvidencePolicy(t *testing.T) {
	t.Parallel()
	if policy, err := ParseConnectorEconomicEvidencePolicy(""); err != nil || policy != ConnectorEconomicEvidenceDegrade {
		t.Fatalf("empty policy = %q/%v, want the degrade default", policy, err)
	}
	if policy, err := ParseConnectorEconomicEvidencePolicy("strict"); err != nil || policy != ConnectorEconomicEvidenceStrict {
		t.Fatalf("strict policy = %q/%v", policy, err)
	}
	if _, err := ParseConnectorEconomicEvidencePolicy("refuse"); err == nil {
		t.Fatal("an unknown policy value was accepted")
	}
}

// A connector claim whose Correlation carrier contradicts the trusted attempt
// identity is a conflicting claim. The attempt identity is checked in every
// carrier it is carried in, before the host stamp erases the conflict.
func TestSourceReferenceMap_CorrelationCarrierConflictIsRejected(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	cases := []struct {
		name   string
		mutate func(claim *metering.Observation)
	}{
		{"a_leg", func(claim *metering.Observation) { claim.Correlation.ALegID = "foreign-aleg" }},
		{"request", func(claim *metering.Observation) { claim.Correlation.RequestID = "foreign-request" }},
		{"billing_call", func(claim *metering.Observation) { claim.Correlation.BillingCallID = "foreign-billing-call" }},
		{"call", func(claim *metering.Observation) { claim.Correlation.CallID = "foreign-call" }},
		{"attempt", func(claim *metering.Observation) { claim.Correlation.AttemptID = "foreign-attempt" }},
		{"b_leg", func(claim *metering.Observation) { claim.Correlation.BLegID = "foreign-bleg" }},
		{"attempt_seq", func(claim *metering.Observation) { claim.Correlation.AttemptSeq = trusted.AttemptSeq + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refs := NewSourceReferenceMap(0)
			claim := hostBindingClaim(t, "local-1", "source-event-1")
			tc.mutate(&claim)
			outcome := refs.Bind(claim, trusted)
			if outcome.Reason != ConnectorEvidenceRejectedTrustAttemptIdentityConflict {
				t.Fatalf("conflicting %s carrier was admitted or misclassified: reason=%q", tc.name, outcome.Reason)
			}
			if refs.Len() != 0 {
				t.Fatalf("logical items = %d, want the rejected claim to bind nothing", refs.Len())
			}
		})
	}
}

// A claim that agrees with the trusted attempt identity in every carrier that
// carries it is admitted. The transport leg pseudonym is accepted and replaced,
// and the host-owned dimensions stay host-owned.
func TestSourceReferenceMap_AttemptIdentityIsAcceptedInBothCarriers(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	claim := hostBindingClaim(t, "local-1", "source-event-1")
	claim.Subject.ALegID = trusted.ALegID
	claim.Subject.AttemptSeq = trusted.AttemptSeq
	claim.Correlation.ALegID = trusted.ALegID
	claim.Correlation.AttemptSeq = trusted.AttemptSeq
	claim.Correlation.BLegID = trusted.ConnectorLegPseudonym
	refs := NewSourceReferenceMap(0)
	outcome := refs.Bind(claim, trusted)
	if outcome.Reason != "" {
		t.Fatalf("bind reason = %q, want admitted", outcome.Reason)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want one", refs.Len())
	}
}

// A rejected reference consumes no ordinal: the next admitted claim with a
// missing sequence receives the first host receipt ordinal.
func TestSourceReferenceMap_RejectedReferenceConsumesNoOrdinal(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	refs := NewSourceReferenceMap(0)
	predecessor := hostBindingClaim(t, "local-predecessor", "source-event-predecessor")
	predecessor.Sequence = 77
	if outcome := refs.Bind(predecessor, trusted); outcome.Reason != "" {
		t.Fatalf("predecessor bind reason = %q, want admitted", outcome.Reason)
	}
	bad := hostBindingClaim(t, "local-bad", "source-event-bad")
	bad.Semantics = metering.SemanticsCorrection
	bad.Supersedes = []metering.ObservationRef{{
		StoreID:       predecessor.Subject.StoreID,
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   strings.Repeat("0", 64),
	}}
	if outcome := refs.Bind(bad, trusted); outcome.Reason != ConnectorEvidenceRejectedSourceFingerprintMismatch {
		t.Fatalf("bad reference reason = %q, want %q", outcome.Reason, ConnectorEvidenceRejectedSourceFingerprintMismatch)
	}
	valid := hostBindingClaim(t, "local-valid", "source-event-valid")
	outcome := refs.Bind(valid, trusted)
	if outcome.Reason != "" {
		t.Fatalf("valid bind reason = %q, want admitted", outcome.Reason)
	}
	if outcome.Bound.Observation.Sequence != 1 {
		t.Fatalf("next ordinal = %d, want 1 (the rejected reference consumed no ordinal)", outcome.Bound.Observation.Sequence)
	}
}

// A held claim that is rejected while it is released is reported with its
// closed reason, its exact first host receipt and its connector-owned source
// fingerprint, and the map keeps no residue for it.
func TestSourceReferenceMap_ReleaseReportsTheRejectedHeldClaim(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	refs := NewSourceReferenceMap(0)
	predecessor := hostBindingClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBindingClaim(t, "local-successor", "source-event-successor")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       predecessor.Subject.StoreID,
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   strings.Repeat("0", 64),
	}}
	held := refs.Bind(successor, trusted)
	if held.Reason != "" || !held.Held {
		t.Fatalf("forward reference outcome = %+v, want a held claim", held)
	}
	released := refs.Bind(predecessor, trusted)
	if released.Reason != "" {
		t.Fatalf("predecessor bind reason = %q, want admitted", released.Reason)
	}
	if len(released.Released) != 0 {
		t.Fatalf("released claims = %d, want none (the held claim was rejected)", len(released.Released))
	}
	if len(released.ReleasedRejections) != 1 {
		t.Fatalf("released rejections = %d, want one", len(released.ReleasedRejections))
	}
	rejection := released.ReleasedRejections[0]
	if rejection.Reason != ConnectorEvidenceRejectedSourceFingerprintMismatch {
		t.Fatalf("released rejection reason = %q, want %q", rejection.Reason, ConnectorEvidenceRejectedSourceFingerprintMismatch)
	}
	if rejection.ClaimKey != held.Bound.ClaimKey {
		t.Fatalf("released rejection claim key = %q, want the held claim %q", rejection.ClaimKey, held.Bound.ClaimKey)
	}
	if rejection.HostObservationID != held.Bound.HostObservationID {
		t.Fatalf("released rejection host id = %q, want the held claim %q", rejection.HostObservationID, held.Bound.HostObservationID)
	}
	if !rejection.ReceivedAt.Equal(held.Bound.Observation.ReceivedAt) {
		t.Fatalf("released rejection receipt = %v, want the held claim's first receipt %v", rejection.ReceivedAt, held.Bound.Observation.ReceivedAt)
	}
	if rejection.SourceFingerprint != successor.Fingerprint() {
		t.Fatalf("released rejection source fingerprint = %q, want the connector's own fingerprint", rejection.SourceFingerprint)
	}
	if refs.Len() != 1 {
		t.Fatalf("logical items = %d, want the rejected held claim removed", refs.Len())
	}
}

// A held claim is bound and counted once before it is released, so its host
// receipt ordinal is spent. The ordinal counter is lifetime monotonic: a
// rejection decided at release time never hands an ordinal back.
func TestSourceReferenceMap_HeldClaimRejectedAtReleaseKeepsItsOrdinal(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	refs := NewSourceReferenceMap(0)
	predecessor := hostBindingClaim(t, "local-predecessor", "source-event-predecessor")
	successor := hostBindingClaim(t, "local-successor", "source-event-successor")
	successor.Semantics = metering.SemanticsCorrection
	successor.Supersedes = []metering.ObservationRef{{
		StoreID:       predecessor.Subject.StoreID,
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   strings.Repeat("0", 64),
	}}
	held := refs.Bind(successor, trusted)
	if !held.Held {
		t.Fatalf("forward reference outcome = %+v, want a held claim", held)
	}
	if held.Bound.Observation.Sequence != 1 {
		t.Fatalf("held claim ordinal = %d, want 1", held.Bound.Observation.Sequence)
	}
	if outcome := refs.Bind(predecessor, trusted); outcome.Reason != "" {
		t.Fatalf("predecessor bind reason = %q, want admitted", outcome.Reason)
	}
	next := hostBindingClaim(t, "local-next", "source-event-next")
	outcome := refs.Bind(next, trusted)
	if outcome.Reason != "" {
		t.Fatalf("next bind reason = %q, want admitted", outcome.Reason)
	}
	if outcome.Bound.Observation.Sequence != 3 {
		t.Fatalf("next ordinal = %d, want 3 (the held claim was bound and counted once before it was rejected)", outcome.Bound.Observation.Sequence)
	}
}

// A rejected claim leaves no reference slot: a later claim that names the
// rejected claim's reference key finds no predecessor and stays held. The
// rejected claim consumed no capacity slot and the replay decision of the
// first binding is unaffected.
func TestSourceReferenceMap_RejectedClaimLeavesNoReferenceSlot(t *testing.T) {
	t.Parallel()
	trusted := hostBindingTrusted()
	refs := NewSourceReferenceMap(0)
	predecessor := hostBindingClaim(t, "local-predecessor", "source-event-predecessor")
	if outcome := refs.Bind(predecessor, trusted); outcome.Reason != "" {
		t.Fatalf("predecessor bind reason = %q, want admitted", outcome.Reason)
	}
	rejected := hostBindingClaim(t, "local-rejected", "source-event-rejected")
	rejected.Semantics = metering.SemanticsCorrection
	rejected.Supersedes = []metering.ObservationRef{{
		StoreID:       predecessor.Subject.StoreID,
		ObservationID: predecessor.ID,
		Revision:      predecessor.Revision,
		PayloadHash:   strings.Repeat("0", 64),
	}}
	if outcome := refs.Bind(rejected, trusted); outcome.Reason != ConnectorEvidenceRejectedSourceFingerprintMismatch {
		t.Fatalf("rejected bind reason = %q, want %q", outcome.Reason, ConnectorEvidenceRejectedSourceFingerprintMismatch)
	}
	after := hostBindingClaim(t, "local-after", "source-event-after")
	after.Semantics = metering.SemanticsCorrection
	after.Supersedes = []metering.ObservationRef{{
		StoreID:       predecessor.Subject.StoreID,
		ObservationID: rejected.ID,
		Revision:      rejected.Revision,
		PayloadHash:   rejected.Fingerprint(),
	}}
	outcome := refs.Bind(after, trusted)
	if outcome.Reason != "" || !outcome.Held {
		t.Fatalf("a claim naming the rejected claim's reference = %+v, want a held claim (the rejected claim left no reference slot)", outcome)
	}
	if refs.Len() != 2 {
		t.Fatalf("logical items = %d, want the predecessor and the held claim only", refs.Len())
	}
	replay := refs.Bind(predecessor, trusted)
	if replay.Reason != "" || !replay.Replay {
		t.Fatalf("replay after a rejected claim = %+v, want a replay no-op", replay)
	}
}
