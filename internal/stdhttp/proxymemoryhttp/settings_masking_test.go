package proxymemoryhttp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	proxymemoryhttp "github.com/aiproxer/aiproxer/internal/stdhttp/proxymemoryhttp"
)

// The settings resource of design §4: a client never sends or reads the
// trusted write-time dependency context, while the effective projection still
// masks an effort whose inherited operator target was removed (Requirement 2.4).

// maskingSettingsConfig builds one operator generation whose effort-only owner
// override is accepted against a default target. The current generation drops
// the default target but keeps the model lock, so the inherited target changes
// and the dependent effort must be masked.
func maskingSettingsConfig(defaultModel, defaultEffort string) proxymemory.Config {
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{httpTestPrincipal, httpTestOtherPrincipal}
	cfg.ExtractRoute = "openai:base"
	cfg.ConsolidateRoute = "openai:base"
	cfg.ModelsDeclared = true
	defaults := proxymemory.PhaseTarget{}
	if defaultModel != "" {
		defaults.Model = defaultModel
	}
	if defaultEffort != "" {
		defaults.ReasoningEffort = defaultEffort
	}
	policy := proxymemory.PhasePolicy{
		Defaults: defaults,
		Locks:    proxymemory.TargetLocks{Model: true},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"base", "chosen"},
			Targets: []proxymemory.TargetPolicy{
				{Backend: "openai", Model: "base", ReasoningEfforts: []string{"low", "medium"}},
				{Backend: "openai", Model: "chosen", ReasoningEfforts: []string{"high"}},
			},
		},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	cfg.TargetCompiler = &settingsCompiler{}
	return cfg
}

func maskingService(cfg proxymemory.Config, store proxymemory.Store) *proxymemory.Service {
	return proxymemory.NewService(cfg, store, nil, func() time.Time {
		return time.UnixMilli(1_800_000_000_000).UTC()
	})
}

// TestHandlerSettingsMasksRemovedDefaultDependentEffortWithoutWideningTheWire
// pins both halves of the requirement: the authenticated write stores the
// trusted context without any client member, and the read masks the dependent
// effort to the operator's current default while the raw overrides stay as the
// owner wrote them.
func TestHandlerSettingsMasksRemovedDefaultDependentEffortWithoutWideningTheWire(t *testing.T) {
	store := newFakeStore()
	prior := maskingService(maskingSettingsConfig("chosen", "high"), store)
	body := `{"extraction":{"reasoning_effort":"high"},"consolidation":{"reasoning_effort":"high"}}`
	written := serve(proxymemoryhttp.NewHandler(prior), scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if written.Code != http.StatusOK {
		t.Fatalf("the prior authority refused the effort-only override: status=%d body=%s", written.Code, written.Body.String())
	}
	storedAfterWrite := store.settings[settingsPrincipal(httpTestPrincipal)]
	if !storedAfterWrite.Extraction.Dependency.Present || !storedAfterWrite.Consolidation.Dependency.Present {
		t.Fatalf("a normal authenticated PUT stored no trusted dependency context: %+v", storedAfterWrite)
	}
	if got, want := storedAfterWrite.Extraction.Dependency.Model, "chosen"; got != want {
		t.Fatalf("stored extraction context model=%q, want the write-time effective target %q", got, want)
	}
	if got, want := storedAfterWrite.Consolidation.Dependency.Model, "chosen"; got != want {
		t.Fatalf("stored consolidation context model=%q, want the write-time effective target %q", got, want)
	}

	current := maskingService(maskingSettingsConfig("", ""), store)
	read := serve(proxymemoryhttp.NewHandler(current), scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if read.Code != http.StatusOK {
		t.Fatalf("the effective projection refused a removed-default dependent effort: status=%d body=%s", read.Code, read.Body.String())
	}
	if strings.Contains(read.Body.String(), "dependency") || strings.Contains(read.Body.String(), "inherited") {
		t.Fatalf("the settings response exposed the trusted context: %s", read.Body.String())
	}
	var document settingsDocument
	if err := json.Unmarshal(read.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode settings document: %v", err)
	}
	if got, want := document.Overrides.Extraction.ReasoningEffort, "high"; got != want {
		t.Fatalf("stored extraction override=%q, want the owner's raw %q", got, want)
	}
	if got, want := document.Overrides.Consolidation.ReasoningEffort, "high"; got != want {
		t.Fatalf("stored consolidation override=%q, want the owner's raw %q", got, want)
	}
	if got, want := document.Phases[phaseOf(t, document, "extraction")].Effective.ReasoningEffort, "low"; got != want {
		t.Fatalf("extraction effective effort=%q, want the operator's current default %q", got, want)
	}
	if got, want := document.Phases[phaseOf(t, document, "consolidation")].Effective.ReasoningEffort, "medium"; got != want {
		t.Fatalf("consolidation effective effort=%q, want the operator's current default %q", got, want)
	}

	// The context is a server-side authority: a client member that names it is
	// an unknown member, and the refused write stores nothing.
	before := store.settings[settingsPrincipal(httpTestPrincipal)]
	unknown := serve(proxymemoryhttp.NewHandler(current), scopedRequest(http.MethodPut, httpSettingsPath,
		strings.NewReader(`{"extraction":{"reasoning_effort":"high","dependency":{"present":true}}}`), httpTestPrincipal))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("a client-supplied dependency context status=%d, want 400", unknown.Code)
	}
	if got := store.settings[settingsPrincipal(httpTestPrincipal)]; got != before {
		t.Fatalf("the refused unknown member changed the stored settings: %+v", got)
	}
}
