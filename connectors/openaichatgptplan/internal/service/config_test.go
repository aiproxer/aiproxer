package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestParseConfigYAML_ExposesPolicyModeAndRefusesAnEndpointOverride proves the
// execution-phase operator surface is the admission posture ONLY.
//
// The SIWC preview policy has two postures, and task 4.3 deliberately left the
// operator wiring to this task: the policy was implemented with a mode
// parameter, so the connector's configuration is where the operator chooses
// it. The Responses endpoint is NOT an operator option: requirement 4.2 sends
// the selected registration's bearer credential only to the documented public
// resource, so no configuration value may replace it - not a private route, not
// a look-alike host, and not a loopback reference server.
//
// The test fails on an ASSERTION, not on a missing symbol: today the strict
// decoder accepts responses_base_url for the public origin and for a loopback
// address.
func TestParseConfigYAML_ExposesPolicyModeAndRefusesAnEndpointOverride(t *testing.T) {
	t.Parallel()

	if _, err := ParseConfigYAML([]byte("policy_mode: strict\n")); err != nil {
		t.Fatalf("ParseConfigYAML must accept the strict operator posture: %v", err)
	}
	if _, err := ParseConfigYAML([]byte("policy_mode: interoperable\n")); err != nil {
		t.Fatalf("ParseConfigYAML must accept the interoperable posture: %v", err)
	}

	// The endpoint override is not part of the shipped surface, so every
	// spelling of it - including the documented public origin and the
	// loopback reference server the contract tests use - is refused by name.
	for _, endpoint := range []string{
		"http://127.0.0.1:9",
		"https://api.openai.com",
		"https://example.invalid",
		"http://backend-api.example.invalid/v1",
	} {
		_, err := ParseConfigYAML([]byte("responses_base_url: " + endpoint + "\n"))
		if err == nil {
			t.Fatalf("ParseConfigYAML accepted responses_base_url %q; the shipped configuration must not name a Responses endpoint", endpoint)
		}
		if !errors.Is(err, ErrResponsesBaseURLNotConfigurable) {
			t.Fatalf("the refusal of %q must be the typed endpoint refusal: %v", endpoint, err)
		}
		if !strings.Contains(err.Error(), "responses_base_url") {
			t.Fatalf("the refusal of %q must name the configuration key: %v", endpoint, err)
		}
	}

	// An unknown posture is a configuration error, never a fallback to the
	// default: the strict operator would otherwise receive the degradation
	// they asked to refuse.
	if _, err := ParseConfigYAML([]byte("policy_mode: loose\n")); err == nil {
		t.Fatal("ParseConfigYAML must refuse an unknown policy_mode")
	} else if !strings.Contains(err.Error(), "policy") {
		t.Fatalf("the unknown-posture failure must name the posture: %v", err)
	}
}

// TestConfigure_RefusesAnOperatorConfiguredReferenceEndpoint drives the shipped
// production constructor: a configuration that names a Responses endpoint must
// be refused before the instance exists, so the selected registration's bearer
// token cannot be routed to an operator-chosen listener.
//
// The refusal is the J2 remediation: reference routing remains available to the
// connector's tests through the test-only construction path, never through
// YAML handed to Configure.
func TestConfigure_RefusesAnOperatorConfiguredReferenceEndpoint(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, nil)

	for _, endpoint := range []string{provider.baseURL(), "https://api.openai.com"} {
		raw := []byte("responses_base_url: " + endpoint + "\n")
		configured, err := New().Configure(context.Background(), backendplugin.ConfigureRequest{
			InstanceID:  "endpoint-override-instance",
			FactoryKind: FactoryKind,
			ConfigYAML:  raw,
		})
		if err == nil {
			t.Fatalf("production Configure accepted responses_base_url %q; the bearer credential must only reach the documented public resource", endpoint)
		}
		if configured != nil {
			t.Fatalf("a refused configuration must not build an instance (%q)", endpoint)
		}
		if !errors.Is(err, ErrResponsesBaseURLNotConfigurable) {
			t.Fatalf("the refusal of %q must be the typed endpoint refusal: %v", endpoint, err)
		}
		if !strings.Contains(err.Error(), "responses_base_url") {
			t.Fatalf("the refusal of %q must name the configuration key: %v", endpoint, err)
		}
	}
}
