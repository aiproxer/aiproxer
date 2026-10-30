package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/testkit/execviewfixture"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/stretchr/testify/require"
)

type bootstrapTrafficCapture struct{ ctp [][]byte }

func (c *bootstrapTrafficCapture) OnObservation(_ context.Context, ev sdktraffic.Observation) error {
	if ev.Leg == sdktraffic.LegCTP {
		c.ctp = append(c.ctp, append([]byte(nil), ev.Body...))
	}
	return nil
}

func TestConversationBootstrap_LogicalIntentAllLeaves(t *testing.T) {
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{{Pattern: "^friendly$", Replacement: "be:logical"}})
	require.NoError(t, err)
	e := TestExecutor()
	e.DefaultBackend, e.SelectorAliases = "be", aliases
	for _, tc := range []struct {
		selector, model string
		ambiguous       bool
	}{
		{"friendly", "logical", false},
		{"logical", "logical", false},
		{"be:logical|other:logical", "logical", false},
		{"be:logical!other:logical", "logical", false},
		{"be:logical^other:logical", "logical", false},
		{"be:logical^other:different", "", true},
		{"be:logical!other:different|third:logical", "", true},
		{"[thinker]be:logical^(other:logical!third:different)", "", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			intent, err := e.initialModelIntent(tc.selector)
			require.NoError(t, err)
			require.Equal(t, InitialModelIntent{Model: tc.model, Ambiguous: tc.ambiguous}, intent)
		})
	}
	_, err = e.initialModelIntent("bad:")
	require.Error(t, err)
}

func TestConversationBootstrap_FirstSnapshotAndLazyReuse(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "secure"}[secure], func(t *testing.T) {
			reader := &countingReader{}
			traffic := &bootstrapTrafficCapture{}
			ex, authority := newSecureExecutorForCV(t, reader, extensions.SnapshotOptions{TrafficObserver: traffic})
			cv := conversationview.NewReferenceStore()
			reader.base = cv
			atomic := conversationview.NewAuthorizedReferenceStore(cv, authority)
			resolved := 0
			ex.ConversationBootstrap = func(ctx context.Context, id string, accepted *aipapi.Call, resolve func() (InitialModelIntent, error)) (string, error) {
				require.NotNil(t, accepted)
				require.Equal(t, id, accepted.Session.ALegID)
				require.NoError(t, cv.CreateALeg(ctx, id))
				_, err := atomic.BootstrapSteering(ctx, id, "owner", func() (conversationview.BootstrapDecision, error) {
					resolved++
					intent, err := resolve()
					require.NoError(t, err)
					require.Equal(t, "logical", intent.Model)
					return conversationview.BootstrapDecision{Outcome: conversationview.BootstrapMatched, Model: intent.Model, Overlays: []conversationview.PutSteeringRequest{{OverlayID: "owner.one", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "PRIVATE-735-STRUCTURAL-e8c1f"}, Placement: conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix}, AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback, Reason: "test"}}}, nil
				})
				return "", err
			}
			ctx := withTestPrincipal(t.Context(), "bootstrap-owner")
			if !secure {
				ctx = execDetachedCtx(ctx)
			}
			call := recordingCall("be:logical", nil)
			call.Instructions = []aipapi.Message{
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("  client-system-735\n exact  ")}},
				{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("client-developer-735\tuntouched")}},
			}
			recorder := &capturingTurnRecorder{}
			ex.SecureSessionRecorder = recorder
			pr, _, cleanup, err := ex.prepareRequest(ctx, call)
			require.NoError(t, err)
			require.Len(t, pr.conversationSnapshot.Steering, 1)
			require.Len(t, pr.call.Instructions, 3)
			require.Equal(t, call.Instructions, pr.call.Instructions[:2])
			require.Equal(t, call.Instructions, pr.identity.ingressCall.Instructions)
			if secure {
				require.Len(t, recorder.recorded, 1)
				require.Len(t, recorder.recorded[0].Lines, 3, "hidden system overlay must not become a structural client instruction")
			}
			legID := pr.identity.aLeg.ALegID
			resume := call.Session
			prefix := aipapi.CloneCall(*pr.call).Instructions
			cleanup()
			for turn := 2; turn <= 3; turn++ {
				later := aipapi.CloneCall(*call)
				later.ID = ""
				later.Route.Selector = "be:changed"
				later.Session = resume
				later.Session.ALegID = legID
				later.Messages = append(later.Messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("next-client-turn")}})
				pr, _, cleanup, err = ex.prepareRequest(ctx, &later)
				require.NoError(t, err)
				require.Equal(t, legID, pr.identity.aLeg.ALegID)
				require.Equal(t, prefix, pr.call.Instructions)
				require.Equal(t, later.Instructions, pr.identity.ingressCall.Instructions)
				if secure {
					require.Len(t, recorder.recorded[turn-1].Lines, 2+len(later.Messages))
				}
				cleanup()
				call = &later
			}
			require.Equal(t, 1, resolved)
			require.Equal(t, 3, reader.Count())
			if secure {
				require.Len(t, traffic.ctp, 3)
			}
			for _, body := range traffic.ctp {
				require.NotContains(t, string(body), "PRIVATE-735-STRUCTURAL-e8c1f")
			}
		})
	}
}

func TestConversationBootstrap_LocalSelectionBeforeSnapshotAndOnlyOnce(t *testing.T) {
	reader := &countingReader{}
	h := &fakeHandler{id: "local"}
	ex, st := newSecureExecutorForCV(t, reader, extensions.SnapshotOptions{FeaturePlanes: freezeBundle(testFeatureBundle{LocalTurnHandlers: []localturn.Handler{h}})})
	ex.ConversationViewTagger = newFakeTagger()
	cv := conversationview.NewReferenceStore()
	atomic := conversationview.NewAuthorizedReferenceStore(cv, st)
	bootstrapCalls := 0
	h.matchFn = func(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
		require.Equal(t, int(h.matchCalls.Load())-1, reader.Count(), "Match must precede each snapshot")
		if h.matchCalls.Load() == 1 {
			return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "test"}, nil
		}
		return localturn.MatchResult{}, nil
	}
	ex.ConversationBootstrap = func(ctx context.Context, id string, _ *aipapi.Call, resolve func() (InitialModelIntent, error)) (string, error) {
		bootstrapCalls++
		require.NoError(t, cv.CreateALeg(ctx, id))
		_, err := atomic.BootstrapSteering(ctx, id, "owner", func() (conversationview.BootstrapDecision, error) {
			intent, err := resolve()
			require.NoError(t, err)
			require.Equal(t, "logical", intent.Model)
			return conversationview.BootstrapDecision{Outcome: conversationview.BootstrapMatched, Overlays: []conversationview.PutSteeringRequest{{OverlayID: "owner.one", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "first inference"}, Placement: conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix}, AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback, Reason: "test"}}}, nil
		})
		reader.base = cv
		return "", err
	}
	ctx := execDetachedCtx(t.Context())
	call := recordingCall("be:logical", nil)
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	require.NoError(t, err)
	require.True(t, pr.isLocal)
	cleanup()
	require.Zero(t, bootstrapCalls)
	require.NoError(t, st.WithBLegAllocationAuthority(ctx, pr.identity.aLeg.ALegID, func(hasAllocated bool) error { require.False(t, hasAllocated); return nil }))
	call.Session.ALegID = pr.identity.aLeg.ALegID
	pr, _, cleanup, err = ex.prepareRequest(ctx, call)
	require.NoError(t, err)
	defer cleanup()
	require.Len(t, pr.conversationSnapshot.Steering, 1)
	require.Equal(t, int32(2), h.matchCalls.Load())
	require.Equal(t, int32(1), h.handleCalls.Load())
	require.Equal(t, 1, bootstrapCalls)
	require.Equal(t, 2, reader.Count())
}

// TestConversationBootstrap_StructuralCauseReachesThePreparedCall proves the
// bootstrap's committed instruction change rides the request it changed: the
// runtime records the bounded label the bootstrap returned on the live working
// call every later stage derives from, and never on the client ingress copy.
func TestConversationBootstrap_StructuralCauseReachesThePreparedCall(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "secure"}[secure], func(t *testing.T) {
			ex, _ := newSecureExecutorForCV(t, &countingReader{}, extensions.SnapshotOptions{})
			ex.ConversationBootstrap = func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
				return "system-prompt", nil
			}
			ctx := withTestPrincipal(t.Context(), "cause-owner")
			if !secure {
				ctx = execDetachedCtx(ctx)
			}
			pr, _, cleanup, err := ex.prepareRequest(ctx, recordingCall("be:logical", nil))
			require.NoError(t, err)
			defer cleanup()
			require.Equal(t, "system-prompt", pr.call.StructuralCause)
			require.Empty(t, pr.identity.ingressCall.StructuralCause, "the cause is proxy-internal and never on the client ingress")
		})
	}
}

// TestConversationBootstrap_StructuralCauseBoundaries pins the fail-open
// boundaries of the cause the bootstrap returns: the first cause on the request
// wins, and a label that cannot cross the canonical bound is dropped instead of
// failing the request.
func TestConversationBootstrap_StructuralCauseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		returned string
		existing string
		want     string
	}{
		{name: "committed instructions change", returned: "system-prompt", want: "system-prompt"},
		{name: "nothing changed", returned: "", want: ""},
		{name: "first cause wins", returned: "system-prompt", existing: "compaction", want: "compaction"},
		{name: "in-bounds non-vocabulary label is dropped", returned: "interleaved-memo", want: ""},
		{name: "label beyond the canonical bound is dropped", returned: strings.Repeat("x", aipapi.MaxStructuralCauseBytes+1), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, st := newSecureExecutorForCV(t, &countingReader{}, extensions.SnapshotOptions{})
			parent, err := st.CreateALeg(t.Context(), "cause-parent")
			require.NoError(t, err)
			ex.ConversationBootstrap = func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
				return tc.returned, nil
			}
			call := recordingCall("be:logical", nil)
			call.StructuralCause = tc.existing
			ibt := &identityBoundTurn{aLeg: parent, ingressCall: recordingCall("be:client", nil)}
			require.NoError(t, ex.selectLocalAndBootstrap(t.Context(), ibt, call))
			require.Equal(t, tc.want, call.StructuralCause)
			require.Empty(t, ibt.ingressCall.StructuralCause)
		})
	}
}

// TestConversationBootstrap_StructuralCauseFailurePropagates proves a failed
// bootstrap still fails the request and records no cause: the label is
// advisory and never a substitute for the bootstrap's own contract.
func TestConversationBootstrap_StructuralCauseFailurePropagates(t *testing.T) {
	ex, st := newSecureExecutorForCV(t, &countingReader{}, extensions.SnapshotOptions{})
	parent, err := st.CreateALeg(t.Context(), "cause-failure-parent")
	require.NoError(t, err)
	ex.ConversationBootstrap = func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
		return "system-prompt", errors.New("storage refused")
	}
	call := recordingCall("be:logical", nil)
	ibt := &identityBoundTurn{aLeg: parent, ingressCall: recordingCall("be:client", nil)}
	err = ex.selectLocalAndBootstrap(t.Context(), ibt, call)
	require.ErrorContains(t, err, "storage refused")
	require.Empty(t, call.StructuralCause)
}

func TestConversationBootstrap_FrozenOverrideAndDetachedPrivateOwner(t *testing.T) {
	ex, st := newSecureExecutorForCV(t, &countingReader{}, extensions.SnapshotOptions{})
	parent, err := st.CreateALeg(t.Context(), "parent")
	require.NoError(t, err)
	var owner string
	ex.ConversationBootstrap = func(_ context.Context, id string, _ *aipapi.Call, resolve func() (InitialModelIntent, error)) (string, error) {
		owner = id
		intent, err := resolve()
		require.NoError(t, err)
		require.Equal(t, "child", intent.Model)
		return "", nil
	}
	call := recordingCall("be:child", nil)
	call.Session.ALegID = parent.ALegID
	ctx := execctx.WithDetachedSession(t.Context(), execctx.DetachedSession{ParentALegID: parent.ALegID})
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	require.NoError(t, err)
	cleanup()
	require.NotEmpty(t, owner, "detached bootstrap must run for the private child A-leg")
	require.Equal(t, pr.identity.aLeg.ALegID, owner, "detached bootstrap must be keyed to the private child A-leg")
	require.NotEqual(t, parent.ALegID, owner)
	ibt := &identityBoundTurn{aLeg: parent, ingressCall: recordingCall("be:client", nil), routeAuth: routeAuthoritySnapshot{State: routeoverride.State{Active: true, Selector: "be:override"}}}
	ex.ConversationBootstrap = func(_ context.Context, _ string, _ *aipapi.Call, resolve func() (InitialModelIntent, error)) (string, error) {
		intent, err := resolve()
		require.NoError(t, err)
		require.Equal(t, "override", intent.Model)
		return "", nil
	}
	require.NoError(t, ex.selectLocalAndBootstrap(t.Context(), ibt, ibt.ingressCall))
}

func TestConversationBootstrap_FailureBeforeSnapshot(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "secure"}[secure], func(t *testing.T) {
			reader := &countingReader{}
			ex, _ := newSecureExecutorForCV(t, reader, extensions.SnapshotOptions{})
			authority := &sequencingAuthorityRecorder{}
			ex.UsageAuthority = authority
			attachPhase6Coordinators(ex)
			ex.ConversationBootstrap = func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
				return "", errors.New("storage refused")
			}
			ctx := withTestPrincipal(t.Context(), "failure-owner")
			if !secure {
				ctx = execDetachedCtx(ctx)
			}
			_, _, _, err := ex.prepareRequest(ctx, recordingCall("be:logical", nil))
			require.ErrorContains(t, err, "storage refused")
			require.Zero(t, reader.Count())
			require.Equal(t, int64(1), authority.releaseCalls.Load())
		})
	}
}

func TestConversationBootstrap_DetachedMatchAndHandleUsePrivateChildViews(t *testing.T) {
	reader := &countingReader{}
	tagger := newFakeTagger()
	h := &fakeHandler{id: "private-child"}
	ex, st := newSecureExecutorForCV(t, reader, extensions.SnapshotOptions{FeaturePlanes: freezeBundle(testFeatureBundle{LocalTurnHandlers: []localturn.Handler{h}})})
	ex.ConversationViewTagger = tagger
	parent, err := st.CreateALeg(t.Context(), "parent")
	require.NoError(t, err)
	parentViews := execviewfixture.PoisonedParent(parent.ALegID)
	trustedScope := parentViews.Scope
	parentCtx := execctx.WithViews(t.Context(), parentViews)
	parentCtx = session.WithSecureTurnPolicy(parentCtx, session.SecureTurnPolicyView{TranscriptEnabled: true})
	lineage := execctx.DetachedSession{ParentSessionID: "parent-session", ParentALegID: parent.ALegID, ParentTraceID: "parent-trace", AuxiliaryRole: "compaction_continuity_extractor"}
	ctx := execctx.WithDetachedSession(parentCtx, lineage)
	var matchViews, handleViews execctx.Views
	readViews := func(ctx context.Context) execctx.Views {
		views, ok := execctx.FromContext(ctx)
		require.True(t, ok)
		sdkSession, ok := session.SessionViewFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, views.Session, sdkSession)
		sdkWorkspace, ok := aipworkspace.WorkspaceViewFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, views.Workspace, sdkWorkspace)
		_, authorized := session.SecureTurnPolicyFromContext(ctx)
		require.False(t, authorized)
		gotLineage, ok := execctx.DetachedSessionFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, lineage, gotLineage, "parent identifiers remain correlation-only")
		return views
	}
	h.matchFn = func(ctx context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
		matchViews = readViews(ctx)
		require.Empty(t, tagger.Calls(), "Match must precede source tagging")
		require.Equal(t, matchViews.Session.ALegID, call.Session.ALegID)
		reason := localturn.ReasonCode("child_claim")
		if matchViews.Session.Labels["parent_claim"] == "poison" {
			reason = "parent_claim"
		}
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: reason}, nil
	}
	h.handleFn = func(ctx context.Context, in localturn.HandleInput) (localturn.Reply, error) {
		handleViews = readViews(ctx)
		require.Len(t, tagger.Calls(), 1, "source tag must precede Handle")
		return localturn.Reply{Text: string(in.Match.Reason)}, nil
	}
	bootstrapCalls := 0
	ex.ConversationBootstrap = func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
		bootstrapCalls++
		return "", nil
	}
	call := recordingCall("be:child", nil)
	call.ID = "child-trace"
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	require.NoError(t, err)
	defer cleanup()
	require.True(t, pr.isLocal)
	require.Equal(t, int32(1), h.matchCalls.Load())
	require.Equal(t, int32(1), h.handleCalls.Load())
	require.Equal(t, 1, reader.Count())
	require.Zero(t, bootstrapCalls)
	childID := pr.identity.aLeg.ALegID
	require.NotEqual(t, parent.ALegID, childID)
	expected := execctx.Views{Principal: trustedScope.Principal(), Scope: trustedScope, Session: session.SessionView{ALegID: childID}, Attempt: execview.AttemptView{TraceID: "child-trace"}, Annotations: map[string]string{"execution_mode": "detached"}}
	require.Equal(t, expected, matchViews, "Match must not inherit parent labels, attempt or annotations")
	require.Equal(t, expected, handleViews, "Handle must see exactly the same private child authority")
	tags := tagger.Calls()
	require.Len(t, tags, 2)
	require.Equal(t, conversationprojection.ReasonCode("child_claim"), tags[0][0].Reason)
	require.NoError(t, st.WithBLegAllocationAuthority(ctx, childID, func(hasAllocated bool) error { require.False(t, hasAllocated); return nil }))
	unchanged, ok := execctx.FromContext(parentCtx)
	require.True(t, ok)
	require.Equal(t, parentViews, unchanged)
}
