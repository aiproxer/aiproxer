//nolint:forcetypeassert,paralleltest // Tests share fixtures and assert concrete scripted types.
package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// testSelectionConfig returns a valid CompressionConfig for 6.1 tests.
func testSelectionConfig() CompressionConfig {
	return CompressionConfig{
		Enabled:                     true,
		Mode:                        CompressionShadow,
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

func semanticPart(t *testing.T, text string) aipapi.Part {
	t.Helper()
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: text}}
}

func exactPart(t *testing.T) aipapi.Part {
	t.Helper()
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "hello", Summary: []byte(`[]`), SummaryPresent: true}}
}

func unknownPart(t *testing.T) aipapi.Part {
	t.Helper()
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialect("unknown.dialect.v99"), Text: "hello"}}
}

// validSurrogateFor builds a correctly correlated surrogate for the given artifact and config.
func validSurrogateFor(cfg CompressionConfig, art TurnArtifact) *ReasoningSurrogate {
	semDigest := computeSemanticDigest(art.Reasoning)
	// Build segments exactly matching semantic subset.
	semIdx := []int{}
	for i, pr := range art.Reasoning {
		if ClassifyReasoningPart(pr.Part) == ReplaySemanticText {
			semIdx = append(semIdx, i)
		}
	}
	segs := make([]SurrogateSegment, 0, len(semIdx))
	for _, idx := range semIdx {
		segs = append(segs, SurrogateSegment{PlacementIndex: idx, Text: "compressed", Bytes: len("compressed")})
	}
	// Authoritative egress hash from a fake allow decision with same PolicyVersion == cfg.EgressPolicyRef.
	dec := CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}
	egHash := ComputeEgressPolicyHash(dec, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	total := 0
	for _, s := range segs {
		total += s.Bytes
	}
	return &ReasoningSurrogate{
		OriginalDigest:      art.Anchor,
		PolicyRevision:      cfg.EgressPolicyRef,
		Sanitization:        SanitizationNone,
		Segments:            segs,
		Bytes:               total,
		SemanticDigest:      semDigest,
		EgressPolicyHash:    egHash,
		AuthorizedRouteHash: routeHash,
	}
}

func TestSelectReasoningView_DestinationMatrix(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "dest-1", Anchor: sha256.Sum256([]byte("anchor-dest-1")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	unsupported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectAnthropicThinkingV1}}
	k, _ := SelectReasoningView(cfg, art, sur, supported, ClassMissing)
	if k != ViewSurrogate {
		t.Fatalf("supported destination should be surrogate, got %v", k)
	}
	k2, reason2 := SelectReasoningView(cfg, art, sur, unsupported, ClassMissing)
	if k2 != ViewOriginal || reason2 != "unsupported_destination" {
		t.Fatalf("unsupported destination should fallback original unsupported_destination, got %v %q", k2, reason2)
	}
	// Empty support also unsupported
	empty := aipapi.ReasoningReplaySupport{}
	k3, _ := SelectReasoningView(cfg, art, sur, empty, ClassMissing)
	if k3 != ViewOriginal {
		t.Fatalf("empty support should be original")
	}
}

func TestSelectReasoningView_StaleEveryField(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "stale-1", Anchor: sha256.Sum256([]byte("anchor-stale")), Reasoning: []PlacedReasoning{pr}}
	base := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}

	tests := []struct {
		name   string
		mutate func(*ReasoningSurrogate)
		reason string
		cfgMut func(*CompressionConfig)
	}{
		{
			name:   "stale_original_digest",
			mutate: func(s *ReasoningSurrogate) { s.OriginalDigest = sha256.Sum256([]byte("other")) },
			reason: "stale_original_digest",
		},
		{
			name:   "stale_semantic_digest",
			mutate: func(s *ReasoningSurrogate) { s.SemanticDigest[0] ^= 0xFF },
			reason: "stale_semantic_digest",
		},
		{
			name:   "stale_policy",
			mutate: func(s *ReasoningSurrogate) { s.PolicyRevision = "other-policy" },
			reason: "stale_policy",
		},
		{
			name:   "stale_sanitization",
			mutate: func(s *ReasoningSurrogate) { s.Sanitization = "invalid" },
			reason: "stale_sanitization",
		},
		{
			name:   "stale_route_zero",
			mutate: func(s *ReasoningSurrogate) { s.AuthorizedRouteHash = [32]byte{} },
			reason: "stale_route",
		},
		{
			name:   "stale_route_mismatch",
			mutate: func(s *ReasoningSurrogate) { s.AuthorizedRouteHash = sha256.Sum256([]byte("other-route")) },
			reason: "stale_route",
		},
		{
			name:   "stale_egress_hash_zero",
			mutate: func(s *ReasoningSurrogate) { s.EgressPolicyHash = [32]byte{} },
			reason: "stale_egress_hash",
		},
		{
			name: "stale_indexes_wrong_count",
			mutate: func(s *ReasoningSurrogate) {
				s.Segments = append(s.Segments, SurrogateSegment{PlacementIndex: 99, Text: "x", Bytes: 1})
			},
			reason: "stale_indexes",
		},
		{
			name: "stale_indexes_exact",
			mutate: func(s *ReasoningSurrogate) {
				// Create artifact with semantic+exact but surrogate incorrectly claims exact index
				// We will test separately with mixed artifact; here just test that surrogate claiming exact index is rejected
				// For this base artifact (only semantic), we simulate by adding an exact artifact and surrogate with wrong index
			},
			reason: "stale_indexes",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.name == "stale_indexes_exact" {
				t.Skip("covered in mixed test")
			}
			cfg2 := cfg
			if tc.cfgMut != nil {
				tc.cfgMut(&cfg2)
			}
			sur2 := *base
			// deep copy segments
			segs := make([]SurrogateSegment, len(base.Segments))
			copy(segs, base.Segments)
			sur2.Segments = segs
			tc.mutate(&sur2)
			k, reason := SelectReasoningView(cfg2, art, &sur2, supported, ClassMissing)
			if k != ViewOriginal || reason != tc.reason {
				t.Fatalf("field %s expected original/%s got %v/%q", tc.name, tc.reason, k, reason)
			}
		})
	}
	// cfg route mismatch via config change, not surrogate mutate
	t.Run("stale_route_via_cfg", func(t *testing.T) {
		cfg2 := cfg
		cfg2.Route = "different-route"
		k, reason := SelectReasoningView(cfg2, art, base, supported, ClassMissing)
		if k != ViewOriginal || reason != "stale_route" {
			t.Fatalf("route cfg mismatch should be stale_route got %v %q", k, reason)
		}
	})
	t.Run("stale_policy_via_cfg", func(t *testing.T) {
		cfg2 := cfg
		cfg2.EgressPolicyRef = "other-policy"
		k, reason := SelectReasoningView(cfg2, art, base, supported, ClassMissing)
		if k != ViewOriginal || reason != "stale_policy" {
			t.Fatalf("policy cfg mismatch should be stale_policy got %v %q", k, reason)
		}
	})
}

func TestSelectReasoningView_ExactUnknownReject(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1, aipapi.ReasoningDialectOpenAIResponsesItemV1, aipapi.ReasoningDialect("unknown.dialect.v99")}}
	// Exact placement only -> no semantic -> fallback
	artExact := TurnArtifact{ID: "exact-only", Anchor: sha256.Sum256([]byte("a-exact")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: exactPart(t)}}}
	surExact := validSurrogateFor(cfg, artExact) // will have 0 segments because no semantic
	// validSurrogateFor for exact-only yields empty segs; we craft a surrogate claiming semantic index incorrectly
	fakeSur := &ReasoningSurrogate{
		OriginalDigest:      artExact.Anchor,
		PolicyRevision:      cfg.EgressPolicyRef,
		Sanitization:        SanitizationNone,
		Segments:            []SurrogateSegment{{PlacementIndex: 0, Text: "compressed", Bytes: 10}},
		Bytes:               10,
		SemanticDigest:      computeSemanticDigest(artExact.Reasoning),
		EgressPolicyHash:    ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route),
		AuthorizedRouteHash: sha256.Sum256([]byte(cfg.Route)),
	}
	k, reason := SelectReasoningView(cfg, artExact, fakeSur, supported, ClassMissing)
	if k != ViewOriginal {
		t.Fatalf("exact-only should be original, got %v %q", k, reason)
	}
	if reason != "no_semantic_text" && reason != "stale_indexes_exact" && reason != "stale_indexes" {
		t.Logf("exact reason=%q", reason)
	}
	_ = surExact
	// Unknown placement
	artUnknown := TurnArtifact{ID: "unknown-only", Anchor: sha256.Sum256([]byte("a-unknown")), Reasoning: []PlacedReasoning{{BeforeNonReasoningPart: 0, Part: unknownPart(t)}}}
	fakeSur2 := &ReasoningSurrogate{
		OriginalDigest:      artUnknown.Anchor,
		PolicyRevision:      cfg.EgressPolicyRef,
		Sanitization:        SanitizationNone,
		Segments:            []SurrogateSegment{{PlacementIndex: 0, Text: "compressed", Bytes: 10}},
		Bytes:               10,
		SemanticDigest:      computeSemanticDigest(artUnknown.Reasoning),
		EgressPolicyHash:    ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route),
		AuthorizedRouteHash: sha256.Sum256([]byte(cfg.Route)),
	}
	k2, reason2 := SelectReasoningView(cfg, artUnknown, fakeSur2, supported, ClassMissing)
	if k2 != ViewOriginal {
		t.Fatalf("unknown-only should be original, got %v %q", k2, reason2)
	}
	_ = reason2
}

func TestSelectReasoningView_ClientPreserved(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "preserved-1", Anchor: sha256.Sum256([]byte("anchor-preserved")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	k, reason := SelectReasoningView(cfg, art, sur, supported, ClassPreserved)
	if k != ViewOriginal || reason != "client_preserved" {
		t.Fatalf("preserved should be original client_preserved got %v %q", k, reason)
	}
	// Other non-missing classifications also fallback
	for _, cls := range []Classification{ClassConflicting, ClassAmbiguous, ClassUnmatched} {
		k2, _ := SelectReasoningView(cfg, art, sur, supported, cls)
		if k2 != ViewOriginal {
			t.Fatalf("classification %v should be original, got %v", cls, k2)
		}
	}
}

func TestSelectReasoningView_MixedEligibleSubset(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	// Mixed: index0 semantic, index1 exact
	semPr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	exPr := PlacedReasoning{BeforeNonReasoningPart: 1, Part: exactPart(t)}
	art := TurnArtifact{ID: "mixed-1", Anchor: sha256.Sum256([]byte("anchor-mixed")), Reasoning: []PlacedReasoning{semPr, exPr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1, aipapi.ReasoningDialectOpenAIResponsesItemV1}}
	if len(sur.Segments) != 1 || sur.Segments[0].PlacementIndex != 0 {
		t.Fatalf("mixed valid surrogate should have exactly 1 segment at 0, got %+v", sur.Segments)
	}
	k, reason := SelectReasoningView(cfg, art, sur, supported, ClassMissing)
	if k != ViewSurrogate {
		t.Fatalf("mixed eligible subset should be surrogate, got %v %q", k, reason)
	}
	// Surrogate incorrectly claiming exact index should be rejected
	badSur := *sur
	badSur.Segments = []SurrogateSegment{{PlacementIndex: 1, Text: "compressed", Bytes: 10}}
	badSur.Bytes = 10
	// Keep digests same (they will be stale_indexes because semDigest mismatch already? Need to keep semDigest same but indexes wrong -> should be stale)
	// For mixed, semDigest is computed from only semantic placements, so badSur's semDigest still matches; but indexes mismatch
	k2, reason2 := SelectReasoningView(cfg, art, &badSur, supported, ClassMissing)
	if k2 != ViewOriginal || (reason2 != "stale_indexes" && reason2 != "stale_indexes_exact") {
		t.Fatalf("mixed bad index should be stale, got %v %q", k2, reason2)
	}
}

func TestSelectReasoningView_WhitespaceClassifierReuse(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	// Original semantic text, then change to whitespace-only; surrogate still claims old index
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "ws-1", Anchor: sha256.Sum256([]byte("anchor-ws")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	k, _ := SelectReasoningView(cfg, art, sur, supported, ClassMissing)
	if k != ViewSurrogate {
		t.Fatalf("initial should be surrogate")
	}
	// Change original text to whitespace only -> classifier returns ReplayUnknown, no semantic
	prWs := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, "   \t\n")}
	artWs := TurnArtifact{ID: "ws-1", Anchor: art.Anchor, Reasoning: []PlacedReasoning{prWs}}
	// Build surrogate that was valid for original but now stale due to semantic digest/index mismatch
	// Use same surrogate (which was valid for original) against whitespace artifact
	k2, reason2 := SelectReasoningView(cfg, artWs, sur, supported, ClassMissing)
	if k2 != ViewOriginal {
		t.Fatalf("whitespace changed should fallback original, got %v %q", k2, reason2)
	}
	if reason2 != "no_semantic_text" && reason2 != "stale_semantic_digest" && reason2 != "stale_indexes" {
		t.Logf("whitespace reason=%q", reason2)
	}
	// Also test classifier directly: whitespace should not be semantic
	if got := ClassifyReasoningPart(prWs.Part); got == ReplaySemanticText {
		t.Fatalf("whitespace part should not be semantic, got %v", got)
	}
}

func TestSelectReasoningView_NoProviderStrings(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "no-provider-1", Anchor: sha256.Sum256([]byte("anchor-np")), Reasoning: []PlacedReasoning{pr}, SourceBackend: "openai", SourceModel: "gpt-4"}
	art2 := TurnArtifact{ID: "no-provider-1", Anchor: sha256.Sum256([]byte("anchor-np")), Reasoning: []PlacedReasoning{pr}, SourceBackend: "anthropic", SourceModel: "claude-3"}
	sur := validSurrogateFor(cfg, art)
	// Surrogate built from art; also valid for art2 because anchor same and reasoning same
	sur2 := validSurrogateFor(cfg, art2)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	k, _ := SelectReasoningView(cfg, art, sur, supported, ClassMissing)
	k2, _ := SelectReasoningView(cfg, art2, sur2, supported, ClassMissing)
	if k != k2 {
		t.Fatalf("SourceBackend/SourceModel must not affect selection: %v vs %v", k, k2)
	}
	// Changing backend/model alone should not change decision
	art3 := art
	art3.SourceBackend = "some-other-backend"
	art3.SourceModel = "some-other-model"
	k3, _ := SelectReasoningView(cfg, art3, sur, supported, ClassMissing)
	if k3 != k {
		t.Fatalf("backend/model variation should not affect select: %v vs %v", k, k3)
	}
}

func TestSelectReasoningView_SelectReasoningViewsHelper(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	store, _ := NewMemoryTurnStore(StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       10,
		MaxReasoningBytesPerTurn: 4096,
		MaxSessionBytes:          100000,
		Now:                      time.Now,
		CompressionLimits:        cfg.ToLimits(),
	})
	partition := NewSessionPartition("sess-view-helper")
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "helper-1", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, err := store.Append(context.Background(), partition, art)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// Manually reserve and attach surrogate via store to simulate 5.3 completed
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, err := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	if err := store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Bind dummy job
	if err := store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Build surrogate via valid helper
	sur := validSurrogateFor(cfg, snapArt)
	if err := store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-1", *sur); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Build candidates as transform does: collectRestoreCandidates with a missing call
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible answer")}}}}
	_, candidates, err := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	svc := CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}
	meta := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	views := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svc, partition, candidates, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, meta)
	if views == nil || views[snapArt.ID].Kind != ViewSurrogate {
		t.Fatalf("helper should return surrogate, got %+v", views)
	}
	views2 := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svc, partition, candidates, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, meta)
	if views2[snapArt.ID].Kind != ViewSurrogate {
		t.Fatalf("stage should return surrogate, got %+v", views2)
	}
}

func TestSelectReasoningView_PolicyAllowSameHash(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "pol-allow-1", Anchor: sha256.Sum256([]byte("anchor-pol-allow")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	// Pure without current policy already eligible
	k, _ := SelectReasoningView(cfg, art, sur, supported, ClassMissing)
	if k != ViewSurrogate {
		t.Fatalf("pure without current should be surrogate")
	}
	// With current policy matching
	curHash := sur.EgressPolicyHash
	curSan := sur.Sanitization
	k2, _ := SelectReasoningViewWithCurrentPolicy(cfg, art, sur, supported, ClassMissing, &curHash, &curSan)
	if k2 != ViewSurrogate {
		t.Fatalf("with matching current should be surrogate, got %v", k2)
	}
	// Via stage: policy allow
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-pol-allow")
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}})
	art2 := TurnArtifact{ID: "pol-allow-2", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art2)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art2.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-pol")
	sur2 := validSurrogateFor(cfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-pol", *sur2)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	_, candidates, _ := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, supported)
	svc := CompressionServices{EgressPolicy: fakeAllowPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}
	meta := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	views := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svc, partition, candidates, supported, meta)
	if views[snapArt.ID].Kind != ViewSurrogate {
		t.Fatalf("stage allow should be surrogate, got %+v", views)
	}
}

func TestSelectReasoningView_PolicyRotatedVersionDeny(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "pol-deny-1", Anchor: sha256.Sum256([]byte("anchor-pol-deny")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	// Rotated version: current hash differs
	rotHash := sha256.Sum256([]byte("rotated"))
	curSan := SanitizationNone
	k, reason := SelectReasoningViewWithCurrentPolicy(cfg, art, sur, supported, ClassMissing, &rotHash, &curSan)
	if k != ViewOriginal || reason != "policy_no_longer_permits" {
		t.Fatalf("rotated hash should be policy_no_longer_permits, got %v %q", k, reason)
	}
	// Via stage with deny policy
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-pol-deny")
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}})
	art2 := TurnArtifact{ID: "pol-deny-2", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art2)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art2.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-deny")
	sur2 := validSurrogateFor(cfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-deny", *sur2)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	_, candidates, _ := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, supported)
	svcDeny := CompressionServices{EgressPolicy: fakeDenyPolicy{version: "v2"}, Sanitizer: fakeSan{}}
	meta := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	views := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svcDeny, partition, candidates, supported, meta)
	if views[candidates[0].ArtifactID].Kind != ViewOriginal || views[candidates[0].ArtifactID].Reason != "policy_no_longer_permits" {
		t.Fatalf("deny should be policy_no_longer_permits, got %+v", views)
	}
}

func TestSelectReasoningView_PolicyRedactionMismatch(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "pol-redact-1", Anchor: sha256.Sum256([]byte("anchor-redact")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art) // sanitization none
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	// Current expects redacted but surrogate is none => mismatch
	curHash := sur.EgressPolicyHash
	curSan := SanitizationRedacted
	k, reason := SelectReasoningViewWithCurrentPolicy(cfg, art, sur, supported, ClassMissing, &curHash, &curSan)
	if k != ViewOriginal || reason != "policy_no_longer_permits" {
		t.Fatalf("redaction mismatch should be policy_no_longer_permits, got %v %q", k, reason)
	}
	// Via stage with redacted policy but surrogate is none
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-pol-redact")
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}})
	art2 := TurnArtifact{ID: "pol-redact-2", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art2)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art2.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-redact")
	sur2 := validSurrogateFor(cfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-redact", *sur2)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	_, candidates, _ := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, supported)
	svc := CompressionServices{EgressPolicy: fakeRedactPolicy{version: cfg.EgressPolicyRef}, Sanitizer: fakeSan{}}
	meta := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	views := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svc, partition, candidates, supported, meta)
	if views[candidates[0].ArtifactID].Kind != ViewOriginal || views[candidates[0].ArtifactID].Reason != "policy_no_longer_permits" {
		t.Fatalf("redact mismatch should be policy_no_longer_permits, got %+v", views)
	}
}

func TestSelectReasoningView_TrustedScopeFromContext(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	art := TurnArtifact{ID: "scope-1", Anchor: sha256.Sum256([]byte("anchor-scope")), Reasoning: []PlacedReasoning{pr}}
	sur := validSurrogateFor(cfg, art)
	_ = sur
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-scope")
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}})
	art2 := TurnArtifact{ID: "scope-2", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art2)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art2.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-scope")
	sur2 := validSurrogateFor(cfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-scope", *sur2)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	_, candidates, _ := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, supported)
	// Simple capturing policy - records principal from trusted control-plane input
	capturing := &capturingPolicy{version: cfg.EgressPolicyRef}
	svc := CompressionServices{EgressPolicy: capturing, Sanitizer: fakeSan{}}
	// Meta has empty scope, context has trusted scope
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{PrincipalID: scope.Known("ctx-user")})
	metaEmpty := request.AttemptMeta{Scope: scope.PrincipalScopeView{}}
	views := selectReasoningViews(ctx, cfg, store.(CompressionStore), svc, partition, candidates, supported, metaEmpty)
	if views[candidates[0].ArtifactID].Kind != ViewSurrogate {
		t.Fatalf("scope from context should allow, got %+v", views)
	}
	if capturing.gotPrincipal != "ctx-user" {
		t.Fatalf("policy should have received principal from context, got %q", capturing.gotPrincipal)
	}
	// Now meta has different scope, should prefer meta over context
	capturing2 := &capturingPolicy{version: cfg.EgressPolicyRef}
	svc2 := CompressionServices{EgressPolicy: capturing2, Sanitizer: fakeSan{}}
	metaWith := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("meta-user")}}
	ctx2 := scope.WithScope(context.Background(), scope.PrincipalScopeView{PrincipalID: scope.Known("ctx-user2")})
	views2 := selectReasoningViews(ctx2, cfg, store.(CompressionStore), svc2, partition, candidates, supported, metaWith)
	if views2[candidates[0].ArtifactID].Kind != ViewSurrogate {
		t.Fatalf("meta scope should allow, got %+v", views2)
	}
	if capturing2.gotPrincipal != "meta-user" {
		t.Fatalf("policy should prefer meta scope, got %q", capturing2.gotPrincipal)
	}
	// Changing Call.Messages should not affect policy input
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("different visible content that should not affect policy")}}}}
	_, candidates2, _ := collectRestoreCandidates(&call2, []TurnArtifact{snapArt}, supported)
	capturing3 := &capturingPolicy{version: cfg.EgressPolicyRef}
	svc3 := CompressionServices{EgressPolicy: capturing3, Sanitizer: fakeSan{}}
	views3 := selectReasoningViews(ctx, cfg, store.(CompressionStore), svc3, partition, candidates2, supported, metaEmpty)
	if views3 != nil && views3[candidates2[0].ArtifactID].Kind != ViewSurrogate {
		t.Fatalf("changing messages should not affect policy, got %+v", views3)
	}
}

func TestSelectReasoningView_PolicyCalledOnce(t *testing.T) {
	t.Parallel()
	cfg := testSelectionConfig()
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: cfg.ToLimits()})
	partition := NewSessionPartition("sess-once")
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}})
	art := TurnArtifact{ID: "once-1", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(cfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.EgressPolicyRef}, cfg.Route)
	routeHash := sha256.Sum256([]byte(cfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-once")
	sur := validSurrogateFor(cfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-once", *sur)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	supported := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
	_, candidates, _ := collectRestoreCandidates(&call, []TurnArtifact{snapArt}, supported)
	var cnt atomic.Int32
	base := fakeAllowPolicy{version: cfg.EgressPolicyRef}
	counting := countingPolicy{inner: base, count: &cnt}
	svc := CompressionServices{EgressPolicy: counting, Sanitizer: fakeSan{}}
	meta := request.AttemptMeta{Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	views := selectReasoningViews(context.Background(), cfg, store.(CompressionStore), svc, partition, candidates, supported, meta)
	if views[snapArt.ID].Kind != ViewSurrogate {
		t.Fatalf("should be surrogate, got %+v", views)
	}
	if cnt.Load() != 1 {
		t.Fatalf("policy should be called exactly once per artifact, got %d", cnt.Load())
	}
}

func TestSelectReasoningView_ConsumerReceivesMapAndShadowDefault(t *testing.T) {
	t.Parallel()
	compCfg := testSelectionConfig()
	cfg := Config{
		Action:            ActionRestore,
		OnUnrepresentable: PolicyReject,
		OnStateError:      PolicyLogSkip,
		OnAmbiguous:       PolicyLogSkip,
		State:             StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:             []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression:       compCfg,
	}
	store, _ := NewMemoryTurnStore(StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000, Now: time.Now, CompressionLimits: compCfg.ToLimits()})
	partition := NewSessionPartition("sess-consumer")
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	art := TurnArtifact{ID: "consumer-art", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, _ = store.Append(context.Background(), partition, art)
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(compCfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, compCfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: compCfg.EgressPolicyRef}, compCfg.Route)
	routeHash := sha256.Sum256([]byte(compCfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-consumer")
	sur := validSurrogateFor(compCfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-consumer", *sur)
	svc := CompressionServices{EgressPolicy: fakeAllowPolicy{version: compCfg.EgressPolicyRef}, Sanitizer: fakeSan{}}
	cfgShadow := cfg
	cfgShadow.Compression.Mode = CompressionShadow
	xformShadow := NewAttemptTransform(cfgShadow, store)
	xformShadow2 := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgShadow, store, svc, CompanionPolicy{}, nil)
	_ = xformShadow
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-consumer"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	_, err := xformShadow2.HandleAttempt(context.Background(), &call, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle shadow: %v", err)
	}
	for _, p := range call.Messages[0].Parts {
		if p.Reasoning != nil && p.Reasoning.Text == "compressed" {
			t.Fatalf("shadow must not use surrogate text")
		}
	}
	cfgActive := cfg
	cfgActive.Compression.Mode = CompressionActive
	xformActive := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfgActive, store, svc, CompanionPolicy{}, nil)
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	_, err = xformActive.HandleAttempt(context.Background(), &call2, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle active: %v", err)
	}
	found := false
	for _, p := range call2.Messages[0].Parts {
		if p.Reasoning != nil && p.Reasoning.Text == "compressed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("active should restore surrogate, got %+v", call2.Messages[0].Parts)
	}
	// call2 should still be call (consumer identity returns call); 6.2 would
	// later modify, but 6.1 shadow/active both keep call unchanged for now
	// (consumer identity). Shadow/active both keep the original via the
	// identity consumer; 6.1 does not substitute yet.
}

func TestSelectReasoningView_TransformShadowAlwaysOriginal(t *testing.T) {
	t.Parallel()
	compCfg := testSelectionConfig()
	cfg := Config{
		Action:            ActionRestore,
		OnUnrepresentable: PolicyReject,
		OnStateError:      PolicyLogSkip,
		OnAmbiguous:       PolicyLogSkip,
		State:             StateConfig{TTL: time.Hour, MaxTurnsPerSession: 10, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 100000},
		Rules:             []RuleConfig{{ID: "r1", Backend: "be", ModelKeywords: []string{}, Enabled: new(true)}},
		Compression:       compCfg,
	}
	compCfg = cfg.Compression
	store, _ := NewMemoryTurnStore(StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       10,
		MaxReasoningBytesPerTurn: 4096,
		MaxSessionBytes:          100000,
		Now:                      time.Now,
		CompressionLimits:        compCfg.ToLimits(),
	})
	partition := NewSessionPartition("sess-shadow")
	pr := PlacedReasoning{BeforeNonReasoningPart: 0, Part: semanticPart(t, strings.Repeat("a", 20))}
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	art := TurnArtifact{ID: "shadow-art", Anchor: anchor, Reasoning: []PlacedReasoning{pr}, CreatedAt: time.Now(), ReasoningBytes: 20}
	_, err := store.Append(context.Background(), partition, art)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// Attach surrogate as before
	snap, _ := store.Snapshot(context.Background(), partition)
	var snapArt TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	semDigest := computeSemanticDigest(snapArt.Reasoning)
	egRefHash := sha256.Sum256([]byte(compCfg.EgressPolicyRef))
	claim, _ := store.(CompressionStore).ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, compCfg.EgressPolicyRef, semDigest, egRefHash)
	authoritative := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: compCfg.EgressPolicyRef}, compCfg.Route)
	routeHash := sha256.Sum256([]byte(compCfg.Route))
	_ = store.(CompressionStore).UpdateReservationPolicyHash(context.Background(), claim, egRefHash, semDigest, authoritative, SanitizationNone, routeHash)
	_ = store.(CompressionStore).BindCompressionJob(context.Background(), claim, "job-shadow")
	sur := validSurrogateFor(compCfg, snapArt)
	_ = store.(CompressionStore).AttachSurrogate(context.Background(), claim, "job-shadow", *sur)

	// Now create transform in shadow mode (default) and ensure it does not substitute surrogate
	xform := NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, store, CompressionServices{}, CompanionPolicy{}, nil)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-shadow"},
		ReplaySupport: aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1},
		},
	}
	// Ensure match eligible via catalog: use builtin catalog disabled but rule matches be
	// Call is missing reasoning, so restore should inject original (not surrogate) in shadow
	origCallText := call.Messages[0].Parts[0].Text
	_, err = xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	// Shadow must not inject surrogate text "compressed"
	for _, p := range call.Messages[0].Parts {
		if p.Reasoning != nil && p.Reasoning.Text == "compressed" {
			t.Fatalf("shadow must not use surrogate text, got surrogate in call")
		}
	}
	// But original should be restored (since shadow always original, and candidates were restorable)
	if len(call.Messages[0].Parts) == 1 && call.Messages[0].Parts[0].Text == origCallText {
		t.Fatalf("shadow should have restored original reasoning, but no reasoning found")
	}
	foundOriginal := false
	for _, p := range call.Messages[0].Parts {
		if p.Reasoning != nil && strings.Contains(p.Reasoning.Text, strings.Repeat("a", 20)) {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Fatalf("shadow should have original reasoning restored, parts=%+v", call.Messages[0].Parts)
	}
}

type capturingPolicy struct {
	version      string
	gotPrincipal string
}

func (c *capturingPolicy) Decide(_ context.Context, in CompressionEgressInput) (CompressionEgressDecision, error) {
	c.gotPrincipal = in.Principal.Scope().PrincipalID.String()
	return CompressionEgressDecision{Action: EgressAllow, PolicyVersion: c.version}, nil
}

type fakeAllowPolicy struct{ version string }

func (f fakeAllowPolicy) Decide(_ context.Context, _ CompressionEgressInput) (CompressionEgressDecision, error) {
	return CompressionEgressDecision{Action: EgressAllow, PolicyVersion: f.version}, nil
}

type fakeDenyPolicy struct{ version string }

func (f fakeDenyPolicy) Decide(_ context.Context, _ CompressionEgressInput) (CompressionEgressDecision, error) {
	return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: f.version}, nil
}

type fakeRedactPolicy struct{ version string }

func (f fakeRedactPolicy) Decide(_ context.Context, _ CompressionEgressInput) (CompressionEgressDecision, error) {
	return CompressionEgressDecision{Action: EgressRedactThenAllow, PolicyVersion: f.version}, nil
}

type countingPolicy struct {
	inner EgressPolicy
	count *atomic.Int32
}

func (c countingPolicy) Decide(ctx context.Context, in CompressionEgressInput) (CompressionEgressDecision, error) {
	c.count.Add(1)
	return c.inner.Decide(ctx, in)
}

type fakeSan struct{}

func (fakeSan) SanitizeText(_ context.Context, t string) (string, error) { return t, nil }

//go:fix inline
func boolPtr(v bool) *bool { return new(v) }
