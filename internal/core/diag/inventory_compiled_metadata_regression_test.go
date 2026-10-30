package diag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"gopkg.in/yaml.v3"
)

func TestInventoryHandler_UsesAcceptedFeatureMetadataWithoutRebuildingFactories(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "inventory-fixture"
		factoryID  = "counting-feature"
	)
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{
		ID: instanceID, Kind: factoryID, Enabled: true,
	}}}}
	registry := &changingInventoryFactory{}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(
		registry,
		config.RegistrationsFromConfig(cfg),
		featurebundle.HostContributions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registry.calls != 1 {
		t.Fatalf("generation compile factory calls: got %d, want 1", registry.calls)
	}
	metadata := featurehost.InventoryMetadataFromMergeSurface(merged)
	if len(metadata) != 1 || metadata[0].InstanceID != instanceID {
		t.Fatalf("compiled feature projections: %+v", metadata)
	}
	acceptedMetadata := metadata[0]

	extras := &diag.InventoryExtras{
		Reg:             registry,
		Registrations:   config.RegistrationsFromConfig(cfg),
		FeatureMetadata: []diag.InventoryFeatureMetadata{acceptedMetadata},
	}
	handler, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}

	var snapshots []diag.InventorySnapshot
	for range 2 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/inventory", nil).WithContext(context.Background())
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET status %d: %s", recorder.Code, recorder.Body.String())
		}
		var snapshot diag.InventorySnapshot
		if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if registry.calls != 1 {
		t.Fatalf("feature factory calls after repeated inventory GETs: got %d, want 1 (generation compile only)", registry.calls)
	}
	if len(acceptedMetadata.StageOccupancy) != 1 || len(acceptedMetadata.StageOccupancy[0].HandlerIDs) != 1 {
		t.Fatalf("fixture must produce one accepted occupant: %+v", acceptedMetadata)
	}
	for i, snapshot := range snapshots {
		if len(snapshot.Extensions.Features) != 1 {
			t.Fatalf("GET %d feature rows: got %d, want 1", i+1, len(snapshot.Extensions.Features))
		}
		feature := snapshot.Extensions.Features[0]
		if len(feature.StageOccupancy) != 1 || feature.StageOccupancy[0].HandlerIDs[0] != acceptedMetadata.StageOccupancy[0].HandlerIDs[0] {
			t.Errorf("GET %d occupancy did not use accepted generation metadata: %+v", i+1, feature.StageOccupancy)
		}
		if !feature.Privileges.AuxiliaryRequests {
			t.Errorf("GET %d lost accepted auxiliary_requests privilege", i+1)
		}
	}
}

type changingInventoryFactory struct {
	calls int
}

func (f *changingInventoryFactory) BuildFeatureBundle(_ string, _ yaml.Node) (aipfeature.FeatureBundle, error) {
	f.calls++
	id := "compiled-attempt"
	if f.calls > 1 {
		id = fmt.Sprintf("recomputed-%d", f.calls)
	}
	contributions := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(contributions, aipfeature.PlaneAttemptTransforms, "counting-feature", []request.AttemptTransform{
		inventoryAttemptTransform{id: id},
	}); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return aipfeature.BundleFromPlanes(contributions.Freeze(), nil), nil
}

type inventoryAttemptTransform struct{ id string }

func (t inventoryAttemptTransform) ID() string                      { return t.id }
func (inventoryAttemptTransform) Order() int                        { return 10 }
func (inventoryAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (inventoryAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}
