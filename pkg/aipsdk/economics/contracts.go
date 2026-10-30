package economics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	MaxRatingObservations   = 1024
	MaxQuoteObservationRefs = 1024
	MaxQuoteLimits          = 128
	MaxQuoteAssumptions     = 128
	MaxStatementLines       = 4096
	MaxImportResultIDs      = 4096
	MaxReconciliationPage   = 1024
)

// Rater evaluates one explicit economic plane against an immutable input set.
// Implementations may be supplied by an application or enterprise module; the
// contract contains no provider SDK or persistence dependency. Runtime stream
// handlers do not invoke this post-usage seam.
type Rater interface {
	Rate(ctx context.Context, in RatingInput) (Valuation, error)
}

// Quoter computes a bounded pre-execution exposure quote for one commercial
// policy. It does not reserve or mutate money.
type Quoter interface {
	Quote(ctx context.Context, in QuoteInput) (ExposureQuote, error)
}

// StatementImporter accepts normalized statement evidence. Provider parsing,
// authentication and raw-payload handling belong to the supplying adapter.
type StatementImporter interface {
	Import(ctx context.Context, in StatementBatch) (ImportResult, error)
}

// ReconciliationReader reads immutable reconciliation results and never
// performs rating, posting or provider calls.
type ReconciliationReader interface {
	Query(ctx context.Context, in ReconciliationQuery) (ReconciliationPage, error)
}

// RatingInput explicitly selects one valuation basis. Observations are copied
// by Clone before an implementation mutates local working state.
type RatingInput struct {
	Version         uint32                       `json:"version"`
	Perspective     metering.EconomicPerspective `json:"perspective"`
	Basis           ValuationBasis               `json:"basis"`
	Subject         metering.SubjectRef          `json:"subject"`
	Scope           string                       `json:"scope,omitempty"`
	Payer           metering.PaymentParty        `json:"payer,omitzero"`
	Observations    []metering.Observation       `json:"observations,omitempty"`
	ObservationRefs []metering.ObservationRef    `json:"observation_refs,omitempty"`
	// AllocationCoverageRefs declares the exact immutable allocation
	// revisions whose conserved distribution is an economic input to this
	// valuation. They are separate from the observation plane: the input-set
	// hash authenticates observations only, while the full allocation-aware
	// valuation identity additionally authenticates this set. An allocation-only
	// correction therefore produces a distinct immutable revision without
	// fabricating observation revisions. Empty preserves legacy behavior.
	AllocationCoverageRefs []AllocationRef      `json:"allocation_coverage_refs,omitempty"`
	EffectiveQualifiers    []metering.Dimension `json:"effective_qualifiers,omitempty"`
	Rater                  RatingSnapshotRef    `json:"rater"`
	RaterContent           *SnapshotContentRef  `json:"rater_content,omitempty"`
	Tariff                 RatingSnapshotRef    `json:"tariff"`
	TariffContent          *SnapshotContentRef  `json:"tariff_content,omitempty"`
	Policy                 PolicySnapshotRef    `json:"policy"`
	PolicyContent          *SnapshotContentRef  `json:"policy_content,omitempty"`
	InputSetHash           string               `json:"input_set_hash,omitempty"`
	QualifierSnapshotRef   *SnapshotContentRef  `json:"qualifier_snapshot_ref,omitempty"`
	AsOf                   time.Time            `json:"as_of,omitzero"`
}

// PostUsageRatingInput is the billing-owned spelling of the existing
// provider-neutral rating input. Keeping this alias distinct at integration
// boundaries prevents internal post-usage code from being mistaken for the
// deleted stream-time customer-rating bridge while preserving wire and type
// compatibility for callers that already construct RatingInput values.
type PostUsageRatingInput = RatingInput

func (in RatingInput) Validate() error {
	if in.Version == 0 {
		return fmt.Errorf("%w: version required", ErrInvalidRating)
	}
	if err := in.Perspective.Validate(); err != nil {
		return fmt.Errorf("%w: perspective: %v", ErrInvalidRating, err)
	}
	if err := in.Basis.Validate(); err != nil {
		return fmt.Errorf("%w: basis: %v", ErrInvalidRating, err)
	}
	if err := in.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidRating, err)
	}
	if err := in.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: payer: %v", ErrInvalidRating, err)
	}
	if in.Scope != "" {
		if err := validatePublicRef("rating scope", in.Scope); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRating, err)
		}
	}
	if !isZeroRatingRef(in.Rater) || derivedBasis(in.Basis) {
		if err := validateRatingRefForInput("rater", in.Rater); err != nil {
			return err
		}
	}
	if !isZeroRatingRef(in.Tariff) || in.Basis == BasisLocalExpected || in.Basis == BasisProviderQuantityLocal {
		if err := validateRatingRefForInput("tariff", in.Tariff); err != nil {
			return err
		}
	}
	if !isZeroPolicyRef(in.Policy) || in.Basis == BasisCustomerPolicy {
		if err := validatePolicyRefForInput("policy", in.Policy); err != nil {
			return err
		}
	}
	if err := validateRatingInputSnapshotContext(in); err != nil {
		return err
	}
	if len(in.Observations) > MaxRatingObservations || len(in.ObservationRefs) > MaxRatingObservations {
		return fmt.Errorf("%w: observation bound exceeded", ErrInvalidRating)
	}
	if len(in.Observations) == 0 && len(in.ObservationRefs) == 0 {
		return fmt.Errorf("%w: immutable observation set required", ErrInvalidRating)
	}
	for i, observation := range in.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation %d: %v", ErrInvalidRating, i, err)
		}
		if observation.Subject.StoreID != in.Subject.StoreID {
			return fmt.Errorf("%w: observation %d store mismatch", ErrInvalidRating, i)
		}
	}
	if err := validateInputObservationRefs(in.ObservationRefs, in.Subject.StoreID); err != nil {
		return err
	}
	if len(in.AllocationCoverageRefs) > MaxAllocationRefs {
		return fmt.Errorf("%w: allocation coverage bound exceeded", ErrInvalidRating)
	}
	for i, ref := range in.AllocationCoverageRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: allocation coverage ref %d: %v", ErrInvalidRating, i, err)
		}
		if ref.StoreID != in.Subject.StoreID {
			return fmt.Errorf("%w: allocation coverage ref %d store mismatch", ErrInvalidRating, i)
		}
	}
	if err := validateDimensions(in.EffectiveQualifiers, ErrInvalidRating); err != nil {
		return err
	}
	return nil
}

func validateRatingRefForInput(name string, ref RatingSnapshotRef) error {
	if err := validateVersionRefForInput(name, ref.VersionRef); err != nil {
		return err
	}
	if ref.RaterID == "" {
		if name == "rater" {
			return fmt.Errorf("%w: %s rater_id required", ErrInvalidRating, name)
		}
	} else if err := validatePublicRef(name+" rater_id", ref.RaterID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRating, err)
	}
	return nil
}

func validatePolicyRefForInput(name string, ref PolicySnapshotRef) error {
	if err := validateVersionRefForInput(name, ref.VersionRef); err != nil {
		return err
	}
	if err := validatePublicRef(name+" policy_id", ref.PolicyID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRating, err)
	}
	return nil
}

func validateVersionRefForInput(name string, ref VersionRef) error {
	if err := validatePublicRef(name+" id", ref.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRating, err)
	}
	if err := validatePublicRef(name+" version", ref.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRating, err)
	}
	return nil
}

func validateInputObservationRefs(refs []metering.ObservationRef, store string) error {
	if err := validateObservationRefCollection(refs, store, "observation"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRating, err)
	}
	return nil
}

func validateDimensions(dimensions []metering.Dimension, target error) error {
	if len(dimensions) > metering.MaxDimensions {
		return fmt.Errorf("%w: qualifier bound exceeded", target)
	}
	seen := make(map[string]struct{}, len(dimensions))
	for i, dimension := range dimensions {
		if err := dimension.Validate(); err != nil {
			return fmt.Errorf("%w: qualifier %d: %v", target, i, err)
		}
		if _, ok := seen[dimension.Name]; ok {
			return fmt.Errorf("%w: duplicate qualifier %q", target, dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
	}
	return nil
}

// Clone protects custom raters from mutating caller-owned observations and
// slices. It does not claim persistence immutability by itself.
func (in RatingInput) Clone() RatingInput {
	out := in
	out.Subject = in.Subject.Clone()
	if in.Observations != nil {
		out.Observations = make([]metering.Observation, len(in.Observations))
		for i, observation := range in.Observations {
			out.Observations[i] = observation.Clone()
		}
	}
	out.ObservationRefs = append([]metering.ObservationRef(nil), in.ObservationRefs...)
	out.AllocationCoverageRefs = append([]AllocationRef(nil), in.AllocationCoverageRefs...)
	out.EffectiveQualifiers = append([]metering.Dimension(nil), in.EffectiveQualifiers...)
	out.RaterContent = cloneSnapshotContentRef(in.RaterContent)
	out.TariffContent = cloneSnapshotContentRef(in.TariffContent)
	out.PolicyContent = cloneSnapshotContentRef(in.PolicyContent)
	out.QualifierSnapshotRef = cloneSnapshotContentRef(in.QualifierSnapshotRef)
	return out
}

// Limit is one finite candidate/work quantity for exposure admission. Value is
// an exact integer bound; it is not a provider utilization gauge.
type Limit struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

func (l Limit) Validate() error {
	if err := validatePublicRef("limit name", l.Name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	if err := validatePublicRef("limit unit", l.Unit); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	if l.Value < 0 {
		return fmt.Errorf("%w: negative limit %q", ErrInvalidQuote, l.Name)
	}
	return nil
}

// QuoteInput carries finite execution candidates and an explicit commercial
// policy version. It cannot accept an opaque provider request or mutate an
// account balance. Routes is the optional ordered planned-leaf occurrence list
// carried for route-tariff-capable quoters; version-1 quotes leave it empty.
type QuoteInput struct {
	Version              uint32                       `json:"version"`
	Perspective          metering.EconomicPerspective `json:"perspective,omitempty"`
	Basis                ValuationBasis               `json:"basis,omitempty"`
	Subject              metering.SubjectRef          `json:"subject"`
	Scope                string                       `json:"scope,omitempty"`
	ObservationRefs      []metering.ObservationRef    `json:"observation_refs,omitempty"`
	EffectiveQualifiers  []metering.Dimension         `json:"effective_qualifiers,omitempty"`
	InputSetHash         string                       `json:"input_set_hash,omitempty"`
	QualifierSnapshotRef *SnapshotContentRef          `json:"qualifier_snapshot_ref,omitempty"`
	CandidateLimits      []Limit                      `json:"candidate_limits"`
	WorkLimits           []Limit                      `json:"work_limits,omitempty"`
	Routes               []RouteCandidate             `json:"routes,omitempty"`
	Tariff               RatingSnapshotRef            `json:"tariff"`
	TariffContent        *SnapshotContentRef          `json:"tariff_content,omitempty"`
	Policy               PolicySnapshotRef            `json:"policy"`
	PolicyContent        *SnapshotContentRef          `json:"policy_content,omitempty"`
	AsOf                 time.Time                    `json:"as_of,omitzero"`
}

func (in QuoteInput) Validate() error {
	if in.Version == 0 {
		return fmt.Errorf("%w: version required", ErrInvalidQuote)
	}
	if in.Perspective != "" {
		if err := in.Perspective.Validate(); err != nil {
			return fmt.Errorf("%w: perspective: %v", ErrInvalidQuote, err)
		}
	}
	if in.Basis != "" {
		if err := in.Basis.Validate(); err != nil {
			return fmt.Errorf("%w: basis: %v", ErrInvalidQuote, err)
		}
	}
	if err := in.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidQuote, err)
	}
	if in.Scope != "" {
		if err := validatePublicRef("quote scope", in.Scope); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
	}
	if err := validateInputSetIdentity("quote input set hash", in.InputSetHash, false, ErrInvalidQuote); err != nil {
		return err
	}
	if err := validateRatingRefForQuote("tariff", in.Tariff); err != nil {
		return err
	}
	if err := validatePolicyRefForQuote("policy", in.Policy); err != nil {
		return err
	}
	if err := validateQuoteSnapshotContext(in); err != nil {
		return err
	}
	if len(in.ObservationRefs) > MaxQuoteObservationRefs {
		return fmt.Errorf("%w: observation reference bound exceeded", ErrInvalidQuote)
	}
	if err := validateInputObservationRefsForQuote(in.ObservationRefs, in.Subject.StoreID); err != nil {
		return err
	}
	if err := validateDimensions(in.EffectiveQualifiers, ErrInvalidQuote); err != nil {
		return err
	}
	if len(in.CandidateLimits) == 0 {
		return fmt.Errorf("%w: at least one candidate limit required", ErrInvalidQuote)
	}
	if len(in.CandidateLimits) > MaxQuoteLimits || len(in.WorkLimits) > MaxQuoteLimits {
		return fmt.Errorf("%w: limit bound exceeded", ErrInvalidQuote)
	}
	if err := validateLimits(in.CandidateLimits); err != nil {
		return err
	}
	if err := validateLimits(in.WorkLimits); err != nil {
		return err
	}
	return validateRouteCandidates(in.Routes)
}

func validateRatingRefForQuote(name string, ref RatingSnapshotRef) error {
	if err := validateVersionRefForQuote(name, ref.VersionRef); err != nil {
		return err
	}
	if ref.RaterID != "" {
		if err := validatePublicRef(name+" rater_id", ref.RaterID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
	}
	return nil
}

func validatePolicyRefForQuote(name string, ref PolicySnapshotRef) error {
	if err := validateVersionRefForQuote(name, ref.VersionRef); err != nil {
		return err
	}
	if err := validatePublicRef(name+" policy_id", ref.PolicyID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	return nil
}

func validateVersionRefForQuote(name string, ref VersionRef) error {
	if err := validatePublicRef(name+" id", ref.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	if err := validatePublicRef(name+" version", ref.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	return nil
}

func validateInputObservationRefsForQuote(refs []metering.ObservationRef, store string) error {
	if err := validateObservationRefCollection(refs, store, "observation"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	return nil
}

func validateLimits(limits []Limit) error {
	seen := make(map[string]struct{}, len(limits))
	for i, limit := range limits {
		if err := limit.Validate(); err != nil {
			return fmt.Errorf("%w: limit %d: %v", ErrInvalidQuote, i, err)
		}
		key := limit.Name + "\x00" + limit.Unit
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate limit %q", ErrInvalidQuote, limit.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (in QuoteInput) Clone() QuoteInput {
	out := in
	out.Subject = in.Subject.Clone()
	out.ObservationRefs = append([]metering.ObservationRef(nil), in.ObservationRefs...)
	out.EffectiveQualifiers = append([]metering.Dimension(nil), in.EffectiveQualifiers...)
	out.TariffContent = cloneSnapshotContentRef(in.TariffContent)
	out.PolicyContent = cloneSnapshotContentRef(in.PolicyContent)
	out.QualifierSnapshotRef = cloneSnapshotContentRef(in.QualifierSnapshotRef)
	out.CandidateLimits = append([]Limit(nil), in.CandidateLimits...)
	out.WorkLimits = append([]Limit(nil), in.WorkLimits...)
	if in.Routes != nil {
		out.Routes = make([]RouteCandidate, len(in.Routes))
		for i, route := range in.Routes {
			out.Routes[i] = route.Clone()
		}
	}
	return out
}

// QuoteAssumption is a safe, human-readable assumption label. It is not raw
// prompt text and has no effect on account state.
type QuoteAssumption struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Reason string `json:"reason,omitempty"`
}

func (a QuoteAssumption) Validate() error {
	for name, value := range map[string]string{"assumption name": a.Name, "assumption value": a.Value, "assumption reason": a.Reason} {
		if value != "" {
			if err := validatePublicRef(name, value); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
			}
		}
	}
	if a.Name == "" || a.Value == "" {
		return fmt.Errorf("%w: assumption name and value required", ErrInvalidQuote)
	}
	return nil
}

// EvidenceCapability states an evidence requirement for a quote without
// embedding provider-specific request data.
type EvidenceCapability struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// UnitBound keeps non-monetary credits or allowances separate from Money. A
// present zero is distinct from an unavailable bound.
type UnitBound struct {
	Unit    string            `json:"unit"`
	Amount  *metering.Decimal `json:"amount,omitempty"`
	Present bool              `json:"present"`
}

func (b UnitBound) Validate() error {
	if err := validatePublicRef("unit bound", b.Unit); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	if b.Amount == nil {
		if b.Present {
			return fmt.Errorf("%w: present unit bound requires amount", ErrInvalidQuote)
		}
		return nil
	}
	if !b.Present {
		return fmt.Errorf("%w: absent unit bound cannot carry amount", ErrInvalidQuote)
	}
	n, err := b.Amount.Normalize()
	if err != nil {
		return fmt.Errorf("%w: unit bound amount: %v", ErrInvalidQuote, err)
	}
	if strings.HasPrefix(n.Coefficient, "-") {
		return fmt.Errorf("%w: negative unit bound", ErrInvalidQuote)
	}
	if (b.Unit == metering.UnitToken || b.Unit == metering.UnitCount) && n.Scale != 0 {
		return fmt.Errorf("%w: unit bound for %s must be an exact integer", ErrInvalidQuote, b.Unit)
	}
	return nil
}

func (b UnitBound) Clone() UnitBound {
	out := b
	if b.Amount != nil {
		amount := *b.Amount
		out.Amount = &amount
	}
	return out
}

func (c EvidenceCapability) Validate() error {
	if err := validatePublicRef("evidence capability", c.Name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	return nil
}

// ExposureQuote is a bounded quote result. Money remains absent/present-aware;
// no quote silently turns unavailable exposure into zero. RouteTariffs carries
// one frozen binding per distinct canonical route key behind the explicit
// RouteTariffMode; both stay empty on the legacy scalar path.
type ExposureQuote struct {
	ID                   string                       `json:"id,omitempty"`
	Version              uint32                       `json:"version"`
	Perspective          metering.EconomicPerspective `json:"perspective,omitempty"`
	Basis                ValuationBasis               `json:"basis,omitempty"`
	Subject              metering.SubjectRef          `json:"subject"`
	Minimum              Money                        `json:"minimum,omitzero"`
	Maximum              Money                        `json:"maximum,omitzero"`
	CreditBound          Money                        `json:"credit_bound,omitzero"`
	CreditUnits          []UnitBound                  `json:"credit_units,omitempty"`
	AllowanceUnits       []UnitBound                  `json:"allowance_units,omitempty"`
	Assumptions          []QuoteAssumption            `json:"assumptions,omitempty"`
	RequiredCapabilities []EvidenceCapability         `json:"required_capabilities,omitempty"`
	RouteTariffs         []RouteTariffBinding         `json:"route_tariffs,omitempty"`
	RouteTariffMode      RouteTariffMode              `json:"route_tariff_mode,omitempty"`
	Policy               PolicySnapshotRef            `json:"policy"`
	PolicyContent        *SnapshotContentRef          `json:"policy_content,omitempty"`
	Tariff               RatingSnapshotRef            `json:"tariff"`
	TariffContent        *SnapshotContentRef          `json:"tariff_content,omitempty"`
	InputSetHash         string                       `json:"input_set_hash,omitempty"`
	QualifierSnapshotRef *SnapshotContentRef          `json:"qualifier_snapshot_ref,omitempty"`
	Completeness         Completeness                 `json:"completeness"`
	CreatedAt            time.Time                    `json:"created_at,omitzero"`
}

func (q ExposureQuote) Validate() error {
	if q.Version == 0 {
		return fmt.Errorf("%w: version required", ErrInvalidQuote)
	}
	if q.ID != "" {
		if err := validatePublicRef("quote id", q.ID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
	}
	if q.Perspective != "" {
		if err := q.Perspective.Validate(); err != nil {
			return fmt.Errorf("%w: perspective: %v", ErrInvalidQuote, err)
		}
	}
	if q.Basis != "" {
		if err := q.Basis.Validate(); err != nil {
			return fmt.Errorf("%w: basis: %v", ErrInvalidQuote, err)
		}
	}
	if err := q.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidQuote, err)
	}
	if err := validateInputSetIdentity("quote input set hash", q.InputSetHash, false, ErrInvalidQuote); err != nil {
		return err
	}
	if err := validateQualifierSnapshot("", q.QualifierSnapshotRef, ErrInvalidQuote); err != nil {
		return err
	}
	if err := validateV2Money("minimum", q.Minimum); err != nil {
		return fmt.Errorf("%w: minimum: %v", ErrInvalidQuote, err)
	}
	if err := validateV2Money("maximum", q.Maximum); err != nil {
		return fmt.Errorf("%w: maximum: %v", ErrInvalidQuote, err)
	}
	if err := validateV2Money("credit bound", q.CreditBound); err != nil {
		return fmt.Errorf("%w: credit bound: %v", ErrInvalidQuote, err)
	}
	if !q.Completeness.IsKnown() {
		return fmt.Errorf("%w: unknown completeness %q", ErrInvalidQuote, q.Completeness)
	}
	if len(q.CreditUnits) > MaxQuoteLimits || len(q.AllowanceUnits) > MaxQuoteLimits {
		return fmt.Errorf("%w: credit/allowance bound exceeded", ErrInvalidQuote)
	}
	if err := validateUnitBounds(q.CreditUnits); err != nil {
		return err
	}
	if err := validateUnitBounds(q.AllowanceUnits); err != nil {
		return err
	}
	if q.Minimum.Present && q.Maximum.Present {
		if q.Minimum.Currency != q.Maximum.Currency {
			return fmt.Errorf("%w: minimum/maximum currency mismatch", ErrInvalidQuote)
		}
		if q.Minimum.NanoUnits > q.Maximum.NanoUnits {
			return fmt.Errorf("%w: minimum exceeds maximum", ErrInvalidQuote)
		}
	}
	if len(q.Assumptions) > MaxQuoteAssumptions || len(q.RequiredCapabilities) > MaxQuoteAssumptions {
		return fmt.Errorf("%w: assumption/capability bound exceeded", ErrInvalidQuote)
	}
	seenAssumptions := make(map[string]struct{}, len(q.Assumptions))
	for i, assumption := range q.Assumptions {
		if err := assumption.Validate(); err != nil {
			return fmt.Errorf("%w: assumption %d: %v", ErrInvalidQuote, i, err)
		}
		if _, exists := seenAssumptions[assumption.Name]; exists {
			return fmt.Errorf("%w: duplicate assumption %q", ErrInvalidQuote, assumption.Name)
		}
		seenAssumptions[assumption.Name] = struct{}{}
	}
	seenCapabilities := make(map[string]struct{}, len(q.RequiredCapabilities))
	for i, capability := range q.RequiredCapabilities {
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("%w: capability %d: %v", ErrInvalidQuote, i, err)
		}
		if _, exists := seenCapabilities[capability.Name]; exists {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidQuote, capability.Name)
		}
		seenCapabilities[capability.Name] = struct{}{}
	}
	if err := validatePolicyRefForQuote("policy", q.Policy); err != nil {
		return err
	}
	if q.Tariff.ID != "" || q.Tariff.Version != "" || q.Tariff.RaterID != "" {
		if err := validateRatingRefForQuote("tariff", q.Tariff); err != nil {
			return err
		}
	}
	if err := validateExposureQuoteSnapshotContext(q); err != nil {
		return err
	}
	if err := validateRouteTariffCarriage(q.RouteTariffMode, q.RouteTariffs); err != nil {
		return err
	}
	return nil
}

func (q ExposureQuote) Clone() ExposureQuote {
	out := q
	out.Subject = q.Subject.Clone()
	out.Assumptions = append([]QuoteAssumption(nil), q.Assumptions...)
	out.RequiredCapabilities = append([]EvidenceCapability(nil), q.RequiredCapabilities...)
	out.CreditUnits = cloneUnitBounds(q.CreditUnits)
	out.AllowanceUnits = cloneUnitBounds(q.AllowanceUnits)
	out.RouteTariffs = append([]RouteTariffBinding(nil), q.RouteTariffs...)
	out.TariffContent = cloneSnapshotContentRef(q.TariffContent)
	out.PolicyContent = cloneSnapshotContentRef(q.PolicyContent)
	out.QualifierSnapshotRef = cloneSnapshotContentRef(q.QualifierSnapshotRef)
	return out
}

func validateUnitBounds(bounds []UnitBound) error {
	seen := make(map[string]struct{}, len(bounds))
	for i, bound := range bounds {
		if err := bound.Validate(); err != nil {
			return fmt.Errorf("%w: unit bound %d: %v", ErrInvalidQuote, i, err)
		}
		if _, ok := seen[bound.Unit]; ok {
			return fmt.Errorf("%w: duplicate unit bound %q", ErrInvalidQuote, bound.Unit)
		}
		seen[bound.Unit] = struct{}{}
	}
	return nil
}

func cloneUnitBounds(bounds []UnitBound) []UnitBound {
	if bounds == nil {
		return nil
	}
	out := make([]UnitBound, len(bounds))
	for i, bound := range bounds {
		out[i] = bound.Clone()
	}
	return out
}

// StatementLine is one already-normalized provider statement line. Its charge
// identity is retained as a safe ID and references are store-scoped.
type StatementLine struct {
	ID              string                  `json:"id"`
	Revision        uint64                  `json:"revision"`
	Subject         metering.SubjectRef     `json:"subject"`
	Observation     metering.ObservationRef `json:"observation"`
	ChargeItemID    string                  `json:"charge_item_id"`
	Outcome         StatementLineOutcome    `json:"outcome,omitempty"`
	UnmatchedReason string                  `json:"unmatched_reason,omitempty"`
}

func (l StatementLine) Validate(store string) error {
	if err := validatePublicRef("statement line id", l.ID); err != nil {
		return fmt.Errorf("economics: invalid statement line: %v", err)
	}
	if l.Revision == 0 {
		return errors.New("economics: statement line revision required")
	}
	if err := l.Subject.Validate(); err != nil {
		return fmt.Errorf("economics: statement line subject: %v", err)
	}
	if l.Subject.Kind != metering.SubjectStatementLine {
		return errors.New("economics: statement line subject must be statement_line")
	}
	if l.Subject.StoreID != store {
		return errors.New("economics: statement line subject store mismatch")
	}
	if l.Outcome != "" && !l.Outcome.IsKnown() {
		return fmt.Errorf("economics: unknown statement line outcome %q", l.Outcome)
	}
	if l.Outcome == StatementLineUnmatched {
		if err := validatePublicRef("statement unmatched reason", l.UnmatchedReason); err != nil {
			return err
		}
		if !isZeroObservationRef(l.Observation) || l.ChargeItemID != "" {
			return errors.New("economics: unmatched statement line cannot carry a charge linkage")
		}
		return nil
	}
	if l.UnmatchedReason != "" {
		return errors.New("economics: matched statement line cannot carry unmatched reason")
	}
	if err := l.Observation.Validate(); err != nil {
		return fmt.Errorf("economics: statement line observation: %v", err)
	}
	if l.Observation.StoreID != store {
		return errors.New("economics: statement line observation store mismatch")
	}
	return validatePublicRef("statement charge item id", l.ChargeItemID)
}

// StatementBatch is a normalized statement import envelope. Raw statement
// bytes and provider-shaped fields deliberately have no public representation.
type StatementBatch struct {
	Version            uint32                 `json:"version"`
	ProviderAccountKey string                 `json:"provider_account_key"`
	StatementID        string                 `json:"statement_id"`
	Revision           uint64                 `json:"revision"`
	PeriodID           string                 `json:"period_id"`
	Subject            metering.SubjectRef    `json:"subject"`
	Observations       []metering.Observation `json:"observations,omitempty"`
	Lines              []StatementLine        `json:"lines"`
}

func (b StatementBatch) Validate() error {
	if b.Version == 0 {
		return errors.New("economics: statement batch version required")
	}
	for name, value := range map[string]string{"provider account key": b.ProviderAccountKey, "statement id": b.StatementID, "period id": b.PeriodID} {
		if err := validatePublicRef(name, value); err != nil {
			return err
		}
	}
	if b.Revision == 0 {
		return errors.New("economics: statement batch revision required")
	}
	if err := b.Subject.Validate(); err != nil {
		return fmt.Errorf("economics: statement subject: %v", err)
	}
	if len(b.Observations) > MaxRatingObservations || len(b.Lines) > MaxStatementLines {
		return errors.New("economics: statement batch bound exceeded")
	}
	for i, observation := range b.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("economics: statement observation %d: %v", i, err)
		}
		if observation.Origin != metering.OriginStatement {
			return fmt.Errorf("economics: statement observation %d must use statement origin", i)
		}
		if observation.Subject.StoreID != b.Subject.StoreID {
			return fmt.Errorf("economics: statement observation %d store mismatch", i)
		}
	}
	seen := make(map[string]struct{}, len(b.Lines))
	for i, line := range b.Lines {
		if err := line.Validate(b.Subject.StoreID); err != nil {
			return fmt.Errorf("economics: statement line %d: %v", i, err)
		}
		if _, ok := seen[line.ID]; ok {
			return fmt.Errorf("economics: duplicate statement line %q", line.ID)
		}
		seen[line.ID] = struct{}{}
	}
	if err := validateStatementBatchLinkage(b); err != nil {
		return err
	}
	return nil
}

// ImportResult reports identity-level outcomes without executing model calls.
type ImportResult struct {
	Accepted  []string `json:"accepted,omitempty"`
	Replayed  []string `json:"replayed,omitempty"`
	Unmatched []string `json:"unmatched,omitempty"`
	Rejected  []string `json:"rejected,omitempty"`
}

func (r ImportResult) Validate() error {
	count := len(r.Accepted) + len(r.Replayed) + len(r.Unmatched) + len(r.Rejected)
	if count > MaxImportResultIDs {
		return fmt.Errorf("economics: import result id bound exceeded")
	}
	return validateIDLists("import result", r.Accepted, r.Replayed, r.Unmatched, r.Rejected)
}

func validateIDLists(label string, groups ...[]string) error {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			if err := validatePublicRef(fmt.Sprintf("%s id", label), value); err != nil {
				return err
			}
			if _, ok := seen[value]; ok {
				return fmt.Errorf("economics: duplicate %s outcome id %q", label, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

// ReconciliationQuery identifies a bounded immutable read window.
type ReconciliationQuery struct {
	StoreID          string               `json:"store_id"`
	Subject          *metering.SubjectRef `json:"subject,omitempty"`
	Basis            ValuationBasis       `json:"basis,omitempty"`
	Currency         string               `json:"currency,omitempty"`
	AfterValuationID string               `json:"after_valuation_id,omitempty"`
	Limit            uint32               `json:"limit"`
}

func (q ReconciliationQuery) Validate() error {
	if err := validatePublicRef("reconciliation store id", q.StoreID); err != nil {
		return err
	}
	if q.Subject != nil {
		if err := q.Subject.Validate(); err != nil {
			return fmt.Errorf("economics: reconciliation subject: %v", err)
		}
		if q.Subject.StoreID != q.StoreID {
			return errors.New("economics: reconciliation subject store mismatch")
		}
	}
	if q.Basis != "" {
		if err := q.Basis.Validate(); err != nil {
			return err
		}
	}
	if q.Currency != "" {
		if _, err := NormalizeCurrency(q.Currency); err != nil {
			return err
		}
	}
	if q.AfterValuationID != "" {
		if err := validatePublicRef("after valuation id", q.AfterValuationID); err != nil {
			return err
		}
	}
	if q.Limit == 0 || q.Limit > 1024 {
		return errors.New("economics: reconciliation limit must be between 1 and 1024")
	}
	return nil
}

// ReconciliationPage is a typed result page; implementations may add details
// in later versions without exposing persistence handles.
type ReconciliationPage struct {
	Valuations []Valuation `json:"valuations,omitempty"`
	Next       string      `json:"next,omitempty"`
	Complete   bool        `json:"complete"`
}

func (p ReconciliationPage) Validate() error {
	if len(p.Valuations) > MaxReconciliationPage {
		return errors.New("economics: reconciliation page bound exceeded")
	}
	seen := make(map[string]struct{}, len(p.Valuations))
	for i, valuation := range p.Valuations {
		if err := valuation.Validate(); err != nil {
			return fmt.Errorf("economics: reconciliation valuation %d: %v", i, err)
		}
		if _, ok := seen[valuation.ID]; ok {
			return fmt.Errorf("economics: duplicate reconciliation valuation %q", valuation.ID)
		}
		seen[valuation.ID] = struct{}{}
	}
	if p.Next != "" {
		if err := validatePublicRef("reconciliation next", p.Next); err != nil {
			return err
		}
	}
	return nil
}

// Clone deep-copies page valuations for a consumer that needs local sorting.
func (p ReconciliationPage) Clone() ReconciliationPage {
	out := p
	if p.Valuations != nil {
		out.Valuations = make([]Valuation, len(p.Valuations))
		for i, valuation := range p.Valuations {
			out.Valuations[i] = valuation.Clone()
		}
	}
	return out
}

// SortLimits returns a copy sorted by stable name/unit identity. It is a
// helper for quote implementations and does not mutate caller-owned slices.
func SortLimits(in []Limit) []Limit {
	out := append([]Limit(nil), in...)
	slices.SortFunc(out, func(a, b Limit) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Unit, b.Unit)
	})
	return out
}
