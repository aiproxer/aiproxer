package compatibleparity_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
)

func TestCompatibleParity_fixtureMatrixCoversFamiliesAndScenarios(t *testing.T) {
	t.Parallel()
	fxs := compatibleparity.ParityFixtures()
	minCount := len(compatibleparity.AllFamilies()) * len(compatibleparity.AllScenarios())
	if len(fxs) < minCount {
		t.Fatalf("fixture count = %d want at least %d", len(fxs), minCount)
	}
	seen := map[string]bool{}
	for _, fx := range fxs {
		if fx.Name == "" || fx.Call.Messages == nil {
			t.Fatalf("incomplete fixture %#v", fx)
		}
		if seen[fx.Name] {
			t.Fatalf("duplicate fixture %q", fx.Name)
		}
		seen[fx.Name] = true
	}
}

func TestCompatibleParity_essentialMatchesGeneric(t *testing.T) {
	t.Parallel()
	runEssentialGenericParity(t, compatibleparity.GenericBackend)
}

func TestCompatibleParity_factoryBuiltMatchesEssential(t *testing.T) {
	runEssentialGenericParitySequential(t, func(t *testing.T, family compatibleparity.Family, prefix, baseURL, apiKey string, client *http.Client) execbackend.Backend {
		t.Helper()
		envRoot := "COMPAT_PARITY_FACTORY_" + strings.ToUpper(strings.ReplaceAll(prefix, "-", "_"))
		return compatibleparity.FactoryGenericBackend(t, family, prefix, baseURL, envRoot, apiKey, client)
	})
}

func runEssentialGenericParitySequential(t *testing.T, buildGeneric func(*testing.T, compatibleparity.Family, string, string, string, *http.Client) execbackend.Backend) {
	t.Helper()
	for _, fx := range compatibleparity.ParityFixtures() {
		t.Run(fx.Name, func(t *testing.T) {
			runParityFixture(t, fx, buildGeneric)
		})
	}
}

func runEssentialGenericParity(t *testing.T, buildGeneric func(*testing.T, compatibleparity.Family, string, string, string, *http.Client) execbackend.Backend) {
	t.Helper()
	for _, fx := range compatibleparity.ParityFixtures() {
		t.Run(fx.Name, func(t *testing.T) {
			t.Parallel()
			runParityFixture(t, fx, buildGeneric)
		})
	}
}

func runParityFixture(t *testing.T, fx compatibleparity.Fixture, buildGeneric func(*testing.T, compatibleparity.Family, string, string, string, *http.Client) execbackend.Backend) {
	t.Helper()
	essWS := compatibleparity.NewWireServer(t, fx.Family, fx.Scenario)
	genWS := compatibleparity.NewWireServer(t, fx.Family, fx.Scenario)
	essClient := essWS.Server.Client()
	genClient := genWS.Server.Client()
	essential := compatibleparity.EssentialBackend(t, fx.Family, essWS.URL, essClient)
	generic := buildGeneric(t, fx.Family, "compat-"+string(fx.Family), genWS.URL, "sk-test-generic", genClient)

	ess := compatibleparity.CollectOutcome(t, essential, fx, essWS)
	gen := compatibleparity.CollectOutcome(t, generic, fx, genWS)
	compatibleparity.AssertOutcomesEqual(t, fx, ess, gen)

	if fx.Scenario == compatibleparity.ScenarioMultimodal && !fx.ExpectFailure() {
		compatibleparity.AssertMultimodalRequestMapped(t, essWS, fx.Family)
		compatibleparity.AssertMultimodalRequestMapped(t, genWS, fx.Family)
	}
}
