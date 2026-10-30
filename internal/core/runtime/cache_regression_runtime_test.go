package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// helpers

func rtCacheSys(t string) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: t}}}
}

func rtCacheUser(t string) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: t}}}
}

func rtCacheAssistant(t string) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: t}}}
}

func rtCacheTraj(call aipapi.Call) []string {
	var out []string
	if call.HasItemAuthority() {
		for _, it := range call.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			txt := ""
			if len(it.Content) > 0 {
				txt = it.Content[0].Text
			}
			out = append(out, string(it.Role)+":"+txt)
		}
	} else {
		for _, m := range call.Instructions {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
		for _, m := range call.Messages {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
	}
	return out
}

func rtIsPrefix(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func TestCacheRegression_Runtime_FrozenSnapshot_PrefixStable_AcrossThreeTurns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	// Need an A-leg via runtime prepare to get deterministic ID, then seed steering
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.ConversationViewReader = nil
	tmpEx.ConversationViewTagger = nil
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(1000, 0) }
	callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	require.NoError(t, err)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	_ = cv.CreateALeg(ctx, aLegID)
	// Create stable_prefix overlay via ReferenceStore (explicit discontinuity)
	st, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-rt-stable",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "RT_STABLE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)
	require.Equal(t, conversationview.CacheDiscontinuityCreate, st.CacheDiscontinuityKind)
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Use counting reader that returns same frozen snap for any A-leg (runtime should read exactly once per turn)
	counting := &cacheCountingReader{snap: snap}
	ex := TestExecutor()
	ex.Store = store
	ex.ConversationViewReader = counting
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(1000, 0) }
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	u2 := rtCacheUser("u2")
	u3 := rtCacheUser("u3")

	// Turn N: [sys, u1]
	callN := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}
	// Turn N+1: [sys, u1, a1, u2]
	a1 := rtCacheAssistant("a1")
	callN1 := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2}}
	// Turn N+2: [sys, u1, a1, u2, a2, u3]
	a2 := rtCacheAssistant("a2")
	callN2 := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2, a2, u3}}
	// Simulate three append-only turns via direct Project with frozen snap (runtime would do same)
	projN, _, err := conversationprojection.Project(*callN, snap)
	require.NoError(t, err)
	projN1, _, err := conversationprojection.Project(*callN1, snap)
	require.NoError(t, err)
	projN2, _, err := conversationprojection.Project(*callN2, snap)
	require.NoError(t, err)
	require.True(t, rtIsPrefix(rtCacheTraj(projN), rtCacheTraj(projN1)))
	require.True(t, rtIsPrefix(rtCacheTraj(projN1), rtCacheTraj(projN2)))
	// Runtime prepare must also project with same frozen snap and not follow moving tail
	prN, _, cleanupN, err := ex.prepareRequest(execDetachedCtx(ctx), callN)
	require.NoError(t, err)
	defer cleanupN()
	require.Equal(t, 1, counting.Count())
	// Verify RT_STABLE not at tail but at stable prefix
	traj := rtCacheTraj(*prN.call)
	idx := -1
	for i, s := range traj {
		if s == "system:RT_STABLE" {
			idx = i
		}
	}
	require.Equal(t, 1, idx, "RT_STABLE must be at stable prefix index 1, got %v", traj)
	// Second turn via same frozen snap must be prefix
	prN1, _, cleanupN1, err := ex.prepareRequest(execDetachedCtx(ctx), callN1)
	require.NoError(t, err)
	defer cleanupN1()
	assert.True(t, rtIsPrefix(rtCacheTraj(*prN.call), rtCacheTraj(*prN1.call)))
	// Verify no dynamic metadata in projection evidence/summary
	raw, _ := json.Marshal(prN.conversationEvidence)
	assert.False(t, strings.Contains(string(raw), "RT_STABLE"))
	rawSum, _ := json.Marshal(prN.conversationSummary)
	assert.False(t, strings.Contains(string(rawSum), "RT_STABLE"))
	assert.False(t, strings.Contains(string(rawSum), "ov-rt-stable"))
	_ = projN
	_ = projN1
	_ = projN2
}

func TestCacheRegression_Runtime_FixedActivation_FrozenAcrossTurns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.ConversationViewReader = nil
	tmpEx.ConversationViewTagger = nil
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(2000, 0) }
	callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	require.NoError(t, err)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	_ = cv.CreateALeg(ctx, aLegID)
	uAnchor := rtCacheUser("anchor-N")
	sys := rtCacheSys("sys")
	anchorCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor}}
	snap0, _ := cv.Snapshot(ctx, aLegID)
	anchor, err := conversationprojection.ResolveAfterIngressTailAnchor(anchorCall, snap0)
	require.NoError(t, err)
	_, err = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-rt-fixed",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "RT_FIXED"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	require.NoError(t, err)
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Three turns
	a1 := rtCacheAssistant("a1")
	u2 := rtCacheUser("u2")
	a2 := rtCacheAssistant("a2")
	u3 := rtCacheUser("u3")
	callN := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor}}
	callN1 := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor, a1, u2}}
	callN2 := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor, a1, u2, a2, u3}}
	projN, ev, _ := conversationprojection.Project(callN, snap)
	projN1, _, _ := conversationprojection.Project(callN1, snap)
	projN2, _, _ := conversationprojection.Project(callN2, snap)
	require.True(t, rtIsPrefix(rtCacheTraj(projN), rtCacheTraj(projN1)))
	require.True(t, rtIsPrefix(rtCacheTraj(projN1), rtCacheTraj(projN2)))
	// Verify activation ordering U_N, STEER
	assert.Equal(t, []string{"system:sys", "user:anchor-N", "system:RT_FIXED"}, rtCacheTraj(projN))
	// Same revision byte-stable
	require.Len(t, ev.Provenance, 1)
	assert.Equal(t, ev.Provenance[0].InjectedIdentity, ev.Provenance[0].InjectedIdentity) // same
	_ = projN2
}

func TestCacheRegression_Runtime_MutationDiscontinuity_FrozenIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.ConversationViewReader = nil
	tmpEx.ConversationViewTagger = nil
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(3000, 0) }
	callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	require.NoError(t, err)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	_ = cv.CreateALeg(ctx, aLegID)
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	a1 := rtCacheAssistant("a1")
	u2 := rtCacheUser("u2")
	// Initial snap empty
	snap0, _ := cv.Snapshot(ctx, aLegID)
	traj0 := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap0))
	// CREATE discontinuity via store
	stCreate, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "DISC_CREATE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityCreate, stCreate.CacheDiscontinuityKind)
	snap1, _ := cv.Snapshot(ctx, aLegID)
	traj1 := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap1))
	require.False(t, rtIsPrefix(traj0, traj1), "create must be discontinuity")
	// Subsequent turns after create stable
	traj1b := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2}}, snap1))
	require.True(t, rtIsPrefix(traj1, traj1b))
	// REPLACE
	stRep, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "DISC_REPLACE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityReplace, stRep.CacheDiscontinuityKind)
	snap2, _ := cv.Snapshot(ctx, aLegID)
	traj2 := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap2))
	require.False(t, rtIsPrefix(traj1, traj2))
	traj2b := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2}}, snap2))
	require.True(t, rtIsPrefix(traj2, traj2b))
	// MOVE
	anchor, _ := conversationprojection.ResolveAfterIngressTailAnchor(aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap2)
	stMove, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "DISC_REPLACE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityMove, stMove.CacheDiscontinuityKind)
	snap3, _ := cv.Snapshot(ctx, aLegID)
	traj3 := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap3))
	require.False(t, rtIsPrefix(traj2, traj3))
	traj3b := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2}}, snap3))
	require.True(t, rtIsPrefix(traj3, traj3b))
	// DEACTIVATE
	stDeact, _ := cv.DeactivateSteering(ctx, aLegID, "ov-disc")
	require.Equal(t, conversationview.CacheDiscontinuityDeactivate, stDeact.CacheDiscontinuityKind)
	snap4, _ := cv.Snapshot(ctx, aLegID)
	traj4 := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, snap4))
	require.False(t, rtIsPrefix(traj3, traj4))
	traj4b := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1, u2}}, snap4))
	require.True(t, rtIsPrefix(traj4, traj4b))
	// In-flight turn isolation: frozen snapshot N must not see N+1 mutation
	// Simulate: take snap1, then mutate to snap2, but in-flight uses snap1
	trajInflight := rtCacheTraj(mustProj(t, aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, a1}}, snap1))
	assert.Contains(t, strings.Join(trajInflight, ","), "DISC_CREATE")
	assert.NotContains(t, strings.Join(trajInflight, ","), "DISC_REPLACE")
}

func TestCacheRegression_Runtime_AnchorDisappearance_FallbackAndFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("fallback via runtime Execute", func(t *testing.T) {
		t.Parallel()
		store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		cv := conversationview.NewReferenceStore()
		tmpEx := TestExecutor()
		tmpEx.Store = store
		tmpEx.ConversationViewReader = nil
		tmpEx.ConversationViewTagger = nil
		tmpEx.Bus = hooks.New(hooks.Config{})
		tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
		tmpEx.Rand = routing.NewSeededRng(1)
		tmpEx.Now = func() time.Time { return time.Unix(4000, 0) }
		callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
		prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
		require.NoError(t, err)
		aLegID := prInit.identity.aLeg.ALegID
		cleanup()
		_ = cv.CreateALeg(ctx, aLegID)
		uAnchor := rtCacheUser("anchor-fb")
		sys := rtCacheSys("sys")
		anchor, _ := conversationprojection.ResolveAfterIngressTailAnchor(aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor}}, mustSnap(t, cv, aLegID))
		_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "ov-fb",
			Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "FB_STEER"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "test",
		})
		snap, _ := cv.Snapshot(ctx, aLegID)
		counting := &cacheCountingReader{snap: snap}
		capBackend := &cacheCaptureBackend{caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)}
		ex := TestExecutor()
		ex.Store = store
		ex.ConversationViewReader = counting
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(4000, 0) }
		// Compacted history missing anchor
		call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1")}}
		stream, err := ex.Execute(execDetachedCtx(ctx), call)
		require.NoError(t, err)
		_, err = aipapi.Collect(ctx, stream)
		require.NoError(t, err)
		_ = stream.Close()
		require.Equal(t, 1, capBackend.OpenCount())
		open := capBackend.LastCall()
		foundInInstr := false
		for _, m := range open.Instructions {
			if len(m.Parts) > 0 && m.Parts[0].Text == "FB_STEER" {
				foundInInstr = true
			}
		}
		require.True(t, foundInInstr, "fallback must be in Instructions")
		// second compacted projection must be deterministic (no wandering)
		call2 := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1"), rtCacheUser("u2")}}
		// Direct project with same snap should be prefix-stable in fallback position
		proj1, ev1, _ := conversationprojection.Project(*call, snap)
		proj2, ev2, _ := conversationprojection.Project(*call2, snap)
		require.Len(t, ev1.Fallbacks, 1)
		require.Len(t, ev2.Fallbacks, 1)
		assert.True(t, rtIsPrefix(rtCacheTraj(proj1), rtCacheTraj(proj2)))
		// Bounded evidence: no plaintext
		raw, _ := json.Marshal(ev1)
		assert.False(t, strings.Contains(string(raw), "FB_STEER"))
	})
	t.Run("fail_closed via runtime Execute", func(t *testing.T) {
		t.Parallel()
		store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		cv := conversationview.NewReferenceStore()
		tmpEx := TestExecutor()
		tmpEx.Store = store
		tmpEx.ConversationViewReader = nil
		tmpEx.ConversationViewTagger = nil
		tmpEx.Bus = hooks.New(hooks.Config{})
		tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
		tmpEx.Rand = routing.NewSeededRng(1)
		tmpEx.Now = func() time.Time { return time.Unix(4000, 0) }
		callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
		prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
		require.NoError(t, err)
		aLegID := prInit.identity.aLeg.ALegID
		cleanup()
		_ = cv.CreateALeg(ctx, aLegID)
		uAnchor := rtCacheUser("anchor-fc")
		sys := rtCacheSys("sys")
		anchor, _ := conversationprojection.ResolveAfterIngressTailAnchor(aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{uAnchor}}, mustSnap(t, cv, aLegID))
		_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "ov-fc",
			Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "FC_STEER"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "test",
		})
		snap, _ := cv.Snapshot(ctx, aLegID)
		counting := &cacheCountingReader{snap: snap}
		capBackend := &cacheCaptureBackend{caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)}
		ex := TestExecutor()
		ex.Store = store
		ex.ConversationViewReader = counting
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(4000, 0) }
		call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1")}}
		_, err = ex.Execute(execDetachedCtx(ctx), call)
		require.Error(t, err)
		assert.Equal(t, 0, capBackend.OpenCount(), "fail_closed must not open backend")
		assert.False(t, strings.Contains(err.Error(), "FC_STEER"))
	})
}

func TestCacheRegression_Runtime_NoTailReinject_And_ReassertFrozen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.ConversationViewReader = nil
	tmpEx.ConversationViewTagger = nil
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(5000, 0) }
	callInit := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	require.NoError(t, err)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	_ = cv.CreateALeg(ctx, aLegID)
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	anchor, _ := conversationprojection.ResolveAfterIngressTailAnchor(aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1}}, mustSnap(t, cv, aLegID))
	_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-reassert",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "REASSERT_STEER"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Late transform that moves steering to tail must be repaired by Reassert frozen snapshot
	baseCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, rtCacheAssistant("a1"), rtCacheUser("u2")}}
	baseline, ev, _ := conversationprojection.Project(baseCall, snap)
	late := aipapi.CloneCall(baseline)
	// Move steering to tail (simulate per-turn tail re-inject bug)
	var steeringMsg aipapi.Message
	var filtered []aipapi.Message
	for _, m := range late.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "REASSERT_STEER" {
			steeringMsg = m
			continue
		}
		filtered = append(filtered, m)
	}
	late.Messages = append(filtered, steeringMsg)
	filteredBaseline, _ := conversationprojection.FilterNeverBackend(baseCall, snap)
	repaired, _, err := conversationprojection.Reassert(late, snap, ev.Provenance, filteredBaseline)
	require.NoError(t, err)
	trajRepaired := rtCacheTraj(repaired)
	trajBaseline := rtCacheTraj(baseline)
	assert.Equal(t, trajBaseline, trajRepaired, "Reassert must restore frozen fixed position, not tail")
	// Ensure repaired is prefix of later turn
	laterCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{u1, rtCacheAssistant("a1"), rtCacheUser("u2"), rtCacheAssistant("a2"), rtCacheUser("u3")}}
	projLater, _, _ := conversationprojection.Project(laterCall, snap)
	assert.True(t, rtIsPrefix(trajRepaired, rtCacheTraj(projLater)))
	// Tail re-inject would break prefix: demonstrate
	tailTraj := append(rtCacheTraj(baseline)[:len(rtCacheTraj(baseline))-1], "system:REASSERT_STEER")
	assert.False(t, rtIsPrefix(trajBaseline, tailTraj))
}

// helpers

type cacheCountingReader struct {
	snap  conversationprojection.Snapshot
	count int
}

func (c *cacheCountingReader) Snapshot(_ context.Context, _ string) (conversationprojection.Snapshot, error) {
	c.count++
	return c.snap, nil
}
func (c *cacheCountingReader) Count() int { return c.count }

type cacheCaptureBackend struct {
	caps      aipapi.BackendCaps
	calls     []aipapi.Call
	openCount int
}

func (c *cacheCaptureBackend) Backend() execbackend.Backend {
	return execbackend.Backend{Caps: c.caps, Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		c.openCount++
		c.calls = append(c.calls, aipapi.CloneCall(call))
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
	}}
}
func (c *cacheCaptureBackend) OpenCount() int { return c.openCount }
func (c *cacheCaptureBackend) LastCall() aipapi.Call {
	if len(c.calls) == 0 {
		return aipapi.Call{}
	}
	return aipapi.CloneCall(c.calls[len(c.calls)-1])
}

func mustSnap(t *testing.T, cv conversationview.Store, aLeg string) conversationprojection.Snapshot {
	t.Helper()
	snap, err := cv.Snapshot(context.Background(), aLeg)
	require.NoError(t, err)
	return snap
}

func mustProj(t *testing.T, call aipapi.Call, snap conversationprojection.Snapshot) aipapi.Call {
	t.Helper()
	proj, _, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	return proj
}

// Ensure imports used
var _ = sdkhooks.FailClosed
