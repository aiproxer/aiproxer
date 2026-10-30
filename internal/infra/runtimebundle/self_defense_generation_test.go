package runtimebundle_test

import (
	"context"
	"net/http"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// selfDefenseResolverConfig is a GeoIP-shaped client-address trust block with no
// country or CIDR enforcement at all: the compiled fixed policy is nil while the
// resolver source and trusted prefixes are fully configured.
func selfDefenseResolverConfig(cfg *config.Config) {
	cfg.Access.GeoIP = config.GeoIPConfig{
		ClientIP: config.GeoIPClientConfig{
			Source:         config.ClientIPSourceXForwardedFor,
			TrustedProxies: []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
	}
}

func selfDefenseGenerationInput(t *testing.T, cfg *config.Config) (*runtimebundle.ProcessServices, httpcontract.StandardHTTPInput) {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Compose: selfDefenseStubComposer,
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	return ps, cand.StandardHTTPInput(cfg, nil, "")
}

// TestGenerationProjectsSelfDefenseWithoutGeoIPEnforcementPolicy proves the
// shared client-address trust boundary: self-defense receives the already
// compiled GeoIP resolver source and trusted prefixes even when the fixed
// GeoIP policy is nil, so it never requires a country database and never creates
// a second forwarding-trust configuration.
func TestGenerationProjectsSelfDefenseWithoutGeoIPEnforcementPolicy(t *testing.T) {
	t.Parallel()

	cfg := selfDefenseConfigWith(nil)
	selfDefenseResolverConfig(cfg)
	ps, input := selfDefenseGenerationInput(t, cfg)

	if input.Security.GeoIP.Policy != nil {
		t.Fatal("the fixture must leave fixed GeoIP enforcement disabled")
	}
	if ps.GeoIP != nil {
		t.Fatal("the fixture must not open a GeoIP country database")
	}
	sd := input.Security.SelfDefense
	if sd.Policy == nil || !sd.Policy.Enabled {
		t.Fatalf("self-defense policy = %+v, want the default-on enabled policy", sd.Policy)
	}
	if sd.State != ps.IngressDefense {
		t.Fatal("the generation must borrow the one process-owned adaptive state")
	}
	if sd.Resolver.Source != config.ClientIPSourceXForwardedFor {
		t.Fatalf("resolver source = %q, want the configured GeoIP client_ip source", sd.Resolver.Source)
	}
	wantProxies := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	if !reflect.DeepEqual(sd.Resolver.TrustedProxies, wantProxies) {
		t.Fatalf("trusted proxies = %v, want the configured GeoIP trusted proxies %v", sd.Resolver.TrustedProxies, wantProxies)
	}
	if !sd.ImpossiblePaths {
		t.Fatal("the default-on impossible-path matcher must be projected")
	}
	if sd.Observer != ps.Metrics.SelfDefense {
		t.Fatalf("self-defense observer = %p, want the one process-owned bounded metrics collector %p", sd.Observer, ps.Metrics.SelfDefense)
	}
}

// TestGenerationProjectsOneCredentialProbeFromTheAuthChainSlice proves the
// conservative probe is built once per generation from the very provider slice
// the transport-auth chain runs, and that the projection never re-derives the
// answer: a chain that can always authenticate projects a probe that always
// answers the fail-open disposition.
func TestGenerationProjectsOneCredentialProbeFromTheAuthChainSlice(t *testing.T) {
	t.Parallel()

	_, input := selfDefenseGenerationInput(t, selfDefenseConfigWith(nil))
	if len(input.Security.HTTPAuthProviders) == 0 {
		t.Fatal("the fixture must compose the standard transport-auth chain")
	}
	probe := input.Security.SelfDefense.Probe
	if probe == nil {
		t.Fatal("the standard local provider chain answers the credential-presence question, so a probe must be projected")
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := probe(req); got != httpcontract.MayAuthenticate {
		t.Fatalf("probe disposition = %d, want MayAuthenticate (%d) for a chain that may authenticate", got, httpcontract.MayAuthenticate)
	}
	req.Header.Set("Authorization", "Bearer sk-live-secret")
	if got := probe(req); got != httpcontract.MayAuthenticate {
		t.Fatalf("probe disposition = %d, want MayAuthenticate (%d)", got, httpcontract.MayAuthenticate)
	}
}

// TestDisabledGenerationOmitsSelfDefenseAndItsObservation projects the explicit
// operator opt-out: the generation carries no self-defense policy, matcher
// toggle, credential probe, observer, or borrowed state, so it performs zero
// self-defense request-side work and the auth chain is never observed.
func TestDisabledGenerationOmitsSelfDefenseAndItsObservation(t *testing.T) {
	t.Parallel()

	cfg := selfDefenseConfigWith(func(sd *config.SelfDefenseConfig) { sd.Enabled = new(false) })
	ps, input := selfDefenseGenerationInput(t, cfg)
	if got := input.Security.SelfDefense; !reflect.DeepEqual(got, httpcontract.SelfDefenseSecurityInput{}) {
		t.Fatalf("self-defense projection = %+v, want the zero projection for an explicitly disabled generation", got)
	}
	if input.Security.SelfDefense.State == ps.IngressDefense {
		t.Fatal("a disabled generation must not even borrow the process-owned state")
	}
}

// TestGenerationSelfDefenseDefaultsAreProjected pins the documented default-on
// posture at the composition root: an omitted access.self_defense block yields the
// documented v1 policy in the standard generation.
func TestGenerationSelfDefenseDefaultsAreProjected(t *testing.T) {
	t.Parallel()

	_, input := selfDefenseGenerationInput(t, selfDefenseConfigWith(nil))
	sd := input.Security.SelfDefense
	if sd.Policy == nil {
		t.Fatal("omitted self_defense must project the documented default policy")
	}
	if sd.Policy.AuthFailures != 5 {
		t.Fatalf("auth failures = %d, want the documented default 5", sd.Policy.AuthFailures)
	}
	if sd.Policy.FailureWindow != time.Minute {
		t.Fatalf("failure window = %s, want the documented default 1m", sd.Policy.FailureWindow)
	}
	if sd.Policy.InitialQuarantine != time.Minute || sd.Policy.MaxQuarantine != 2*time.Hour {
		t.Fatalf("quarantine = %s..%s, want the documented defaults 1m..2h", sd.Policy.InitialQuarantine, sd.Policy.MaxQuarantine)
	}
	if !sd.ImpossiblePaths {
		t.Fatal("impossible-path matching must default to enabled")
	}
}
