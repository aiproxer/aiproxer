// Package main is a separate-module external feature SDK compile and test fixture
// (requirements 8.1, 8.2, 8.3). It imports only public aipsdk/aipapi packages and stdlib.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

const (
	tinyFeaturePluginID = "external_tiny_feature"
	tinySubmitHookID    = "external_tiny_submit_hook"
)

// tinySubmitHook implements hooks.SubmitHook for testing the feature plane contract.
type tinySubmitHook struct {
	id    string
	order int
}

func (h tinySubmitHook) ID() string {
	return h.id
}

func (h tinySubmitHook) Order() int {
	return h.order
}

func (h tinySubmitHook) FailureMode() hooks.FailureMode {
	return hooks.FailOpen
}

func (h tinySubmitHook) Handle(_ context.Context, _ *aipapi.Call, _ *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

// BuildTinyFeatureBundle builds a feature bundle contributing a tinySubmitHook to PlaneSubmitHooks.
func BuildTinyFeatureBundle(hookID string, order int) (feature.FeatureBundle, error) {
	cs := feature.NewContributionSet()
	hook := tinySubmitHook{id: hookID, order: order}
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, tinyFeaturePluginID, []hooks.SubmitHook{hook}); err != nil {
		return feature.FeatureBundle{}, fmt.Errorf("contribute failed: %w", err)
	}
	bundle := feature.BundleFromPlanes(cs.Freeze(), nil)
	if err := bundle.Validate(); err != nil {
		return feature.FeatureBundle{}, fmt.Errorf("bundle validate failed: %w", err)
	}
	return bundle, nil
}

// ArbitraryUngeneratedPlane creates an ungenerated, unbound Plane definition.
var ArbitraryUngeneratedPlane = feature.Plane[[]string]{
	ID:           "arbitrary_ungenerated_plane",
	Multiplicity: feature.MultOrdered,
	Rules: feature.SourceRules{
		Feature: feature.CombConcatenate,
	},
}

// VerifyContract executes the full contract verification for the external feature SDK.
func VerifyContract() error {
	// 1. Build tiny feature via NewContributionSet -> Contribute -> Freeze -> BundleFromPlanes
	bundle, err := BuildTinyFeatureBundle(tinySubmitHookID, 42)
	if err != nil {
		return fmt.Errorf("BuildTinyFeatureBundle: %w", err)
	}

	// 2. Test bundle/plane value
	hookList := feature.Get(bundle.PlaneSet, feature.PlaneSubmitHooks)
	if len(hookList) != 1 {
		return fmt.Errorf("expected 1 hook, got %d", len(hookList))
	}
	if hookList[0].ID() != tinySubmitHookID {
		return fmt.Errorf("expected hook ID %q, got %q", tinySubmitHookID, hookList[0].ID())
	}
	if hookList[0].Order() != 42 {
		return fmt.Errorf("expected hook order 42, got %d", hookList[0].Order())
	}

	// 3. Test public replay/read
	replaySet := feature.NewContributionSet()
	if err := bundle.PlaneSet.ReplayTo(replaySet, "external_replayer"); err != nil {
		return fmt.Errorf("ReplayTo: %w", err)
	}
	replayedFrozen := replaySet.Freeze()
	replayedHooks := feature.Get(replayedFrozen, feature.PlaneSubmitHooks)
	if len(replayedHooks) != 1 {
		return fmt.Errorf("expected 1 replayed hook, got %d", len(replayedHooks))
	}
	if replayedHooks[0].ID() != tinySubmitHookID {
		return fmt.Errorf("expected replayed hook ID %q, got %q", tinySubmitHookID, replayedHooks[0].ID())
	}

	// 4. Same consumer: arbitrary ungenerated plane must fail with errors.Is(err, feature.ErrUngeneratedPlane)
	badSet := feature.NewContributionSet()
	err = feature.Contribute(badSet, ArbitraryUngeneratedPlane, tinyFeaturePluginID, []string{"unsupported"})
	if err == nil {
		return fmt.Errorf("expected error contributing to ungenerated plane, got nil")
	}
	if !errors.Is(err, feature.ErrUngeneratedPlane) {
		return fmt.Errorf("expected errors.Is(err, feature.ErrUngeneratedPlane), got %w", err)
	}

	return nil
}

func main() {
	if err := VerifyContract(); err != nil {
		fmt.Fprintf(os.Stderr, "external_feature_sdk: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("external_feature_sdk: ok")
}
