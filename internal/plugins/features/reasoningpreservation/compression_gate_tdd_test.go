//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/require"
)

func gateConfigForMode(t *testing.T, mode string, enabled bool) reasoningpreservation.Config {
	t.Helper()
	if !enabled {
		return decodeValidConfig(t, `
action: restore
use_builtin_catalog: false
rules:
  - id: r1
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	}
	yamlBody := `
action: restore
use_builtin_catalog: false
rules:
  - id: r1
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
compression:
  enabled: true
  mode: ` + mode + `
  route: comp-route
  timeout: 5s
  max_input_tokens: 1000
  max_input_bytes: 10000
  max_output_tokens: 1000
  max_output_bytes: 4096
  max_surrogate_bytes: 1024
  min_source_bytes: 10
  min_saved_bytes: 5
  min_savings_ratio: 0.1
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 32768
  max_pending_total: 100
  max_surrogate_bytes_total: 262144
  egress_policy_ref: v1
`
	return decodeValidConfig(t, yamlBody)
}

func gateMakeArt(id string, anchor [32]byte, pr ...reasoningpreservation.PlacedReasoning) reasoningpreservation.TurnArtifact {
	a := turnArtifact(id, anchor, pr...)
	a.CreatedAt = time.Now().UTC()
	return a
}

func gateAttachValidSurrogate(t *testing.T, cs reasoningpreservation.CompressionStore, partition reasoningpreservation.SessionPartition, art reasoningpreservation.TurnArtifact, cfg reasoningpreservation.Config) {
	t.Helper()
	snap, err := cs.Snapshot(context.Background(), partition)
	require.NoError(t, err)
	var snapArt reasoningpreservation.TurnArtifact
	for _, a := range snap {
		if a.ID == art.ID {
			snapArt = a
			break
		}
	}
	require.Equal(t, art.ID, snapArt.ID)
	segs := reasoningpreservation.ExtractSemanticSegments(snapArt.Reasoning)
	require.NotEmpty(t, segs)
	h := sha256.New()
	for _, s := range segs {
		var idx [8]byte
		idx[0] = byte(s.Index >> 56)
		idx[1] = byte(s.Index >> 48)
		idx[2] = byte(s.Index >> 40)
		idx[3] = byte(s.Index >> 32)
		idx[4] = byte(s.Index >> 24)
		idx[5] = byte(s.Index >> 16)
		idx[6] = byte(s.Index >> 8)
		idx[7] = byte(s.Index)
		h.Write(idx[:])
		var l [4]byte
		l[0] = byte(len(s.Text) >> 24)
		l[1] = byte(len(s.Text) >> 16)
		l[2] = byte(len(s.Text) >> 8)
		l[3] = byte(len(s.Text))
		h.Write(l[:])
		h.Write([]byte(s.Text))
	}
	var semDigest [32]byte
	copy(semDigest[:], h.Sum(nil))
	egHash := sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef))
	claim, err := cs.ReserveCompression(context.Background(), partition, snapArt.ID, snapArt.Anchor, cfg.Compression.EgressPolicyRef, semDigest, egHash)
	require.NoError(t, err)
	authoritative := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: cfg.Compression.EgressPolicyRef}, cfg.Compression.Route)
	routeHash := sha256.Sum256([]byte(cfg.Compression.Route))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egHash, semDigest, authoritative, reasoningpreservation.SanitizationNone, routeHash))
	jobID := auxiliary.JobID("job-" + snapArt.ID)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, jobID))
	semIdx := []int{}
	for i, pr := range snapArt.Reasoning {
		if reasoningpreservation.ClassifyReasoningPart(pr.Part) == reasoningpreservation.ReplaySemanticText {
			semIdx = append(semIdx, i)
		}
	}
	segs2 := make([]reasoningpreservation.SurrogateSegment, 0, len(semIdx))
	for _, idx := range semIdx {
		segs2 = append(segs2, reasoningpreservation.SurrogateSegment{PlacementIndex: idx, Text: "compressed", Bytes: len("compressed")})
	}
	total := 0
	for _, s := range segs2 {
		total += s.Bytes
	}
	sur := reasoningpreservation.ReasoningSurrogate{
		OriginalDigest: snapArt.Anchor, PolicyRevision: cfg.Compression.EgressPolicyRef, Sanitization: reasoningpreservation.SanitizationNone,
		Segments: segs2, Bytes: total, SemanticDigest: semDigest, EgressPolicyHash: authoritative, AuthorizedRouteHash: routeHash,
	}
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, jobID, sur))
}

func gateCallHasCompressed(call aipapi.Call) bool {
	for _, m := range call.Messages {
		for _, p := range m.Parts {
			if p.Reasoning != nil && p.Reasoning.Text == "compressed" {
				return true
			}
		}
	}
	return false
}

func TestTDD_Gate_ModesTable(t *testing.T) {
	t.Parallel()
	origText := strings.Repeat("a", 30)
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := reasoningpreservation.ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	_ = gateMakeArt("gate-art", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))

	cases := []struct {
		name           string
		enabled        bool
		mode           string
		wantCompressed bool
		wantActiveUsed int64
	}{
		{"disabled", false, "", false, 0},
		{"shadow", true, "shadow", false, 0},
		{"active", true, "active", true, 1},
		{"invalid", true, "turbo", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cfg reasoningpreservation.Config
			if tc.name == "invalid" {
				base := gateConfigForMode(t, "shadow", true)
				cfg = base
				cfg.Compression.Mode = reasoningpreservation.CompressionMode("turbo")
				cfg.Compression.Enabled = true
			} else {
				cfg = gateConfigForMode(t, tc.mode, tc.enabled)
			}
			var xform *reasoningpreservation.AttemptTransform
			var tel *reasoningpreservation.Telemetry
			if tc.enabled && tc.name != "invalid" {
				svc := reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: cfg.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}, Client: &fakeBgGate{}, Poller: &fakePollerGate{}}
				parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
				require.NoError(t, err)
				tel = parts.Telemetry
				xform = parts.Transform
				partition := reasoningpreservation.NewSessionPartition("sess-gate-" + tc.name)
				cs := parts.Store.(reasoningpreservation.CompressionStore)
				art2 := gateMakeArt("gate-art", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
				_, _ = parts.Store.Append(context.Background(), partition, art2)
				gateAttachValidSurrogate(t, cs, partition, art2, cfg)
			} else if tc.name == "invalid" {
				tel = reasoningpreservation.NewTelemetry()
				store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
					TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144,
					Now: time.Now, CompressionLimits: cfg.Compression.ToLimits(),
				})
				cs := store.(reasoningpreservation.CompressionStore)
				partition := reasoningpreservation.NewSessionPartition("sess-gate-invalid")
				artInv := gateMakeArt("gate-art", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
				_, _ = store.Append(context.Background(), partition, artInv)
				shadowCfg := gateConfigForMode(t, "shadow", true)
				gateAttachValidSurrogate(t, cs, partition, artInv, shadowCfg)
				svc := reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: shadowCfg.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}}
				xform = reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, store, svc, reasoningpreservation.CompanionPolicy{}, reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, tel), tel)
			} else {
				tel = reasoningpreservation.NewTelemetry()
				store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
					TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144,
					Now: time.Now, CompressionLimits: cfg.Compression.ToLimits(),
				})
				xform = reasoningpreservation.NewAttemptTransform(cfg, store, tel)
			}
			call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
			meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-gate-" + tc.name}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
			dec, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
			require.NoError(t, err)
			require.Equal(t, request.AttemptContinue, dec.Kind)
			hasCompressed := gateCallHasCompressed(call)
			require.Equal(t, tc.wantCompressed, hasCompressed, "mode %s compressed mismatch call=%+v", tc.name, call.Messages)
			snap := tel.Snapshot()
			require.Equal(t, tc.wantActiveUsed, snap[reasoningpreservation.OutcomeActiveUsed], "mode %s active_used", tc.name)
			if tc.name == "active" && tc.wantActiveUsed == 1 {
				// ensure not shadow_ready
				require.Equal(t, int64(0), snap[reasoningpreservation.OutcomeShadowReady], "active should not have shadow_ready")
			}
			require.Equal(t, sdkhooks.FailClosed, xform.FailureMode())
		})
	}
}

func TestTDD_Gate_ActiveFailureReasonsFallback(t *testing.T) {
	t.Parallel()
	origText := strings.Repeat("a", 30)
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := reasoningpreservation.ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	cases := []string{"classifier_exact", "classifier_unknown", "correlation_stale_policy", "destination_unsupported", "current_policy_deny"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := gateConfigForMode(t, "active", true)
			partsSvc := reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: cfg.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}, Client: &fakeBgGate{}, Poller: &fakePollerGate{}}
			if name == "current_policy_deny" {
				partsSvc.EgressPolicy = fakeDenyGate{version: "v1"}
			}
			parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, partsSvc, reasoningpreservation.CompanionPolicy{})
			require.NoError(t, err)
			partition := reasoningpreservation.NewSessionPartition("sess-fail-" + name)
			var art reasoningpreservation.TurnArtifact
			switch name {
			case "classifier_exact":
				art = gateMakeArt("art-fail", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, origText, "", mustOpaqueJSON(t, `[]`))))
				art.Reasoning[0].Part.Reasoning.Summary = mustOpaqueJSON(t, `[]`)
				art.Reasoning[0].Part.Reasoning.SummaryPresent = true
			case "classifier_unknown":
				art = gateMakeArt("art-fail", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialect("unknown.dialect.v99"), origText, "", nil)))
			case "correlation_stale_policy":
				art = gateMakeArt("art-fail", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
			case "destination_unsupported", "current_policy_deny":
				art = gateMakeArt("art-fail", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
			}
			cs := parts.Store.(reasoningpreservation.CompressionStore)
			_, _ = cs.Append(context.Background(), partition, art)
			if name == "correlation_stale_policy" {
				gateAttachValidSurrogate(t, cs, partition, art, cfg)
				cfg.Compression.EgressPolicyRef = "other-policy"
				svc := reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: "other-policy"}, Sanitizer: fakeSanGate{}}
				tel2 := reasoningpreservation.NewTelemetry()
				xform2 := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, parts.Store, svc, reasoningpreservation.CompanionPolicy{}, reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, tel2), tel2)
				call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
				support := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
				meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-fail-" + name}, ReplaySupport: support, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
				dec, err := xform2.HandleAttempt(context.Background(), &call, meta, request.Services{})
				require.NoError(t, err)
				require.Equal(t, request.AttemptContinue, dec.Kind)
				require.False(t, gateCallHasCompressed(call), "case %s should fallback", name)
				snap := tel2.Snapshot()
				require.Equal(t, int64(0), snap[reasoningpreservation.OutcomeActiveUsed])
				return
			}
			if name != "classifier_exact" && name != "classifier_unknown" {
				gateAttachValidSurrogate(t, cs, partition, art, cfg)
			}
			xform := parts.Transform
			call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
			support := aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}
			if name == "destination_unsupported" {
				support = aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectAnthropicThinkingV1}}
			}
			meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-fail-" + name}, ReplaySupport: support, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
			dec, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
			require.NoError(t, err)
			// classifier exact/unknown and destination unsupported are authoritative unrepresentable - they may Exclude per OnUnrepresentable=reject; we only check no surrogate injected
			if name == "classifier_exact" || name == "classifier_unknown" || name == "destination_unsupported" {
				// these may be Exclude, but must not have surrogate
				require.False(t, gateCallHasCompressed(call), "case %s should fallback original", name)
			} else {
				require.Equal(t, request.AttemptContinue, dec.Kind, "case %s must not Exclude", name)
				require.False(t, gateCallHasCompressed(call), "case %s should fallback original", name)
			}
			snap := parts.Telemetry.Snapshot()
			require.Equal(t, int64(0), snap[reasoningpreservation.OutcomeActiveUsed], "case %s active_used must be 0", name)
		})
	}
}

func TestTDD_Gate_OnStateErrorRejectCompressionFailuresNoExclude(t *testing.T) {
	t.Parallel()
	cfg := gateConfigForMode(t, "active", true)
	cfg.OnStateError = reasoningpreservation.PolicyReject
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	art := gateMakeArt("gate-reject", sha256.Sum256([]byte("anchor-reject")), placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, strings.Repeat("a", 30), "", nil)))
	store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144, Now: time.Now, CompressionLimits: cfg.Compression.ToLimits()})
	partition := reasoningpreservation.NewSessionPartition("sess-reject2")
	_, _ = store.Append(context.Background(), partition, art)
	cs := store.(reasoningpreservation.CompressionStore)
	segs := reasoningpreservation.ExtractSemanticSegments(art.Reasoning)
	h := sha256.New()
	for _, s := range segs {
		var idx [8]byte
		idx[0] = byte(s.Index >> 56)
		idx[1] = byte(s.Index >> 48)
		idx[2] = byte(s.Index >> 40)
		idx[3] = byte(s.Index >> 32)
		idx[4] = byte(s.Index >> 24)
		idx[5] = byte(s.Index >> 16)
		idx[6] = byte(s.Index >> 8)
		idx[7] = byte(s.Index)
		h.Write(idx[:])
		var l [4]byte
		l[0] = byte(len(s.Text) >> 24)
		l[1] = byte(len(s.Text) >> 16)
		l[2] = byte(len(s.Text) >> 8)
		l[3] = byte(len(s.Text))
		h.Write(l[:])
		h.Write([]byte(s.Text))
	}
	var semDigest [32]byte
	copy(semDigest[:], h.Sum(nil))
	egHash := sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef))
	claim, _ := cs.ReserveCompression(context.Background(), partition, art.ID, art.Anchor, cfg.Compression.EgressPolicyRef, semDigest, egHash)
	authoritative := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: cfg.Compression.EgressPolicyRef}, cfg.Compression.Route)
	routeHash := sha256.Sum256([]byte(cfg.Compression.Route))
	_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egHash, semDigest, authoritative, reasoningpreservation.SanitizationNone, routeHash)
	_ = cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job-gate-reject"))
	poller := &fakePollerGateWithError{err: fakePollErr{"poll boom"}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: fakeAllowGate{version: cfg.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}}
	tel := reasoningpreservation.NewTelemetry()
	stage := reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, tel)
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, store, svc, reasoningpreservation.CompanionPolicy{}, stage, tel)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-reject2"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	dec, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind, "poll error must not Exclude even on_state_error=reject")
}

func TestTDD_Gate_ActiveUsedExactlyOnceAndNoPostOutputRetry(t *testing.T) {
	t.Parallel()
	cfgActive := gateConfigForMode(t, "active", true)
	origText := strings.Repeat("a", 30)
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := reasoningpreservation.ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	art := gateMakeArt("gate-once", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfgActive, reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: cfgActive.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}, Client: &fakeBgGate{}, Poller: &fakePollerGate{}}, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	partition := reasoningpreservation.NewSessionPartition("sess-once")
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	_, _ = cs.Append(context.Background(), partition, art)
	gateAttachValidSurrogate(t, cs, partition, art, cfgActive)
	xform := parts.Transform
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-once"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	dec, err := xform.HandleAttempt(context.Background(), &call, meta, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.True(t, gateCallHasCompressed(call))
	snap := parts.Telemetry.Snapshot()
	require.Equal(t, int64(1), snap[reasoningpreservation.OutcomeActiveUsed], "active_used exactly once")
	require.Equal(t, int64(0), snap[reasoningpreservation.OutcomeShadowReady], "shadow_ready not active")
	require.Equal(t, sdkhooks.FailClosed, xform.FailureMode())
	cfgDisabled := gateConfigForMode(t, "", false)
	storeDis, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144, Now: time.Now, CompressionLimits: cfgDisabled.Compression.ToLimits()})
	xformDis := reasoningpreservation.NewAttemptTransform(cfgDisabled, storeDis, reasoningpreservation.NewTelemetry())
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta2 := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-once"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}}
	dec2, err := xformDis.HandleAttempt(context.Background(), &call2, meta2, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec2.Kind)
	require.False(t, gateCallHasCompressed(call2))
}

func TestTDD_Gate_NoSubstitutionAfterViewDecisionRace(t *testing.T) {
	t.Parallel()
	cfgActive := gateConfigForMode(t, "active", true)
	origText := strings.Repeat("a", 30)
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := reasoningpreservation.ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	art := gateMakeArt("gate-race", anchor, placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, origText, "", nil)))
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfgActive, reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: cfgActive.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}, Client: &fakeBgGate{}, Poller: &fakePollerGate{}}, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	partition := reasoningpreservation.NewSessionPartition("sess-race")
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	_, _ = cs.Append(context.Background(), partition, art)
	xform := parts.Transform
	call1 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-race"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("user-1")}}
	dec1, _ := xform.HandleAttempt(context.Background(), &call1, meta, request.Services{})
	require.Equal(t, request.AttemptContinue, dec1.Kind)
	require.False(t, gateCallHasCompressed(call1), "before surrogate, must be original")
	gateAttachValidSurrogate(t, cs, partition, art, cfgActive)
	call2 := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}}}
	dec2, _ := xform.HandleAttempt(context.Background(), &call2, meta, request.Services{})
	require.Equal(t, request.AttemptContinue, dec2.Kind)
	require.True(t, gateCallHasCompressed(call2), "after surrogate, active should use surrogate")
	st, ok, _ := cs.GetCompressionState(context.Background(), partition, art.ID)
	require.True(t, ok)
	surStale := st.Surrogate
	cfgStale := cfgActive
	cfgStale.Compression.Route = "different-route"
	k, _ := reasoningpreservation.SelectReasoningView(cfgStale.Compression, art, surStale, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, reasoningpreservation.ClassMissing)
	require.Equal(t, reasoningpreservation.ViewOriginal, k, "stale route must fallback")
}

func TestTDD_Gate_NoProviderStrings(t *testing.T) {
	t.Parallel()
	cfg := gateConfigForMode(t, "active", true)
	art1 := gateMakeArt("arch-1", sha256.Sum256([]byte("a1")), placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, strings.Repeat("a", 20), "", nil)))
	art1.SourceBackend = "openai"
	art1.SourceModel = "gpt-4"
	art2 := art1
	art2.SourceBackend = "anthropic"
	art2.SourceModel = "claude"
	parts, _, _ := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, reasoningpreservation.CompressionServices{EgressPolicy: fakeAllowGate{version: cfg.Compression.EgressPolicyRef}, Sanitizer: fakeSanGate{}, Client: &fakeBgGate{}, Poller: &fakePollerGate{}}, reasoningpreservation.CompanionPolicy{})
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	partition := reasoningpreservation.NewSessionPartition("sess-arch")
	_, _ = cs.Append(context.Background(), partition, art1)
	gateAttachValidSurrogate(t, cs, partition, art1, cfg)
	st, ok, _ := cs.GetCompressionState(context.Background(), partition, art1.ID)
	require.True(t, ok)
	sur := st.Surrogate
	k1, _ := reasoningpreservation.SelectReasoningView(cfg.Compression, art1, sur, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, reasoningpreservation.ClassMissing)
	k2, _ := reasoningpreservation.SelectReasoningView(cfg.Compression, art2, sur, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}, reasoningpreservation.ClassMissing)
	require.Equal(t, k1, k2, "provider strings must not affect selection")
}

type fakeAllowGate struct{ version string }

func (f fakeAllowGate) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: f.version}, nil
}

type fakeDenyGate struct{ version string }

func (f fakeDenyGate) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressDeny, PolicyVersion: f.version}, nil
}

type fakeSanGate struct{}

func (fakeSanGate) SanitizeText(_ context.Context, t string) (string, error) { return t, nil }

type fakeBgGate struct{}

func (f *fakeBgGate) SubmitCollect(_ context.Context, _ auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "job", nil
}

func (f *fakeBgGate) Await(_ context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *fakeBgGate) Forget(_ auxiliary.JobID) {}

type fakePollErr struct{ msg string }

func (e fakePollErr) Error() string { return e.msg }

type fakePollerGateWithError struct{ err error }

func (f *fakePollerGateWithError) Poll(_ context.Context, _ auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{}, f.err
}

func (f *fakePollerGateWithError) SubmitCollect(_ context.Context, _ auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "job", nil
}

func (f *fakePollerGateWithError) Await(_ context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *fakePollerGateWithError) Forget(_ auxiliary.JobID) {}

type fakePollerGate struct{}

func (f *fakePollerGate) Poll(_ context.Context, _ auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
}

func (f *fakePollerGate) SubmitCollect(_ context.Context, _ auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "job", nil
}

func (f *fakePollerGate) Await(_ context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *fakePollerGate) Forget(_ auxiliary.JobID) {}
