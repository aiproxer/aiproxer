package featurehost

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// stageOccupancyFromBundle reduces one compiled bundle's diagnostic
// projections through the featurehost inventory authority.
func stageOccupancyFromBundle(b aipfeature.FeatureBundle) []diag.InventoryStageOccupancy {
	frozen, err := featurebundle.FreezeBundle(b, "feature")
	if err != nil {
		return []diag.InventoryStageOccupancy{}
	}
	metadata := InventoryFeatureMetadataFromProjections("feature", aipfeature.ProjectDiagnostics(frozen))
	if metadata.StageOccupancy == nil {
		return []diag.InventoryStageOccupancy{}
	}
	return metadata.StageOccupancy
}

type invPol struct {
	id  string
	ord int
}

func (p invPol) ID() string                        { return p.id }
func (p invPol) Order() int                        { return p.ord }
func (p invPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (p invPol) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type invTR struct {
	id string
}

func (t invTR) ID() string { return t.id }

func (invTR) Order() int { return 0 }

func (invTR) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type invSessionClassifier struct{ id string }

func (c invSessionClassifier) ID() string { return c.id }

func (invSessionClassifier) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

type invSecretGuard struct {
	id  string
	ord int
}

func (g invSecretGuard) ID() string                         { return g.id }
func (g invSecretGuard) Order() int                         { return g.ord }
func (invSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (invSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type invAttemptTransform struct {
	id  string
	ord int
}

func (t invAttemptTransform) ID() string                      { return t.id }
func (t invAttemptTransform) Order() int                      { return t.ord }
func (invAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (invAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type invStreamObserverFactory struct {
	id  string
	ord int
}

func (f invStreamObserverFactory) ID() string                      { return f.id }
func (f invStreamObserverFactory) Order() int                      { return f.ord }
func (invStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (invStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type inventoryFixtureRegistry struct {
	bundles map[string]aipfeature.FeatureBundle
}

func (r *inventoryFixtureRegistry) BuildFeatureBundle(factoryKey string, _ yaml.Node) (aipfeature.FeatureBundle, error) {
	if b, ok := r.bundles[factoryKey]; ok {
		return b, nil
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}

func TestStageOccupancyFromBundle_sessionClassifierUsesClassificationStage(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneSessionClassifier, "session-classification", sessionclassification.Classifier(invSessionClassifier{id: "local-classifier"})))
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	occ := stageOccupancyFromBundle(b)
	require.Len(t, occ, 1)
	require.Equal(t, aipfeature.StageIDSessionClassification, occ[0].StageID)
	require.Equal(t, []string{"classifier"}, occ[0].HandlerIDs)
	require.Equal(t, 1, occ[0].Count)
}

func TestStageOccupancyFromBundle_toolPoliciesSortedBeforeReactorsStablePrefixes(t *testing.T) {
	t.Parallel()
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{
		invPol{id: "z", ord: 2},
		invPol{id: "a", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, "test", []sdkhooks.ToolReactor{invTR{id: "react"}})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	occ := stageOccupancyFromBundle(b)
	var reaction *diag.InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == extensions.StageToolEventReaction {
			reaction = &occ[i]
			break
		}
	}
	if reaction == nil {
		t.Fatal("missing tool_event_reaction occupancy")
		return
	}
	want := []string{"tool_policy:a", "tool_policy:z", "react"}
	if len(reaction.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", reaction.HandlerIDs)
	}
	for i := range want {
		if reaction.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], reaction.HandlerIDs)
		}
	}
}

type stubTrafficObs struct{}

func (stubTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type stubUsageObs struct{}

func (stubUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

func TestStageOccupancyFromBundle_trafficObservationTrafficAndUsageObserverIndices(t *testing.T) {
	t.Parallel()
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "test", []traffic.Observer{
		stubTrafficObs{},
		nil,
		stubTrafficObs{},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "test", []usage.Observer{
		stubUsageObs{},
		stubUsageObs{},
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	occ := stageOccupancyFromBundle(b)
	var trafficOcc *diag.InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == aipfeature.StageIDTrafficObservation {
			trafficOcc = &occ[i]
			break
		}
	}
	if trafficOcc == nil {
		t.Fatal("missing traffic_observation occupancy")
		return
	}
	want := []string{"traffic_observer:0", "traffic_observer:2", "usage_observer:0", "usage_observer:1"}
	if len(trafficOcc.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", trafficOcc.HandlerIDs)
	}
	for i := range want {
		if trafficOcc.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], trafficOcc.HandlerIDs)
		}
	}
}

func TestStageOccupancyFromBundle_nilAttemptAndStreamEntriesSkippedWithoutPanic(t *testing.T) {
	t.Parallel()
	at := []request.AttemptTransform{
		nil,
		invAttemptTransform{id: "keep", ord: 1},
		nil,
	}
	so := []response.StreamObserverFactory{
		nil,
		invStreamObserverFactory{id: "keep", ord: 1},
		nil,
	}

	atOccs := aipfeature.PlaneAttemptTransforms.MaterializeOccupants(at)
	if len(atOccs) != 1 || atOccs[0].Label != "attempt_transform:keep" {
		t.Fatalf("unexpected at occupants: %+v", atOccs)
	}

	soOccs := aipfeature.PlaneStreamObserverFactories.MaterializeOccupants(so)
	if len(soOccs) != 1 || soOccs[0].Label != "stream_observer:keep" {
		t.Fatalf("unexpected so occupants: %+v", soOccs)
	}

	proj := []aipfeature.DiagnosticPlaneProjection{
		{
			PlaneID:   aipfeature.PlaneAttemptTransforms.ID,
			StageID:   aipfeature.PlaneAttemptTransforms.Diagnostics.StageID,
			Order:     aipfeature.PlaneAttemptTransforms.Diagnostics.Order,
			Occupants: atOccs,
		},
		{
			PlaneID:   aipfeature.PlaneStreamObserverFactories.ID,
			StageID:   aipfeature.PlaneStreamObserverFactories.Diagnostics.StageID,
			Order:     aipfeature.PlaneStreamObserverFactories.Diagnostics.Order,
			Occupants: soOccs,
		},
	}
	occ, _, err := reduceDiagnosticProjections(proj)
	if err != nil {
		t.Fatalf("reduceDiagnosticProjections err: %v", err)
	}
	var atOcc, soOcc *diag.InventoryStageOccupancy
	for i := range occ {
		switch occ[i].StageID {
		case extensions.StageCandidateAttemptTransform:
			atOcc = &occ[i]
		case extensions.StageFinalStreamObservation:
			soOcc = &occ[i]
		}
	}
	if atOcc == nil || len(atOcc.HandlerIDs) != 1 || atOcc.HandlerIDs[0] != "attempt_transform:keep" {
		t.Fatalf("attempt occupancy=%#v", atOcc)
	}
	if soOcc == nil || len(soOcc.HandlerIDs) != 1 || soOcc.HandlerIDs[0] != "stream_observer:keep" {
		t.Fatalf("stream occupancy=%#v", soOcc)
	}
}

func TestStageOccupancyFromBundle_attemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{
		invAttemptTransform{id: "z", ord: 2},
		invAttemptTransform{id: "a", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{
		invStreamObserverFactory{id: "z", ord: 2},
		invStreamObserverFactory{id: "a", ord: 1},
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	occ := stageOccupancyFromBundle(b)
	var atOcc, soOcc *diag.InventoryStageOccupancy
	for i := range occ {
		switch occ[i].StageID {
		case extensions.StageCandidateAttemptTransform:
			atOcc = &occ[i]
		case extensions.StageFinalStreamObservation:
			soOcc = &occ[i]
		}
	}
	if atOcc == nil {
		t.Fatal("missing candidate_attempt_transform occupancy")
	}
	wantAT := []string{"attempt_transform:a", "attempt_transform:z"}
	if len(atOcc.HandlerIDs) != len(wantAT) {
		t.Fatalf("attempt HandlerIDs=%#v", atOcc.HandlerIDs)
	}
	for i := range wantAT {
		if atOcc.HandlerIDs[i] != wantAT[i] {
			t.Fatalf("attempt idx %d want %q got %#v", i, wantAT[i], atOcc.HandlerIDs)
		}
	}
	if soOcc == nil {
		t.Fatal("missing final_stream_observation occupancy")
	}
	wantSO := []string{"stream_observer:a", "stream_observer:z"}
	if len(soOcc.HandlerIDs) != len(wantSO) {
		t.Fatalf("observer HandlerIDs=%#v", soOcc.HandlerIDs)
	}
	for i := range wantSO {
		if soOcc.HandlerIDs[i] != wantSO[i] {
			t.Fatalf("observer idx %d want %q got %#v", i, wantSO[i], soOcc.HandlerIDs)
		}
	}
	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: b.PlaneSet,
	})
	if got := snap.AttemptTransforms(); len(got) != 2 || got[0].ID() != "a" || got[1].ID() != "z" {
		t.Fatalf("snapshot AttemptTransforms sort mismatch: %#v", got)
	}
	if got := snap.StreamObserverFactories(); len(got) != 2 || got[0].ID() != "a" || got[1].ID() != "z" {
		t.Fatalf("snapshot StreamObserverFactories sort mismatch: %#v", got)
	}
}

func TestStageOccupancyFromBundle_secretGuardsSortedWithPrefix(t *testing.T) {
	t.Parallel()
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test", []secretguard.Guard{
		invSecretGuard{id: "z", ord: 2},
		invSecretGuard{id: "a", ord: 1},
		invSecretGuard{id: "b", ord: 1},
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	occ := stageOccupancyFromBundle(b)
	var guardOcc *diag.InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == aipfeature.StageIDSecretGuard {
			guardOcc = &occ[i]
			break
		}
	}
	if guardOcc == nil {
		t.Fatal("missing secret_guard occupancy")
		return
	}
	want := []string{"secret_guard:a", "secret_guard:b", "secret_guard:z"}
	if len(guardOcc.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", guardOcc.HandlerIDs)
	}
	for i := range want {
		if guardOcc.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], guardOcc.HandlerIDs)
		}
	}
	if guardOcc.Count != len(want) {
		t.Fatalf("count %d want %d", guardOcc.Count, len(want))
	}
}

// TestInventoryFeatureMetadataFromProjections_ToolPoliciesAloneDoNotImplyAuxiliaryRequests
// pins the privilege rule formerly asserted through the diagnostics DTO:
// tool-call policies occupy the tool-event-reaction stage without granting the
// auxiliary_requests privilege.
func TestInventoryFeatureMetadataFromProjections_ToolPoliciesAloneDoNotImplyAuxiliaryRequests(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{invPol{id: "solo-pol", ord: 0}})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)

	metadata := InventoryFeatureMetadataFromProjections("feat-pol-only", aipfeature.ProjectDiagnostics(b.PlaneSet))
	require.Empty(t, metadata.BundleError)
	assert.False(t, metadata.Privileges.AuxiliaryRequests,
		"tool-call policies alone must not imply auxiliary_requests privilege")

	var sawPol bool
	for _, occ := range metadata.StageOccupancy {
		if occ.StageID != extensions.StageToolEventReaction {
			continue
		}
		for _, hid := range occ.HandlerIDs {
			if hid == "tool_policy:solo-pol" {
				sawPol = true
			}
		}
	}
	if !sawPol {
		t.Fatalf("missing tool_policy inventory tag in occupancy %#v", metadata.StageOccupancy)
	}
}

func TestReduceDiagnosticProjections_DisposableProbePlane(t *testing.T) {
	t.Parallel()

	projections := []aipfeature.DiagnosticPlaneProjection{
		{
			PlaneID:       "disposable_probe",
			StageID:       extensions.StageToolEventReaction,
			CoalesceGroup: "probe_group",
			Order:         85,
			Occupants: []aipfeature.DiagnosticOccupant{
				{Label: "probe:alpha"},
			},
			Privileges: aipfeature.PrivilegeProjection{
				Flags: []string{aipfeature.PrivilegeAuxiliaryRequests},
			},
		},
		{
			PlaneID:       "tool_policies",
			StageID:       extensions.StageToolEventReaction,
			CoalesceGroup: "probe_group",
			Order:         85,
			Occupants: []aipfeature.DiagnosticOccupant{
				{Label: "tool_policy:beta"},
			},
		},
	}

	occ, priv, err := reduceDiagnosticProjections(projections)
	require.NoError(t, err)
	require.Len(t, occ, 1)
	assert.Equal(t, extensions.StageToolEventReaction, occ[0].StageID)
	assert.Equal(t, []string{"probe:alpha", "tool_policy:beta"}, occ[0].HandlerIDs)
	assert.True(t, priv.AuxiliaryRequests)
}

func TestReduceDiagnosticProjections_UnknownPrivilegeFails(t *testing.T) {
	t.Parallel()

	projections := []aipfeature.DiagnosticPlaneProjection{
		{
			PlaneID: "bad_priv",
			Privileges: aipfeature.PrivilegeProjection{
				Flags: []string{"unknown_priv_flag_xyz"},
			},
		},
	}

	_, _, err := reduceDiagnosticProjections(projections)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown privilege flag "unknown_priv_flag_xyz"`)
}

// TestInventoryFeatureMetadataFromProjections_UnknownPrivilegeBecomesBundleError
// pins the reduction error contract: an unknown privilege flag surfaces
// verbatim as the operator-visible bundle_error with empty occupancy.
func TestInventoryFeatureMetadataFromProjections_UnknownPrivilegeBecomesBundleError(t *testing.T) {
	t.Parallel()

	metadata := InventoryFeatureMetadataFromProjections("feat-bad", []aipfeature.DiagnosticPlaneProjection{
		{PlaneID: "bad_priv", Privileges: aipfeature.PrivilegeProjection{Flags: []string{"unknown_priv_flag_xyz"}}},
	})
	assert.Equal(t, "feat-bad", metadata.InstanceID)
	assert.Empty(t, metadata.StageOccupancy)
	assert.Equal(t, diag.InventoryPrivileges{}, metadata.Privileges)
	assert.Contains(t, metadata.BundleError, `unknown privilege flag "unknown_priv_flag_xyz"`)
}

// TestInventoryMetadataFromMergeSurface_ReducesEnabledFeatureProjections pins
// that the compiled merge surface is reduced per enabled feature instance and
// that disabled registrations never contribute a row.
func TestInventoryMetadataFromMergeSurface_ReducesEnabledFeatureProjections(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "mixed", []request.AttemptTransform{
		invAttemptTransform{id: "att-z", ord: 20},
		invAttemptTransform{id: "att-a", ord: 10},
	}))
	registry := &inventoryFixtureRegistry{bundles: map[string]aipfeature.FeatureBundle{
		"mixed":    aipfeature.BundleFromPlanes(cs.Freeze(), nil),
		"disabled": aipfeature.BundleFromPlanes(aipfeature.NewContributionSet().Freeze(), nil),
	}}
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{
		{ID: "feat-mixed", Kind: "mixed", Enabled: true},
		{ID: "feat-off", Kind: "disabled", Enabled: false},
	}}}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(registry, config.RegistrationsFromConfig(cfg), featurebundle.HostContributions{})
	require.NoError(t, err)

	metadata := InventoryMetadataFromMergeSurface(merged)
	require.Len(t, metadata, 1)
	require.Equal(t, "feat-mixed", metadata[0].InstanceID)
	require.Len(t, metadata[0].StageOccupancy, 1)
	assert.Equal(t, extensions.StageCandidateAttemptTransform, metadata[0].StageOccupancy[0].StageID)
	assert.Equal(t, []string{"attempt_transform:att-a", "attempt_transform:att-z"}, metadata[0].StageOccupancy[0].HandlerIDs)
	assert.Equal(t, 2, metadata[0].StageOccupancy[0].Count)
	assert.True(t, metadata[0].Privileges.AuxiliaryRequests)
}

func TestInventoryMetadataFromMergeSurface_EmptySurfaceYieldsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, InventoryMetadataFromMergeSurface(featurebundle.GeneratedMergeSurface{}))
}

// TestInventoryMetadataForConfig_AssociatesSecretGuardPostureWithInstance pins
// the relocated secret-guard instance association: a configured instance gets
// the composed posture with its own instance id, a configured instance without
// a compiled row keeps an empty stage occupancy, and compiled occupancy of the
// secrets-guard instance is preserved.
func TestInventoryMetadataForConfig_AssociatesSecretGuardPostureWithInstance(t *testing.T) {
	t.Parallel()

	posture := &diag.InventorySecretGuard{
		CatalogEntryCount: 16,
		SourceCategories:  []string{"proxy_env", "operator_env"},
		AccessMode:        "single_user",
		Action:            "block",
	}
	compiled := []diag.InventoryFeatureMetadata{
		{
			InstanceID:     "sg-main",
			StageOccupancy: []diag.InventoryStageOccupancy{{StageID: aipfeature.StageIDSecretGuard, HandlerIDs: []string{"secret_guard:sg-core"}, Count: 1}},
		},
		{InstanceID: "other-feature", StageOccupancy: []diag.InventoryStageOccupancy{}},
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{
		{ID: "sg-main", Kind: "secrets-guard", Enabled: true},
		{ID: "sg-disabled", Kind: "secrets-guard", Enabled: false},
		{ID: "other-feature", Kind: "other", Enabled: true},
	}}}

	metadata := InventoryMetadataForConfig(cfg, compiled, posture)
	require.Len(t, metadata, 3)

	require.Equal(t, "sg-main", metadata[0].InstanceID)
	require.NotNil(t, metadata[0].SecretGuard)
	assert.Equal(t, "sg-main", metadata[0].SecretGuard.InstanceID)
	assert.Equal(t, 16, metadata[0].SecretGuard.CatalogEntryCount)
	assert.Equal(t, []string{"proxy_env", "operator_env"}, metadata[0].SecretGuard.SourceCategories)
	require.Len(t, metadata[0].StageOccupancy, 1, "compiled occupancy must survive the association")

	require.Equal(t, "other-feature", metadata[1].InstanceID)
	assert.Nil(t, metadata[1].SecretGuard, "non-secret-guard instances never gain a secret_guard row")

	require.Equal(t, "sg-disabled", metadata[2].InstanceID)
	require.NotNil(t, metadata[2].SecretGuard, "a configured instance without a compiled row keeps an empty occupancy row")
	assert.Empty(t, metadata[2].StageOccupancy)
	assert.Equal(t, 16, metadata[2].SecretGuard.CatalogEntryCount)

	// Defensive copy: mutating the returned row or categories must not touch
	// the caller's posture or a sibling row.
	metadata[0].SecretGuard.SourceCategories[0] = "mutated"
	assert.Equal(t, "proxy_env", posture.SourceCategories[0])
	assert.Equal(t, []string{"proxy_env", "operator_env"}, metadata[2].SecretGuard.SourceCategories)

	// The caller's compiled slice stays untouched.
	require.Len(t, compiled, 2)
	assert.Nil(t, compiled[0].SecretGuard)
}

// TestInventoryMetadataForConfig_NilPostureKeepsEmptyRow pins the disabled /
// not-composed generation contract: a nil posture still yields a present but
// empty secret-guard row so the inventory shape stays stable.
func TestInventoryMetadataForConfig_NilPostureKeepsEmptyRow(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{
		{ID: "sg-main", Kind: "secrets-guard", Enabled: true},
	}}}

	metadata := InventoryMetadataForConfig(cfg, nil, nil)
	require.Len(t, metadata, 1)
	require.Equal(t, "sg-main", metadata[0].InstanceID)
	require.NotNil(t, metadata[0].SecretGuard)
	assert.Equal(t, diag.InventorySecretGuard{InstanceID: "sg-main"}, *metadata[0].SecretGuard)
	assert.Empty(t, metadata[0].StageOccupancy)
}

func TestInventoryMetadataForConfig_NilConfigYieldsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, InventoryMetadataForConfig(nil, []diag.InventoryFeatureMetadata{{InstanceID: "feat"}}, nil))
}

// TestInventoryFeatureMetadataFromProjections_ValidMixedBundleSortingAndNils
// pins the reduction's sorting, nil filtering, and prefix rules for the mixed
// plane set formerly asserted through the diagnostics DTO.
func TestInventoryFeatureMetadataFromProjections_ValidMixedBundleSortingAndNils(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "mixed", []toolpolicy.Policy{
		nil,
		invPol{id: "pol-b", ord: 10},
		invPol{id: "pol-a", ord: 10},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "mixed", []request.AttemptTransform{
		invAttemptTransform{id: "att-z", ord: 20},
		invAttemptTransform{id: "att-a", ord: 10},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "mixed", []response.StreamObserverFactory{
		invStreamObserverFactory{id: "so-z", ord: 20},
		invStreamObserverFactory{id: "so-a", ord: 10},
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)

	metadata := InventoryFeatureMetadataFromProjections("feat-mixed", aipfeature.ProjectDiagnostics(b.PlaneSet))
	require.Empty(t, metadata.BundleError)
	require.NotEmpty(t, metadata.StageOccupancy)

	for _, occ := range metadata.StageOccupancy {
		switch occ.StageID {
		case extensions.StageToolEventReaction:
			assert.Equal(t, []string{"tool_policy:pol-a", "tool_policy:pol-b"}, occ.HandlerIDs)
		case extensions.StageCandidateAttemptTransform:
			assert.Equal(t, []string{"attempt_transform:att-a", "attempt_transform:att-z"}, occ.HandlerIDs)
		case extensions.StageFinalStreamObservation:
			assert.Equal(t, []string{"stream_observer:so-a", "stream_observer:so-z"}, occ.HandlerIDs)
		}
	}
	assert.True(t, metadata.Privileges.AuxiliaryRequests)
}
