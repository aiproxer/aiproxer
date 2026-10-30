package featurebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mergeStubAttemptTransform struct{ id string }

func (s mergeStubAttemptTransform) ID() string                        { return s.id }
func (s mergeStubAttemptTransform) Order() int                        { return 0 }
func (s mergeStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (s mergeStubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type mergeStubStreamObserverFactory struct{ id string }

func (s mergeStubStreamObserverFactory) ID() string                        { return s.id }
func (s mergeStubStreamObserverFactory) Order() int                        { return 0 }
func (s mergeStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (s mergeStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func TestGeneratedMergeSurface_carriesAttemptTransforms(t *testing.T) {
	t.Parallel()
	bundle := testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "feat", []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}})
	}, nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	at := aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(at) != 1 || at[0].ID() != "at" {
		t.Fatalf("AttemptTransforms=%v", at)
	}
}

func TestGeneratedMergeSurface_carriesStreamObservers(t *testing.T) {
	t.Parallel()
	bundle := testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feat", []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}})
	}, nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	so := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
	if len(so) != 1 || so[0].ID() != "obs" {
		t.Fatalf("StreamObserverFactories=%v", so)
	}
}

func TestSnapshotOptions_carriesFeaturePlanes(t *testing.T) {
	t.Parallel()
	frozen := testkit.FreezeBundle(testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "feat", []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feat", []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}})
	}, nil))
	opts := extensions.SnapshotOptions{
		FeaturePlanes: frozen,
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), opts)
	if len(snap.AttemptTransforms()) != 1 || len(snap.StreamObserverFactories()) != 1 {
		t.Fatalf("SnapshotOptions fields missing: %+v", opts)
	}
}

type mergeStubCompactionObserver struct{ id string }

func (s mergeStubCompactionObserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type mergeStubCompactionPreserver struct{ id string }

func (s mergeStubCompactionPreserver) ID() string { return s.id }

func (mergeStubCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (mergeStubCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (mergeStubCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

// TestCompactionObservers_portWiring proves the compaction observer surface is
// additive through the single merge point, the snapshot options, and the frozen
// snapshot accessor with defensive-copy semantics (requirements 2.1-2.2).
func TestCompactionObservers_portWiring(t *testing.T) {
	t.Parallel()
	bundle := testkit.FeatureBundle(t, "feat-1", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "feat-1", []compaction.Observer{mergeStubCompactionObserver{id: "a"}, mergeStubCompactionObserver{id: "z"}})
	}, nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle, testkit.FeatureBundle(t, "feat-2", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "feat-2", []compaction.Observer{mergeStubCompactionObserver{id: "m"}})
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	obs := aipfeature.Get(gen.Frozen, aipfeature.PlaneCompactionObservers)
	if len(obs) != 3 {
		t.Fatalf("CompactionObservers len=%d want 3", len(obs))
	}
	for i, want := range []string{"a", "z", "m"} {
		got, ok := obs[i].(mergeStubCompactionObserver)
		if !ok || got.id != want {
			t.Fatalf("merged observer[%d]=%T/%q want %q", i, obs[i], got.id, want)
		}
	}

	opts := extensions.SnapshotOptions{FeaturePlanes: gen.Frozen}

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), opts)
	got := snap.CompactionObservers()
	if len(got) != 3 {
		t.Fatalf("snapshot CompactionObservers len=%d want 3", len(got))
	}
	for i, want := range []string{"a", "z", "m"} {
		observer, ok := got[i].(mergeStubCompactionObserver)
		if !ok || observer.id != want {
			t.Fatalf("snapshot observer[%d]=%T/%q want %q", i, got[i], observer.id, want)
		}
	}
	// The accessor must return a defensive copy: mutating it cannot change the
	// frozen snapshot backing store.
	got[0] = nil
	again := snap.CompactionObservers()
	if len(again) != 3 || again[0] == nil {
		t.Fatal("CompactionObservers() must return a defensive copy of the frozen slice")
	}
}

func TestCompactionPreservers_portWiringAndDefensiveSnapshot(t *testing.T) {
	t.Parallel()
	bundle := testkit.FeatureBundle(t, "feat-1", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{mergeStubCompactionPreserver{id: "a"}, mergeStubCompactionPreserver{id: "z"}})
	}, nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle, testkit.FeatureBundle(t, "feat-2", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "feat-2", []compaction.Preserver{mergeStubCompactionPreserver{id: "m"}})
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	pres := aipfeature.Get(gen.Frozen, aipfeature.PlaneCompactionPreservers)
	if len(pres) != 3 {
		t.Fatalf("CompactionPreservers len=%d want 3", len(pres))
	}
	for i, want := range []string{"a", "z", "m"} {
		got, ok := pres[i].(mergeStubCompactionPreserver)
		if !ok || got.id != want {
			t.Fatalf("merged preserver[%d]=%T/%q want %q", i, pres[i], got.id, want)
		}
	}

	opts := extensions.SnapshotOptions{FeaturePlanes: gen.Frozen}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), opts)
	got := snap.CompactionPreservers()
	if len(got) != 3 {
		t.Fatalf("snapshot CompactionPreservers len=%d want 3", len(got))
	}
	pres[0] = nil
	if frozen := snap.CompactionPreservers(); frozen[0] == nil {
		t.Fatal("snapshot must freeze an input defensive copy")
	}
	got[0] = nil
	if again := snap.CompactionPreservers(); len(again) != 3 || again[0] == nil {
		t.Fatal("CompactionPreservers() must return a defensive copy")
	}
}

func TestRequestRuntimeSnapshot_exposesAttemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeBundle(testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "feat", []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feat", []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}})
		}, nil)),
	})
	gotAT := snap.AttemptTransforms()
	gotSO := snap.StreamObserverFactories()
	if len(gotAT) != 1 || gotAT[0].ID() != "at" {
		t.Fatalf("AttemptTransforms()=%v", gotAT)
	}
	if len(gotSO) != 1 || gotSO[0].ID() != "obs" {
		t.Fatalf("StreamObserverFactories()=%v", gotSO)
	}
	gotAT[0] = nil
	gotSO[0] = nil
	if len(snap.AttemptTransforms()) != 1 || snap.AttemptTransforms()[0] == nil {
		t.Fatal("AttemptTransforms must return a defensive copy")
	}
	if len(snap.StreamObserverFactories()) != 1 || snap.StreamObserverFactories()[0] == nil {
		t.Fatal("StreamObserverFactories must return a defensive copy")
	}
}

func TestContributeBundle_NilContributionSetError(t *testing.T) {
	t.Parallel()

	b := testkit.FeatureBundle(t, "my-plugin", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "my-plugin", []sdkhooks.SubmitHook{
			mergeStubSubmitHook{id: "sub-1"},
		})
	}, nil)

	err := featurebundle.ContributeBundle(nil, "my-plugin", b)
	require.EqualError(t, err, "featurebundle: nil ContributionSet")
}

func TestContributeBundle_EmptyPluginIDFallback(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	b := testkit.FeatureBundle(t, "sub-1", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "sub-1", []sdkhooks.SubmitHook{
			mergeStubSubmitHook{id: "sub-1"},
		})
	}, nil)

	err := featurebundle.ContributeBundle(cs, "", b)
	require.NoError(t, err)

	frozen := cs.Freeze()
	hooks := aipfeature.Get(frozen, aipfeature.PlaneSubmitHooks)
	require.Len(t, hooks, 1)
	assert.Equal(t, "sub-1", hooks[0].ID())
}

type mergeStubSubmitHook struct{ id string }

func (s mergeStubSubmitHook) ID() string                        { return s.id }
func (s mergeStubSubmitHook) Order() int                        { return 0 }
func (s mergeStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s mergeStubSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type mergeStubTerminalProvider struct {
	id string
}

func (p mergeStubTerminalProvider) ID() string {
	return p.id
}

func (p mergeStubTerminalProvider) ProviderIdentity() (string, error) {
	return p.id, nil
}

func (p mergeStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

func TestFreezeBundle_ParityAndFallbacks(t *testing.T) {
	t.Parallel()

	// 1. Explicit empty SessionOpeners vs nil SessionOpeners
	csEmpty := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(csEmpty, aipfeature.PlaneSessionOpeners, "test-plugin", []session.Opener{}))
	bExplicitEmpty := aipfeature.BundleFromPlanes(csEmpty.Freeze(), nil)

	frozenEmpty, err := featurebundle.FreezeBundle(bExplicitEmpty, "test-plugin")
	require.NoError(t, err)
	gotEmptyOpeners := aipfeature.Get(frozenEmpty, aipfeature.PlaneSessionOpeners)
	require.NotNil(t, gotEmptyOpeners, "explicitly empty SessionOpeners must materialize non-nil slice")
	assert.Empty(t, gotEmptyOpeners)

	bNilOpeners := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
	}
	frozenNil, err := featurebundle.FreezeBundle(bNilOpeners, "test-plugin")
	require.NoError(t, err)
	gotNilOpeners := aipfeature.Get(frozenNil, aipfeature.PlaneSessionOpeners)
	assert.Nil(t, gotNilOpeners, "nil SessionOpeners must remain nil")

	// 2. Empty plugin ID fallback to terminal decision provider identity
	csProv := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(csProv, aipfeature.PlaneTerminalDecisionProvider, "provider-auth", terminaldecision.Provider(mergeStubTerminalProvider{id: "provider-auth"})))
	bWithProvider := aipfeature.BundleFromPlanes(csProv.Freeze(), nil)

	frozenProv, err := featurebundle.FreezeBundle(bWithProvider, "")
	require.NoError(t, err)
	prov := aipfeature.Get(frozenProv, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, prov)
	id, err := terminaldecision.ProviderIdentity(prov)
	require.NoError(t, err)
	assert.Equal(t, "provider-auth", id)

	// 3. Empty plugin ID with no provider defaults to "feature"
	csGen := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(csGen, aipfeature.PlaneSubmitHooks, "sub-gen", []sdkhooks.SubmitHook{
		mergeStubSubmitHook{id: "sub-gen"},
	}))
	bGeneric := aipfeature.BundleFromPlanes(csGen.Freeze(), nil)

	frozenGeneric, err := featurebundle.FreezeBundle(bGeneric, "")
	require.NoError(t, err)
	subHooks := aipfeature.Get(frozenGeneric, aipfeature.PlaneSubmitHooks)
	require.Len(t, subHooks, 1)
	assert.Equal(t, "sub-gen", subHooks[0].ID())
}
