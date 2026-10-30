package feature_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Minimal test stubs for candidate freeze tests

type freezeTestSubmitHook struct {
	id  string
	ord int
}

func (h freezeTestSubmitHook) ID() string                     { return h.id }
func (h freezeTestSubmitHook) Order() int                     { return h.ord }
func (h freezeTestSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h freezeTestSubmitHook) Handle(context.Context, *aipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type freezeTestGate struct {
	id  string
	ord int
}

func (g freezeTestGate) ID() string                     { return g.id }
func (g freezeTestGate) Order() int                     { return g.ord }
func (g freezeTestGate) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (g freezeTestGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type freezeTestTerminalProvider struct {
	id string
}

func (p freezeTestTerminalProvider) ID() string { return p.id }
func (p freezeTestTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

type freezeTestLocalTurnHandler struct {
	id  string
	ord int
}

func (h freezeTestLocalTurnHandler) ID() string                     { return h.id }
func (h freezeTestLocalTurnHandler) Order() int                     { return h.ord }
func (h freezeTestLocalTurnHandler) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h freezeTestLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h freezeTestLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type freezeTestRequestTransform struct {
	id  string
	ord int
}

func (t freezeTestRequestTransform) ID() string                     { return t.id }
func (t freezeTestRequestTransform) Order() int                     { return t.ord }
func (t freezeTestRequestTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t freezeTestRequestTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type freezeTestAttemptTransform struct {
	id  string
	ord int
}

func (t freezeTestAttemptTransform) ID() string                     { return t.id }
func (t freezeTestAttemptTransform) Order() int                     { return t.ord }
func (t freezeTestAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t freezeTestAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type freezeTestSessionOpener struct {
	id string
}

func (o freezeTestSessionOpener) ID() string { return o.id }
func (freezeTestSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type freezeTestWorkspaceResolver struct {
	id string
}

func (r freezeTestWorkspaceResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{ID: r.id, ProjectRoot: "/tmp/" + r.id}, nil
}

type freezeTestFinalizer struct {
	id string
}

func (f freezeTestFinalizer) ID() string { return f.id }
func (freezeTestFinalizer) Order() int   { return 0 }
func (freezeTestFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type freezeTestCompactionObserver struct {
	tag string
}

func (o freezeTestCompactionObserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type freezeTestCompactionPreserver struct {
	id string
}

func (p freezeTestCompactionPreserver) ID() string { return p.id }
func (freezeTestCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (freezeTestCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (freezeTestCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type freezeTestPanicPreserver struct {
	panicVal any
}

func (p freezeTestPanicPreserver) ID() string {
	if p.panicVal != nil {
		panic(p.panicVal)
	}
	panic("panicking preserver")
}

func (freezeTestPanicPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (freezeTestPanicPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (freezeTestPanicPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

// TestContribute_FailBeforeMutate_TableDriven proves that any failed contribution
// (validation, conflict, combiner, nil reject, unhandled source, empty plugin ID)
// leaves the ContributionSet state byte-for-byte identical before and after.
func TestContribute_FailBeforeMutate_TableDriven(t *testing.T) {
	t.Parallel()

	testMutPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.fail_before_mutate_table",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			if len(inc) > 0 && inc[0] == "trigger_error" {
				if len(cur) > 0 {
					cur[0] = "TAMPERED_IN_COMBINER"
				}
				return nil, errors.New("combiner intentional error")
			}
			return append(cur, inc...), nil
		},
	})

	testHostOnlyPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.host_only_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Host: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	tests := []struct {
		name          string
		setup         func(s *feature.ContributionSet)
		contributeFn  func(s *feature.ContributionSet) error
		assertState   func(t *testing.T, f feature.FrozenPlaneSet)
		wantErrTarget error
		wantErrSubstr string
	}{
		{
			name: "validation failure on PlaneToolCallFinalizationMaxArgsBytes leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 1024))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				// Negative bytes fails Validate
				return feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", -50)
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				assert.Equal(t, 1024, feature.Get(f, feature.PlaneToolCallFinalizationMaxArgsBytes))
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "must be >= 0",
		},
		{
			name: "exclusive conflict on PlaneTerminalDecisionProvider leaves initial provider unchanged",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-alpha", terminaldecision.Provider(freezeTestTerminalProvider{id: "provider-alpha"})))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-beta", terminaldecision.Provider(freezeTestTerminalProvider{id: "provider-beta"}))
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				assert.Equal(t, "provider-alpha", feature.Get(f, feature.PlaneTerminalDecisionProvider).ID())
				id, ok := feature.FrozenIdentity(f, feature.PlaneTerminalDecisionProvider)
				assert.True(t, ok)
				assert.Equal(t, "provider-alpha", id)
			},
			wantErrTarget: feature.ErrExclusiveConflict,
			wantErrSubstr: `"provider-alpha" and "provider-beta"`,
		},
		{
			name: "NilReject on PlaneLocalTurnHandlers with nil entry leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestLocalTurnHandler{id: "turn-h1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", []localturn.Handler{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-2", []localturn.Handler{nil})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				handlers := feature.Get(f, feature.PlaneLocalTurnHandlers)
				require.Len(t, handlers, 1)
				assert.Equal(t, "turn-h1", handlers[0].ID())
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "must not be nil",
		},
		{
			name: "empty plugin ID leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestSubmitHook{id: "hook-1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				h2 := freezeTestSubmitHook{id: "hook-2", ord: 2}
				return feature.Contribute(s, feature.PlaneSubmitHooks, "", []hooks.SubmitHook{h2})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				hooks := feature.Get(f, feature.PlaneSubmitHooks)
				require.Len(t, hooks, 1)
				assert.Equal(t, "hook-1", hooks[0].ID())
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "plugin ID must not be empty",
		},
		{
			name: "mutating combiner failure leaves accumulated slice untampered",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, testMutPlane, "plugin-1", []string{"good-1", "good-2"}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, testMutPlane, "plugin-2", []string{"trigger_error"})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				got := feature.Get(f, testMutPlane)
				assert.Equal(t, []string{"good-1", "good-2"}, got)
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "combiner intentional error",
		},
		{
			name: "unsupported source leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestSubmitHook{id: "hook-1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, testHostOnlyPlane, "plugin-2", []string{"bad_source"})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				hooks := feature.Get(f, feature.PlaneSubmitHooks)
				require.Len(t, hooks, 1)
				assert.Equal(t, "hook-1", hooks[0].ID())
			},
			wantErrTarget: feature.ErrUnsupportedSource,
			wantErrSubstr: "source feature is not supported on plane",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := feature.NewContributionSet()
			tc.setup(s)

			frozenBefore := s.Freeze()
			tc.assertState(t, frozenBefore)

			err := tc.contributeFn(s)
			require.Error(t, err)
			if tc.wantErrTarget != nil {
				require.ErrorIs(t, err, tc.wantErrTarget)
			}
			if tc.wantErrSubstr != "" {
				assert.Contains(t, err.Error(), tc.wantErrSubstr)
			}

			frozenAfter := s.Freeze()
			tc.assertState(t, frozenAfter)
			assertFrozenPlaneSetsEqual(t, frozenBefore, frozenAfter)
		})
	}
}

func assertFrozenPlaneSetsEqual(t *testing.T, before, after feature.FrozenPlaneSet) {
	t.Helper()
	assert.Equal(t, before, after, "FrozenPlaneSet before and after failed mutation must be deeply equal")

	assert.Equal(t, feature.Get(before, feature.PlaneSubmitHooks), feature.Get(after, feature.PlaneSubmitHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneRequestPartHooks), feature.Get(after, feature.PlaneRequestPartHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneResponsePartHooks), feature.Get(after, feature.PlaneResponsePartHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneToolReactors), feature.Get(after, feature.PlaneToolReactors))
	assert.Equal(t, feature.Get(before, feature.PlaneSessionOpeners), feature.Get(after, feature.PlaneSessionOpeners))
	assert.Equal(t, feature.Get(before, feature.PlaneWorkspaceResolvers), feature.Get(after, feature.PlaneWorkspaceResolvers))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCatalogFilters), feature.Get(after, feature.PlaneToolCatalogFilters))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallPolicies), feature.Get(after, feature.PlaneToolCallPolicies))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallFinalizers), feature.Get(after, feature.PlaneToolCallFinalizers))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallFinalizationMaxArgsBytes), feature.Get(after, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, feature.Get(before, feature.PlaneRequestTransforms), feature.Get(after, feature.PlaneRequestTransforms))
	assert.Equal(t, feature.Get(before, feature.PlanePreRequestHandlers), feature.Get(after, feature.PlanePreRequestHandlers))
	assert.Equal(t, feature.Get(before, feature.PlaneRouteHintProviders), feature.Get(after, feature.PlaneRouteHintProviders))
	assert.Equal(t, feature.Get(before, feature.PlaneCompletionGates), feature.Get(after, feature.PlaneCompletionGates))
	assert.Equal(t, feature.Get(before, feature.PlaneAttemptTransforms), feature.Get(after, feature.PlaneAttemptTransforms))
	assert.Equal(t, feature.Get(before, feature.PlaneStreamObserverFactories), feature.Get(after, feature.PlaneStreamObserverFactories))
	assert.Equal(t, feature.Get(before, feature.PlaneTrafficObservers), feature.Get(after, feature.PlaneTrafficObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneUsageObservers), feature.Get(after, feature.PlaneUsageObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneRawCaptureSinks), feature.Get(after, feature.PlaneRawCaptureSinks))
	assert.Equal(t, feature.Get(before, feature.PlaneTrafficRedactors), feature.Get(after, feature.PlaneTrafficRedactors))
	assert.Equal(t, feature.Get(before, feature.PlaneCompactionObservers), feature.Get(after, feature.PlaneCompactionObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneCompactionPreservers), feature.Get(after, feature.PlaneCompactionPreservers))
	assert.Equal(t, feature.Get(before, feature.PlaneSecretGuards), feature.Get(after, feature.PlaneSecretGuards))
	assert.Equal(t, feature.Get(before, feature.PlaneLocalTurnHandlers), feature.Get(after, feature.PlaneLocalTurnHandlers))
	assert.Equal(t, feature.Get(before, feature.PlaneTerminalDecisionProvider), feature.Get(after, feature.PlaneTerminalDecisionProvider))

	idBefore, okBefore := feature.FrozenIdentity(before, feature.PlaneTerminalDecisionProvider)
	idAfter, okAfter := feature.FrozenIdentity(after, feature.PlaneTerminalDecisionProvider)
	assert.Equal(t, okBefore, okAfter)
	assert.Equal(t, idBefore, idAfter)
}

// TestContribute_NonNilEmptySemantics_TableDriven proves that contributing an explicit
// empty slice ([]T{}) normalizes to a non-nil empty slice in FrozenPlaneSet, whereas
// uncontributed planes return nil.
func TestContribute_NonNilEmptySemantics_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("PlaneCompletionGates non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		// Uncontributed: Get returns nil
		frozenUnset := s.Freeze()
		gotUnset := feature.Get(frozenUnset, feature.PlaneCompletionGates)
		assert.Nil(t, gotUnset, "uncontributed plane must return nil")

		// Contributed with explicit empty slice []completion.Gate{}
		err := feature.Contribute(s, feature.PlaneCompletionGates, "plugin-1", []completion.Gate{})
		require.NoError(t, err)

		frozenEmpty := s.Freeze()
		gotEmpty := feature.Get(frozenEmpty, feature.PlaneCompletionGates)
		assert.NotNil(t, gotEmpty, "contributed empty slice must return non-nil empty slice")
		assert.Empty(t, gotEmpty)
	})

	t.Run("PlaneSubmitHooks non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSubmitHooks)
		assert.NotNil(t, got, "contributed empty slice must return non-nil empty slice")
		assert.Empty(t, got)
	})

	t.Run("PlaneSessionOpeners non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSessionOpeners, "plugin-1", []session.Opener{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSessionOpeners)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneWorkspaceResolvers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneWorkspaceResolvers, "plugin-1", []workspace.Resolver{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneWorkspaceResolvers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCatalogFilters non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCatalogFilters, "plugin-1", []toolcatalog.Filter{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCatalogFilters)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCallPolicies non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCallPolicies, "plugin-1", []toolpolicy.Policy{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCallPolicies)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCallFinalizers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCallFinalizers, "plugin-1", []toolcall.Finalizer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCallFinalizers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRequestTransforms non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRequestTransforms, "plugin-1", []request.Transform{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRequestTransforms)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlanePreRequestHandlers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlanePreRequestHandlers, "plugin-1", []prerequest.Handler{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlanePreRequestHandlers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRouteHintProviders non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRouteHintProviders, "plugin-1", []routehint.Provider{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRouteHintProviders)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneAttemptTransforms non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneAttemptTransforms)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneStreamObserverFactories non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneStreamObserverFactories, "plugin-1", []response.StreamObserverFactory{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneStreamObserverFactories)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneTrafficObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneTrafficObservers, "plugin-1", []traffic.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneTrafficObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneUsageObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneUsageObservers, "plugin-1", []usage.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneUsageObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRawCaptureSinks non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRawCaptureSinks, "plugin-1", []traffic.RawCaptureSink{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRawCaptureSinks)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneTrafficRedactors non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneTrafficRedactors, "plugin-1", []traffic.Redactor{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneTrafficRedactors)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneCompactionObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionObservers, "plugin-1", []compaction.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneCompactionObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneCompactionPreservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "plugin-1", []compaction.Preserver{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneCompactionPreservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneSecretGuards non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSecretGuards, "plugin-1", []secretguard.Guard{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSecretGuards)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneLocalTurnHandlers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", []localturn.Handler{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})
}

// TestBackingArrayIsolation_SourceAndGet_TableDriven proves that:
// 1. Mutating a source slice after Contribute does NOT affect the stored candidate or frozen snapshot.
// 2. Mutating a slice returned by Get does NOT corrupt the FrozenPlaneSet backing store.
func TestBackingArrayIsolation_SourceAndGet_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("PlaneCompletionGates backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []completion.Gate{
			freezeTestGate{id: "gate-1", ord: 1},
			freezeTestGate{id: "gate-2", ord: 2},
		}

		err := feature.Contribute(s, feature.PlaneCompletionGates, "plugin-1", source)
		require.NoError(t, err)

		// Mutate source slice after Contribute
		source[0] = freezeTestGate{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneCompletionGates)
		require.Len(t, got1, 2)
		assert.Equal(t, "gate-1", got1[0].ID(), "source mutation after Contribute must not affect frozen store")

		// Mutate slice returned by Get
		got1[0] = freezeTestGate{id: "MUTATED_GET", ord: 999}

		// Subsequent Get must return uncorrupted data
		got2 := feature.Get(frozen, feature.PlaneCompletionGates)
		require.Len(t, got2, 2)
		assert.Equal(t, "gate-1", got2[0].ID(), "mutating slice from Get must not corrupt frozen store")
	})

	t.Run("PlaneSubmitHooks backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []hooks.SubmitHook{
			freezeTestSubmitHook{id: "hook-1", ord: 1},
			freezeTestSubmitHook{id: "hook-2", ord: 2},
		}

		err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", source)
		require.NoError(t, err)

		// Mutate source
		source[0] = freezeTestSubmitHook{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneSubmitHooks)
		require.Len(t, got1, 2)
		assert.Equal(t, "hook-1", got1[0].ID())

		// Mutate Get result
		got1[0] = freezeTestSubmitHook{id: "MUTATED_GET", ord: 999}

		got2 := feature.Get(frozen, feature.PlaneSubmitHooks)
		require.Len(t, got2, 2)
		assert.Equal(t, "hook-1", got2[0].ID())
	})

	t.Run("PlaneLocalTurnHandlers backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []localturn.Handler{
			freezeTestLocalTurnHandler{id: "turn-1", ord: 1},
		}

		err := feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", source)
		require.NoError(t, err)

		source[0] = freezeTestLocalTurnHandler{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		require.Len(t, got1, 1)
		assert.Equal(t, "turn-1", got1[0].ID())

		got1[0] = freezeTestLocalTurnHandler{id: "MUTATED_GET", ord: 999}

		got2 := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		require.Len(t, got2, 1)
		assert.Equal(t, "turn-1", got2[0].ID())
	})
}

// TestFrozenPlaneSet_Immutability_TableDriven proves that calling Freeze multiple times
// or mutating the ContributionSet after Freeze produces independent, immutable snapshots.
func TestFrozenPlaneSet_Immutability_TableDriven(t *testing.T) {
	t.Parallel()

	s := feature.NewContributionSet()

	// Initial contributions across slice, scalar, and exclusive planes
	err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		freezeTestSubmitHook{id: "h1", ord: 1},
	})
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096)
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-term", terminaldecision.Provider(freezeTestTerminalProvider{id: "term-1"}))
	require.NoError(t, err)

	// Snapshot 1
	snap1 := s.Freeze()

	// Verify Snapshot 1
	require.Len(t, feature.Get(snap1, feature.PlaneSubmitHooks), 1)
	assert.Equal(t, "h1", feature.Get(snap1, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, 4096, feature.Get(snap1, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap1, feature.PlaneTerminalDecisionProvider).ID())

	// Mutate ContributionSet by adding more items
	err = feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-2", []hooks.SubmitHook{
		freezeTestSubmitHook{id: "h2", ord: 2},
	})
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", 2048)
	require.NoError(t, err)

	// Snapshot 2
	snap2 := s.Freeze()

	// PROVE Snapshot 1 is immutable and uncorrupted
	require.Len(t, feature.Get(snap1, feature.PlaneSubmitHooks), 1)
	assert.Equal(t, "h1", feature.Get(snap1, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, 4096, feature.Get(snap1, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap1, feature.PlaneTerminalDecisionProvider).ID())

	// Snapshot 2 reflects the new accumulated state
	require.Len(t, feature.Get(snap2, feature.PlaneSubmitHooks), 2)
	assert.Equal(t, "h1", feature.Get(snap2, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, "h2", feature.Get(snap2, feature.PlaneSubmitHooks)[1].ID())
	assert.Equal(t, 2048, feature.Get(snap2, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap2, feature.PlaneTerminalDecisionProvider).ID())

	// Calling Freeze() again without mutating s produces identical results
	snap3 := s.Freeze()
	assert.Equal(t, feature.Get(snap2, feature.PlaneSubmitHooks), feature.Get(snap3, feature.PlaneSubmitHooks))
	assert.Equal(t, feature.Get(snap2, feature.PlaneToolCallFinalizationMaxArgsBytes), feature.Get(snap3, feature.PlaneToolCallFinalizationMaxArgsBytes))
}

// TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn proves that contributing
// to an interface-valued plane where the incoming value is a slice but the combiner returns
// a non-slice, non-nil-capable concrete type does not panic on reflect IsNil checks.
func TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn(t *testing.T) {
	t.Parallel()

	type dummyNonSlice struct {
		name string
	}

	testPlane := feature.BindGeneratedTestPlane(feature.Plane[any]{
		ID:           "test.interface_plane_non_slice_return",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc any) (any, error) {
			return dummyNonSlice{name: "combined_result"}, nil
		},
	})

	s := feature.NewContributionSet()
	// Contribute a slice (Kind == Slice, not nil) into interface plane Plane[any]
	err := feature.Contribute(s, testPlane, "plugin-1", any([]string{"item-1", "item-2"}))
	require.NoError(t, err)

	frozen := s.Freeze()
	got := feature.Get(frozen, testPlane)
	require.Equal(t, dummyNonSlice{name: "combined_result"}, got)
}

func TestContributeCandidateTo_ExplicitEmptySlice_GeneratedAndMapParity(t *testing.T) {
	t.Parallel()

	t.Run("generated storage preserves explicit empty candidate slices", func(t *testing.T) {
		t.Parallel()

		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneSessionOpeners, "cand", []session.Opener{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneWorkspaceResolvers, "cand", []workspace.Resolver{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneToolCatalogFilters, "cand", []toolcatalog.Filter{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneToolCallPolicies, "cand", []toolpolicy.Policy{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneToolCallFinalizers, "cand", []toolcall.Finalizer{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneRequestTransforms, "cand", []request.Transform{}))
		require.NoError(t, feature.Contribute(src, feature.PlanePreRequestHandlers, "cand", []prerequest.Handler{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneRouteHintProviders, "cand", []routehint.Provider{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneCompletionGates, "cand", []completion.Gate{}))
		require.NoError(t, feature.Contribute(src, feature.PlaneAttemptTransforms, "cand", []request.AttemptTransform{}))

		candFrozen := src.Freeze()

		dst := feature.NewContributionSet()
		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "cand"))

		dstFrozen := dst.Freeze()

		so := feature.Get(dstFrozen, feature.PlaneSessionOpeners)
		assert.NotNil(t, so)
		assert.Empty(t, so)

		wr := feature.Get(dstFrozen, feature.PlaneWorkspaceResolvers)
		assert.NotNil(t, wr)
		assert.Empty(t, wr)

		cat := feature.Get(dstFrozen, feature.PlaneToolCatalogFilters)
		assert.NotNil(t, cat)
		assert.Empty(t, cat)

		pol := feature.Get(dstFrozen, feature.PlaneToolCallPolicies)
		assert.NotNil(t, pol)
		assert.Empty(t, pol)

		fin := feature.Get(dstFrozen, feature.PlaneToolCallFinalizers)
		assert.NotNil(t, fin)
		assert.Empty(t, fin)

		reqTr := feature.Get(dstFrozen, feature.PlaneRequestTransforms)
		assert.NotNil(t, reqTr)
		assert.Empty(t, reqTr)

		preReq := feature.Get(dstFrozen, feature.PlanePreRequestHandlers)
		assert.NotNil(t, preReq)
		assert.Empty(t, preReq)

		rh := feature.Get(dstFrozen, feature.PlaneRouteHintProviders)
		assert.NotNil(t, rh)
		assert.Empty(t, rh)

		cg := feature.Get(dstFrozen, feature.PlaneCompletionGates)
		assert.NotNil(t, cg)
		assert.Empty(t, cg)

		attTr := feature.Get(dstFrozen, feature.PlaneAttemptTransforms)
		assert.NotNil(t, attTr)
		assert.Empty(t, attTr)
	})
}

// TestContributeCandidateTo_GeneratedMapDispatchStructuralOwnership proves that candidate map dispatch
// is NOT present in plane_generated.go, with zero per-plane switch logic in frozen.go.
func TestContributeCandidateTo_GeneratedMapDispatchStructuralOwnership(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)

	// 1. plane_generated.go must NOT contain the removed contributeCandidateMapTo function
	genPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go")
	genContent, err := os.ReadFile(genPath)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(genContent), "func contributeCandidateMapTo("),
		"plane_generated.go must not define contributeCandidateMapTo")

	// 2. frozen.go must not contain hand-written per-plane switch branches
	frozenPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "frozen.go")
	frozenContent, err := os.ReadFile(frozenPath)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(frozenContent), "switch k"),
		"frozen.go must not contain hand-written per-plane switch branches for candidate dispatch")
}

// TestContributeCandidateTo_GeneratedStorage_AtomicTransaction verifies that if candidate projection
// fails on a later candidate plane in generated storage (e.g. invalid attempt transforms),
// earlier valid candidate contributions are not applied and the destination is unchanged.
func TestContributeCandidateTo_GeneratedStorage_AtomicTransaction(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneSubmitHooks, "base-plugin", []hooks.SubmitHook{
		freezeTestSubmitHook{id: "base-hook", ord: 1},
	}))
	require.NoError(t, feature.Contribute(dst, feature.PlaneRequestTransforms, "base-plugin", []request.Transform{
		freezeTestRequestTransform{id: "base-req-tr", ord: 1},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with earlier valid RequestTransforms and later invalid AttemptTransforms (literal nil element)
	malformedCand := feature.NewMalformedGeneratedFrozenCandidateForTest(
		[]request.Transform{
			freezeTestRequestTransform{id: "cand-req-tr", ord: 10},
		},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	// Assert error attribution
	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

// TestContributeCandidateTo_AtomicTransaction_SuccessPreservesDestinationIdentity verifies that
// successful candidate projection commits atomically, preserving destination pointer identity
// and appending contributions in expected source order.
func TestContributeCandidateTo_AtomicTransaction_SuccessPreservesDestinationIdentity(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneRequestTransforms, "base-plugin", []request.Transform{
		freezeTestRequestTransform{id: "base-req-tr", ord: 1},
	}))
	require.NoError(t, feature.Contribute(dst, feature.PlaneAttemptTransforms, "base-plugin", []request.AttemptTransform{
		freezeTestAttemptTransform{id: "base-att-tr", ord: 1},
	}))

	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneRequestTransforms, "cand-plugin", []request.Transform{
		freezeTestRequestTransform{id: "cand-req-tr", ord: 2},
	}))
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneAttemptTransforms, "cand-plugin", []request.AttemptTransform{
		freezeTestAttemptTransform{id: "cand-att-tr", ord: 2},
	}))
	candFrozen := candSrc.Freeze()

	dstPtrBefore := dst
	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.NoError(t, err)
	assert.Same(t, dstPtrBefore, dst, "transaction commit must preserve destination pointer identity")

	frozen := dst.Freeze()
	reqTr := feature.Get(frozen, feature.PlaneRequestTransforms)
	require.Len(t, reqTr, 2)
	assert.Equal(t, "base-req-tr", reqTr[0].ID(), "base contribution must come first")
	assert.Equal(t, "cand-req-tr", reqTr[1].ID(), "candidate contribution must be appended in expected source order")

	attTr := feature.Get(frozen, feature.PlaneAttemptTransforms)
	require.Len(t, attTr, 2)
	assert.Equal(t, "base-att-tr", attTr[0].ID(), "base contribution must come first")
	assert.Equal(t, "cand-att-tr", attTr[1].ID(), "candidate contribution must be appended in expected source order")
}

// TestContributeCandidateTo_GeneratedStorage_SessionWorkspace_AtomicRollback verifies that if candidate projection
// fails on a later candidate plane in generated storage (e.g. invalid attempt transforms),
// earlier valid candidate contributions for session openers and workspace resolvers are rolled back.
func TestContributeCandidateTo_GeneratedStorage_SessionWorkspace_AtomicRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneSessionOpeners, "base-plugin", []session.Opener{
		freezeTestSessionOpener{id: "base-so"},
	}))
	require.NoError(t, feature.Contribute(dst, feature.PlaneWorkspaceResolvers, "base-plugin", []workspace.Resolver{
		freezeTestWorkspaceResolver{id: "base-wr"},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with earlier valid SessionOpeners and WorkspaceResolvers, but later invalid AttemptTransforms (literal nil)
	malformedCand := feature.NewMalformedGeneratedFrozenSessionWorkspaceCandidateForTest(
		[]session.Opener{
			freezeTestSessionOpener{id: "cand-so"},
		},
		[]workspace.Resolver{
			freezeTestWorkspaceResolver{id: "cand-wr"},
		},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	// Assert error attribution to the late invalid plane
	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities (rollback)
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

// TestContributeCandidateTo_SessionAndWorkspace_SuccessOrderAndIsolation verifies that
// candidate contributions of SessionOpeners and WorkspaceResolvers in generated and map storage
// append in source order, preserve pointer identity, and isolate backing storage.
func TestContributeCandidateTo_SessionAndWorkspace_SuccessOrderAndIsolation(t *testing.T) {
	t.Parallel()

	t.Run("generated_storage", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSessionOpeners, "base-plugin", []session.Opener{
			freezeTestSessionOpener{id: "base-so"},
		}))
		require.NoError(t, feature.Contribute(dst, feature.PlaneWorkspaceResolvers, "base-plugin", []workspace.Resolver{
			freezeTestWorkspaceResolver{id: "base-wr"},
		}))

		candSrc := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneSessionOpeners, "cand-plugin", []session.Opener{
			freezeTestSessionOpener{id: "cand-so"},
		}))
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneWorkspaceResolvers, "cand-plugin", []workspace.Resolver{
			freezeTestWorkspaceResolver{id: "cand-wr"},
		}))
		candFrozen := candSrc.Freeze()

		dstPtrBefore := dst
		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)
		assert.Same(t, dstPtrBefore, dst)

		frozen := dst.Freeze()
		so := feature.Get(frozen, feature.PlaneSessionOpeners)
		require.Len(t, so, 2)
		assert.Equal(t, "base-so", so[0].ID())
		assert.Equal(t, "cand-so", so[1].ID())

		wr := feature.Get(frozen, feature.PlaneWorkspaceResolvers)
		require.Len(t, wr, 2)
		wr0, ok0 := wr[0].(freezeTestWorkspaceResolver)
		wr1, ok1 := wr[1].(freezeTestWorkspaceResolver)
		require.True(t, ok0)
		require.True(t, ok1)
		assert.Equal(t, "base-wr", wr0.id)
		assert.Equal(t, "cand-wr", wr1.id)
	})
}

// TestContributeCandidateTo_ToolFinalizersAndBufferReduction_SuccessOrderAndMinReduction verifies that
// candidate contributions of ToolCallFinalizers and ToolCallFinalizationMaxArgsBytes in generated and map storage
// append in source order for finalizers and min-reduce positive buffer caps.
func TestContributeCandidateTo_ToolFinalizersAndBufferReduction_SuccessOrderAndMinReduction(t *testing.T) {
	t.Parallel()

	t.Run("generated_storage_min_reduction_and_ordering", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizers, "base-plugin", []toolcall.Finalizer{
			freezeTestFinalizer{id: "base-fin"},
		}))
		require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizationMaxArgsBytes, "base-plugin", 4096))

		candSrc := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneToolCallFinalizers, "cand-plugin", []toolcall.Finalizer{
			freezeTestFinalizer{id: "cand-fin"},
		}))
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneToolCallFinalizationMaxArgsBytes, "cand-plugin", 1024))
		candFrozen := candSrc.Freeze()

		dstPtrBefore := dst
		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)
		assert.Same(t, dstPtrBefore, dst)

		frozen := dst.Freeze()
		fins := feature.Get(frozen, feature.PlaneToolCallFinalizers)
		require.Len(t, fins, 2)
		assert.Equal(t, "base-fin", fins[0].ID())
		assert.Equal(t, "cand-fin", fins[1].ID())

		maxArgs := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
		assert.Equal(t, 1024, maxArgs, "candidate overlay must min-reduce buffer cap (1024 vs 4096)")
	})

	t.Run("generated_storage_candidate_larger_does_not_overwrite", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizationMaxArgsBytes, "base-plugin", 1024))

		candSrc := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneToolCallFinalizationMaxArgsBytes, "cand-plugin", 8192))
		candFrozen := candSrc.Freeze()

		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)

		frozen := dst.Freeze()
		maxArgs := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
		assert.Equal(t, 1024, maxArgs, "candidate larger cap must NOT overwrite smaller base cap (min-reduction)")
	})
}

// TestContributeCandidateTo_ToolCallFinalizationMaxArgsBytes_InvalidNegativeRollback verifies that
// invalid candidate buffer reduction values fail and roll back destination atomically.
func TestContributeCandidateTo_ToolCallFinalizationMaxArgsBytes_InvalidNegativeRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizers, "base-plugin", []toolcall.Finalizer{
		freezeTestFinalizer{id: "base-fin"},
	}))
	require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizationMaxArgsBytes, "base-plugin", 4096))

	malformedCand := feature.NewMalformedGeneratedFrozenToolCallFinalizationCandidateForTest(
		[]toolcall.Finalizer{
			freezeTestFinalizer{id: "cand-fin"},
		},
		-100, // Invalid negative
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	// Validate atomic rollback: dst is untouched
	frozen := dst.Freeze()
	fins := feature.Get(frozen, feature.PlaneToolCallFinalizers)
	require.Len(t, fins, 1)
	assert.Equal(t, "base-fin", fins[0].ID())

	maxArgs := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 4096, maxArgs)
}

type freezeTestSecretGuard struct {
	id  string
	ord int
}

func (g freezeTestSecretGuard) ID() string                         { return g.id }
func (g freezeTestSecretGuard) Order() int                         { return g.ord }
func (freezeTestSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (freezeTestSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

// TestContributeCandidateTo_SecretGuards_SuccessOrderAndIsolation verifies that
// candidate contributions of SecretGuards in generated and map storage append
// in source order (base then candidate) and preserve backing-array isolation.
func TestContributeCandidateTo_SecretGuards_SuccessOrderAndIsolation(t *testing.T) {
	t.Parallel()

	t.Run("generated_storage_ordering_and_isolation", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSecretGuards, "base-plugin", []secretguard.Guard{
			freezeTestSecretGuard{id: "base-sg-1", ord: 1},
			freezeTestSecretGuard{id: "base-sg-2", ord: 2},
		}))

		candSrc := feature.NewContributionSet()
		candGuards := []secretguard.Guard{
			freezeTestSecretGuard{id: "cand-sg-1", ord: 10},
		}
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneSecretGuards, "cand-plugin", candGuards))
		candFrozen := candSrc.Freeze()

		dstPtrBefore := dst
		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)
		assert.Same(t, dstPtrBefore, dst)

		frozen := dst.Freeze()
		guards := feature.Get(frozen, feature.PlaneSecretGuards)
		require.Len(t, guards, 3)
		assert.Equal(t, "base-sg-1", guards[0].ID())
		assert.Equal(t, "base-sg-2", guards[1].ID())
		assert.Equal(t, "cand-sg-1", guards[2].ID())

		// Backing array isolation
		candGuards[0] = freezeTestSecretGuard{id: "mutated", ord: 99}
		guardsAgain := feature.Get(frozen, feature.PlaneSecretGuards)
		assert.Equal(t, "cand-sg-1", guardsAgain[2].ID())
	})

	t.Run("literal_and_typed_nil_elements_preserved", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSecretGuards, "base-plugin", []secretguard.Guard{
			freezeTestSecretGuard{id: "base-sg-1", ord: 1},
		}))

		var typedNil *freezeTestSecretGuard
		candSrc := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneSecretGuards, "cand-plugin", []secretguard.Guard{
			nil,
			typedNil,
			freezeTestSecretGuard{id: "cand-sg-1", ord: 10},
		}))
		candFrozen := candSrc.Freeze()

		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)

		frozen := dst.Freeze()
		guards := feature.Get(frozen, feature.PlaneSecretGuards)
		require.Len(t, guards, 4)
		assert.Equal(t, "base-sg-1", guards[0].ID())
		assert.Nil(t, guards[1])
		assert.True(t, guards[2] != nil, "boxed typed-nil must not equal untyped nil interface")
		assert.True(t, secretguard.IsNilGuard(guards[2]))
		assert.Equal(t, "cand-sg-1", guards[3].ID())
	})
}

// TestContributeCandidateTo_GeneratedStorage_SecretGuards_AtomicRollback verifies that if candidate projection
// in generated storage encounters a validation failure on a later candidate plane (e.g. invalid attempt transforms),
// earlier valid candidate secret guards are rolled back atomically and the destination remains unchanged.
func TestContributeCandidateTo_GeneratedStorage_SecretGuards_AtomicRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneSecretGuards, "base-plugin", []secretguard.Guard{
		freezeTestSecretGuard{id: "base-sg", ord: 1},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with valid SecretGuards and invalid AttemptTransforms (nil entry fails Validate)
	malformedCand := feature.NewMalformedGeneratedFrozenSecretGuardsCandidateForTest(
		[]secretguard.Guard{
			freezeTestSecretGuard{id: "cand-sg", ord: 10},
		},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities (rollback)
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

// TestContributeCandidateTo_CompactionObservers_GeneratedAndMapParity verifies that
// candidate contributions of CompactionObservers in generated and map storage append in source order.
func TestContributeCandidateTo_CompactionObservers_GeneratedAndMapParity(t *testing.T) {
	t.Parallel()

	t.Run("generated_storage", func(t *testing.T) {
		t.Parallel()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneCompactionObservers, "base-plugin", []compaction.Observer{
			freezeTestCompactionObserver{tag: "base-obs-1"},
		}))

		candSrc := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candSrc, feature.PlaneCompactionObservers, "cand-plugin", []compaction.Observer{
			freezeTestCompactionObserver{tag: "cand-obs-1"},
		}))
		candFrozen := candSrc.Freeze()

		dstPtrBefore := dst
		err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
		require.NoError(t, err)
		assert.Same(t, dstPtrBefore, dst)

		frozen := dst.Freeze()
		obs := feature.Get(frozen, feature.PlaneCompactionObservers)
		require.Len(t, obs, 2)
		o0, ok0 := obs[0].(freezeTestCompactionObserver)
		o1, ok1 := obs[1].(freezeTestCompactionObserver)
		require.True(t, ok0)
		require.True(t, ok1)
		assert.Equal(t, "base-obs-1", o0.tag)
		assert.Equal(t, "cand-obs-1", o1.tag)
	})
}

// TestContributeCandidateTo_GeneratedStorage_CompactionObservers_AtomicRollback verifies that if candidate projection
// in generated storage encounters a validation failure on a later candidate plane, earlier valid compaction observers are rolled back.
func TestContributeCandidateTo_GeneratedStorage_CompactionObservers_AtomicRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneCompactionObservers, "base-plugin", []compaction.Observer{
		freezeTestCompactionObserver{tag: "base-obs"},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with valid CompactionObservers and invalid AttemptTransforms (nil entry fails Validate)
	malformedCand := feature.NewMalformedGeneratedFrozenCompactionCandidateForTest(
		[]compaction.Observer{
			freezeTestCompactionObserver{tag: "cand-obs"},
		},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities (rollback)
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

// TestContributionSet_BindCompactionPreservers_GenerationBinderSemantics verifies the complete
// generation-binder semantics for PlaneCompactionPreservers.
func TestContributionSet_BindCompactionPreservers_GenerationBinderSemantics(t *testing.T) {
	t.Parallel()

	t.Run("replace_by_identity_replaces_matching_official_and_moves_to_end", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		// Initial contributions from features
		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			freezeTestCompactionPreserver{id: "custom-a"},
			freezeTestCompactionPreserver{id: "official-continuity"},
			freezeTestCompactionPreserver{id: "custom-b"},
		})
		require.NoError(t, err)

		// Generation binder replaces official-continuity
		boundOfficial := freezeTestCompactionPreserver{id: "official-continuity"}
		err = s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial})
		require.NoError(t, err)

		frozen := s.Freeze()
		preservers := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, preservers, 3)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.Equal(t, "custom-b", preservers[1].ID())
		assert.Equal(t, "official-continuity", preservers[2].ID())
	})

	t.Run("no_prior_matching_appends_to_end", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			freezeTestCompactionPreserver{id: "custom-a"},
			freezeTestCompactionPreserver{id: "custom-b"},
		})
		require.NoError(t, err)

		boundOfficial := freezeTestCompactionPreserver{id: "official-continuity"}
		err = s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial})
		require.NoError(t, err)

		frozen := s.Freeze()
		preservers := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, preservers, 3)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.Equal(t, "custom-b", preservers[1].ID())
		assert.Equal(t, "official-continuity", preservers[2].ID())
	})

	t.Run("multiple_duplicates_all_stripped_and_replaced_at_end", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			freezeTestCompactionPreserver{id: "official-continuity"},
			freezeTestCompactionPreserver{id: "custom-a"},
			freezeTestCompactionPreserver{id: "official-continuity"},
			freezeTestCompactionPreserver{id: "custom-b"},
			freezeTestCompactionPreserver{id: "official-continuity"},
		})
		require.NoError(t, err)

		boundOfficial := freezeTestCompactionPreserver{id: "official-continuity"}
		err = s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial})
		require.NoError(t, err)

		frozen := s.Freeze()
		preservers := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, preservers, 3)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.Equal(t, "custom-b", preservers[1].ID())
		assert.Equal(t, "official-continuity", preservers[2].ID())
	})

	t.Run("repeated_bindings_are_idempotent", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			freezeTestCompactionPreserver{id: "custom-a"},
			freezeTestCompactionPreserver{id: "official-continuity"},
		})
		require.NoError(t, err)

		boundOfficial := freezeTestCompactionPreserver{id: "official-continuity"}
		require.NoError(t, s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial}))
		require.NoError(t, s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial}))
		require.NoError(t, s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial}))

		frozen := s.Freeze()
		preservers := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, preservers, 2)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.Equal(t, "official-continuity", preservers[1].ID())
	})

	t.Run("panic_safety_during_identity_extraction", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			freezeTestCompactionPreserver{id: "custom-a"},
			freezeTestPanicPreserver{panicVal: "deliberate panic"},
			freezeTestCompactionPreserver{id: "official-continuity"},
			freezeTestCompactionPreserver{id: "custom-b"},
		})
		require.NoError(t, err)

		boundOfficial := freezeTestCompactionPreserver{id: "official-continuity"}
		err = s.BindCompactionPreservers("official-continuity", []compaction.Preserver{boundOfficial})
		require.NoError(t, err)

		frozen := s.Freeze()
		preservers := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, preservers, 4)
		assert.Equal(t, "custom-a", preservers[0].ID())
		assert.IsType(t, freezeTestPanicPreserver{}, preservers[1])
		assert.Equal(t, "custom-b", preservers[2].ID())
		assert.Equal(t, "official-continuity", preservers[3].ID())
	})

	t.Run("compaction_observers_exact_historical_nil_semantics", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		// Nil slice is accepted
		err := feature.Contribute(s, feature.PlaneCompactionObservers, "p1", nil)
		require.NoError(t, err)

		// Slice with literal nil is accepted
		err = feature.Contribute(s, feature.PlaneCompactionObservers, "p2", []compaction.Observer{nil})
		require.NoError(t, err)

		// Slice with boxed typed nil is accepted
		var typedNil *freezeTestCompactionObserver
		err = feature.Contribute(s, feature.PlaneCompactionObservers, "p3", []compaction.Observer{typedNil})
		require.NoError(t, err)

		frozen := s.Freeze()
		obs := feature.Get(frozen, feature.PlaneCompactionObservers)
		require.Len(t, obs, 2)
	})

	t.Run("compaction_preservers_exact_historical_nil_semantics", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		// Nil slice is accepted on feature contribution
		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "p1", nil)
		require.NoError(t, err)

		// Slice with literal nil is rejected by Validate
		err = feature.Contribute(s, feature.PlaneCompactionPreservers, "p2", []compaction.Preserver{nil})
		require.Error(t, err)
		assert.ErrorIs(t, err, feature.ErrInvalidContribution)
		assert.Contains(t, err.Error(), "CompactionPreservers[0] must not be nil")

		// Slice with boxed typed nil is accepted (historical FeatureBundle.Validate checked p == nil only)
		var typedNil *freezeTestCompactionPreserver
		err = feature.Contribute(s, feature.PlaneCompactionPreservers, "p3", []compaction.Preserver{typedNil})
		require.NoError(t, err)

		frozen := s.Freeze()
		pres := feature.Get(frozen, feature.PlaneCompactionPreservers)
		require.Len(t, pres, 1)
	})
}

func TestContributeCandidateTo_GeneratedStorage_LocalTurnHandlers(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneLocalTurnHandlers, "base-plugin", []localturn.Handler{
		freezeTestLocalTurnHandler{id: "base-lt-1", ord: 10},
	}))

	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneLocalTurnHandlers, "cand-plugin", []localturn.Handler{
		freezeTestLocalTurnHandler{id: "cand-lt-1", ord: 20},
	}))
	candFrozen := candSrc.Freeze()

	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.NoError(t, err)

	frozen := dst.Freeze()
	handlers := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
	require.Len(t, handlers, 2)
	assert.Equal(t, "base-lt-1", handlers[0].ID())
	assert.Equal(t, "cand-lt-1", handlers[1].ID())
}

// TestContributeCandidateTo_GeneratedStorage_LocalTurnHandlers_AtomicRollback verifies that if candidate projection
// in generated storage encounters a validation failure on a later candidate plane, earlier valid local turn handlers are rolled back.
func TestContributeCandidateTo_GeneratedStorage_LocalTurnHandlers_AtomicRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneLocalTurnHandlers, "base-plugin", []localturn.Handler{
		freezeTestLocalTurnHandler{id: "base-lt", ord: 10},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with valid LocalTurnHandlers and invalid AttemptTransforms (nil entry fails Validate)
	malformedCand := feature.NewMalformedGeneratedFrozenLocalTurnCandidateForTest(
		[]localturn.Handler{
			freezeTestLocalTurnHandler{id: "cand-lt", ord: 20},
		},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities (rollback)
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()

	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(freezeTestTerminalProvider{id: "cand-term-1"})))
	candFrozen := candSrc.Freeze()

	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.NoError(t, err)

	frozen := dst.Freeze()
	provider := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, provider)
	assert.Equal(t, "cand-term-1", provider.ID())
	id, hasID := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "cand-term-1", id)
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_ExclusiveConflict(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(freezeTestTerminalProvider{id: "base-term"})))

	beforeFreeze := dst.Freeze()

	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(freezeTestTerminalProvider{id: "cand-term"})))
	candFrozen := candSrc.Freeze()

	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.Contains(t, err.Error(), `"base-term" and "cand-term"`)

	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
	assert.Equal(t, "base-term", feature.Get(afterFreeze, feature.PlaneTerminalDecisionProvider).ID())
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_SameIDConflict(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(freezeTestTerminalProvider{id: "same-term"})))

	beforeFreeze := dst.Freeze()

	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(freezeTestTerminalProvider{id: "same-term"})))
	candFrozen := candSrc.Freeze()

	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.Contains(t, err.Error(), `"same-term" and "same-term"`)

	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

// TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_AtomicRollback verifies that if candidate projection
// in generated storage encounters a validation failure on a later candidate plane, earlier valid terminal decision provider is rolled back.
func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_AtomicRollback(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneLocalTurnHandlers, "base-plugin", []localturn.Handler{
		freezeTestLocalTurnHandler{id: "base-lt", ord: 10},
	}))

	beforeFreeze := dst.Freeze()

	// Malformed candidate with valid TerminalDecisionProvider and invalid AttemptTransforms (nil entry fails Validate)
	malformedCand := feature.NewMalformedGeneratedFrozenTerminalDecisionCandidateForTest(
		freezeTestTerminalProvider{id: "cand-term"},
		[]request.AttemptTransform{
			nil, // Invalid nil AttemptTransform
		},
	)

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "must not be nil")

	// Destination must remain completely unchanged across all planes and identities (rollback)
	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

type freezeTestSealingTerminalDecisionProvider struct {
	id     string
	calls  atomic.Int32
	sealed atomic.Bool
}

func (p *freezeTestSealingTerminalDecisionProvider) ID() string {
	if p.sealed.Load() {
		panic("ID() called after sealing")
	}
	p.calls.Add(1)
	return p.id
}

func (p *freezeTestSealingTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_NoIDCallAfterFreeze_Success(t *testing.T) {
	t.Parallel()

	prov := &freezeTestSealingTerminalDecisionProvider{id: "cand-sealed"}
	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(prov)))
	candFrozen := candSrc.Freeze()

	idCallsBefore := prov.calls.Load()
	require.Greater(t, idCallsBefore, int32(0))

	// Provider is sealed: any subsequent ID() call will panic.
	prov.sealed.Store(true)

	dst := feature.NewContributionSet()
	require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
	assert.Equal(t, idCallsBefore, prov.calls.Load(), "candidate merge must not invoke ID()")

	frozenDst := dst.Freeze()
	assert.Equal(t, idCallsBefore, prov.calls.Load(), "freeze must not invoke ID() on merged plane")
	assert.Equal(t, prov, feature.Get(frozenDst, feature.PlaneTerminalDecisionProvider))
	id, hasID := feature.FrozenIdentity(frozenDst, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "cand-sealed", id)
	assert.Equal(t, idCallsBefore, prov.calls.Load(), "FrozenIdentity must read stored identity without calling ID()")
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_NoIDCallAfterFreeze_Conflict(t *testing.T) {
	t.Parallel()

	provCand := &freezeTestSealingTerminalDecisionProvider{id: "cand-sealed"}
	candSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candSrc, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(provCand)))
	candFrozen := candSrc.Freeze()

	idCallsBeforeCand := provCand.calls.Load()
	provCand.sealed.Store(true)

	provBase := &freezeTestSealingTerminalDecisionProvider{id: "base-provider"}
	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(provBase)))
	idCallsBeforeBase := provBase.calls.Load()
	provBase.sealed.Store(true)

	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	require.ErrorIs(t, err, feature.ErrTerminalDecisionProviderConflict)
	assert.Contains(t, err.Error(), `"base-provider" and "cand-sealed"`)

	assert.Equal(t, idCallsBeforeCand, provCand.calls.Load(), "conflict check must use candidate frozen ID without calling ID()")
	assert.Equal(t, idCallsBeforeBase, provBase.calls.Load(), "conflict check must use destination frozen ID without calling ID()")
}

func TestContributeCandidateTo_GeneratedStorage_TerminalDecisionProvider_MissingFrozenIdentity(t *testing.T) {
	t.Parallel()

	prov := &freezeTestSealingTerminalDecisionProvider{id: "malformed-provider"}
	malformedCand := feature.NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(prov)

	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneLocalTurnHandlers, "base-plugin", []localturn.Handler{
		freezeTestLocalTurnHandler{id: "base-lt", ord: 10},
	}))
	beforeFreeze := dst.Freeze()

	err := malformedCand.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, "candidate", attrErr.PluginID)
	assert.Equal(t, feature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Contains(t, err.Error(), "frozen exclusive identity is missing")

	afterFreeze := dst.Freeze()
	assertFrozenPlaneSetsEqual(t, beforeFreeze, afterFreeze)
}

func TestTerminalDecisionProvider_ExactAttributedErrorStringAndErrorsIs(t *testing.T) {
	t.Parallel()

	dst := feature.NewContributionSet()
	provA := &freezeTestSealingTerminalDecisionProvider{id: "provider-a"}
	provB := &freezeTestSealingTerminalDecisionProvider{id: "provider-b"}
	require.NoError(t, feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "provider-a-plugin", terminaldecision.Provider(provA)))

	err := feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "provider-b-plugin", terminaldecision.Provider(provB))
	require.Error(t, err)

	expectedErrStr := "feature: plugin \"provider-b-plugin\" plane \"terminal_decision_provider\": feature: exclusive slot conflict\nfeaturebundle: terminal-decision provider conflict: \"provider-a\" and \"provider-b\""
	assert.Equal(t, expectedErrStr, err.Error())
	require.True(t, errors.Is(err, feature.ErrExclusiveConflict))
	require.True(t, errors.Is(err, feature.ErrTerminalDecisionProviderConflict))

	var attributed *feature.AttributedError
	require.True(t, errors.As(err, &attributed))
	assert.Equal(t, "provider-b-plugin", attributed.PluginID)
	assert.Equal(t, "terminal_decision_provider", attributed.PlaneID)

	// Second same-ID contribution is also rejected
	errSame := feature.Contribute(dst, feature.PlaneTerminalDecisionProvider, "provider-b-plugin", terminaldecision.Provider(provA))
	require.Error(t, errSame)
	require.True(t, errors.Is(errSame, feature.ErrExclusiveConflict))
	require.True(t, errors.Is(errSame, feature.ErrTerminalDecisionProviderConflict))

	// Destination remains provider A
	frozen := dst.Freeze()
	assert.Equal(t, provA, feature.Get(frozen, feature.PlaneTerminalDecisionProvider))
	id, hasID := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "provider-a", id)
}

func TestFreezeRequestPlanes_panicsOnNilAttemptTransform(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("want panic on nil AttemptTransform")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "AttemptTransforms contains nil entry") {
			t.Fatalf("panic=%v", r)
		}
	}()
	frozen := feature.NewMalformedGeneratedFrozenCandidateForTest(nil, []request.AttemptTransform{nil})
	_ = feature.FreezeRequestPlanes(frozen)
}

func TestFreezeRequestPlanes_panicsOnNilStreamObserverFactory(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("want panic on nil StreamObserverFactory")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "StreamObserverFactories contains nil entry") {
			t.Fatalf("panic=%v", r)
		}
	}()
	frozen := feature.NewMalformedGeneratedFrozenStreamObserversCandidateForTest([]response.StreamObserverFactory{nil})
	_ = feature.FreezeRequestPlanes(frozen)
}
