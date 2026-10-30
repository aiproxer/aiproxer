package authority

import (
	"fmt"
	"strings"
)

// RequestPriority is the deterministic evaluation class for request authorities
// (requirement 3.3; design Descriptor-Bound Registrations).
type RequestPriority string

const (
	RequestPriorityConcurrency     RequestPriority = "concurrency"
	RequestPriorityCreditWallet    RequestPriority = "credit_wallet"
	RequestPriorityQuotaBudgetRate RequestPriority = "quota_budget_rate"
	RequestPriorityAdvisory        RequestPriority = "advisory"
)

// IsKnown reports whether p is a documented request priority.
func (p RequestPriority) IsKnown() bool {
	switch p {
	case RequestPriorityConcurrency, RequestPriorityCreditWallet, RequestPriorityQuotaBudgetRate, RequestPriorityAdvisory:
		return true
	}
	return false
}

// Validate returns an error when p is not a known request priority.
func (p RequestPriority) Validate() error {
	if !p.IsKnown() {
		return fmt.Errorf("authority: unknown request priority %q", p)
	}
	return nil
}

// AttemptPriority is the deterministic evaluation class for attempt authorities
// (requirement 3.4; design Descriptor-Bound Registrations).
type AttemptPriority string

const (
	AttemptPriorityHardSpend AttemptPriority = "hard_spend"
	AttemptPriorityQuotaRate AttemptPriority = "quota_rate"
	AttemptPriorityAdvisory  AttemptPriority = "advisory"
)

// IsKnown reports whether p is a documented attempt priority.
func (p AttemptPriority) IsKnown() bool {
	switch p {
	case AttemptPriorityHardSpend, AttemptPriorityQuotaRate, AttemptPriorityAdvisory:
		return true
	}
	return false
}

// Validate returns an error when p is not a known attempt priority.
func (p AttemptPriority) Validate() error {
	if !p.IsKnown() {
		return fmt.Errorf("authority: unknown attempt priority %q", p)
	}
	return nil
}

// RequestRegistration binds a provider descriptor, priority, and request
// authority instance into one production registration (design D4).
type RequestRegistration struct {
	Descriptor ProviderDescriptor
	Priority   RequestPriority
	Provider   RequestProvider
}

// Validate checks descriptor posture, priority, and provider instance binding.
func (r RequestRegistration) Validate() error {
	if isNilValue(r.Provider) {
		return fmt.Errorf("authority: request registration provider required")
	}
	if err := r.Priority.Validate(); err != nil {
		return err
	}
	if err := r.Descriptor.Validate(); err != nil {
		return err
	}
	if r.Descriptor.EffectiveKind() != ProviderKindAuthority {
		return fmt.Errorf("authority: request registration requires authority kind")
	}
	if _, ok := AdmitPosture(r.Descriptor, StageRequestAdmit); !ok {
		return fmt.Errorf("authority: request registration descriptor missing request admit posture")
	}
	if err := describerMatches(r.Provider, r.Descriptor); err != nil {
		return err
	}
	return nil
}

// AttemptRegistration binds a provider descriptor, priority, and attempt
// authority instance into one production registration (design D4).
type AttemptRegistration struct {
	Descriptor ProviderDescriptor
	Priority   AttemptPriority
	Provider   AttemptProvider
}

// Validate checks descriptor posture, priority, and provider instance binding.
func (r AttemptRegistration) Validate() error {
	if isNilValue(r.Provider) {
		return fmt.Errorf("authority: attempt registration provider required")
	}
	if err := r.Priority.Validate(); err != nil {
		return err
	}
	if err := r.Descriptor.Validate(); err != nil {
		return err
	}
	if r.Descriptor.EffectiveKind() != ProviderKindAuthority {
		return fmt.Errorf("authority: attempt registration requires authority kind")
	}
	if _, ok := AdmitPosture(r.Descriptor, StageAttemptAdmit); !ok {
		return fmt.Errorf("authority: attempt registration descriptor missing attempt admit posture")
	}
	if err := describerMatches(r.Provider, r.Descriptor); err != nil {
		return err
	}
	return nil
}

// ConcurrencyRegistration binds a provider descriptor and concurrency provider
// instance into one production registration (design D4).
type ConcurrencyRegistration struct {
	Descriptor ProviderDescriptor
	Provider   ConcurrencyProvider
}

// Validate checks descriptor posture and provider instance binding.
func (r ConcurrencyRegistration) Validate() error {
	if isNilValue(r.Provider) {
		return fmt.Errorf("authority: concurrency registration provider required")
	}
	if err := r.Descriptor.Validate(); err != nil {
		return err
	}
	if r.Descriptor.EffectiveKind() != ProviderKindAuthority {
		return fmt.Errorf("authority: concurrency registration requires authority kind")
	}
	if _, ok := AdmitPosture(r.Descriptor, StageLeaseAdmit); !ok {
		return fmt.Errorf("authority: concurrency registration descriptor missing lease admit posture")
	}
	if err := describerMatches(r.Provider, r.Descriptor); err != nil {
		return err
	}
	return nil
}

func describerMatches(provider any, desc ProviderDescriptor) error {
	d, ok := provider.(Describer)
	if !ok {
		return nil
	}
	got := strings.TrimSpace(d.Describe().ID)
	want := strings.TrimSpace(desc.ID)
	if got != want {
		return fmt.Errorf("authority: describer id %q mismatches registration descriptor id %q", got, want)
	}
	return nil
}

// AdmitPosture returns the admit-stage posture for the registration family.
// Missing admit posture returns zero values and false.
func AdmitPosture(d ProviderDescriptor, admit Stage) (StagePosture, bool) {
	for _, p := range d.Postures {
		if p.Stage == admit {
			return p, true
		}
	}
	return StagePosture{}, false
}

// RequireAdmitPosture returns the admit-stage posture or an error when absent.
func RequireAdmitPosture(d ProviderDescriptor, admit Stage) (StagePosture, error) {
	p, ok := AdmitPosture(d, admit)
	if !ok {
		return StagePosture{}, fmt.Errorf("authority: missing admit posture for stage %q", admit)
	}
	return p, nil
}
