package aipsdk

import (
	"errors"
	"fmt"
	"strings"
)

var ErrDuplicateRegistration = errors.New("aipsdk: duplicate plugin registration")

// Requirement defines a mandatory plugin that must exist in a bundle.
type Requirement struct {
	Kind PluginKind
	// ID is the mandatory instance id for frontends and features (matches Registration.ID).
	// For backends, ID is the required registry factory key unless RegistryFactoryID is set.
	ID string
	// RegistryFactoryID selects the bundled factory when it differs from ID.
	// Empty means the factory key equals ID for ValidateBundledFactories and backend mandatory checks.
	RegistryFactoryID string
}

// DuplicateRegistrationError reports a conflicting plugin identity.
type DuplicateRegistrationError struct {
	Kind PluginKind
	ID   string
}

func (e *DuplicateRegistrationError) Error() string {
	return fmt.Sprintf("%v: %s/%s", ErrDuplicateRegistration, e.Kind, e.ID)
}

func (e *DuplicateRegistrationError) Unwrap() error {
	return ErrDuplicateRegistration
}

// MissingRequirementError reports a missing mandatory plugin.
type MissingRequirementError struct {
	Kind PluginKind
	ID   string
}

func (e *MissingRequirementError) Error() string {
	return fmt.Sprintf("missing mandatory plugin: %s/%s", e.Kind, e.ID)
}

// DisabledMandatoryPluginError reports a mandatory plugin ID listed but disabled.
type DisabledMandatoryPluginError struct {
	Kind PluginKind
	ID   string
}

func (e *DisabledMandatoryPluginError) Error() string {
	return fmt.Sprintf("mandatory plugin %s/%s is present but disabled", e.Kind, e.ID)
}

func requirementRegistryFactoryKey(r Requirement) string {
	if s := strings.TrimSpace(r.RegistryFactoryID); s != "" {
		return s
	}
	return strings.TrimSpace(r.ID)
}

// ValidateRegistrations checks duplicate IDs inside a kind and verifies mandatory entries.
// For each mandatory requirement, a matching registration must exist; if Kind is Frontend
// or Feature, Enabled must be true (reference config may list backends with enabled: false).
func ValidateRegistrations(registrations []Registration, required []Requirement) error {
	byKindID := make(map[PluginKind]map[string]Registration, len(registrations))

	for _, registration := range registrations {
		if registration.ID == "" {
			return fmt.Errorf("aipsdk: plugin registration id is required")
		}
		if registration.Kind == "" {
			return fmt.Errorf("aipsdk: plugin registration kind is required for %q", registration.ID)
		}

		ids := byKindID[registration.Kind]
		if ids == nil {
			ids = map[string]Registration{}
			byKindID[registration.Kind] = ids
		}

		if _, exists := ids[registration.ID]; exists {
			return &DuplicateRegistrationError{Kind: registration.Kind, ID: registration.ID}
		}

		ids[registration.ID] = registration
	}

	for _, requirement := range required {
		if requirement.Kind == PluginKindBackend {
			key := requirementRegistryFactoryKey(requirement)
			var reg *Registration
			for i := range registrations {
				r := &registrations[i]
				if r.Kind != PluginKindBackend {
					continue
				}
				if r.RegistryFactoryKey() == key {
					reg = r
					break
				}
			}
			if reg == nil {
				return &MissingRequirementError{Kind: requirement.Kind, ID: requirement.ID}
			}
			continue
		}

		ids := byKindID[requirement.Kind]
		reg, ok := ids[requirement.ID]
		if !ok {
			return &MissingRequirementError{Kind: requirement.Kind, ID: requirement.ID}
		}
		// Reference distribution keeps backend rows in config with enabled: false until
		// an operator wires credentials; only frontends and feature hooks must be active.
		if !reg.Enabled && (requirement.Kind == PluginKindFrontend || requirement.Kind == PluginKindFeature) {
			return &DisabledMandatoryPluginError{Kind: requirement.Kind, ID: requirement.ID}
		}
	}

	return nil
}
