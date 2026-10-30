package metering

import "fmt"

// PaymentPartyKind identifies who is economically responsible for a reported
// charge. Empty is the absent/unknown state and is never interpreted as the
// operator by a consumer.
type PaymentPartyKind string

const (
	PaymentPartyUnknown     PaymentPartyKind = "unknown"
	PaymentPartyOperator    PaymentPartyKind = "operator"
	PaymentPartyCustomer    PaymentPartyKind = "customer"
	PaymentPartyUnallocated PaymentPartyKind = "unallocated"
)

func (k PaymentPartyKind) IsKnown() bool {
	switch k {
	case PaymentPartyUnknown, PaymentPartyOperator, PaymentPartyCustomer, PaymentPartyUnallocated:
		return true
	default:
		return false
	}
}

// PaymentParty is a provider-neutral payer classification. ID is an optional
// safe owner/account identity for known parties; no payer policy is evaluated
// while validating or decoding this DTO.
type PaymentParty struct {
	Kind PaymentPartyKind `json:"kind,omitempty"`
	ID   string           `json:"id,omitempty"`
}

// Validate accepts an absent party as unknown so incomplete provider evidence
// can be retained. Explicit unknown/unallocated values cannot carry an ID that
// might be mistaken for an attributable owner.
func (p PaymentParty) Validate() error {
	if p.Kind == "" {
		if p.ID != "" {
			return fmt.Errorf("metering: absent payment party cannot carry an ID")
		}
		return nil
	}
	if !p.Kind.IsKnown() {
		return fmt.Errorf("metering: unknown payment party kind %q", p.Kind)
	}
	if (p.Kind == PaymentPartyUnknown || p.Kind == PaymentPartyUnallocated) && p.ID != "" {
		return fmt.Errorf("metering: %s payment party cannot carry an owner ID", p.Kind)
	}
	if p.ID != "" {
		if err := validateIdentityText("payment party id", p.ID, MaxSchemaIDBytes); err != nil {
			return fmt.Errorf("metering: %v", err)
		}
	}
	return nil
}

func (p PaymentParty) IsUnknown() bool {
	return p.Kind == "" || p.Kind == PaymentPartyUnknown || p.Kind == PaymentPartyUnallocated
}
