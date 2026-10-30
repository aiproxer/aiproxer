package proxymemoryhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	proxymemoryhttp "github.com/aiproxer/aiproxer/internal/stdhttp/proxymemoryhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The settings resource of design §4: the authenticated, owner-scoped read,
// update and delete of the caller's own per-phase overrides.

// httpSettingsPath is the settings resource's path.
const httpSettingsPath = "/v1/aip/memory/settings"

// settingsCompiler is the generation-bound target compiler of the settings
// cases: it answers with the candidate the derived selector names, so the
// compiled actual target of a phase is the phase's own target.
//
// The compiled options are the call's own options, which is what the runtime
// merges the route parameters onto, and the reported capabilities carry the
// reasoning capability the phase's effort needs.
type settingsCompiler struct {
	calls int
	// installation and effortSupport are the deployment facts the compiler
	// answers: 0 unknown, 1 present or supported, 2 absent or unsupported.
	installation  int
	effortSupport int
	// absentModel names the model the deployment does not carry, so the
	// refusal lands on the phase that addresses it.
	absentModel string
}

func (c *settingsCompiler) CompileTarget(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	c.calls++
	backend, model := settingsSelectorTarget(call.Route.Selector)
	visit("candidate:"+backend+":"+model, backend, model, call.Options,
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning))
	return nil
}

// CompileTargetFacts answers the deployment facts of every candidate beside the
// compiled target, the way the composition root's binding does.
func (c *settingsCompiler) CompileTargetFacts(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int)) error {
	c.calls++
	backend, model := settingsSelectorTarget(call.Route.Selector)
	installation, effortSupport := c.installation, c.effortSupport
	if c.absentModel != "" && model == c.absentModel {
		installation = 2
	}
	visit("candidate:"+backend+":"+model, backend, model, call.Options,
		aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning),
		installation, effortSupport)
	return nil
}

func (c *settingsCompiler) CompileIdentity(_ context.Context, call aipapi.Call, visit func(backend, model string)) error {
	backend, model := settingsSelectorTarget(call.Route.Selector)
	visit(backend, model)
	return nil
}

// unavailableCompiler is the target compiler of a deployment whose generation
// carries no bound compiler: every compilation fails.
type unavailableCompiler struct {
	settingsCompiler
}

func (c *unavailableCompiler) CompileTarget(_ context.Context, _ aipapi.Call, _ func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	c.calls++
	return errors.New("the target compiler is unavailable")
}

// CompileTargetFacts fails as well: the facts extension is the port the write
// path uses, so an unbound compiler must refuse there too.
func (c *unavailableCompiler) CompileTargetFacts(_ context.Context, _ aipapi.Call, _ func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int)) error {
	c.calls++
	return errors.New("the target compiler is unavailable")
}

// settingsSelectorTarget reads the backend and the model of a derived route
// selector: the canonical grammar reads the first colon as the backend
// separator.
func settingsSelectorTarget(selector string) (string, string) {
	backend, model, found := strings.Cut(selector, ":")
	if !found {
		return "", selector
	}
	return backend, model
}

// newSettingsService builds a real Service over the fake store with the
// per-phase model policy of design §3: the extraction phase locks its model
// and its reasoning effort, the consolidation phase locks nothing.
func newSettingsService(t *testing.T, store proxymemory.Store) *proxymemory.Service {
	t.Helper()
	return newSettingsServiceWithCompiler(t, store, &settingsCompiler{})
}

// newSettingsServiceWithCompiler builds the settings service over the given
// generation-bound target compiler.
func newSettingsServiceWithCompiler(t *testing.T, store proxymemory.Store, compiler proxymemory.TargetCompiler) *proxymemory.Service {
	t.Helper()
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{httpTestPrincipal, httpTestOtherPrincipal}
	cfg.ExtractRoute = "openai:gpt-5-mini"
	cfg.ConsolidateRoute = "openai:gpt-5"
	cfg.ModelsDeclared = true
	cfg.Models.Extraction = proxymemory.PhasePolicy{
		Defaults: proxymemory.PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"},
		Locks:    proxymemory.TargetLocks{Model: true, ReasoningEffort: true},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"openai", "anthropic"},
			Models:   []string{"gpt-5-mini", "gpt-5"},
			Targets: []proxymemory.TargetPolicy{
				{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
				{Backend: "anthropic", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
			},
		},
	}
	cfg.Models.Consolidation = proxymemory.PhasePolicy{
		Defaults: proxymemory.PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"gpt-5"},
			Targets: []proxymemory.TargetPolicy{
				{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"medium", "high"}},
			},
		},
	}
	cfg.TargetCompiler = compiler
	return proxymemory.NewService(cfg, store, nil, func() time.Time {
		return time.UnixMilli(1_800_000_000_000).UTC()
	})
}

// settingsPrincipal is the authenticated owner of the settings cases.
func settingsPrincipal(principal string) proxymemory.Principal {
	return proxymemory.Principal{TenantID: httpTestTenant, PrincipalID: principal}
}

// seedSettings stores one owner's overrides with no trusted context: the
// projection of a legacy row follows the existing behavior.
func (s *fakeStore) seedSettings(principal string, o proxymemory.OwnerOverrides) {
	if s.settings == nil {
		s.settings = map[proxymemory.Principal]proxymemory.StoredOwnerSettings{}
	}
	s.settings[settingsPrincipal(principal)] = proxymemory.StoredOwnerSettings{
		Extraction:    proxymemory.StoredPhaseSettings{Overrides: o.Extraction},
		Consolidation: proxymemory.StoredPhaseSettings{Overrides: o.Consolidation},
	}
}

// settingsDocument is the decoded settings document.
type settingsDocument struct {
	Overrides struct {
		Extraction    phaseValues `json:"extraction"`
		Consolidation phaseValues `json:"consolidation"`
	} `json:"overrides"`
	Phases []struct {
		Phase     string      `json:"phase"`
		Defaults  phaseValues `json:"defaults"`
		Effective phaseValues `json:"effective"`
		Locks     struct {
			Backend         bool `json:"backend"`
			Model           bool `json:"model"`
			ReasoningEffort bool `json:"reasoning_effort"`
		} `json:"locks"`
	} `json:"phases"`
}

// phaseValues holds the three owner-selectable values of one phase.
type phaseValues struct {
	Backend         string `json:"backend"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// phaseOf returns the document entry of the named phase.
func phaseOf(t *testing.T, document settingsDocument, phase string) int {
	t.Helper()
	for i, entry := range document.Phases {
		if entry.Phase == phase {
			return i
		}
	}
	t.Fatalf("the document carries no %q phase", phase)
	return -1
}

// TestHandlerSettingsReadsTheCallersOwnSettings pins Requirement 2.1 and
// Requirement 2.2: the read answers with the stored overrides, the effective
// values and the per-field lock flags of the caller's own settings.
func TestHandlerSettingsReadsTheCallersOwnSettings(t *testing.T) {
	store := newFakeStore()
	store.seedSettings(httpTestPrincipal, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "anthropic"},
	})
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var document settingsDocument
	decodeBody(t, rec, &document)
	if want := (phaseValues{Backend: "anthropic"}); document.Overrides.Extraction != want {
		t.Fatalf("stored extraction overrides = %+v, want %+v", document.Overrides.Extraction, want)
	}
	if want := (phaseValues{}); document.Overrides.Consolidation != want {
		t.Fatalf("stored consolidation overrides = %+v, want %+v", document.Overrides.Consolidation, want)
	}

	extraction := document.Phases[phaseOf(t, document, "extraction")]
	if want := (phaseValues{Backend: "anthropic", Model: "gpt-5-mini", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective extraction target = %+v, want %+v", extraction.Effective, want)
	}
	if !extraction.Locks.Model || !extraction.Locks.ReasoningEffort || extraction.Locks.Backend {
		t.Fatalf("extraction lock flags = %+v, want the model and the effort locked", extraction.Locks)
	}

	consolidation := document.Phases[phaseOf(t, document, "consolidation")]
	if want := (phaseValues{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"}); consolidation.Effective != want {
		t.Fatalf("effective consolidation target = %+v, want %+v", consolidation.Effective, want)
	}
	if consolidation.Locks.Backend || consolidation.Locks.Model || consolidation.Locks.ReasoningEffort {
		t.Fatalf("consolidation lock flags = %+v, want none locked", consolidation.Locks)
	}
}

// TestHandlerSettingsTheResourceIsNotTheScopeItemRoute pins Requirement 2.2:
// the settings segment is classified before the per-scope item route, so the
// settings request reaches the settings resource and never the scope path.
func TestHandlerSettingsTheResourceIsNotTheScopeItemRoute(t *testing.T) {
	store := newFakeStore()
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	settings := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if settings.Code != http.StatusOK {
		t.Fatalf("settings status = %d, want %d: the settings segment was parsed as a scope", settings.Code, http.StatusOK)
	}

	// The per-scope item route keeps its own answer.
	item := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory/"+httpTestUnknownScope, nil, httpTestPrincipal))
	if item.Code != http.StatusNotFound {
		t.Fatalf("scope item status = %d, want %d", item.Code, http.StatusNotFound)
	}
	requireErrorBody(t, item, "not_found")
}

// TestHandlerSettingsTheWriteStoresTheCallersOverrides pins the write path:
// the accepted set is stored for the authenticated caller and the answer
// carries the effective values the write produced.
func TestHandlerSettingsTheWriteStoresTheCallersOverrides(t *testing.T) {
	store := newFakeStore()
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	body := `{"extraction":{"backend":"anthropic"},"consolidation":{"model":"gpt-5"}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	stored := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides()
	if want := (proxymemory.OwnerOverrides{
		Extraction:    proxymemory.TargetOverrides{Backend: "anthropic"},
		Consolidation: proxymemory.TargetOverrides{Model: "gpt-5"},
	}); stored != want {
		t.Fatalf("stored overrides = %+v, want %+v", stored, want)
	}
	if len(store.settings) != 1 {
		t.Fatalf("the write touched %d owners, want only the caller", len(store.settings))
	}

	var document settingsDocument
	decodeBody(t, rec, &document)
	extraction := document.Phases[phaseOf(t, document, "extraction")]
	if want := (phaseValues{Backend: "anthropic", Model: "gpt-5-mini", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective extraction target = %+v, want %+v", extraction.Effective, want)
	}
}

// TestHandlerSettingsARefusedWriteStoresNothing pins Requirement 1.2,
// Requirement 1.4 and Requirement 2.2: a write that changes a locked field
// or names a value outside the allowed inventory is refused explicitly and
// persists nothing.
func TestHandlerSettingsARefusedWriteStoresNothing(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantPhase string
		wantField string
	}{
		{
			name:      "a locked field",
			body:      `{"extraction":{"model":"gpt-5"}}`,
			wantPhase: "extraction",
			wantField: "model",
		},
		{
			name:      "a value outside the allowed inventory",
			body:      `{"extraction":{"backend":"gemini"}}`,
			wantPhase: "extraction",
			wantField: "backend",
		},
		{
			name:      "a value outside the allowed inventory of the other phase",
			body:      `{"consolidation":{"backend":"anthropic"}}`,
			wantPhase: "consolidation",
			wantField: "backend",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			// The owner already stored a row: a refused write must leave it
			// exactly as it is.
			stored := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
			store.seedSettings(httpTestPrincipal, stored)
			h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

			rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(tc.body), httpTestPrincipal))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
			}
			var refusal struct {
				Error string `json:"error"`
				Phase string `json:"phase"`
				Field string `json:"field"`
			}
			decodeBody(t, rec, &refusal)
			if refusal.Error != "policy_refused" {
				t.Fatalf("error = %q, want policy_refused", refusal.Error)
			}
			if refusal.Phase != tc.wantPhase || refusal.Field != tc.wantField {
				t.Fatalf("refusal = %+v, want the phase %q and the field %q", refusal, tc.wantPhase, tc.wantField)
			}
			if got := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(); got != stored {
				t.Fatalf("the stored overrides = %+v, want %+v: the refused write changed the stored row", got, stored)
			}
		})
	}
}

// TestHandlerSettingsARefusedDeploymentFactsStoresNothing pins the write-path
// factual validation at the HTTP boundary: a proposed target the deployment
// does not carry is refused explicitly, and the caller's stored settings stay
// exactly as they were.
func TestHandlerSettingsARefusedDeploymentFactsStoresNothing(t *testing.T) {
	store := newFakeStore()
	stored := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
	store.seedSettings(httpTestPrincipal, stored)
	compiler := &settingsCompiler{absentModel: "gpt-5", installation: 1, effortSupport: 1}
	h := proxymemoryhttp.NewHandler(newSettingsServiceWithCompiler(t, store, compiler))

	body := `{"consolidation":{"reasoning_effort":"high"}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var refusal struct {
		Error string `json:"error"`
		Phase string `json:"phase"`
		Field string `json:"field"`
	}
	decodeBody(t, rec, &refusal)
	if refusal.Error != "policy_refused" {
		t.Fatalf("error = %q, want policy_refused", refusal.Error)
	}
	if refusal.Phase != "consolidation" || refusal.Field != "model" {
		t.Fatalf("refusal = %+v, want the consolidation phase and the model field", refusal)
	}
	if got := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(); got != stored {
		t.Fatalf("the stored overrides = %+v, want %+v: the refused write changed the stored row", got, stored)
	}
	if compiler.calls == 0 {
		t.Fatal("the write never compiled the actual target")
	}
}

// TestHandlerSettingsTheResponseCarriesTheOperatorDefaults pins the settings
// projection of design §4 and the task 1.2 handoff: the response carries the
// administrator's defaults beside the owner's overrides, the effective values
// and the lock flags, so the owner can tell what clearing an override would
// restore.
//
// The operator's consolidation effort is "medium" and the owner's is "high":
// both must be visible, and the read must not change the stored overrides.
func TestHandlerSettingsTheResponseCarriesTheOperatorDefaults(t *testing.T) {
	store := newFakeStore()
	stored := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"}}
	store.seedSettings(httpTestPrincipal, stored)
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var document settingsDocument
	decodeBody(t, rec, &document)

	consolidation := document.Phases[phaseOf(t, document, "consolidation")]
	if want := (phaseValues{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"}); consolidation.Defaults != want {
		t.Fatalf("the operator's consolidation defaults = %+v, want %+v", consolidation.Defaults, want)
	}
	if want := (phaseValues{ReasoningEffort: "high"}); document.Overrides.Consolidation != want {
		t.Fatalf("the stored consolidation overrides = %+v, want %+v", document.Overrides.Consolidation, want)
	}
	if want := (phaseValues{Backend: "openai", Model: "gpt-5", ReasoningEffort: "high"}); consolidation.Effective != want {
		t.Fatalf("the effective consolidation target = %+v, want %+v", consolidation.Effective, want)
	}

	extraction := document.Phases[phaseOf(t, document, "extraction")]
	if want := (phaseValues{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"}); extraction.Defaults != want {
		t.Fatalf("the operator's extraction defaults = %+v, want %+v", extraction.Defaults, want)
	}

	// The read does not change the stored overrides.
	if got := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(); got != stored {
		t.Fatalf("the stored overrides = %+v, want %+v: the read changed the stored row", got, stored)
	}
}

// TestHandlerSettingsARefusedActualTargetStoresNothing pins the write-path
// validation of design §3 at the HTTP boundary: a proposed target the
// compiled actual target does not cover is refused explicitly, and the
// caller's stored settings are unchanged.
func TestHandlerSettingsARefusedActualTargetStoresNothing(t *testing.T) {
	store := newFakeStore()
	stored := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
	store.seedSettings(httpTestPrincipal, stored)
	h := proxymemoryhttp.NewHandler(newSettingsServiceWithCompiler(t, store, &unavailableCompiler{}))

	body := `{"consolidation":{"reasoning_effort":"high"}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var refusal struct {
		Error string `json:"error"`
		Phase string `json:"phase"`
		Field string `json:"field"`
	}
	decodeBody(t, rec, &refusal)
	if refusal.Error != "policy_refused" {
		t.Fatalf("error = %q, want policy_refused", refusal.Error)
	}
	if refusal.Phase != "extraction" && refusal.Phase != "consolidation" {
		t.Fatalf("refusal phase = %q, want a governed phase", refusal.Phase)
	}
	if strings.Contains(rec.Body.String(), "high") || strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("the refusal body carried the value or the compiler error: %s", rec.Body.String())
	}
	if got := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(); got != stored {
		t.Fatalf("the stored overrides = %+v, want %+v: the refused write changed the stored row", got, stored)
	}
}

// TestHandlerSettingsTheRevocationRemovesTheSettings pins the delete path.
func TestHandlerSettingsTheRevocationRemovesTheSettings(t *testing.T) {
	store := newFakeStore()
	store.seedSettings(httpTestPrincipal, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "anthropic"},
	})
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodDelete, httpSettingsPath, nil, httpTestPrincipal))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(store.settings) != 0 {
		t.Fatalf("the revocation left %d owner settings, want none", len(store.settings))
	}

	again := serve(h, scopedRequest(http.MethodDelete, httpSettingsPath, nil, httpTestPrincipal))
	if again.Code != http.StatusNotFound {
		t.Fatalf("second revocation status = %d, want %d", again.Code, http.StatusNotFound)
	}
	requireErrorBody(t, again, "not_found")
}

// TestHandlerSettingsAnotherOwnerNeverSeesAnotherOwnersSettings pins
// Requirement 2.2: the settings of one owner are invisible to another, and
// the answer for the other owner is the answer for an owner without
// settings.
func TestHandlerSettingsAnotherOwnerNeverSeesAnotherOwnersSettings(t *testing.T) {
	store := newFakeStore()
	store.seedSettings(httpTestPrincipal, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "anthropic"},
	})
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestOtherPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var document settingsDocument
	decodeBody(t, rec, &document)
	if want := (phaseValues{}); document.Overrides.Extraction != want {
		t.Fatalf("the other owner's overrides = %+v, want %+v", document.Overrides.Extraction, want)
	}
	if strings.Contains(rec.Body.String(), "anthropic") {
		t.Fatal("the response leaked another owner's override")
	}
}

// TestHandlerSettingsTheBodyAndTheQueryCarryNoOwnerAuthority pins
// Requirement 2.2: the owner is the authenticated principal, so a body that
// names an owner is refused and a query that names one changes nothing.
func TestHandlerSettingsTheBodyAndTheQueryCarryNoOwnerAuthority(t *testing.T) {
	store := newFakeStore()
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	body := `{"tenant_id":"other-tenant","principal_id":"other-principal","extraction":{"backend":"anthropic"}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	requireErrorBody(t, rec, "invalid_input")
	if len(store.settings) != 0 {
		t.Fatalf("the refused write stored %d owner settings, want none", len(store.settings))
	}

	query := httpSettingsPath + "?tenant_id=other-tenant&principal_id=" + httpTestOtherPrincipal
	accepted := serve(h, scopedRequest(http.MethodPut, query, strings.NewReader(`{"extraction":{"backend":"anthropic"}}`), httpTestPrincipal))
	if accepted.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", accepted.Code, http.StatusOK, accepted.Body.String())
	}
	if _, ok := store.settings[settingsPrincipal(httpTestOtherPrincipal)]; ok {
		t.Fatal("the query string changed another owner's settings")
	}
	if len(store.settings) != 1 {
		t.Fatalf("the write touched %d owners, want only the caller", len(store.settings))
	}
}

// TestHandlerSettingsANullAndAnEmptyStringNameNoValue pins the presence
// semantics: an absent member, a JSON null and an empty string all name no
// value, which is the existing policy's empty-value semantics.
func TestHandlerSettingsANullAndAnEmptyStringNameNoValue(t *testing.T) {
	store := newFakeStore()
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	body := `{"extraction":{"backend":null,"model":"","reasoning_effort":null}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if want := (proxymemory.OwnerOverrides{}); store.settings[settingsPrincipal(httpTestPrincipal)].Overrides() != want {
		t.Fatalf("stored overrides = %+v, want %+v", store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(), want)
	}
}

// TestHandlerSettingsAMalformedDocumentIsRefused pins that only one settings
// document is accepted and that a malformed member never reaches the store.
func TestHandlerSettingsAMalformedDocumentIsRefused(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "a malformed document", body: `{"extraction":`},
		{name: "an empty document", body: ``},
		{name: "a null document", body: `null`},
		{name: "a non-string value", body: `{"extraction":{"backend":7}}`},
		{name: "a second document", body: `{"extraction":{}} {"extraction":{}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

			rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(tc.body), httpTestPrincipal))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			requireErrorBody(t, rec, "invalid_input")
			if len(store.settings) != 0 {
				t.Fatalf("the refused write stored %d owner settings, want none", len(store.settings))
			}
		})
	}
}

// TestHandlerSettingsAStoreFailureIsContentFree pins the error privacy of
// design §Error Handling: a store failure answers with a content-free 500.
func TestHandlerSettingsAStoreFailureIsContentFree(t *testing.T) {
	store := newFakeStore()
	store.settingsErr = errors.New("proxymemorystore: select settings: the database is unreachable")
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	requireErrorBody(t, rec, "unavailable")
	if strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatal("the response carried the store error text")
	}
}

// TestHandlerSettingsTheMCPToolCatalogExposesNoSettingsMutation pins
// Requirement 2.2: the memory tool endpoint exposes no settings mutation.
func TestHandlerSettingsTheMCPToolCatalogExposesNoSettingsMutation(t *testing.T) {
	store := newFakeStore()
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	rec := serve(h, scopedRequest(http.MethodPost, "/v1/aip/memory/mcp", strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var answer struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	decodeBody(t, rec, &answer)
	got := make([]string, 0, len(answer.Result.Tools))
	for _, tool := range answer.Result.Tools {
		got = append(got, tool.Name)
	}
	want := []string{"memory_list", "memory_read", "memory_search", "memory_add_note"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool catalog = %v, want %v", got, want)
	}
}

// TestHandlerSettingsTheDocumentCarriesNoAccountIdentity pins the wire
// contract: the settings document carries only the owner's own values.
func TestHandlerSettingsTheDocumentCarriesNoAccountIdentity(t *testing.T) {
	store := newFakeStore()
	store.seedSettings(httpTestPrincipal, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "anthropic"},
	})
	h := proxymemoryhttp.NewHandler(newSettingsService(t, store))

	rec := serve(h, scopedRequest(http.MethodGet, httpSettingsPath, nil, httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var document map[string]json.RawMessage
	decodeBody(t, rec, &document)
	for _, key := range []string{"tenant_id", "principal_id", "account", "account_id", "scope"} {
		if _, ok := document[key]; ok {
			t.Fatalf("the document carries the %q member", key)
		}
	}
}

// failoverSettingsCompiler is the target compiler of the operator's failover
// route cases: the route names several leaves, so the compilation reports
// every one of them, the way the runtime's candidate extraction does.
type failoverSettingsCompiler struct {
	leaves []settingsLeaf
}

// settingsLeaf is one compiled leaf of the operator's route.
type settingsLeaf struct {
	backend string
	model   string
}

func (c *failoverSettingsCompiler) CompileTarget(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	for _, leaf := range c.leaves {
		visit("candidate:"+leaf.backend+":"+leaf.model, leaf.backend, leaf.model, call.Options,
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning))
	}
	return nil
}

// CompileTargetFacts answers the deployment facts of every leaf beside the
// compiled target, the way the composition root's binding does.
func (c *failoverSettingsCompiler) CompileTargetFacts(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int)) error {
	for _, leaf := range c.leaves {
		visit("candidate:"+leaf.backend+":"+leaf.model, leaf.backend, leaf.model, call.Options,
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning), 1, 1)
	}
	return nil
}

func (c *failoverSettingsCompiler) CompileIdentity(_ context.Context, _ aipapi.Call, visit func(backend, model string)) error {
	for _, leaf := range c.leaves {
		visit(leaf.backend, leaf.model)
	}
	return nil
}

// newFailoverSettingsService builds a real Service over the fake store with an
// operator configuration whose phases keep the operator's own failover route:
// the phase names no target of its own, so the route stays the operator's and
// every leaf of it is compiled and checked.
func newFailoverSettingsService(t *testing.T, store proxymemory.Store) *proxymemory.Service {
	t.Helper()
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{httpTestPrincipal, httpTestOtherPrincipal}
	cfg.ExtractRoute = "openai:gpt-5|openai:gpt-4"
	cfg.ConsolidateRoute = "openai:gpt-5|openai:gpt-4"
	cfg.ModelsDeclared = true
	inventory := proxymemory.PhaseInventory{
		Backends: []string{"openai"},
		Models:   []string{"gpt-5", "gpt-4"},
		Targets: []proxymemory.TargetPolicy{
			{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium", "high"}},
			{Backend: "openai", Model: "gpt-4", ReasoningEfforts: []string{"low", "medium", "high"}},
		},
	}
	cfg.Models.Extraction = proxymemory.PhasePolicy{
		Defaults:  proxymemory.PhaseTarget{ReasoningEffort: "low"},
		Inventory: inventory,
	}
	cfg.Models.Consolidation = proxymemory.PhasePolicy{
		Defaults:  proxymemory.PhaseTarget{ReasoningEffort: "medium"},
		Inventory: inventory,
	}
	cfg.TargetCompiler = &failoverSettingsCompiler{leaves: []settingsLeaf{
		{backend: "openai", model: "gpt-5"},
		{backend: "openai", model: "gpt-4"},
	}}
	return proxymemory.NewService(cfg, store, nil, func() time.Time {
		return time.UnixMilli(1_800_000_000_000).UTC()
	})
}

// TestHandlerSettingsAnUntouchedMultiTargetRouteDoesNotBlockAnOtherPhaseWrite
// pins Requirement 1.3, Requirement 2.1 and Requirement 2.2 at the HTTP
// boundary: a phase whose operator named no target keeps the operator's own
// failover route, so an owner write to the *other* phase is not refused, and
// the answer carries the operator's defaults and the effective values and
// nothing else.
func TestHandlerSettingsAnUntouchedMultiTargetRouteDoesNotBlockAnOtherPhaseWrite(t *testing.T) {
	store := newFakeStore()
	stored := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
	store.seedSettings(httpTestPrincipal, stored)
	h := proxymemoryhttp.NewHandler(newFailoverSettingsService(t, store))

	body := `{"consolidation":{"reasoning_effort":"high"}}`
	rec := serve(h, scopedRequest(http.MethodPut, httpSettingsPath, strings.NewReader(body), httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if got, want := store.settings[settingsPrincipal(httpTestPrincipal)].Overrides(), (proxymemory.OwnerOverrides{
		Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"},
	}); got != want {
		t.Fatalf("stored overrides = %+v, want %+v", got, want)
	}

	var document settingsDocument
	decodeBody(t, rec, &document)
	consolidation := document.Phases[phaseOf(t, document, "consolidation")]
	if want := (phaseValues{Backend: "openai", Model: "gpt-5", ReasoningEffort: "high"}); consolidation.Effective != want {
		t.Fatalf("effective consolidation target = %+v, want %+v", consolidation.Effective, want)
	}
	if want := (phaseValues{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"}); consolidation.Defaults != want {
		t.Fatalf("operator consolidation defaults = %+v, want %+v", consolidation.Defaults, want)
	}

	// The wire carries the four documented concepts of a phase and nothing
	// else: the internal provenance of the derived selector is not exposed.
	var raw struct {
		Phases []map[string]json.RawMessage `json:"phases"`
	}
	decodeBody(t, rec, &raw)
	if len(raw.Phases) != 2 {
		t.Fatalf("phase entries = %d, want 2", len(raw.Phases))
	}
	for _, entry := range raw.Phases {
		for key := range entry {
			switch key {
			case "phase", "defaults", "effective", "locks":
			default:
				t.Fatalf("the phase entry carries the %q member", key)
			}
		}
	}
}
