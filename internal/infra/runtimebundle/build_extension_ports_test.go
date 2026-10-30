package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

type portsStubAttemptTransform struct {
	id  string
	ord int
}

func (s portsStubAttemptTransform) ID() string                      { return s.id }
func (s portsStubAttemptTransform) Order() int                      { return s.ord }
func (portsStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (portsStubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type portsStubStreamObserverFactory struct {
	id  string
	ord int
}

func (s portsStubStreamObserverFactory) ID() string                      { return s.id }
func (s portsStubStreamObserverFactory) Order() int                      { return s.ord }
func (portsStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (portsStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

// TestBuildRuntimeSnapshot_featureBundlePortsReachSnap mirrors bootstrap wiring:
// MergeBundlesGenerated → buildRuntimeSnapshot → sorted Snap accessors.
// Inventory occupancy IDs/stages for these ports are covered in package diag.
func TestBuildRuntimeSnapshot_featureBundlePortsReachSnap(t *testing.T) {
	t.Parallel()
	bundle := testkit.FeatureBundle(t, "test-feature", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test-feature", []request.AttemptTransform{
			portsStubAttemptTransform{id: "z", ord: 2},
			portsStubAttemptTransform{id: "a", ord: 1},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "test-feature", []response.StreamObserverFactory{
			portsStubStreamObserverFactory{id: "z", ord: 2},
			portsStubStreamObserverFactory{id: "a", ord: 1},
		})
	}, nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	genMerged, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: genMerged.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	gotAT := snap.AttemptTransforms()
	if len(gotAT) != 2 || gotAT[0].ID() != "a" || gotAT[1].ID() != "z" {
		t.Fatalf("Snap.AttemptTransforms IDs=%v want [a z]", portAttemptIDs(gotAT))
	}
	gotSO := snap.StreamObserverFactories()
	if len(gotSO) != 2 || gotSO[0].ID() != "a" || gotSO[1].ID() != "z" {
		t.Fatalf("Snap.StreamObserverFactories IDs=%v want [a z]", portStreamIDs(gotSO))
	}
}

func portAttemptIDs(in []request.AttemptTransform) []string {
	out := make([]string, len(in))
	for i, tr := range in {
		if tr == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = tr.ID()
	}
	return out
}

func portStreamIDs(in []response.StreamObserverFactory) []string {
	out := make([]string, len(in))
	for i, f := range in {
		if f == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = f.ID()
	}
	return out
}
