package proxymemory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// The store wrapper changes preferences at actual worker boundaries, without
// timers or a second settings authority in production.
type claimSettingsStore struct {
	*workerStoreStub
	overrides OwnerOverrides
	readErr   error
	owners    []Principal
	onInput   func()
}

func (s *claimSettingsStore) ReadSettings(_ context.Context, p Principal) (StoredOwnerSettings, error) {
	s.owners = append(s.owners, p)
	return StoredOwnerSettings{
		Extraction:    StoredPhaseSettings{Overrides: s.overrides.Extraction},
		Consolidation: StoredPhaseSettings{Overrides: s.overrides.Consolidation},
	}, s.readErr
}

func (s *claimSettingsStore) ConsolidationInput(ctx context.Context, b ClaimedBank, sinceMS int64, limit int) (ConsolidationBatch, error) {
	if s.onInput != nil {
		s.onInput()
	}
	return s.workerStoreStub.ConsolidationInput(ctx, b, sinceMS, limit)
}

func claimSettingsConfig() Config {
	cfg := workerTestConfig()
	cfg.ModelsDeclared = true
	cfg.ExtractRoute, cfg.ConsolidateRoute = "openai:base", "openai:base"
	inventory := PhaseInventory{
		Backends: []string{"openai"}, Models: []string{"base", "chosen"},
		Targets: []TargetPolicy{
			{Backend: "openai", Model: "base", ReasoningEfforts: []string{"low", "medium", "high"}},
			{Backend: "openai", Model: "chosen", ReasoningEfforts: []string{"low", "medium", "high"}},
		},
	}
	cfg.Models.Extraction.Inventory, cfg.Models.Consolidation.Inventory = inventory, inventory
	cfg.TargetCompiler = &stubTargetCompiler{bySelector: map[string]stubCompiledCandidate{
		"openai:base":   {backend: "openai", model: "base", effort: "low", caps: reasoningCaps()},
		"openai:chosen": {backend: "openai", model: "chosen", effort: "high", caps: reasoningCaps()},
	}}
	return cfg
}

func TestWorkerSettingsEachClaimUsesCurrentOwnerButSubmittedCallStaysFrozen(t *testing.T) {
	h := newWorkerTestHarness(t, claimSettingsConfig())
	s := &claimSettingsStore{workerStoreStub: h.store}
	h.worker.store = s
	h.store.sessions = []ClaimedSession{
		workerClaimedSession(workerTestALeg1, h.base), workerClaimedSession(workerTestALeg2, h.base),
	}
	// Both sessions already exist before this owner changes their preferences.
	s.overrides.Extraction = TargetOverrides{Model: "chosen", ReasoningEffort: "high"}
	h.aux.collect = func(_ auxiliary.JobID) (aipapi.Collected, error) {
		// Change while the first submitted operation still owns its frozen call.
		s.overrides.Extraction = TargetOverrides{ReasoningEffort: "low"}
		var out aipapi.Collected
		out.Text.WriteString(workerPhase1Output)
		return out, nil
	}
	h.worker.phase1(t.Context())
	calls := h.aux.submitCalls()
	if len(calls) != 2 {
		t.Fatalf("submissions=%d, failures=%d, want two successful claims", len(calls), len(h.store.sessionFailCalls()))
	}
	if calls[0].req.Call.Route.Selector != "openai:chosen" || calls[0].req.Call.Options.ReasoningEffort != "high" {
		t.Fatalf("first claimed target=%s/%s, want chosen/high", calls[0].req.Call.Route.Selector, calls[0].req.Call.Options.ReasoningEffort)
	}
	if calls[1].req.Call.Route.Selector != "openai:base" || calls[1].req.Call.Options.ReasoningEffort != "low" {
		t.Fatalf("next claimed target=%s/%s, want base/low", calls[1].req.Call.Route.Selector, calls[1].req.Call.Options.ReasoningEffort)
	}
	want := Principal{TenantID: workerTestTenant, PrincipalID: workerTestPrincipal}
	if !reflect.DeepEqual(s.owners, []Principal{want, want}) {
		t.Fatalf("settings owners=%v, want trusted claim owner without project", s.owners)
	}
}

func TestWorkerSettingsConsolidationFreezesBeforeInputRead(t *testing.T) {
	h := newWorkerTestHarness(t, claimSettingsConfig())
	s := &claimSettingsStore{workerStoreStub: h.store, overrides: OwnerOverrides{
		Extraction:    TargetOverrides{ReasoningEffort: "low"},
		Consolidation: TargetOverrides{Model: "chosen", ReasoningEffort: "high"},
	}}
	h.worker.store = s
	h.store.banks = []ClaimedBank{workerClaimedBank()}
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.aux.text = workerPhase2Output
	s.onInput = func() { s.overrides.Consolidation = TargetOverrides{ReasoningEffort: "low"} }
	h.worker.phase2(t.Context())
	calls := h.aux.submitCalls()
	if len(calls) != 1 {
		t.Fatalf("submissions=%d, failures=%d", len(calls), len(h.store.bankFailCalls()))
	}
	if calls[0].req.Call.Route.Selector != "openai:chosen" || calls[0].req.Call.Options.ReasoningEffort != "high" {
		t.Fatalf("consolidation=%s/%s, want claim-time chosen/high", calls[0].req.Call.Route.Selector, calls[0].req.Call.Options.ReasoningEffort)
	}
}

func TestWorkerSettingsReadFailureFallsBackButInvalidOverrideRefuses(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "store failure", true: "invalid stored value"}[invalid], func(t *testing.T) {
			h := newWorkerTestHarness(t, claimSettingsConfig())
			s := &claimSettingsStore{workerStoreStub: h.store, readErr: errors.New("private store detail")}
			if invalid {
				s.readErr = nil
				s.overrides.Extraction.Model = "removed"
			}
			h.worker.store = s
			h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
			h.worker.phase1(t.Context())
			calls := h.aux.submitCalls()
			if invalid {
				if len(calls) != 0 || len(h.store.sessionFailCalls()) != 1 {
					t.Fatalf("invalid override submissions=%d failures=%d, want no dispatch and released retry", len(calls), len(h.store.sessionFailCalls()))
				}
			} else {
				if len(calls) != 1 || calls[0].req.Call.Route.Selector != "openai:base" {
					t.Fatalf("store failure did not serve the operator default: %d calls", len(calls))
				}
				if got := captureCounter(t, h.metrics, EventStoreUnavailable); got != 1 {
					t.Fatalf("store_unavailable=%v, want explicit degradation", got)
				}
			}
		})
	}
}
