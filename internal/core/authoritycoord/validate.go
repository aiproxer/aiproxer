package authoritycoord

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func validateCoordinatorDecision(d authority.Decision, reg authority.ProviderDescriptor, stage authority.Stage) error {
	if decisionHasHolds(d) && (d.Kind == authority.DecisionDeny || d.Kind == authority.DecisionAdvisory) {
		return errNonAllowWithHolds
	}
	return d.ValidateFor(reg, stage)
}

var errNonAllowWithHolds = fmt.Errorf("non-allow decision with holds")

func validatePreviewClamp(c authority.Clamp) error {
	switch c.Kind {
	case authority.ClampMaxOutputTokens:
		if c.Value < 0 {
			return fmt.Errorf("max_output_tokens clamp value must be non-negative")
		}
		return nil
	case authority.ClampMaxSpend:
		if !c.Money.Present {
			return fmt.Errorf("max_spend clamp requires present money")
		}
		return economics.ValidatePresentMoney(c.Money)
	default:
		return fmt.Errorf("unknown or unapplicable clamp kind %q", c.Kind)
	}
}

func validateSettlement(s authority.Settlement, submitted []string) error {
	return s.ValidateFor(submitted, metering.PerspectiveCustomer)
}

func validateAttemptSettlement(s authority.Settlement, submitted []string) error {
	return s.ValidateFor(submitted, metering.PerspectiveOperator)
}

func validateCoordinatorLease(d authority.LeaseDecision, in authority.LeaseAdmission, reg authority.ProviderDescriptor, now time.Time) error {
	if err := d.ValidateFor(in, reg); err != nil {
		return err
	}
	switch d.Kind {
	case authority.LeaseDeny:
		return nil
	case authority.LeaseAllow, authority.LeaseAdvisory, "":
		if !d.ExpiresAt.IsZero() && !d.ExpiresAt.After(now) {
			return fmt.Errorf("lease already expired")
		}
		for i, occ := range d.Leases {
			if !occ.ExpiresAt.IsZero() && !occ.ExpiresAt.After(now) {
				return fmt.Errorf("leases[%d]: already expired", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown lease decision kind %q", d.Kind)
	}
}

func validateCoordinatorLeaseRenewal(d authority.LeaseDecision, in authority.LeaseRenew, reg authority.ProviderDescriptor, now time.Time) error {
	if err := d.ValidateRenewalFor(in, reg); err != nil {
		return err
	}
	switch d.Kind {
	case authority.LeaseAllow:
		if !d.ExpiresAt.IsZero() && !d.ExpiresAt.After(now) {
			return fmt.Errorf("lease already expired")
		}
		for i, occ := range d.Leases {
			if !occ.ExpiresAt.IsZero() && !occ.ExpiresAt.After(now) {
				return fmt.Errorf("leases[%d]: already expired", i)
			}
		}
		return nil
	case authority.LeaseDeny, authority.LeaseAdvisory, "":
		return fmt.Errorf("renewal kind %q not applied", d.Kind)
	default:
		return fmt.Errorf("unknown lease decision kind %q", d.Kind)
	}
}

func decisionHasHolds(d authority.Decision) bool {
	if len(d.Reservations) > 0 {
		return true
	}
	return strings.TrimSpace(d.CompensationHandle) != ""
}

func advisoryEvidenceFrom(d authority.Decision) authority.SafeEvidence {
	ev := d.Evidence
	if strings.TrimSpace(ev.Category) == "" {
		ev.Category = "authority_advisory"
	}
	if strings.TrimSpace(ev.Code) == "" {
		ev.Code = "advisory_deny"
	}
	return ev
}
