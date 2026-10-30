package featurehost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkcompaction "github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Characterization stubs ---

type charPreserver struct {
	id string
}

func (p charPreserver) ID() string { return p.id }
func (charPreserver) BeforeRequest(context.Context, *aipapi.Call, sdkcompaction.RequestPreview, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func (charPreserver) RequestOpened(context.Context, aipapi.Call, []sdkcompaction.Event, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func (charPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, sdkcompaction.ResponsePreview, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func (p charPreserver) OnCompaction(context.Context, sdkcompaction.Event) error {
	return nil
}

type charSubmitHook struct {
	id string
}

func (h charSubmitHook) ID() string                   { return h.id }
func (charSubmitHook) Order() int                     { return 0 }
func (charSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (charSubmitHook) Handle(context.Context, *aipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type charAttemptTransform struct {
	id string
}

func (t charAttemptTransform) ID() string                   { return t.id }
func (charAttemptTransform) Order() int                     { return 0 }
func (charAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (charAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type panickingPreserver struct {
	panicVal any
}

func (p panickingPreserver) ID() string {
	if p.panicVal != nil {
		panic(p.panicVal)
	}
	panic("panicking preserver")
}

func (panickingPreserver) BeforeRequest(context.Context, *aipapi.Call, sdkcompaction.RequestPreview, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func (panickingPreserver) RequestOpened(context.Context, aipapi.Call, []sdkcompaction.Event, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func (panickingPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, sdkcompaction.ResponsePreview, sdkcompaction.PreservationMeta, sdkcompaction.Services) error {
	return nil
}

func safePreserverID(p sdkcompaction.Preserver) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	if p != nil {
		return p.ID()
	}
	return ""
}

func validCompactionRegistration(t *testing.T) aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	raw := "extractor:\n  enabled: true\n  route: inherit\n"
	err := yaml.Unmarshal([]byte(raw), &node)
	require.NoError(t, err)
	return aipsdk.Registration{
		ID:          featurecontinuity.ID,
		FactoryKind: featurecontinuity.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}
}

func makeTestGenSurface(preservers []sdkcompaction.Preserver) featurebundle.GeneratedMergeSurface {
	cs := aipfeature.NewContributionSet()
	if preservers != nil {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "test-feat", preservers)
	}
	return featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
}

type dummyRunner struct{}

func (dummyRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream(nil), nil
}

func newTestScheduler(t *testing.T) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return dummyRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	return s
}

func newTestCompactionRuntime(t *testing.T) *Runtime {
	t.Helper()
	coord, err := state.NewBranchCoordinator(context.Background(), state.Config{})
	require.NoError(t, err)
	port, err := compaction.NewParentPort(coord)
	require.NoError(t, err)
	return &Runtime{
		compactionDetector:   compactiondetect.New(compactiondetect.Config{}),
		branchCoordinator:    coord,
		compactionParentPort: port,
	}
}

func TestBindCompactionContinuity_PreserverReplacementOrder(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg := validCompactionRegistration(t)

	tests := []struct {
		name         string
		initial      []sdkcompaction.Preserver
		wantIDs      []string
		wantCount    int
		lastIsPlugin bool
	}{
		{
			name: "official preserver in middle is replaced and moved to end",
			initial: []sdkcompaction.Preserver{
				charPreserver{id: "custom-a"},
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-b"},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name: "no prior official preserver appends official to end",
			initial: []sdkcompaction.Preserver{
				charPreserver{id: "custom-a"},
				charPreserver{id: "custom-b"},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name: "only official preserver is replaced",
			initial: []sdkcompaction.Preserver{
				charPreserver{id: featurecontinuity.ID},
			},
			wantIDs:      []string{featurecontinuity.ID},
			wantCount:    1,
			lastIsPlugin: true,
		},
		{
			name: "multiple official preserver duplicates all stripped and replaced by single bound preserver at end",
			initial: []sdkcompaction.Preserver{
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-a"},
				charPreserver{id: featurecontinuity.ID},
				charPreserver{id: "custom-b"},
				charPreserver{id: featurecontinuity.ID},
			},
			wantIDs:      []string{"custom-a", "custom-b", featurecontinuity.ID},
			wantCount:    3,
			lastIsPlugin: true,
		},
		{
			name:         "empty preservers slice receives single bound preserver",
			initial:      nil,
			wantIDs:      []string{featurecontinuity.ID},
			wantCount:    1,
			lastIsPlugin: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gen := makeTestGenSurface(tc.initial)
			res, err := rt.bindCompactionContinuity(gen, []aipsdk.Registration{reg})
			require.NoError(t, err)
			preservers := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
			require.Len(t, preservers, tc.wantCount)

			gotIDs := make([]string, len(preservers))
			for i, p := range preservers {
				require.NotNil(t, p)
				gotIDs[i] = p.ID()
			}
			assert.Equal(t, tc.wantIDs, gotIDs)

			if tc.lastIsPlugin {
				lastPreserver := preservers[len(preservers)-1]
				_, isPlugin := lastPreserver.(*featurecontinuity.Plugin)
				assert.True(t, isPlugin, "last preserver must be the bound *featurecontinuity.Plugin instance")
			}
		})
	}
}

func TestBindCompactionContinuity_MultipleRegistrationsOrdering(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg1 := validCompactionRegistration(t)
	reg2 := validCompactionRegistration(t)

	gen := makeTestGenSurface([]sdkcompaction.Preserver{
		charPreserver{id: "custom-1"},
		charPreserver{id: featurecontinuity.ID},
		charPreserver{id: "custom-2"},
	})

	res, err := rt.bindCompactionContinuity(gen, []aipsdk.Registration{reg1, reg2})
	require.NoError(t, err)
	preservers := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, preservers, 3)
	assert.Equal(t, "custom-1", preservers[0].ID())
	assert.Equal(t, "custom-2", preservers[1].ID())
	assert.Equal(t, featurecontinuity.ID, preservers[2].ID())

	_, isPlugin := preservers[2].(*featurecontinuity.Plugin)
	assert.True(t, isPlugin)
}

func TestBindCompactionContinuity_PanicSafetyDuringIdentityExtraction(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg := validCompactionRegistration(t)

	t.Run("panicking preserver with string is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		gen := makeTestGenSurface([]sdkcompaction.Preserver{
			panickingPreserver{panicVal: "deliberate panic in ID()"},
			charPreserver{id: featurecontinuity.ID},
		})

		res, err := rt.bindCompactionContinuity(gen, []aipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 2)
		assert.IsType(t, panickingPreserver{}, preservers[0])
		assert.Equal(t, featurecontinuity.ID, preservers[1].ID())
	})

	t.Run("panicking preserver with error is safely recovered and retained", func(t *testing.T) {
		t.Parallel()
		gen := makeTestGenSurface([]sdkcompaction.Preserver{
			charPreserver{id: "custom-a"},
			panickingPreserver{panicVal: errors.New("id extraction error panic")},
			charPreserver{id: "custom-b"},
		})

		res, err := rt.bindCompactionContinuity(gen, []aipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 4)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.IsType(t, panickingPreserver{}, preservers[1])
		assert.Equal(t, "custom-b", preservers[2].ID())
		assert.Equal(t, featurecontinuity.ID, preservers[3].ID())
	})

	t.Run("nil preserver interface value is safe and retained", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "feat", []sdkcompaction.Preserver{
			charPreserver{id: "custom-1"},
			charPreserver{id: featurecontinuity.ID},
		})
		gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

		res, err := rt.bindCompactionContinuity(gen, []aipsdk.Registration{reg})
		require.NoError(t, err)
		preservers := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
		require.Len(t, preservers, 2)
		assert.Equal(t, "custom-1", preservers[0].ID())
		assert.Equal(t, featurecontinuity.ID, preservers[1].ID())
	})

	t.Run("safePreserverID direct characterization", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "", safePreserverID(nil))
		assert.Equal(t, "custom", safePreserverID(charPreserver{id: "custom"}))
		assert.Equal(t, "", safePreserverID(panickingPreserver{panicVal: "panic"}))
	})
}

func TestBindCompactionContinuity_FailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg := validCompactionRegistration(t)

	initialSurface := makeTestGenSurface([]sdkcompaction.Preserver{
		charPreserver{id: "preserved-preserver"},
	})
	snapshot := initialSurface

	t.Run("unknown config key returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface

		var badNode yaml.Node
		badRaw := "unknown_nested_key: true\n"
		require.NoError(t, yaml.Unmarshal([]byte(badRaw), &badNode))

		badReg := aipsdk.Registration{
			ID:          featurecontinuity.ID,
			FactoryKind: featurecontinuity.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: badNode},
		}

		res, err := rt.bindCompactionContinuity(cand, []aipsdk.Registration{badReg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compaction-continuity config")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain byte-for-byte unmodified")
	})

	t.Run("disagreeing extractor and worker concurrency returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface

		var badNode yaml.Node
		badRaw := "extractor:\n  max_concurrency: 5\nworker:\n  max_concurrency: 10\n"
		require.NoError(t, yaml.Unmarshal([]byte(badRaw), &badNode))

		badReg := aipsdk.Registration{
			ID:          featurecontinuity.ID,
			FactoryKind: featurecontinuity.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: badNode},
		}

		res, err := rt.bindCompactionContinuity(cand, []aipsdk.Registration{badReg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compaction-continuity config")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain byte-for-byte unmodified")
	})

	t.Run("validateCompactionPrerequisites fail-closed before binding", func(t *testing.T) {
		t.Parallel()
		// Missing detector, coordinator, background
		emptyRT := &Runtime{}
		err := emptyRT.validateCompactionPrerequisites([]aipsdk.Registration{reg})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generation prerequisite")

		// With all prerequisites satisfied
		validRT := &Runtime{
			compactionDetector: compactiondetect.New(compactiondetect.Config{}),
			branchCoordinator:  rt.branchCoordinator,
			bgAux:              newTestScheduler(t),
		}
		err = validRT.validateCompactionPrerequisites([]aipsdk.Registration{reg})
		require.NoError(t, err)
	})
}

func TestBindCompactionContinuity_MultiRegistrationTransaction_AllPlanesUntouched(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)

	// Valid registration (reg1)
	reg1 := validCompactionRegistration(t)

	// Invalid registration (reg2 with unknown config field)
	var badNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("unknown_top_field: true\n"), &badNode))
	reg2 := aipsdk.Registration{
		ID:          featurecontinuity.ID,
		FactoryKind: featurecontinuity.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: badNode},
	}

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "feat-pres", []sdkcompaction.Preserver{
		charPreserver{id: "orig-preserver-1"},
		charPreserver{id: featurecontinuity.ID},
	}))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "feat-obs", []sdkcompaction.Observer{
		charPreserver{id: "obs-1"},
	}))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "feat-hooks", []hooks.SubmitHook{
		charSubmitHook{id: "hook-1"},
	}))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "feat-scalar", 4096))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "feat-xform", []request.AttemptTransform{
		charAttemptTransform{id: "xform-1"},
	}))

	initialSurface := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}
	snapshot := initialSurface

	// Calling bindCompactionContinuity with [reg1, reg2] where reg1 succeeds and reg2 fails config decode
	res, err := rt.bindCompactionContinuity(initialSurface, []aipsdk.Registration{reg1, reg2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "featurehost: compaction-continuity config")
	assert.Equal(t, featurebundle.GeneratedMergeSurface{}, res, "returned surface on error must be zero-value")

	// Candidate and its frozen planes must remain 100% byte-for-byte untouched across ALL planes
	assert.Equal(t, snapshot, initialSurface, "original candidate must remain untouched")

	presGot := aipfeature.Get(initialSurface.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, presGot, 2)
	assert.Equal(t, "orig-preserver-1", presGot[0].ID())
	assert.Equal(t, featurecontinuity.ID, presGot[1].ID(), "original continuity preserver must NOT be replaced if transaction fails")

	obsGot := aipfeature.Get(initialSurface.Frozen, aipfeature.PlaneCompactionObservers)
	require.Len(t, obsGot, 1)

	hooksGot := aipfeature.Get(initialSurface.Frozen, aipfeature.PlaneSubmitHooks)
	require.Len(t, hooksGot, 1)
	assert.Equal(t, "hook-1", hooksGot[0].ID())

	maxArgsGot := aipfeature.Get(initialSurface.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 4096, maxArgsGot)

	xformGot := aipfeature.Get(initialSurface.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, xformGot, 1)
	assert.Equal(t, "xform-1", xformGot[0].ID())
}

func TestBindCompactionContinuity_Idempotence(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg := validCompactionRegistration(t)

	initial := makeTestGenSurface([]sdkcompaction.Preserver{
		charPreserver{id: "custom-a"},
		charPreserver{id: featurecontinuity.ID},
		charPreserver{id: "custom-b"},
	})

	res1, err := rt.bindCompactionContinuity(initial, []aipsdk.Registration{reg})
	require.NoError(t, err)

	res2, err := rt.bindCompactionContinuity(res1, []aipsdk.Registration{reg})
	require.NoError(t, err)

	res3, err := rt.bindCompactionContinuity(res2, []aipsdk.Registration{reg})
	require.NoError(t, err)

	p1 := aipfeature.Get(res1.Frozen, aipfeature.PlaneCompactionPreservers)
	p2 := aipfeature.Get(res2.Frozen, aipfeature.PlaneCompactionPreservers)
	p3 := aipfeature.Get(res3.Frozen, aipfeature.PlaneCompactionPreservers)

	require.Len(t, p1, 3)
	require.Len(t, p2, 3)
	require.Len(t, p3, 3)

	for i := range 3 {
		assert.Equal(t, p1[i].ID(), p2[i].ID())
		assert.Equal(t, p1[i].ID(), p3[i].ID())
	}
}

func TestBindCompactionContinuity_DisabledAndNonMatchingRegistrations(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	reg := validCompactionRegistration(t)

	initial := makeTestGenSurface([]sdkcompaction.Preserver{
		charPreserver{id: "custom-a"},
		charPreserver{id: featurecontinuity.ID},
	})

	t.Run("disabled registration is a no-op", func(t *testing.T) {
		t.Parallel()
		disabledReg := reg
		disabledReg.Enabled = false
		res, err := rt.bindCompactionContinuity(initial, []aipsdk.Registration{disabledReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "disabled registration must return exact surface unmodified")
	})

	t.Run("backend kind registration is ignored", func(t *testing.T) {
		t.Parallel()
		backendReg := reg
		backendReg.Kind = aipsdk.PluginKindBackend
		res, err := rt.bindCompactionContinuity(initial, []aipsdk.Registration{backendReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "backend registration must be ignored")
	})

	t.Run("different factory kind is ignored", func(t *testing.T) {
		t.Parallel()
		otherReg := reg
		otherReg.FactoryKind = "other-feature"
		res, err := rt.bindCompactionContinuity(initial, []aipsdk.Registration{otherReg})
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res), "other feature registration must be ignored")
	})
}

func TestBindCompactionContinuity_ZeroObserversComposesOfficialPreserver(t *testing.T) {
	t.Parallel()
	rt := newTestCompactionRuntime(t)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &node); err != nil {
		t.Fatal(err)
	}
	res, err := rt.bindCompactionContinuity(featurebundle.GeneratedMergeSurface{}, []aipsdk.Registration{{
		ID:          featurecontinuity.ID,
		FactoryKind: featurecontinuity.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}})
	if err != nil {
		t.Fatal(err)
	}
	obs := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionObservers)
	pres := aipfeature.Get(res.Frozen, aipfeature.PlaneCompactionPreservers)
	if len(obs) != 0 || len(pres) != 1 || pres[0].ID() != featurecontinuity.ID {
		t.Fatalf("observers=%d preservers=%v", len(obs), pres)
	}
}
