package authority

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Validate checks Decision kind, reservations, clamps, and hold contradictions
// (requirements 4.1–4.4, 4.6).
func (d Decision) Validate() error {
	if d.Kind != "" && !d.Kind.IsKnown() {
		return fmt.Errorf("authority: unknown decision kind %q", d.Kind)
	}
	if d.Readiness != "" && !d.Readiness.IsKnown() {
		return fmt.Errorf("authority: unknown readiness %q", d.Readiness)
	}
	comp := strings.TrimSpace(d.CompensationHandle)
	if comp != "" && len(d.Reservations) == 0 {
		return fmt.Errorf("authority: standalone compensation handle without reservation")
	}
	hasHolds := len(d.Reservations) > 0 || comp != ""
	switch d.Kind {
	case DecisionDeny, DecisionAdvisory:
		if hasHolds {
			return fmt.Errorf("authority: non-allow decision with holds")
		}
	case DecisionAllow, "":
		// ok
	default:
		return fmt.Errorf("authority: unknown decision kind %q", d.Kind)
	}
	seen := make(map[string]struct{}, len(d.Reservations))
	for i, r := range d.Reservations {
		h := strings.TrimSpace(r.Handle)
		if h == "" {
			return fmt.Errorf("authority: reservation[%d]: empty handle", i)
		}
		if _, dup := seen[h]; dup {
			return fmt.Errorf("authority: reservation[%d]: duplicate handle %q", i, h)
		}
		seen[h] = struct{}{}
		if r.Kind != "" && !r.Kind.IsKnown() {
			return fmt.Errorf("authority: reservation[%d]: unknown kind %q", i, r.Kind)
		}
		if err := validateReservationAmounts(r); err != nil {
			return fmt.Errorf("authority: reservation[%d]: %w", i, err)
		}
	}
	for i, c := range d.Clamps {
		if err := validateClamp(c); err != nil {
			return fmt.Errorf("authority: clamp[%d]: %w", i, err)
		}
	}
	return nil
}

// ValidateFor checks Decision against the registration descriptor and stage
// (design External Result Validation; requirements 3.2, 4.1).
func (d Decision) ValidateFor(reg ProviderDescriptor, stage Stage) error {
	if err := reg.Validate(); err != nil {
		return fmt.Errorf("authority: registration: %w", err)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	got := strings.TrimSpace(d.ProviderID)
	want := strings.TrimSpace(reg.ID)
	if got != "" && got != want {
		return fmt.Errorf("authority: decision provider_id %q does not match registration %q", got, want)
	}
	if stage != "" {
		if !stage.IsKnown() {
			return fmt.Errorf("authority: unknown stage %q", stage)
		}
		if _, ok := AdmitPosture(reg, stage); !ok {
			return fmt.Errorf("authority: stage %q not declared on provider %q", stage, want)
		}
	}
	if d.Stage != "" && stage != "" && d.Stage != stage {
		return fmt.Errorf("authority: decision stage %q does not match expected %q", d.Stage, stage)
	}
	return nil
}

// ValidateFor checks Settlement against submitted provider-owned handles and
// perspective (design External Result Validation; requirements 4.5, 4.6, 4.9).
func (s Settlement) ValidateFor(handles []string, p metering.EconomicPerspective) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("authority: settlement perspective: %w", err)
	}
	if s.Kind == "" || !s.Kind.IsKnown() {
		return fmt.Errorf("authority: unknown settlement kind %q", s.Kind)
	}
	h := strings.TrimSpace(s.Handle)
	switch h {
	case "":
		if len(handles) > 0 && (s.Kind == SettlementFinal || s.Kind == SettlementPartial || s.Kind == SettlementEstimated) {
			return fmt.Errorf("authority: settlement handle required")
		}
	default:
		if !handleOwned(h, handles) {
			return fmt.Errorf("authority: foreign settlement handle %q", h)
		}
	}
	if err := economics.ValidatePresentMoney(s.Consumed); err != nil {
		return fmt.Errorf("authority: settlement consumed: %w", err)
	}
	if err := economics.ValidatePresentMoney(s.Released); err != nil {
		return fmt.Errorf("authority: settlement released: %w", err)
	}
	if s.Consumed.Present && s.Released.Present {
		a, errA := economics.NormalizeCurrency(s.Consumed.Currency)
		b, errB := economics.NormalizeCurrency(s.Released.Currency)
		if errA == nil && errB == nil && a != b {
			return fmt.Errorf("authority: mixed-currency settlement money")
		}
	}
	return nil
}

// Validate checks LeaseDecision kind and occupancy contradictions
// (requirements 4.1, 10.2).
func (d LeaseDecision) Validate() error {
	if d.Kind != "" && !d.Kind.IsKnown() {
		return fmt.Errorf("authority: unknown lease decision kind %q", d.Kind)
	}
	if d.Readiness != "" && !d.Readiness.IsKnown() {
		return fmt.Errorf("authority: unknown readiness %q", d.Readiness)
	}
	switch d.Kind {
	case LeaseDeny:
		if strings.TrimSpace(d.LeaseID) != "" || len(d.Leases) > 0 {
			return fmt.Errorf("authority: deny lease decision must not claim occupancy")
		}
		if d.Generation != 0 || d.RemainingSlots != 0 {
			return fmt.Errorf("authority: deny lease decision has contradictory occupancy fields")
		}
		return nil
	case LeaseAllow, LeaseAdvisory, "":
		if d.Kind == LeaseAllow {
			if strings.TrimSpace(d.LeaseID) == "" && len(d.Leases) == 0 {
				return fmt.Errorf("authority: allow lease decision requires lease id")
			}
			if d.Generation <= 0 {
				return fmt.Errorf("authority: allow lease decision requires positive generation")
			}
			if d.ExpiresAt.IsZero() {
				return fmt.Errorf("authority: allow lease decision requires expires_at")
			}
		}
		if d.Generation < 0 {
			return fmt.Errorf("authority: negative lease generation")
		}
		if d.RemainingSlots < 0 {
			return fmt.Errorf("authority: negative remaining slots")
		}
		if d.TTL > 0 && d.RenewBefore > 0 && d.RenewBefore > d.TTL {
			return fmt.Errorf("authority: renew_before exceeds ttl")
		}
		if !d.ExpiresAt.IsZero() && d.TTL < 0 {
			return fmt.Errorf("authority: negative lease ttl")
		}
		if primary := strings.TrimSpace(d.LeaseID); primary != "" && len(d.Leases) > 0 {
			found := false
			for _, occ := range d.Leases {
				if strings.TrimSpace(occ.LeaseID) == primary {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("authority: primary lease id missing from occupancy set")
			}
		}
		for i, occ := range d.Leases {
			if strings.TrimSpace(occ.LeaseID) == "" {
				return fmt.Errorf("authority: leases[%d]: empty lease id", i)
			}
			if occ.Generation < 0 {
				return fmt.Errorf("authority: leases[%d]: negative generation", i)
			}
			if d.Kind == LeaseAllow && occ.ExpiresAt.IsZero() {
				return fmt.Errorf("authority: leases[%d]: expires_at required", i)
			}
			if occ.TTL > 0 && occ.RenewBefore > 0 && occ.RenewBefore > occ.TTL {
				return fmt.Errorf("authority: leases[%d]: renew_before exceeds ttl", i)
			}
			if occ.TTL < 0 {
				return fmt.Errorf("authority: leases[%d]: negative ttl", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("authority: unknown lease decision kind %q", d.Kind)
	}
}

// ValidateFor checks LeaseDecision against admission input and registration
// (design External Result Validation; requirements 4.1, 10.2).
// Expiry relative to wall/evaluation clock is enforced by the coordinator Now
// seam, not here — public ValidateFor stays deterministic.
func (d LeaseDecision) ValidateFor(in LeaseAdmission, reg ProviderDescriptor) error {
	if err := reg.Validate(); err != nil {
		return fmt.Errorf("authority: registration: %w", err)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if _, ok := AdmitPosture(reg, StageLeaseAdmit); !ok {
		return fmt.Errorf("authority: stage %q not declared on provider %q", StageLeaseAdmit, strings.TrimSpace(reg.ID))
	}
	wantRule := strings.TrimSpace(in.RuleID)
	if wantRule != "" {
		for i, occ := range d.Leases {
			got := strings.TrimSpace(occ.RuleID)
			if got != "" && got != wantRule {
				return fmt.Errorf("authority: leases[%d]: rule_id %q does not match admission %q", i, got, wantRule)
			}
		}
	}
	if in.TTL > 0 && d.TTL > 0 && d.TTL > in.TTL {
		return fmt.Errorf("authority: lease ttl exceeds admission ttl")
	}
	return nil
}

// ValidateRenewalFor checks LeaseDecision against a renew request and registration
// (design External Result Validation; requirements 4.1, 10.2). Single-lease renew
// requires occupancy IDs to match LeaseID. When SetID is set, the complete set
// shape is validated and member lease IDs may differ. Generation may equal
// ExpectedGeneration for TTL-only expiry extension. Expiry relative to wall clock
// is enforced by the coordinator Now seam, not here.
func (d LeaseDecision) ValidateRenewalFor(in LeaseRenew, reg ProviderDescriptor) error {
	if err := reg.Validate(); err != nil {
		return fmt.Errorf("authority: registration: %w", err)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if _, ok := AdmitPosture(reg, StageLeaseAdmit); !ok {
		return fmt.Errorf("authority: stage %q not declared on provider %q", StageLeaseAdmit, strings.TrimSpace(reg.ID))
	}
	wantSet := strings.TrimSpace(in.SetID)
	wantID := strings.TrimSpace(in.LeaseID)
	if wantSet == "" && wantID == "" {
		return fmt.Errorf("authority: renew lease_id or set_id required")
	}
	switch d.Kind {
	case LeaseDeny:
		return nil
	case LeaseAllow, LeaseAdvisory, "":
		if wantSet != "" {
			gotSet := strings.TrimSpace(d.SetID)
			if gotSet != "" && gotSet != wantSet {
				return fmt.Errorf("authority: renewal set_id %q does not match %q", gotSet, wantSet)
			}
			if len(d.Leases) == 0 && strings.TrimSpace(d.LeaseID) == "" {
				return fmt.Errorf("authority: set renewal missing members")
			}
			seen := make(map[string]struct{}, len(d.Leases))
			for i, occ := range d.Leases {
				occID := strings.TrimSpace(occ.LeaseID)
				if occID == "" {
					return fmt.Errorf("authority: leases[%d]: empty lease_id", i)
				}
				if _, dup := seen[occID]; dup {
					return fmt.Errorf("authority: leases[%d]: duplicate lease_id %q", i, occID)
				}
				seen[occID] = struct{}{}
			}
			if d.Kind == LeaseAllow && in.ExpectedGeneration > 0 && d.Generation > 0 && d.Generation < in.ExpectedGeneration {
				return fmt.Errorf("authority: renewal generation %d is stale for expected %d", d.Generation, in.ExpectedGeneration)
			}
			break
		}
		gotID := strings.TrimSpace(d.LeaseID)
		if gotID != "" && gotID != wantID {
			return fmt.Errorf("authority: renewal lease_id %q does not match %q", gotID, wantID)
		}
		if gotID == "" {
			found := false
			for _, occ := range d.Leases {
				if strings.TrimSpace(occ.LeaseID) == wantID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("authority: renewal missing lease_id %q", wantID)
			}
		}
		seen := make(map[string]struct{}, len(d.Leases))
		for i, occ := range d.Leases {
			occID := strings.TrimSpace(occ.LeaseID)
			if occID != wantID {
				return fmt.Errorf("authority: leases[%d]: foreign lease_id %q for renew %q", i, occID, wantID)
			}
			if _, dup := seen[occID]; dup {
				return fmt.Errorf("authority: leases[%d]: duplicate lease_id %q", i, occID)
			}
			seen[occID] = struct{}{}
			if in.ExpectedGeneration > 0 && occ.Generation > 0 && occ.Generation < in.ExpectedGeneration {
				return fmt.Errorf("authority: leases[%d]: renewal generation %d is stale for expected %d", i, occ.Generation, in.ExpectedGeneration)
			}
		}
		if d.Kind == LeaseAllow && in.ExpectedGeneration > 0 && d.Generation > 0 && d.Generation < in.ExpectedGeneration {
			return fmt.Errorf("authority: renewal generation %d is stale for expected %d", d.Generation, in.ExpectedGeneration)
		}
	default:
		return fmt.Errorf("authority: unknown lease decision kind %q", d.Kind)
	}
	wantRule := strings.TrimSpace(in.RuleID)
	if wantRule != "" {
		for i, occ := range d.Leases {
			got := strings.TrimSpace(occ.RuleID)
			if got != "" && got != wantRule {
				return fmt.Errorf("authority: leases[%d]: rule_id %q does not match renew %q", i, got, wantRule)
			}
		}
	}
	if in.TTL > 0 && d.TTL > 0 && d.TTL > in.TTL {
		return fmt.Errorf("authority: lease ttl exceeds renew ttl")
	}
	return nil
}

// OwnedFinalSettlement returns SettlementFinal echoing the first submitted
// owned handle when present (providers must identify an owned handle).
func OwnedFinalSettlement(handles []string) Settlement {
	s := Settlement{Kind: SettlementFinal}
	if len(handles) > 0 {
		s.Handle = strings.TrimSpace(handles[0])
	}
	return s
}

func validateReservationAmounts(r Reservation) error {
	qPresent := r.Quantity != nil && r.Quantity.Present
	mPresent := r.Money != nil && r.Money.Present
	switch {
	case qPresent && mPresent:
		return fmt.Errorf("exactly one of quantity or money must be present")
	case !qPresent && !mPresent:
		return fmt.Errorf("exactly one of quantity or money must be present")
	case qPresent:
		if r.Quantity.Value < 0 {
			return fmt.Errorf("negative quantity")
		}
		return nil
	default:
		return economics.ValidatePresentMoney(*r.Money)
	}
}

func validateClamp(c Clamp) error {
	if !c.Kind.IsKnown() {
		return fmt.Errorf("unknown or unapplicable clamp kind %q", c.Kind)
	}
	switch c.Kind {
	case ClampMaxOutputTokens:
		if c.Value < 0 {
			return fmt.Errorf("max_output_tokens clamp value must be non-negative")
		}
		return nil
	case ClampMaxSpend:
		if !c.Money.Present {
			return fmt.Errorf("max_spend clamp requires present money")
		}
		return economics.ValidatePresentMoney(c.Money)
	case ClampOther:
		return nil
	default:
		return fmt.Errorf("unknown or unapplicable clamp kind %q", c.Kind)
	}
}

func handleOwned(handle string, submitted []string) bool {
	for _, sub := range submitted {
		if strings.TrimSpace(sub) == handle {
			return true
		}
	}
	return false
}
