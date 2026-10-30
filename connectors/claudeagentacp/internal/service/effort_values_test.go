package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestResolvePublishesGradedEffortFacts(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key")
	rows := []product.DiscoveredModel{
		{NativeID: "future-family-17-3", DisplayName: "Future", Description: "F", Capabilities: map[string]json.RawMessage{"supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
		{NativeID: "other-family-8-2", DisplayName: "Other", Description: "O"},
	}
	if err := inst.eng.spec.PublishModels(product.ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	model := "future-family-17-3"
	profile, err := inst.Resolve(t.Context(), &model)
	if err != nil {
		t.Fatal(err)
	}
	decl := profile.ReasoningEffortValues
	if decl == nil || decl.ModelID != model || len(decl.Supported) != 2 || decl.Exhaustive {
		t.Fatalf("expected non-exhaustive facts echoing %q, got %+v", model, decl)
	}
	// Canonical query echoes the canonical ID.
	canonical := "anthropic/future-family-17.3"
	profile, err = inst.Resolve(t.Context(), &canonical)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReasoningEffortValues == nil || profile.ReasoningEffortValues.ModelID != canonical {
		t.Fatalf("canonical query must echo canonical ID, got %+v", profile.ReasoningEffortValues)
	}
	// Unproven model, alias, and agnostic queries publish nothing.
	for _, tc := range []struct {
		name  string
		model *string
	}{
		{"unproven", new("missing-family-9-1")},
		{"alias", new("auto")},
		{"agnostic", nil},
		{"other without graded fact", new("other-family-8-2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := inst.Resolve(t.Context(), tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if got.ReasoningEffortValues != nil {
				t.Fatalf("expected no declaration, got %+v", got.ReasoningEffortValues)
			}
		})
	}
}

func TestResolveDropsGradedEffortFactsAfterFailedRefreshAndRecovers(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	rows := []product.DiscoveredModel{
		{NativeID: "future-family-17-3", DisplayName: "Future", Description: "F", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(`true`), "supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
		{NativeID: "other-family-8-2", DisplayName: "Other", Description: "O"},
	}
	if err := inst.eng.spec.PublishModels(product.ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	model := "future-family-17-3"
	assertFacts := func(want bool) {
		t.Helper()
		profile, err := inst.Resolve(t.Context(), &model)
		if err != nil {
			t.Fatal(err)
		}
		if got := profile.ReasoningEffortValues != nil; got != want {
			t.Fatalf("graded facts present=%v, want %v: %+v", got, want, profile.ReasoningEffortValues)
		}
	}
	assertFacts(true)

	// A failed discovery refresh must not keep advertising the previous
	// snapshot's graded evidence as current.
	adapter.discoveryMode = "empty"
	if _, err := inst.ListModels(t.Context(), 0); err == nil {
		t.Fatal("failed refresh returned no error")
	}
	assertFacts(false)
	// The accepted inventory itself is preserved.
	old, err := inst.eng.spec.Inventory().LoadModels(t.Context())
	if err != nil || len(old.Models) != 2 {
		t.Fatalf("failed refresh replaced inventory: %+v err=%v", old, err)
	}

	// A successful valid refresh restores the graded facts.
	adapter.discoveryMode = ""
	if err := inst.eng.spec.PublishModels(product.ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertFacts(true)
}

// TestEngineLoadModelsSuccessThenFailureInvalidates proves the engine
// boundary orders admission before freshness: the latest refresh failed, so
// previously published graded evidence must not stay trusted.
func TestEngineLoadModelsSuccessThenFailureInvalidates(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	model := "future-family-17-3"
	assertFacts := func(want bool) {
		t.Helper()
		profile, err := inst.Resolve(t.Context(), &model)
		if err != nil {
			t.Fatal(err)
		}
		if got := profile.ReasoningEffortValues != nil; got != want {
			t.Fatalf("graded facts present=%v, want %v: %+v", got, want, profile.ReasoningEffortValues)
		}
	}
	// Success through the engine boundary publishes graded facts.
	adapter.discoveryMode = "graded"
	if _, err := inst.ListModels(t.Context(), 0); err != nil {
		t.Fatalf("graded refresh: %v", err)
	}
	assertFacts(true)
	// The latest refresh failed through the same boundary: facts go Unknown.
	adapter.discoveryMode = "empty"
	if _, err := inst.ListModels(t.Context(), 0); err == nil {
		t.Fatal("failed refresh returned no error")
	}
	assertFacts(false)
	old, err := inst.eng.spec.Inventory().LoadModels(t.Context())
	if err != nil || len(old.Models) != 2 {
		t.Fatalf("failed refresh replaced inventory: %+v err=%v", old, err)
	}
}

// TestEngineLoadModelsFailureThenSuccessRecovers proves the opposite ordering
// through the same boundary: a failure followed by a success republishes
// trusted graded evidence.
func TestEngineLoadModelsFailureThenSuccessRecovers(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	model := "future-family-17-3"
	assertFacts := func(want bool) {
		t.Helper()
		profile, err := inst.Resolve(t.Context(), &model)
		if err != nil {
			t.Fatal(err)
		}
		if got := profile.ReasoningEffortValues != nil; got != want {
			t.Fatalf("graded facts present=%v, want %v: %+v", got, want, profile.ReasoningEffortValues)
		}
	}
	adapter.discoveryMode = "empty"
	if _, err := inst.ListModels(t.Context(), 0); err == nil {
		t.Fatal("failed refresh returned no error")
	}
	assertFacts(false)
	adapter.discoveryMode = "graded"
	if _, err := inst.ListModels(t.Context(), 0); err != nil {
		t.Fatalf("graded refresh: %v", err)
	}
	assertFacts(true)
}

// TestEngineLoadModelsPublishThenCleanupFailureLeavesUnknown proves a refresh
// that publishes graded facts but ultimately fails in transport cleanup
// cannot leave that failed attempt's facts trusted. The failure joins after
// publication, so the engine must invalidate while still owning admission.
func TestEngineLoadModelsPublishThenCleanupFailureLeavesUnknown(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.discoveryMode = "graded"
	gate := &streamRetirementStarter{adapter: adapter, entered: make(chan struct{}), release: make(chan struct{}), waitErr: acp.ErrCleanupIncomplete}
	close(gate.release)
	svc.deps.starter = gate
	configured, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: fmt.Appendf(nil, "default_workspace: %q\ncredential_source: api_key\nadmin_policy: honor\n", t.TempDir()), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private-key")}}})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := configured.(*instance)
	if !ok {
		t.Fatalf("configured instance type: %T", configured)
	}
	// The fixture's fake process is known to be dead after cleanup failure.
	t.Cleanup(func() { _ = removeState(inst.eng.stateDir) })
	if _, err := inst.ListModels(t.Context(), 0); err == nil {
		t.Fatal("cleanup-failed refresh returned no error")
	} else if !errors.Is(err, acp.ErrCleanupIncomplete) {
		t.Fatalf("cleanup failure lost identity: %v", err)
	}
	model := "future-family-17-3"
	profile, err := inst.Resolve(context.Background(), &model)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReasoningEffortValues != nil {
		t.Fatalf("failed attempt's facts must not stay trusted, got %+v", profile.ReasoningEffortValues)
	}
	// The published inventory rows survive; only the graded evidence is stale.
	snapshot, err := inst.eng.spec.Inventory().LoadModels(context.Background())
	if err != nil || len(snapshot.Models) != 2 {
		t.Fatalf("cleanup failure replaced inventory: %+v err=%v", snapshot, err)
	}
}

// TestEngineLoadModelsOverlappingSuccessThenFailureInvalidates reproduces the
// re-review interleaving at the engine boundary without sleep timing: A and B
// both capture the pre-success generation, A succeeds, then B fails. The
// latest refresh failed, so the previously published evidence must go Unknown.
// Channel ordering plus synctest parking proves B captured the old generation
// before A published, deterministically.
func TestEngineLoadModelsOverlappingSuccessThenFailureInvalidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		adapter.discoveryMode = "graded-pause"
		adapter.discoveryEntered = make(chan struct{})
		adapter.discoveryRelease = make(chan struct{})
		inst := configureColdFixture(t, svc, "api_key")
		aRes := make(chan error, 1)
		bRes := make(chan error, 1)
		go func() { _, err := inst.ListModels(t.Context(), 0); aRes <- err }()
		<-adapter.discoveryEntered
		// A's process captured graded-pause; prevent B's future process from
		// closing the same channel a second time.
		adapter.discoveryEntered = nil
		go func() { _, err := inst.ListModels(t.Context(), 0); bRes <- err }()
		// Park until B has captured the pre-success generation and queued on
		// discovery admission behind A.
		synctest.Wait()
		// B's future discovery process will observe the failure mode.
		adapter.discoveryMode = "empty"
		close(adapter.discoveryRelease)
		if err := <-aRes; err != nil {
			t.Fatalf("leading graded refresh: %v", err)
		}
		if err := <-bRes; err == nil {
			t.Fatal("trailing refresh must fail")
		}
		model := "future-family-17-3"
		profile, err := inst.Resolve(t.Context(), &model)
		if err != nil {
			t.Fatal(err)
		}
		if profile.ReasoningEffortValues != nil {
			t.Fatalf("latest failed refresh must invalidate, got %+v", profile.ReasoningEffortValues)
		}
		snapshot, err := inst.eng.spec.Inventory().LoadModels(t.Context())
		if err != nil || len(snapshot.Models) != 2 {
			t.Fatalf("failed refresh replaced inventory: %+v err=%v", snapshot, err)
		}
	})
}
