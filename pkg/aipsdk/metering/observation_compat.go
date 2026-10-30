package metering

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// LegacyV1MappingRef marks an observation that was losslessly lifted from
	// the historical Fact DTO. It is explicit about the loss of richer V2
	// provenance rather than inventing provider/local distinctions.
	LegacyV1MappingRef     = "legacy_v1_fact"
	LegacyV1StoreID        = "legacy_v1"
	LegacyV1UnknownHash    = "legacy_v1_unknown_payload"
	legacyV2ProjectionKind = "v2_nonfinancial_projection"
)

// ErrUnrepresentableV1 identifies a V2 value that cannot be losslessly exposed
// through the integer/token-only V1 DTO.
var ErrUnrepresentableV1 = fmt.Errorf("%w: V2 value is not representable by V1", ErrV1Projection)

// ReadLegacyV1Observation decodes a selected historical V1-lifted observation
// and restores its private provider/request compatibility allowance only after
// checking the versioned legacy marker, acquisition, origin, subject kind, and
// both legacy store-scope fields. The caller must establish that data was
// selected from the trusted durable historical record; JSON values do not
// authenticate provenance by themselves.
func ReadLegacyV1Observation(data []byte) (Observation, error) {
	var observation Observation
	if err := json.Unmarshal(data, &observation); err != nil {
		return Observation{}, fmt.Errorf("%w: legacy observation decode: %v", ErrV1Projection, err)
	}
	if !legacyV1ObservationTuple(observation) {
		return Observation{}, fmt.Errorf("%w: legacy marker, origin, subject or scope is invalid", ErrV1Projection)
	}
	observation.legacyV1Trusted = true
	observation.legacyProviderRequest = legacyProviderRequestTuple(observation)
	if err := observation.Validate(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

// ObservationFromFact lifts a historical Fact into an explicit V2 observation.
// It preserves Fact.SourceEventKey verbatim and marks all inferred identity
// fields as legacy. A Fact projected from V2 is rejected to prevent a
// nonfinancial compatibility view from becoming a second authority.
func ObservationFromFact(f Fact) (Observation, error) {
	if f.IdentityVersion >= int(ObservationVersionV2) || f.SourceEventKind == legacyV2ProjectionKind {
		return Observation{}, fmt.Errorf("%w: projected fact cannot be re-imported", ErrV1Projection)
	}
	if err := f.Validate(); err != nil {
		return Observation{}, err
	}
	store := LegacyV1StoreID
	subject, err := subjectFromV1Fact(f, store)
	if err != nil {
		return Observation{}, err
	}
	if tenantID := f.Scope.TenantID.String(); tenantID != "" {
		subject.TenantID = tenantID
	}
	if err := subject.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: legacy subject: %v", ErrUnrepresentableV1, err)
	}
	correlation := CorrelationV2{
		StoreID: store, RequestID: f.Correlation.RequestID, ALegID: f.Correlation.ALegID,
		BLegID: f.Correlation.BLegID, AttemptID: f.Correlation.AttemptID, TenantID: subject.TenantID,
	}
	semantics, representable := semanticsFromV1(f.Kind)
	if !representable {
		return Observation{}, fmt.Errorf("%w: fact kind %q has no lossless V2 semantics", ErrUnrepresentableV1, f.Kind)
	}
	origin, authority, provenanceRepresentable := provenanceFromV1(f.Source, f.Authority)
	if !provenanceRepresentable {
		return Observation{}, fmt.Errorf("%w: source %q and authority %q have no lossless V2 provenance", ErrUnrepresentableV1, f.Source, f.Authority)
	}
	revision := uint64(f.SourceRevision)
	if revision == 0 {
		revision = 1
	}
	sequence := uint64(f.Sequence)
	if f.Kind == FactKindUnavailable {
		authority = AuthorityUnavailableClaim
	}
	observation := Observation{
		Version: ObservationVersionV2, ID: f.FactID, SourceEventKey: f.SourceEventKey(), Revision: revision,
		StreamID: f.StreamID, Sequence: sequence, Origin: origin,
		Acquisition: LegacyV1MappingRef, Authority: authority,
		Perspective: f.Perspective, Boundary: f.Boundary, Lifecycle: f.Lifecycle,
		Subject: subject, Correlation: correlation, Scope: f.Scope.Clone(), Semantics: semantics,
		ObservedAt: f.RecordedAt, ReceivedAt: f.RecordedAt, MappingRef: LegacyV1MappingRef,
	}
	observation.legacyProviderRequest = legacyProviderRequestTuple(observation)
	observation.legacyV1Trusted = true
	if observation.ObservedAt.IsZero() {
		// V1's timestamp was optional in producer construction. Keep a stable
		// non-zero envelope timestamp without using it in SourceEventKey.
		observation.ObservedAt = unixEpoch
		observation.ReceivedAt = unixEpoch
	}
	for i, quantity := range f.Quantities {
		if f.Kind == FactKindUnavailable && quantity.Present {
			return Observation{}, fmt.Errorf("%w: unavailable fact quantity[%d] is present", ErrUnrepresentableV1, i)
		}
		key, err := componentKeyFromV1(quantity)
		if err != nil {
			return Observation{}, fmt.Errorf("%w: quantity[%d]: %v", ErrUnrepresentableV1, i, err)
		}
		var value *Decimal
		quality := QualityUnavailable
		if quantity.Present {
			d := Decimal{Coefficient: strconv.FormatInt(quantity.Value, 10), Scale: 0}
			value = &d
			quality = QualityObserved
		}
		observation.Measures = append(observation.Measures, Measure{Key: key, Value: value, Quality: quality, MethodRef: LegacyV1MappingRef})
	}
	if f.Money != nil {
		if f.Kind == FactKindUnavailable && f.Money.Present {
			return Observation{}, fmt.Errorf("%w: unavailable fact money is present", ErrUnrepresentableV1)
		}
		charge := ReportedCharge{ChargeItemID: "legacy-money", Kind: ChargeKindAggregate}
		if f.Money.Present {
			amount := DecimalFromNanoUnits(f.Money.NanoUnits)
			charge.Amount = &amount
			charge.Currency = f.Money.Currency
		}
		observation.Charges = append(observation.Charges, charge)
	}
	for _, id := range f.Supersedes {
		observation.Supersedes = append(observation.Supersedes, ObservationRef{StoreID: store, ObservationID: strings.TrimSpace(id), Revision: 1, PayloadHash: LegacyV1UnknownHash})
	}
	if len(observation.Supersedes) > 0 && observation.Semantics != SemanticsCorrection && observation.Semantics != SemanticsReplacement {
		observation.Semantics = SemanticsCorrection
	}
	if len(observation.Measures) == 0 && len(observation.Charges) == 0 && f.Kind != FactKindUnavailable {
		return Observation{}, fmt.Errorf("%w: fact has no representable measures or money", ErrUnrepresentableV1)
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

// V2ObservationFromFact is an explicit compatibility spelling.
func V2ObservationFromFact(f Fact) (Observation, error) { return ObservationFromFact(f) }

func subjectFromV1Fact(f Fact, store string) (SubjectRef, error) {
	base := SubjectRef{StoreID: store, ALegID: f.Correlation.ALegID}
	switch {
	case f.Correlation.BLegID != "":
		base.Kind = SubjectBLeg
		base.BLegID = f.Correlation.BLegID
		base.AttemptID = f.Correlation.AttemptID
	case f.Correlation.ALegID != "":
		base.Kind = SubjectALeg
	case f.Correlation.RequestID != "":
		base.Kind = SubjectRequest
		base.RequestID = f.Correlation.RequestID
	default:
		return SubjectRef{}, fmt.Errorf("%w: legacy fact has no proven subject identity", ErrUnrepresentableV1)
	}
	return base, nil
}

func componentKeyFromV1(q Quantity) (ComponentKey, error) {
	direction := DirectionNone
	switch q.Component {
	case ComponentInputToken, ComponentCacheReadInputToken, ComponentCacheWriteInputToken:
		direction = DirectionInput
	case ComponentOutputToken, ComponentReasoningOutputToken:
		direction = DirectionOutput
	}
	schema := q.Schema
	if schema == "" && IsRegisteredComponent(q.Component) {
		schema = DefaultInclusionSchemaID
	}
	key := ComponentKey{Direction: direction, Component: q.Component, Unit: q.Unit, SchemaID: schema}
	if err := key.Validate(); err != nil {
		return ComponentKey{}, err
	}
	return key, nil
}

func semanticsFromV1(kind FactKind) (string, bool) {
	switch kind {
	case FactKindCumulative:
		return SemanticsCumulative, true
	case FactKindCorrection:
		return SemanticsCorrection, true
	case FactKindAuthoritativeReplacement:
		return SemanticsReplacement, true
	case FactKindDelta:
		return SemanticsDelta, true
	case FactKindUnavailable:
		// V2 carries this explicit state in Authority/Quality rather than
		// pretending it is an observed delta.
		return SemanticsDelta, true
	default:
		return "", false
	}
}

func provenanceFromV1(source Source, authority Authority) (origin, v2Authority string, representable bool) {
	switch source {
	case SourceObserved:
		origin = OriginLocal
	case SourceProviderReported:
		origin = OriginProvider
	case SourceEstimated:
		origin = OriginLocal
	default:
		// Derived/configured values are not independent observations. Without
		// their source inputs V2 cannot retain their provenance losslessly.
		return "", "", false
	}
	switch authority {
	case AuthorityAuthoritative:
		v2Authority = AuthorityObservedClaim
	case AuthorityEstimated:
		v2Authority = AuthorityEstimatedClaim
	case AuthorityUnavailable:
		v2Authority = AuthorityUnavailableClaim
	default:
		// Delegated/advisory V1 authority has no V2 equivalent; do not relabel
		// it as an ordinary observed claim.
		return "", "", false
	}
	return origin, v2Authority, true
}

func sourceFromV2(origin, authority string) (Source, error) {
	if origin == OriginStatement || authority == AuthorityVerifiedStatement {
		return "", fmt.Errorf("%w: statement provenance has no lossless V1 source", ErrUnrepresentableV1)
	}
	if authority == AuthorityEstimatedClaim {
		return SourceEstimated, nil
	}
	if origin == OriginProvider {
		return SourceProviderReported, nil
	}
	return SourceObserved, nil
}

// ProjectObservationToFact creates a one-way, nonfinancial V1 projection. It
// accepts only integer V1 components and marks the result so a later
// ObservationFromFact call cannot treat it as an independent observation.
func ProjectObservationToFact(o Observation) (Fact, error) {
	if err := o.Validate(); err != nil {
		return Fact{}, err
	}
	kind, err := factKindFromV2(o.Semantics)
	if err != nil {
		return Fact{}, err
	}
	if o.Authority == AuthorityUnavailableClaim {
		if err := validateUnavailableV2Projection(o); err != nil {
			return Fact{}, err
		}
		kind = FactKindUnavailable
	}
	source, err := sourceFromV2(o.Origin, o.Authority)
	if err != nil {
		return Fact{}, err
	}
	if len(o.Charges) > 1 {
		return Fact{}, fmt.Errorf("%w: multiple charge items cannot fit V1", ErrUnrepresentableV1)
	}
	f := Fact{
		FactID: o.ID, StreamID: o.StreamID, Sequence: int64(o.Sequence), IdentityVersion: int(ObservationVersionV2),
		SourceRevision: int64(o.Revision), SourceEventKind: legacyV2ProjectionKind, Kind: kind,
		Perspective: o.Perspective, Boundary: o.Boundary, Lifecycle: o.Lifecycle, Scope: o.Scope.Clone(),
		Correlation: Correlation{RequestID: o.Correlation.RequestID, ALegID: o.Correlation.ALegID, BLegID: o.Correlation.BLegID, AttemptID: o.Correlation.AttemptID},
		Source:      source, Authority: authorityToV1(o.Authority), Presence: PresenceUnknown, RecordedAt: o.ReceivedAt,
	}
	for i, measure := range o.Measures {
		if !v1ComponentKey(measure.Key) {
			return Fact{}, fmt.Errorf("%w: measure[%d] component/direction/unit cannot fit V1", ErrUnrepresentableV1, i)
		}
		q := Quantity{Component: measure.Key.Component, Unit: measure.Key.Unit, Schema: measure.Key.SchemaID, Present: measure.Value != nil}
		if measure.Value != nil {
			n, err := measure.Value.Normalize()
			if err != nil || n.Scale != 0 {
				return Fact{}, fmt.Errorf("%w: measure[%d] is not an integer", ErrUnrepresentableV1, i)
			}
			value, err := strconv.ParseInt(n.Coefficient, 10, 64)
			if err != nil {
				return Fact{}, fmt.Errorf("%w: measure[%d] overflows V1 integer", ErrUnrepresentableV1, i)
			}
			q.Value = value
			f.Presence = PresencePresent
		} else if f.Presence == PresenceUnknown {
			f.Presence = PresenceAbsent
		}
		f.Quantities = append(f.Quantities, q)
	}
	if kind == FactKindUnavailable {
		f.Presence = PresenceUnknown
	}
	if len(o.Charges) == 1 {
		charge := o.Charges[0]
		f.Money = &MoneyObservation{Present: false}
		if charge.Amount != nil {
			nanos, err := charge.Amount.ToNanoUnits()
			if err != nil {
				return Fact{}, fmt.Errorf("%w: charge amount: %v", ErrUnrepresentableV1, err)
			}
			f.Money = &MoneyObservation{NanoUnits: nanos, Currency: charge.Currency, Present: true, Source: source}
		}
	}
	for _, ref := range o.Supersedes {
		f.Supersedes = append(f.Supersedes, ref.ObservationID)
	}
	if err := f.Validate(); err != nil {
		return Fact{}, err
	}
	return f, nil
}

// FactFromObservation is an explicit alias retained for callers that use the
// historical direction of the adapter name.
func FactFromObservation(o Observation) (Fact, error) { return ProjectObservationToFact(o) }

func v1ComponentKey(k ComponentKey) bool {
	if !IsRegisteredComponent(k.Component) {
		return false
	}
	// V1 has no qualifier field. A projected key with dimensions would silently
	// merge distinct media/cache identities, so expose it as nonrepresentable.
	if len(k.Dimensions) != 0 {
		return false
	}
	if k.Unit != UnitToken && k.Unit != UnitCount {
		return false
	}
	switch k.Component {
	case ComponentInputToken, ComponentCacheReadInputToken, ComponentCacheWriteInputToken:
		return k.Direction == DirectionInput
	case ComponentOutputToken, ComponentReasoningOutputToken:
		return k.Direction == DirectionOutput
	case ComponentRequest, ComponentTotalToken:
		return k.Direction == DirectionNone || k.Direction == DirectionInput || k.Direction == DirectionOutput
	default:
		return false
	}
}

func factKindFromV2(semantics string) (FactKind, error) {
	switch semantics {
	case SemanticsCumulative:
		return FactKindCumulative, nil
	case SemanticsCorrection:
		return FactKindCorrection, nil
	case SemanticsReplacement:
		return FactKindAuthoritativeReplacement, nil
	case SemanticsDelta:
		return FactKindDelta, nil
	default:
		return "", fmt.Errorf("%w: V2 semantics %q has no lossless V1 kind", ErrUnrepresentableV1, semantics)
	}
}

func validateUnavailableV2Projection(o Observation) error {
	if o.Semantics != SemanticsDelta {
		return fmt.Errorf("%w: unavailable V2 evidence with semantics %q has no lossless V1 kind", ErrUnrepresentableV1, o.Semantics)
	}
	for i, measure := range o.Measures {
		if measure.Value != nil || (measure.Quality != QualityUnavailable && measure.Quality != QualityNotApplicable && measure.Quality != QualityUnknown) {
			return fmt.Errorf("%w: unavailable V2 measure[%d] carries usable evidence", ErrUnrepresentableV1, i)
		}
	}
	for i, charge := range o.Charges {
		if charge.Amount != nil || charge.Component != nil || charge.Kind != ChargeKindAggregate {
			return fmt.Errorf("%w: unavailable V2 charge[%d] carries usable evidence", ErrUnrepresentableV1, i)
		}
	}
	return nil
}

func authorityToV1(authority string) Authority {
	switch authority {
	case AuthorityEstimatedClaim:
		return AuthorityEstimated
	case AuthorityUnavailableClaim:
		return AuthorityUnavailable
	default:
		return AuthorityAuthoritative
	}
}

var unixEpoch = time.Unix(0, 0).UTC()
