package aipsdk_test

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestValidateRegistrationsRejectsDuplicatePluginIDs(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "openai-responses", Kind: aipsdk.PluginKindFrontend},
		{ID: "openai-responses", Kind: aipsdk.PluginKindFrontend},
	}, nil)

	if err == nil {
		t.Fatal("expected duplicate registration error")
	}

	if _, ok := errors.AsType[*aipsdk.DuplicateRegistrationError](err); !ok {
		t.Fatalf("expected DuplicateRegistrationError, got %T", err)
	}
	if !errors.Is(err, aipsdk.ErrDuplicateRegistration) {
		t.Fatal("expected error to unwrap to ErrDuplicateRegistration")
	}
	if got := aipsdk.ErrDuplicateRegistration.Error(); got != "aipsdk: duplicate plugin registration" {
		t.Fatalf("ErrDuplicateRegistration = %q", got)
	}
}

func TestValidateRegistrationsRejectsMissingID(t *testing.T) {
	t.Parallel()
	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{{Kind: aipsdk.PluginKindFrontend}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "aipsdk: plugin registration id is required" {
		t.Fatalf("error = %q", got)
	}
}

func TestValidateRegistrationsRejectsMissingKind(t *testing.T) {
	t.Parallel()
	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{{ID: "frontend-1"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "aipsdk: plugin registration kind is required for \"frontend-1\"" {
		t.Fatalf("error = %q", got)
	}
}

func TestValidateRegistrationsRejectsMissingMandatoryPlugin(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "openai-responses", Kind: aipsdk.PluginKindFrontend},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "anthropic"}})

	if err == nil {
		t.Fatal("expected missing requirement error")
	}

	if _, ok := errors.AsType[*aipsdk.MissingRequirementError](err); !ok {
		t.Fatalf("expected MissingRequirementError, got %T", err)
	}
}

func TestValidateRegistrationsAcceptsOpaqueConfigPayloads(t *testing.T) {
	t.Parallel()

	node := yaml.Node{Kind: yaml.MappingNode}
	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{
			ID:      "submit-noop",
			Kind:    aipsdk.PluginKindFeature,
			Enabled: true,
			Config: aipsdk.ConfigPayload{
				Node: node,
			},
		},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindFeature, ID: "submit-noop"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegistrationsRejectsDisabledMandatoryFrontend(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "fe", Kind: aipsdk.PluginKindFrontend, Enabled: false},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindFrontend, ID: "fe"}})
	if err == nil {
		t.Fatal("expected disabled mandatory error")
	}
	if _, ok := errors.AsType[*aipsdk.DisabledMandatoryPluginError](err); !ok {
		t.Fatalf("expected DisabledMandatoryPluginError, got %T", err)
	}
}

func TestValidateRegistrationsAllowsDisabledMandatoryBackend(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "be", Kind: aipsdk.PluginKindBackend, Enabled: false},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "be"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegistrations_backendMandatoryMatchesFactoryInstance(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "openai-primary", FactoryKind: "openai-responses", Kind: aipsdk.PluginKindBackend, Enabled: true},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "openai-responses"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegistrations_backendMandatoryUsesRequirementRegistryFactoryID(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "p1", FactoryKind: "openai-responses", Kind: aipsdk.PluginKindBackend, Enabled: true},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "ignored", RegistryFactoryID: "openai-responses"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegistrations_backendMandatoryMissingFactory(t *testing.T) {
	t.Parallel()

	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "x", FactoryKind: "gemini", Kind: aipsdk.PluginKindBackend, Enabled: true},
	}, []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "openai-responses"}})
	if err == nil {
		t.Fatal("expected error")
	}
}
