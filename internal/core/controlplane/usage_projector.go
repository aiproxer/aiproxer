package controlplane

import (
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// ObservedStreamUsageInput is the safe usage observation projected from the
// usage observer or secure-session usage delta paths (requirements 1.4, 14.1,
// 14.2, 17.4).
type ObservedStreamUsageInput struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	CostNanoUnits    int64
	CostPresent      bool
	Currency         string
	CostSource       string
}

// ProjectObservedStreamUsage builds a UsageDetail with independent dual-plane
// fields while preserving legacy observed plane/availability projections.
func ProjectObservedStreamUsage(in ObservedStreamUsageInput) cp.UsageDetail {
	return cp.UsageDetail{
		Plane:               cp.UsagePlaneObserved,
		Availability:        cp.UsageAvailabilityObserved,
		Perspective:         cp.UsagePerspectiveOperator,
		Boundary:            cp.UsageBoundaryBackendEgress,
		LifecycleScope:      cp.UsageLifecycleBackendAttempt,
		Provenance:          cp.UsageProvenanceDelegated,
		FactKind:            cp.UsageFactKindDelta,
		Surfaced:            cp.UsageSurfacedUnknown,
		InputTokens:         in.InputTokens,
		OutputTokens:        in.OutputTokens,
		CacheReadTokens:     in.CacheReadTokens,
		CacheWriteTokens:    in.CacheWriteTokens,
		ReasoningTokens:     in.ReasoningTokens,
		TotalTokens:         in.TotalTokens,
		CostNanoUnits:       in.CostNanoUnits,
		CostPresent:         in.CostPresent,
		Currency:            in.Currency,
		AccountingAuthority: in.CostSource,
		CostSource:          in.CostSource,
	}
}

// UsageDetailFromEventInput carries optional dual-plane overrides for usage
// evidence projected from metering facts or other rich sources.
type UsageDetailFromEventInput struct {
	ObservedStreamUsageInput
	Perspective    cp.UsagePerspective
	Boundary       cp.UsageBoundary
	LifecycleScope cp.UsageLifecycleScope
	Provenance     cp.UsageProvenance
	FactKind       cp.UsageFactKind
	Surfaced       cp.UsageSurfaced
	Plane          cp.UsagePlane
	Availability   cp.UsageAvailability
	PolicyVersion  cp.VersionRef
	RatingVersion  cp.VersionRef
}

// ProjectUsageDetail merges optional dual-plane overrides onto the legacy
// observed-stream defaults.
func ProjectUsageDetail(in UsageDetailFromEventInput) cp.UsageDetail {
	out := ProjectObservedStreamUsage(in.ObservedStreamUsageInput)
	if in.Perspective != "" {
		out.Perspective = in.Perspective
	}
	if in.Boundary != "" {
		out.Boundary = in.Boundary
	}
	if in.LifecycleScope != "" {
		out.LifecycleScope = in.LifecycleScope
	}
	if in.Provenance != "" {
		out.Provenance = in.Provenance
	}
	if in.FactKind != "" {
		out.FactKind = in.FactKind
	}
	if in.Surfaced != "" {
		out.Surfaced = in.Surfaced
	}
	if in.Plane != "" {
		out.Plane = in.Plane
	}
	if in.Availability != "" {
		out.Availability = in.Availability
	}
	if in.PolicyVersion.ID != "" || in.PolicyVersion.Version != "" {
		out.PolicyVersion = in.PolicyVersion
	}
	if in.RatingVersion.ID != "" || in.RatingVersion.Version != "" {
		out.RatingVersion = in.RatingVersion
	}
	return out
}

// UsageRowFromEvent projects a usage query row from a normalized usage event.
func UsageRowFromEvent(ev cp.Event) cp.UsageRow {
	u := ev.Usage()
	if u == nil {
		return cp.UsageRow{}
	}
	row := cp.UsageRow{
		Correlation:      ev.Correlation,
		Plane:            u.Plane,
		Availability:     u.Availability,
		Perspective:      u.Perspective,
		Boundary:         u.Boundary,
		LifecycleScope:   u.LifecycleScope,
		Provenance:       u.Provenance,
		FactKind:         u.FactKind,
		Surfaced:         u.Surfaced,
		PolicyVersion:    u.PolicyVersion,
		RatingVersion:    u.RatingVersion,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		ReasoningTokens:  u.ReasoningTokens,
		TotalTokens:      u.TotalTokens,
		TokenPresence:    u.TokenPresence,
		CostNanoUnits:    u.CostNanoUnits,
		CostPresent:      u.CostPresent,
		Currency:         u.Currency,
		EvidenceState:    ev.EvidenceState,
		RedactionState:   ev.RedactionState,
	}
	if row.EvidenceState == "" {
		row.EvidenceState = cp.EvidenceRecorded
	}
	if row.RedactionState == "" {
		row.RedactionState = cp.RedactionNone
	}
	return row
}

// VersionRefFromPolicy maps economics policy snapshot refs onto control-plane
// version refs.
func VersionRefFromPolicy(ref economics.PolicySnapshotRef) cp.VersionRef {
	return cp.VersionRef{
		ID:          firstNonEmpty(ref.PolicyID, ref.ID),
		Version:     ref.Version,
		EffectiveAt: ref.EffectiveAt,
		FetchedAt:   ref.FetchedAt,
	}
}

// VersionRefFromRating maps economics rating snapshot refs onto control-plane
// version refs.
func VersionRefFromRating(ref economics.RatingSnapshotRef) cp.VersionRef {
	return cp.VersionRef{
		ID:          firstNonEmpty(ref.RaterID, ref.ID),
		Version:     ref.Version,
		EffectiveAt: ref.EffectiveAt,
		FetchedAt:   ref.FetchedAt,
	}
}

// ObservedStreamInputFromUsageEvent extracts token/cost fields from a usage
// observer event. CostPresent stays false until the observer event carries an
// explicit presence bit; nonzero cost alone is not treated as present.
func ObservedStreamInputFromUsageEvent(ev usage.Event) ObservedStreamUsageInput {
	return ObservedStreamUsageInput{
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         ev.Currency,
		CostSource:       ev.CostSource,
	}
}

// ProjectMeteringFact projects a metering journal fact onto UsageDetail while
// preserving explicit token/cost presence (requirements 2.9, 5.5-5.7).
func ProjectMeteringFact(f metering.Fact) cp.UsageDetail {
	var presence cp.UsageTokenPresence
	var in, out, cacheRead, cacheWrite, reasoning, total int
	for _, q := range f.Quantities {
		if !q.Present {
			continue
		}
		switch q.Component {
		case metering.ComponentInputToken:
			presence.InputTokens = true
			in = int(q.Value)
		case metering.ComponentOutputToken:
			presence.OutputTokens = true
			out = int(q.Value)
		case metering.ComponentCacheReadInputToken:
			presence.CacheReadTokens = true
			cacheRead = int(q.Value)
		case metering.ComponentCacheWriteInputToken:
			presence.CacheWriteTokens = true
			cacheWrite = int(q.Value)
		case metering.ComponentReasoningOutputToken:
			presence.ReasoningTokens = true
			reasoning = int(q.Value)
		case metering.ComponentTotalToken:
			presence.TotalTokens = true
			total = int(q.Value)
		}
	}
	costPresent := false
	var costNano int64
	currency := ""
	costSource := ""
	if f.Money != nil && f.Money.Present {
		costPresent = true
		costNano = f.Money.NanoUnits
		currency = f.Money.Currency
		costSource = string(f.Money.Source)
	}
	plane, availability := planeAvailabilityFromMetering(f)
	return cp.UsageDetail{
		Plane:               plane,
		Availability:        availability,
		Perspective:         cp.UsagePerspective(f.Perspective),
		Boundary:            cp.UsageBoundary(f.Boundary),
		LifecycleScope:      cp.UsageLifecycleScope(f.Lifecycle),
		Provenance:          provenanceFromMetering(f),
		FactKind:            cp.UsageFactKind(f.Kind),
		Surfaced:            cp.UsageSurfaced(f.Surfaced),
		InputTokens:         in,
		OutputTokens:        out,
		CacheReadTokens:     cacheRead,
		CacheWriteTokens:    cacheWrite,
		ReasoningTokens:     reasoning,
		TotalTokens:         total,
		TokenPresence:       presence,
		CostNanoUnits:       costNano,
		CostPresent:         costPresent,
		Currency:            currency,
		AccountingAuthority: costSource,
		CostSource:          costSource,
	}
}

// UsageRowFromMeteringFact projects metering fact correlation and usage detail
// onto a query row without inventing missing identifiers.
func UsageRowFromMeteringFact(f metering.Fact) cp.UsageRow {
	u := ProjectMeteringFact(f)
	return UsageRowFromEvent(cp.Event{
		Correlation: cp.Correlation{
			TraceID:    f.Correlation.TraceID,
			RequestID:  f.Correlation.RequestID,
			SessionID:  f.Correlation.SessionID,
			ALegID:     f.Correlation.ALegID,
			BLegID:     f.Correlation.BLegID,
			FrontendID: f.FrontendID,
			BackendID:  f.BackendID,
			Model:      f.Model,
		},
		Detail:         &u,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
	})
}

// UsageRowsFromMeteringFacts projects a fact set onto usage rows. Customer
// facts without frontend ingress and operator facts without backend ingress
// are marked EvidencePartial; quantities are not invented.
func UsageRowsFromMeteringFacts(facts []metering.Fact) []cp.UsageRow {
	seenFEIngress := false
	seenBEIngress := false
	seenCustomer := false
	seenOperator := false
	for _, f := range facts {
		switch f.Perspective {
		case metering.PerspectiveCustomer:
			seenCustomer = true
		case metering.PerspectiveOperator:
			seenOperator = true
		}
		switch f.Boundary {
		case metering.BoundaryFrontendIngress:
			seenFEIngress = true
		case metering.BoundaryBackendIngress:
			seenBEIngress = true
		}
	}
	customerIncomplete := seenCustomer && !seenFEIngress
	operatorIncomplete := seenOperator && !seenBEIngress
	out := make([]cp.UsageRow, 0, len(facts))
	for _, f := range facts {
		row := UsageRowFromMeteringFact(f)
		switch f.Perspective {
		case metering.PerspectiveCustomer:
			if customerIncomplete {
				row.EvidenceState = cp.EvidencePartial
			}
		case metering.PerspectiveOperator:
			if operatorIncomplete {
				row.EvidenceState = cp.EvidencePartial
			}
		}
		out = append(out, row)
	}
	return out
}

func provenanceFromMetering(f metering.Fact) cp.UsageProvenance {
	switch f.Authority {
	case metering.AuthorityAuthoritative:
		return cp.UsageProvenanceAuthoritative
	case metering.AuthorityEstimated:
		return cp.UsageProvenanceEstimated
	case metering.AuthorityAdvisory:
		return cp.UsageProvenanceAdvisory
	case metering.AuthorityUnavailable:
		return cp.UsageProvenanceUnavailable
	case metering.AuthorityDelegated:
		return cp.UsageProvenanceDelegated
	default:
		return cp.UsageProvenanceDelegated
	}
}

func planeAvailabilityFromMetering(f metering.Fact) (cp.UsagePlane, cp.UsageAvailability) {
	observedSource := f.Source == metering.SourceObserved || f.Source == metering.SourceEstimated
	switch f.Authority {
	case metering.AuthorityUnavailable:
		if observedSource {
			return cp.UsagePlaneObserved, cp.UsageAvailabilityUnavailable
		}
		return cp.UsagePlaneAccounting, cp.UsageAvailabilityUnavailable
	case metering.AuthorityEstimated, metering.AuthorityAdvisory, metering.AuthorityDelegated:
		return cp.UsagePlaneObserved, cp.UsageAvailabilityObserved
	case metering.AuthorityAuthoritative:
		if observedSource {
			return cp.UsagePlaneObserved, cp.UsageAvailabilityObserved
		}
		return cp.UsagePlaneAccounting, cp.UsageAvailabilityAccountingAuth
	default:
		if observedSource {
			return cp.UsagePlaneObserved, cp.UsageAvailabilityObserved
		}
		return cp.UsagePlaneAccounting, cp.UsageAvailabilityAccountingAuth
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
