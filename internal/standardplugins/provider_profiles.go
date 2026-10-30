package standardplugins

import (
	"fmt"
	"net/url"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
)

// ProviderProfileCatalog is the immutable standard-distribution profile input.
// It is deliberately separate from user PluginConfig rows: private custom
// compatible instances remain accepted without entering this catalog.
func ProviderProfileCatalog() (*providerprofiles.Catalog, error) {
	return providerprofiles.EmbeddedCatalog()
}

// ValidateProviderProfiles performs startup/check-config validation only. It
// does not build adapters, resolve secrets, discover models, or start processes.
func ValidateProviderProfiles() error {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return err
	}
	if _, err := catalog.CompileAll(); err != nil {
		return fmt.Errorf("standard provider profiles: %w", err)
	}
	return nil
}

func PrepareProviderProfiles(cfg *config.Config) (*PreparedProviderProfiles, error) {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	return PrepareProviderProfilesWithCatalog(cfg, catalog)
}

func PrepareProviderProfilesWithCatalog(cfg *config.Config, catalog *providerprofiles.Catalog) (*PreparedProviderProfiles, error) {
	prepared, err := ExpandProviderProfileRowsWithCatalog(cfg, catalog)
	if err != nil {
		return nil, err
	}
	if _, err := catalog.CompileAll(); err != nil {
		return nil, fmt.Errorf("standard provider profiles: %w", err)
	}
	return prepared, nil
}

// ProviderProfileDiagnostics is the bounded, secret-safe source projection used
// by composition diagnostics. It intentionally reports identity, family, and
// endpoint origin only; it never includes credentials or raw profile data.
type ProviderProfileDiagnostic struct {
	ID        string
	Family    string
	Endpoint  string
	Auth      bool
	Tokenizer string
}

func ProviderProfileDiagnostics() ([]ProviderProfileDiagnostic, error) {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	profiles := catalog.Profiles()
	out := make([]ProviderProfileDiagnostic, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, ProviderProfileDiagnostic{ID: profile.ID, Family: string(profile.Family), Endpoint: endpointIdentity(profile.Endpoint.BaseURL), Auth: profile.Auth.Mode != providerprofiles.AuthNone, Tokenizer: profile.Tokenizer.TokenizerID})
	}
	return out, nil
}

func ProjectProviderProfileDiagnostics(cfg *config.Config) []diag.InstanceDiagnostic {
	if cfg == nil {
		return nil
	}
	hasProfileBackend := false
	for _, b := range cfg.Plugins.Backends {
		if b.FactoryID() == "provider-profile" {
			hasProfileBackend = true
			break
		}
	}
	if !hasProfileBackend {
		return nil
	}
	diagnostics, err := ProviderProfileDiagnostics()
	if err != nil {
		return []diag.InstanceDiagnostic{{
			ID:          "embedded_provider_profile_catalog",
			InstanceID:  "embedded_provider_profile_catalog",
			FactoryKind: "provider-profile",
			Origin:      "embedded_provider_profile_catalog",
			Enabled:     false,
			ConfigError: err.Error(),
		}}
	}
	if len(diagnostics) == 0 {
		return nil
	}
	out := make([]diag.InstanceDiagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		out = append(out, diag.InstanceDiagnostic{
			ID:          d.ID,
			InstanceID:  d.ID,
			FactoryKind: d.Family,
			Family:      d.Family,
			Origin:      "embedded_provider_profile_catalog",
			// A catalog row is available in the binary, not a configured runtime
			// instance. Configured/expanded rows are projected separately by the
			// compatible-family projector.
			Enabled: false,
			Profile: d.ID,
			Details: []diag.SafeField{
				{Key: "endpoint_identity", Value: d.Endpoint},
				{Key: "auth_configured", Value: fmt.Sprintf("%v", d.Auth)},
				{Key: "tokenizer_id", Value: d.Tokenizer},
			},
		})
	}
	return out
}

func endpointIdentity(raw string) string {
	// Validation has already checked the URL. Keep only scheme/host so diagnostics
	// cannot accidentally turn into a configuration dump.
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
