//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// helpers for ephemeral view tests

func ephemeralSemanticPart(text string) aipapi.Part {
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: text}}
}

func ephemeralExactPartWithOpaque(text string, opaque json.RawMessage) aipapi.Part {
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: text, Opaque: opaque, Summary: json.RawMessage(`[]`), SummaryPresent: true}}
}

func ephemeralExactPartWithSignature(text, sig string) aipapi.Part {
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectAnthropicThinkingV1, Text: text, Signature: sig}}
}

func testEphemeralConfig(mode CompressionMode) CompressionConfig {
	return CompressionConfig{
		Enabled:                     true,
		Mode:                        mode,
		Route:                       "test-route",
		EgressPolicyRef:             "policy-v1",
		Timeout:                     time.Second,
		MaxInputTokens:              1000,
		MaxInputBytes:               4096,
		MaxOutputTokens:             500,
		MaxOutputBytes:              5000,
		MaxSurrogateBytes:           1024,
		MinSourceBytes:              5,
		MinSavedBytes:               5,
		MinSavingsRatio:             0.1,
		MaxPendingPerSession:        10,
		MaxSurrogateBytesPerSession: 100000,
		MaxPendingTotal:             100,
		MaxSurrogateBytesTotal:      1000000,
	}
}

func TestBuildEphemeralArtifact_MixedPlacementsExactRawEquality(t *testing.T) {
	t.Parallel()
	// Mixed: semantic at 0, exact at 1, semantic at 2
	// Exact part carries Opaque + Summary to verify byte equality.
	opaque := json.RawMessage(`{"k":"v","a":1}`)
	sem0 := ephemeralSemanticPart(strings.Repeat("a", 20))
	exact := ephemeralExactPartWithOpaque("exact-text", opaque)
	sem2 := ephemeralSemanticPart(strings.Repeat("b", 20))
	orig := TurnArtifact{
		ID:     "mix-1",
		Anchor: sha256.Sum256([]byte("anchor-mix")),
		Reasoning: []PlacedReasoning{
			{BeforeNonReasoningPart: 0, Part: sem0},
			{BeforeNonReasoningPart: 1, Part: exact},
			{BeforeNonReasoningPart: 2, Part: sem2},
		},
	}
	// Build surrogate for semantic indexes 0 and 2 only
	sur := &ReasoningSurrogate{
		OriginalDigest:      orig.Anchor,
		PolicyRevision:      "policy-v1",
		Sanitization:        SanitizationNone,
		Segments:            []SurrogateSegment{{PlacementIndex: 0, Text: "compressed0", Bytes: 11}, {PlacementIndex: 2, Text: "compressed2", Bytes: 11}},
		Bytes:               22,
		SemanticDigest:      computeSemanticDigest(orig.Reasoning),
		EgressPolicyHash:    sha256.Sum256([]byte("eg")),
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
	eph := BuildEphemeralArtifact(orig, sur)
	// Check exact part at index 1 preserved byte-equivalent
	if eph.Reasoning[1].Part.Reasoning.Text != "exact-text" {
		t.Fatalf("exact text changed: got %q", eph.Reasoning[1].Part.Reasoning.Text)
	}
	if !bytes.Equal(eph.Reasoning[1].Part.Reasoning.Opaque, opaque) {
		t.Fatalf("exact opaque not preserved: got %s want %s", string(eph.Reasoning[1].Part.Reasoning.Opaque), string(opaque))
	}
	if !bytes.Equal(eph.Reasoning[1].Part.Reasoning.Summary, json.RawMessage(`[]`)) {
		t.Fatalf("exact summary not preserved")
	}
	// Semantic parts replaced
	if eph.Reasoning[0].Part.Reasoning.Text != "compressed0" {
		t.Fatalf("semantic 0 not replaced: got %q", eph.Reasoning[0].Part.Reasoning.Text)
	}
	if eph.Reasoning[2].Part.Reasoning.Text != "compressed2" {
		t.Fatalf("semantic 2 not replaced: got %q", eph.Reasoning[2].Part.Reasoning.Text)
	}
	// Dialect preserved for semantic
	if eph.Reasoning[0].Part.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("dialect not preserved")
	}
	// BeforeNonReasoningPart preserved
	if eph.Reasoning[0].BeforeNonReasoningPart != 0 || eph.Reasoning[1].BeforeNonReasoningPart != 1 || eph.Reasoning[2].BeforeNonReasoningPart != 2 {
		t.Fatalf("placement preserved failed: %+v", eph.Reasoning)
	}
	// Original unchanged
	if orig.Reasoning[0].Part.Reasoning.Text != strings.Repeat("a", 20) {
		t.Fatalf("original mutated")
	}
	// Ensure defensive copy: mutating eph does not affect orig
	eph.Reasoning[0].Part.Reasoning.Text = "mutated"
	if orig.Reasoning[0].Part.Reasoning.Text == "mutated" {
		t.Fatalf("defensive copy failed")
	}
}

func TestBuildEphemeralArtifact_StructuralPlacementOrder(t *testing.T) {
	t.Parallel()
	sem := ephemeralSemanticPart(strings.Repeat("x", 10))
	sem2 := ephemeralSemanticPart(strings.Repeat("y", 10))
	orig := TurnArtifact{
		ID:     "order-1",
		Anchor: sha256.Sum256([]byte("anchor-order")),
		Reasoning: []PlacedReasoning{
			{BeforeNonReasoningPart: 2, Part: sem},
			{BeforeNonReasoningPart: 0, Part: sem2},
		},
	}
	sur := &ReasoningSurrogate{
		OriginalDigest:      orig.Anchor,
		PolicyRevision:      "policy-v1",
		Sanitization:        SanitizationNone,
		Segments:            []SurrogateSegment{{PlacementIndex: 0, Text: "c0", Bytes: 2}, {PlacementIndex: 1, Text: "c1", Bytes: 2}},
		Bytes:               4,
		SemanticDigest:      computeSemanticDigest(orig.Reasoning),
		EgressPolicyHash:    sha256.Sum256([]byte("eg")),
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
	eph := BuildEphemeralArtifact(orig, sur)
	if len(eph.Reasoning) != 2 {
		t.Fatalf("len mismatch")
	}
	if eph.Reasoning[0].BeforeNonReasoningPart != 2 || eph.Reasoning[1].BeforeNonReasoningPart != 0 {
		t.Fatalf("order/placement not preserved: %+v", eph.Reasoning)
	}
	// Text replaced correctly at matching placement index (0->c0, 1->c1) regardless of BeforeNonReasoningPart order
	if eph.Reasoning[0].Part.Reasoning.Text != "c0" || eph.Reasoning[1].Part.Reasoning.Text != "c1" {
		t.Fatalf("text order mismatch: %q %q", eph.Reasoning[0].Part.Reasoning.Text, eph.Reasoning[1].Part.Reasoning.Text)
	}
}

func TestBuildEphemeralArtifact_StoreSnapshotUnchangedAfterCallAndMutation(t *testing.T) {
	t.Parallel()
	cfg := testEphemeralConfig(CompressionActive)
	store, _ := NewMemoryTurnStore(StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       10,
		MaxReasoningBytesPerTurn: 4096,
		MaxSessionBytes:          100000,
		Now:                      time.Now,
		CompressionLimits:        cfg.ToLimits(),
	})
	partition := NewSessionPartition("sess-store-unchanged")
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "store-1", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	if _, err := store.Append(context.Background(), partition, art); err != nil {
		t.Fatalf("append: %v", err)
	}
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	sur := validSurrogateFor(cfg, snapArt)
	cs := store.(CompressionStore)
	// Directly test BuildEphemeralArtifact defensive copy does not mutate store snapshot
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, "job-store")
	_ = cs.AttachSurrogate(context.Background(), claim, "job-store", *sur)
	// Now snapshot before ephemeral view
	beforeSnap, _ := store.Snapshot(context.Background(), partition)
	// Build ephemeral view via helper
	decisions := map[string]ReasoningViewResult{snapArt.ID: {Kind: ViewSurrogate, Reason: "eligible"}}
	ephArts := BuildEphemeralArtifacts(beforeSnap, decisions, func(id string) (*ReasoningSurrogate, bool) {
		st, ok, _ := cs.GetCompressionState(context.Background(), partition, id)
		if !ok || st.Surrogate == nil {
			return nil, false
		}
		return st.Surrogate, true
	})
	// Mutate ephemeral
	ephArts[0].Reasoning[0].Part.Reasoning.Text = "mutated-ephemeral"
	// Snapshot again must be unchanged
	afterSnap, _ := store.Snapshot(context.Background(), partition)
	if afterSnap[0].Reasoning[0].Part.Reasoning.Text == "mutated-ephemeral" {
		t.Fatalf("store mutated via ephemeral")
	}
	if afterSnap[0].Reasoning[0].Part.Reasoning.Text != strings.Repeat("a", 20) {
		t.Fatalf("store original changed: got %q", afterSnap[0].Reasoning[0].Part.Reasoning.Text)
	}
	// Also test via HandleAttempt active path snapshot unchanged
	cfgActive := Config{
		Action:            ActionRestore,
		OnUnrepresentable: PolicyReject,
		OnStateError:      PolicyLogSkip,
		OnAmbiguous:       PolicyLogSkip,
		State:             StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:             []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression:       cfg,
	}
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgActive, store, CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}, CompanionPolicy{}, nil)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-store-unchanged"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	_, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	// Mutate call after handle
	if len(call.Messages[0].Parts) > 0 && call.Messages[0].Parts[0].Reasoning != nil {
		call.Messages[0].Parts[0].Reasoning.Text = "post-mutate"
	}
	snapAfterHandle, _ := store.Snapshot(context.Background(), partition)
	if snapAfterHandle[0].Reasoning[0].Part.Reasoning.Text == "post-mutate" {
		t.Fatalf("store mutated via call")
	}
}

func TestBuildEphemeralArtifact_AmbiguityFallback(t *testing.T) {
	t.Parallel()
	sem := ephemeralSemanticPart(strings.Repeat("a", 10))
	orig := TurnArtifact{ID: "amb-1", Anchor: sha256.Sum256([]byte("anchor-amb")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: sem}}}
	cases := []struct {
		name string
		sur  *ReasoningSurrogate
	}{
		{
			name: "duplicate index",
			sur: &ReasoningSurrogate{
				OriginalDigest: orig.Anchor, PolicyRevision: "policy-v1", Sanitization: SanitizationNone,
				Segments: []SurrogateSegment{{PlacementIndex: 0, Text: "c0", Bytes: 2}, {PlacementIndex: 0, Text: "c0dup", Bytes: 5}},
				Bytes:    7, SemanticDigest: computeSemanticDigest(orig.Reasoning), EgressPolicyHash: sha256.Sum256([]byte("eg")), AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
			},
		},
		{
			name: "missing semantic index",
			sur: &ReasoningSurrogate{
				OriginalDigest: orig.Anchor, PolicyRevision: "policy-v1", Sanitization: SanitizationNone,
				Segments: []SurrogateSegment{}, Bytes: 0,
				SemanticDigest: computeSemanticDigest(orig.Reasoning), EgressPolicyHash: sha256.Sum256([]byte("eg")), AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
			},
		},
		{
			name: "out of range index",
			sur: &ReasoningSurrogate{
				OriginalDigest: orig.Anchor, PolicyRevision: "policy-v1", Sanitization: SanitizationNone,
				Segments: []SurrogateSegment{{PlacementIndex: 99, Text: "c99", Bytes: 3}},
				Bytes:    3, SemanticDigest: computeSemanticDigest(orig.Reasoning), EgressPolicyHash: sha256.Sum256([]byte("eg")), AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
			},
		},
		{
			name: "exact mismatch",
			sur: func() *ReasoningSurrogate {
				// Original now has exact at index1, but surrogate claims it
				sem2 := ephemeralSemanticPart(strings.Repeat("a", 10))
				exact := ephemeralExactPartWithSignature("exact", "sig")
				orig2 := TurnArtifact{ID: "amb-2", Anchor: sha256.Sum256([]byte("anchor-amb2")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: sem2}, {BeforeNonReasoningPart: 1, Part: exact}}}
				return &ReasoningSurrogate{
					OriginalDigest: orig2.Anchor, PolicyRevision: "policy-v1", Sanitization: SanitizationNone,
					Segments: []SurrogateSegment{{PlacementIndex: 1, Text: "should-fallback", Bytes: 15}},
					Bytes:    15, SemanticDigest: computeSemanticDigest(orig2.Reasoning), EgressPolicyHash: sha256.Sum256([]byte("eg")), AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
				}
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var o TurnArtifact
			if tc.name == "exact mismatch" {
				sem2 := ephemeralSemanticPart(strings.Repeat("a", 10))
				exact := ephemeralExactPartWithSignature("exact", "sig")
				o = TurnArtifact{ID: "amb-2", Anchor: sha256.Sum256([]byte("anchor-amb2")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: sem2}, {BeforeNonReasoningPart: 1, Part: exact}}}
			} else {
				o = orig
			}
			eph := BuildEphemeralArtifact(o, tc.sur)
			// Fallback => entire artifact original text preserved
			if eph.Reasoning[0].Part.Reasoning.Text != o.Reasoning[0].Part.Reasoning.Text {
				t.Fatalf("fallback should preserve original text, got %q want %q", eph.Reasoning[0].Part.Reasoning.Text, o.Reasoning[0].Part.Reasoning.Text)
			}
			// Ensure not surrogated
			if len(tc.sur.Segments) > 0 {
				for _, seg := range tc.sur.Segments {
					if seg.PlacementIndex >= 0 && seg.PlacementIndex < len(eph.Reasoning) {
						if eph.Reasoning[seg.PlacementIndex].Part.Reasoning.Text == seg.Text && tc.name != "exact mismatch" {
							// For duplicate/missing/out-of-range cases, fallback must not have sur text
							// Duplicate case has seg 0 with c0, but fallback should keep original, not c0
							if tc.name == "duplicate index" && eph.Reasoning[0].Part.Reasoning.Text == "c0" {
								t.Fatalf("duplicate should fallback, but got surrogate")
							}
						}
					}
				}
			}
		})
	}
}

func TestBuildEphemeralArtifact_MultiArtifactDecisions(t *testing.T) {
	t.Parallel()
	cfg := testEphemeralConfig(CompressionActive)
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-multi")
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor1, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	anchor2, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible2")}})
	pr1 := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("a", 20))}
	pr2 := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("b", 20))}
	art1 := TurnArtifact{ID: "multi-1", Anchor: anchor1, Reasoning: []PlacedReasoning{pr1}, CreatedAt: time.Now(), ReasoningBytes: 20}
	art2 := TurnArtifact{ID: "multi-2", Anchor: anchor2, Reasoning: []PlacedReasoning{pr2}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art1)
	_, _ = store.Append(context.Background(), partition, art2)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snap1, snap2 TurnArtifact
	for _, a := range snap {
		if a.ID == "multi-1" {
			snap1 = a
		}
		if a.ID == "multi-2" {
			snap2 = a
		}
	}
	// Only attach surrogate for art1
	sur1 := validSurrogateFor(cfg, snap1)
	cs := store.(CompressionStore)
	semDigest := computeSemanticDigest(snap1.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, snap1.ID, snap1.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, "job-multi-1")
	_ = cs.AttachSurrogate(context.Background(), claim, "job-multi-1", *sur1)
	// art2 remains without surrogate
	decisions := map[string]ReasoningViewResult{
		snap1.ID: {Kind: ViewSurrogate, Reason: "eligible"},
		snap2.ID: {Kind: ViewOriginal, Reason: "no_surrogate"},
	}
	snapAll, _ := store.Snapshot(context.Background(), partition)
	eph := BuildEphemeralArtifacts(snapAll, decisions, func(id string) (*ReasoningSurrogate, bool) {
		st, ok, _ := cs.GetCompressionState(context.Background(), partition, id)
		if !ok || st.Surrogate == nil {
			return nil, false
		}
		return st.Surrogate, true
	})
	// Verify art1 surrogated, art2 original
	for _, a := range eph {
		if a.ID == "multi-1" {
			if a.Reasoning[0].Part.Reasoning.Text != "compressed" {
				t.Fatalf("multi-1 should be surrogated, got %q", a.Reasoning[0].Part.Reasoning.Text)
			}
		}
		if a.ID == "multi-2" {
			if a.Reasoning[0].Part.Reasoning.Text != strings.Repeat("b", 20) {
				t.Fatalf("multi-2 should remain original, got %q", a.Reasoning[0].Part.Reasoning.Text)
			}
		}
	}
}

func TestBuildEphemeralArtifact_ToolsOrdinaryCallUnchanged(t *testing.T) {
	t.Parallel()
	cfg := testEphemeralConfig(CompressionActive)
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-tools")
	// Build anchor from the exact non-reasoning parts the call will have (visible + file + image)
	filePart := aipapi.FilePart("file-ref", "application/pdf", "doc.pdf")
	imagePart := aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: "img-ref", ImageMIME: "image/png"}
	anchorParts := []aipapi.Part{aipapi.TextPart("visible answer"), filePart, imagePart}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: anchorParts})
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "tools-1", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == "tools-1" {
			snapArt = a
			break
		}
	}
	sur := validSurrogateFor(cfg, snapArt)
	cs := store.(CompressionStore)
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, "job-tools")
	_ = cs.AttachSurrogate(context.Background(), claim, "job-tools", *sur)
	// Build call with tools, files, media, ordinary text
	toolDef := aipapi.ToolDef{Name: "my_tool", Description: "desc", Parameters: json.RawMessage(`{"type":"object"}`)}
	// Call is missing reasoning, but has tools and visible text
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible answer"), filePart, imagePart}}},
		Tools:    []aipapi.ToolDef{toolDef},
	}
	cfgActive := Config{
		Action: ActionRestore, OnUnrepresentable: PolicyReject, OnStateError: PolicyLogSkip, OnAmbiguous: PolicyLogSkip,
		State:       StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:       []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression: cfg,
	}
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgActive, store, CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}, CompanionPolicy{}, nil)
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-tools"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	origTools := append([]aipapi.ToolDef(nil), call.Tools...)
	origParts := append([]aipapi.Part(nil), call.Messages[0].Parts...)
	_, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	// Tools unchanged
	if !reflect.DeepEqual(origTools, call.Tools) {
		t.Fatalf("tools changed: got %+v want %+v", call.Tools, origTools)
	}
	// Ordinary text/file/image preserved
	foundVisible := false
	foundFile := false
	foundImage := false
	for _, p := range call.Messages[0].Parts {
		if p.Kind == aipapi.PartText && p.Text == "visible answer" {
			foundVisible = true
		}
		if p.Kind == aipapi.PartFileRef && p.FileRef == "file-ref" {
			foundFile = true
		}
		if p.Kind == aipapi.PartImageRef && p.ImageRef == "img-ref" {
			foundImage = true
		}
	}
	if !foundVisible || !foundFile || !foundImage {
		t.Fatalf("ordinary parts not preserved: visible %v file %v image %v parts %+v", foundVisible, foundFile, foundImage, call.Messages[0].Parts)
	}
	// Reasoning should be surrogate text, not original
	foundSurrogate := false
	for _, p := range call.Messages[0].Parts {
		if p.Kind == aipapi.PartReasoning && p.Reasoning.Text == "compressed" {
			foundSurrogate = true
		}
	}
	if !foundSurrogate {
		t.Fatalf("surrogate reasoning not found in call parts %+v", call.Messages[0].Parts)
	}
	// Ordinary call with no missing (client preserved) should not be overwritten
	preservedPart := ephemeralSemanticPart(strings.Repeat("a", 20))
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{preservedPart, aipapi.TextPart("visible answer"), filePart, imagePart}}}}
	_, err = xform.HandleAttempt(context.Background(), &call2, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle2: %v", err)
	}
	// Ensure client thought preserved, not overwritten with surrogate
	if call2.Messages[0].Parts[0].Reasoning.Text != strings.Repeat("a", 20) {
		t.Fatalf("client preserved should not be overwritten, got %q", call2.Messages[0].Parts[0].Reasoning.Text)
	}
	// No surrogate should have been injected (still original)
	for _, p := range call2.Messages[0].Parts {
		if p.Kind == aipapi.PartReasoning && p.Reasoning.Text == "compressed" {
			t.Fatalf("preserved call should not get surrogate")
		}
	}
	_ = origParts
}

func TestBuildEphemeralArtifact_Idempotence(t *testing.T) {
	t.Parallel()
	sem := ephemeralSemanticPart(strings.Repeat("a", 20))
	orig := TurnArtifact{ID: "idem-1", Anchor: sha256.Sum256([]byte("anchor-idem")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: sem}}}
	sur := &ReasoningSurrogate{
		OriginalDigest: orig.Anchor, PolicyRevision: "policy-v1", Sanitization: SanitizationNone,
		Segments: []SurrogateSegment{{PlacementIndex: 0, Text: "compressed", Bytes: 10}},
		Bytes:    10, SemanticDigest: computeSemanticDigest(orig.Reasoning), EgressPolicyHash: sha256.Sum256([]byte("eg")), AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
	eph1 := BuildEphemeralArtifact(orig, sur)
	eph2 := BuildEphemeralArtifact(orig, sur)
	if !reflect.DeepEqual(eph1, eph2) {
		t.Fatalf("idempotence: first %+v second %+v", eph1, eph2)
	}
	// Building from ephemeral again should give same (since ephemeral's semantic still same but original classification would treat surrogate text as semantic)
	// However BuildEphemeralArtifact uses original classification, so second build from eph's artifact with same surrogate would still replace correctly but should be idempotent in terms of handle attempt.
	// Test handle idempotence via transform
	cfg := testEphemeralConfig(CompressionActive)
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-idem")
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "idem-store", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == "idem-store" {
			snapArt = a
			break
		}
	}
	sur2 := validSurrogateFor(cfg, snapArt)
	cs := store.(CompressionStore)
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, "job-idem")
	_ = cs.AttachSurrogate(context.Background(), claim, "job-idem", *sur2)
	cfgActive := Config{
		Action: ActionRestore, OnUnrepresentable: PolicyReject, OnStateError: PolicyLogSkip, OnAmbiguous: PolicyLogSkip,
		State:       StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:       []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression: cfg,
	}
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgActive, store, CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}, CompanionPolicy{}, nil)
	call1 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-idem"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	_, err := xform.HandleAttempt(context.Background(), &call1, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle1: %v", err)
	}
	// Clone call1 to call2 for second attempt with same missing input (fresh call missing reasoning)
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	_, err = xform.HandleAttempt(context.Background(), &call2, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle2: %v", err)
	}
	if !reflect.DeepEqual(call1.Messages, call2.Messages) {
		t.Fatalf("idempotent handle: first %+v second %+v", call1.Messages, call2.Messages)
	}
	// Also verify that calling BuildEphemeralArtifacts twice yields same
	decisions := map[string]ReasoningViewResult{snapArt.ID: {Kind: ViewSurrogate}}
	ephA1 := BuildEphemeralArtifacts(snap, decisions, func(id string) (*ReasoningSurrogate, bool) {
		st, ok, _ := cs.GetCompressionState(context.Background(), partition, id)
		if !ok {
			return nil, false
		}
		return st.Surrogate, true
	})
	ephA2 := BuildEphemeralArtifacts(snap, decisions, func(id string) (*ReasoningSurrogate, bool) {
		st, ok, _ := cs.GetCompressionState(context.Background(), partition, id)
		if !ok {
			return nil, false
		}
		return st.Surrogate, true
	})
	if !reflect.DeepEqual(ephA1, ephA2) {
		t.Fatalf("ephemeral artifacts idempotence failed")
	}
}

func TestEphemeralView_ShadowAlwaysOriginal(t *testing.T) {
	t.Parallel()
	cfg := testEphemeralConfig(CompressionShadow)
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-shadow-ephemeral")
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: ephemeralSemanticPart(strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "shadow-1", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == "shadow-1" {
			snapArt = a
			break
		}
	}
	sur := validSurrogateFor(cfg, snapArt)
	cs := store.(CompressionStore)
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, "job-shadow")
	_ = cs.AttachSurrogate(context.Background(), claim, "job-shadow", *sur)
	cfgShadow := Config{
		Action: ActionRestore, OnUnrepresentable: PolicyReject, OnStateError: PolicyLogSkip, OnAmbiguous: PolicyLogSkip,
		State:       StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:       []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression: cfg,
	}
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgShadow, store, CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}, CompanionPolicy{}, nil)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-shadow-ephemeral"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	_, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle shadow: %v", err)
	}
	// Shadow must not contain surrogated text "compressed"
	for _, p := range call.Messages[0].Parts {
		if p.Reasoning != nil && p.Reasoning.Text == "compressed" {
			t.Fatalf("shadow must remain original, got surrogate")
		}
	}
	foundOriginal := false
	for _, p := range call.Messages[0].Parts {
		if p.Reasoning != nil && p.Reasoning.Text == strings.Repeat("a", 20) {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Fatalf("shadow should have original reasoning, got %+v", call.Messages[0].Parts)
	}
}

func TestBundle_EphemeralConsumerWiring(t *testing.T) {
	t.Parallel()
	shadowCfg := Config{
		Action:            ActionRestore,
		OnAmbiguous:       PolicyLogSkip,
		OnUnrepresentable: PolicyReject,
		OnStateError:      PolicyLogSkip,
		State:             StateConfig{TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024, MaxSessionBytes: 4096},
		Rules:             []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression:       testEphemeralConfig(CompressionShadow),
	}
	svc := CompressionServices{EgressPolicy: fakeAllowPolicy{version: "policy-v1"}, Client: &fakeBgClientForBundle{}, Poller: &fakePollerForBundle{}, Sanitizer: fakeSan{}}
	partsShadow, _, err := FeatureBundleWithPartsAndCompression(shadowCfg, svc, CompanionPolicy{})
	if err != nil {
		t.Fatalf("shadow bundle: %v", err)
	}
	shadowConsumer := partsShadow.Transform
	_ = shadowConsumer
	activeCfg := Config{
		Action:            ActionRestore,
		OnAmbiguous:       PolicyLogSkip,
		OnUnrepresentable: PolicyReject,
		OnStateError:      PolicyLogSkip,
		State:             StateConfig{TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024, MaxSessionBytes: 4096},
		Rules:             []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression:       testEphemeralConfig(CompressionActive),
	}
	partsActive, _, err := FeatureBundleWithPartsAndCompression(activeCfg, svc, CompanionPolicy{})
	if err != nil {
		t.Fatalf("active bundle: %v", err)
	}
	_ = partsActive
	if partsShadow.Transform == nil || partsActive.Transform == nil {
		t.Fatalf("transform nil")
	}
}

// fakes for bundle test

type fakeBgClientForBundle struct{}

func (f *fakeBgClientForBundle) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "job", nil
}

func (f *fakeBgClientForBundle) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *fakeBgClientForBundle) Forget(id auxiliary.JobID) {}

type fakePollerForBundle struct{}

func (f *fakePollerForBundle) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
}
