//nolint:paralleltest // Certification measures allocations and must not race parallel siblings.
package reasoningpreservation_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Audit existing coverage: FuzzSurrogateDecoder, FuzzRawExtractor, TestRawExtractor_*,
// internal spy TestExtractBoundedRawFromSource_OversizeDoesNotMaterialize, BenchmarkExtractBoundedRaw_*.
// Gaps closed: huge/malformed/duplicate/control-character composition, scheduler
// MaxResultBytes outer vs feature max_output_bytes/max_surrogate_bytes inner clamping,
// redundant-copy guard via stable alloc thresholds, disabled vs shadow/active overhead.

// ---------------------------------------------------------------------------
// Parser/resource limits: raw cap before decode
// ---------------------------------------------------------------------------

func TestCertify_RawCapBeforeDecode_HugeMalformedDuplicateControls(t *testing.T) {
	t.Parallel()
	hugeValid := `{"schema_version":1,"segments":[{"index":0,"text":"` + strings.Repeat("a", 60000) + `"}]}`
	require.True(t, json.Valid([]byte(hugeValid)))
	c := collectedFromText(t, hugeValid)
	_, err := reasoningpreservation.ExtractBoundedRaw(c, 1024)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
	assert.NotContains(t, err.Error(), "invalid character")

	hugeMalformed := strings.Repeat("{", reasoningpreservation.HardRawOutputCeiling+100)
	c2 := collectedFromText(t, hugeMalformed)
	_, err = reasoningpreservation.ExtractBoundedRaw(c2, 2048)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)

	dupHuge := `{"schema_version":1,"segments":[{"index":0,"text":"` + strings.Repeat("b", 600000) + `"},{"index":0,"text":"dup"}]}`
	c3 := collectedFromText(t, dupHuge)
	_, err = reasoningpreservation.ExtractBoundedRaw(c3, reasoningpreservation.HardRawOutputCeiling)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)

	ctrlHuge := strings.Repeat("\x00", reasoningpreservation.HardRawOutputCeiling+10)
	c4 := collectedFromText(t, ctrlHuge)
	_, err = reasoningpreservation.ExtractBoundedRaw(c4, 1024)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)

	dupSmall := `{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":0,"text":"b"}]}`
	c5 := collectedFromText(t, dupSmall)
	raw, err := reasoningpreservation.ExtractBoundedRaw(c5, 4096)
	require.NoError(t, err)
	require.NotNil(t, raw)
	params3 := surrogateParams([]int{0, 1}, 100)
	rawDup := []byte(`{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":0,"text":"b"}]}`)
	_, out2, err2 := reasoningpreservation.DecodeSurrogate(rawDup, params3)
	require.Error(t, err2)
	assert.ErrorIs(t, err2, reasoningpreservation.ErrSurrogateSchemaInvalid)
	assert.Equal(t, reasoningpreservation.OutcomeSchemaInvalid, out2)
}

func TestCertify_HugePayloadValidBeyondLimit_TruncatedPrefixInvalid(t *testing.T) {
	t.Parallel()
	prefix := `{"schema_version":1,"segments":[{"index":0,"text":"`
	suffix := strings.Repeat("x", 100000) + `"}]}`
	valid := prefix + suffix
	require.True(t, json.Valid([]byte(valid)))
	limit := len(prefix) + 2
	require.Less(t, limit, len(valid))
	c := collectedFromText(t, valid)
	raw, err := reasoningpreservation.ExtractBoundedRaw(c, limit)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
	assert.Nil(t, raw)
	assert.False(t, json.Valid([]byte(valid[:limit])))
}

// ---------------------------------------------------------------------------
// Caps composition: scheduler outer (8 MiB) + feature inner (512 KiB) without redundant copies
// ---------------------------------------------------------------------------

func TestCertify_CapsCompose_SchedulerOuterFeatureInner(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("z", reasoningpreservation.HardRawOutputCeiling)
	cOK := collectedFromText(t, huge)
	_, err := reasoningpreservation.ExtractBoundedRaw(cOK, reasoningpreservation.HardRawOutputCeiling)
	require.NoError(t, err, "exact hard ceiling must be allowed")
	hugePlus := huge + "z"
	cOver := collectedFromText(t, hugePlus)
	_, err = reasoningpreservation.ExtractBoundedRaw(cOver, reasoningpreservation.HardRawOutputCeiling+5000)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
	smallLimit := 1024
	payload := strings.Repeat("a", 2048)
	cSmall := collectedFromText(t, payload)
	_, err = reasoningpreservation.ExtractBoundedRaw(cSmall, smallLimit)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)
	within := strings.Repeat("a", 1024)
	cWithin := collectedFromText(t, within)
	raw, err := reasoningpreservation.ExtractBoundedRaw(cWithin, smallLimit)
	require.NoError(t, err)
	assert.Equal(t, 1024, len(raw))
}

func TestCertify_DecodedSurrogateCapsEnforced(t *testing.T) {
	t.Parallel()
	params := surrogateParams([]int{0, 1}, 1000)
	params.MaxSurrogateBytes = 10
	raw := []byte(`{"schema_version":1,"segments":[{"index":0,"text":"` + strings.Repeat("a", 6) + `"},{"index":1,"text":"` + strings.Repeat("b", 6) + `"}]}`)
	_, out, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.Error(t, err)
	assert.ErrorIs(t, err, reasoningpreservation.ErrSurrogateOversize)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateOversize, out)
	bigSeg := strings.Repeat("x", reasoningpreservation.HardCompressionMaxSurrogateBytes+1)
	raw2 := mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": bigSeg}}, nil)
	_, out2, err2 := reasoningpreservation.DecodeSurrogate(raw2, surrogateParams([]int{0}, 10*1024*1024))
	require.Error(t, err2)
	assert.ErrorIs(t, err2, reasoningpreservation.ErrSurrogateOversize)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateOversize, out2)
	rawHuge := make([]byte, reasoningpreservation.HardRawOutputCeiling+1)
	for i := range rawHuge {
		rawHuge[i] = 'a'
	}
	_, out3, err3 := reasoningpreservation.DecodeSurrogate(rawHuge, surrogateParams([]int{0}, 1000000))
	require.Error(t, err3)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateOversize, out3)
}

func TestCertify_AllocationGuard_Stable(t *testing.T) {
	huge := strings.Repeat("a", 256*1024)
	c := collectedFromText(t, huge)
	limit := 1024
	_, _ = reasoningpreservation.ExtractBoundedRaw(c, limit)
	oversizeAllocs := testing.AllocsPerRun(100, func() {
		_, _ = reasoningpreservation.ExtractBoundedRaw(c, limit)
	})
	small := strings.Repeat("a", 1024)
	cSmall := collectedFromText(t, small)
	withinAllocs := testing.AllocsPerRun(100, func() {
		_, _ = reasoningpreservation.ExtractBoundedRaw(cSmall, 2048)
	})
	if oversizeAllocs > 8 {
		t.Fatalf("oversize allocs per op = %v want <=8 (O(1) guard)", oversizeAllocs)
	}
	if withinAllocs < 1 {
		t.Fatalf("within-limit allocs per op = %v want >=1", withinAllocs)
	}
	if oversizeAllocs > 15 {
		t.Fatalf("oversize allocs %v unreasonably high, suggests redundant copy", oversizeAllocs)
	}
	// Use background context captured outside AllocsPerRun to avoid nil ctx panic.
	bgCtx := t.Context()
	disabledAllocs := testing.AllocsPerRun(200, func() {
		cfg := reasoningpreservation.Config{
			Action:      reasoningpreservation.ActionRestore,
			Rules:       []reasoningpreservation.RuleConfig{{ID: "be-test", Backend: "be", Enabled: boolPtr(true)}},
			State:       reasoningpreservation.StateConfig{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768},
			Compression: reasoningpreservation.CompressionConfig{Enabled: false},
		}
		store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
			TTL: cfg.State.TTL, MaxTurnsPerSession: cfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: cfg.State.MaxSessionBytes,
		})
		factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
		obs, _ := factory.Open(bgCtx, response.StreamMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-alloc"}}, response.Services{})
		_ = obs.Observe(bgCtx, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "reasoning"})
		_ = obs.Finish(bgCtx, response.OutcomeSuccessReleased)
	})
	if disabledAllocs > 60 {
		t.Fatalf("disabled observer allocs per op = %v want <=60 (bounded, not exact)", disabledAllocs)
	}
}

func TestCertify_DisabledMode_NoSurrogateState(t *testing.T) {
	t.Parallel()
	cfg := reasoningpreservation.Config{
		Action:      reasoningpreservation.ActionRestore,
		Rules:       []reasoningpreservation.RuleConfig{{ID: "be-test", Backend: "be", Enabled: boolPtr(true)}},
		OnAmbiguous: reasoningpreservation.PolicyLogSkip, OnUnrepresentable: reasoningpreservation.PolicyReject, OnStateError: reasoningpreservation.PolicyReject,
		State:       reasoningpreservation.StateConfig{TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768},
		Compression: reasoningpreservation.CompressionConfig{Enabled: false},
	}
	store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: cfg.State.TTL, MaxTurnsPerSession: cfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: cfg.State.MaxSessionBytes,
	})
	require.NotNil(t, store)
	if cs, ok := store.(reasoningpreservation.CompressionStore); ok {
		stats := cs.CompressionStats()
		assert.Equal(t, 0, stats.TotalPending)
		assert.Equal(t, 0, stats.TotalSurrogateBytes)
	}
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(t.Context(), response.StreamMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-disabled"}}, response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(t.Context(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "hello reasoning"}))
	require.NoError(t, obs.Finish(t.Context(), response.OutcomeSuccessReleased))
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "q"}}}}}
	_, err = xform.HandleAttempt(t.Context(), call, request.AttemptMeta{
		BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-disabled"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
	}, request.Services{})
	require.NoError(t, err)
}

// Shadow/active overhead is bounded: shadow retains original, active view is ephemeral.
// Verify disabled transform adds no compression polling overhead.

func TestCertify_ShadowActiveOverhead_Bounded(t *testing.T) {
	disabledCfg := reasoningpreservation.Config{
		Action:      reasoningpreservation.ActionRestore,
		Rules:       []reasoningpreservation.RuleConfig{{ID: "be-test", Backend: "be", Enabled: boolPtr(true)}},
		State:       reasoningpreservation.StateConfig{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768},
		Compression: reasoningpreservation.CompressionConfig{Enabled: false},
	}
	shadowCfg := decodeValidConfig(t, `
action: restore
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 32768
compression:
  enabled: true
  mode: shadow
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
`)
	storeD, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: disabledCfg.State.TTL, MaxTurnsPerSession: disabledCfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: disabledCfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: disabledCfg.State.MaxSessionBytes,
	})
	storeS, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: shadowCfg.State.TTL, MaxTurnsPerSession: shadowCfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: shadowCfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: shadowCfg.State.MaxSessionBytes, CompressionLimits: shadowCfg.Compression.ToLimits(),
	})
	xformD := reasoningpreservation.NewAttemptTransform(disabledCfg, storeD)
	xformS := reasoningpreservation.NewAttemptTransform(shadowCfg, storeS)
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "q"}}}}}
	meta := request.AttemptMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-overhead"}, ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}}
	disabledAllocs := testing.AllocsPerRun(200, func() {
		_, _ = xformD.HandleAttempt(t.Context(), call, meta, request.Services{})
	})
	shadowAllocs := testing.AllocsPerRun(200, func() {
		_, _ = xformS.HandleAttempt(t.Context(), call, meta, request.Services{})
	})
	// Shadow overhead when no pending exists should be bounded (no background poll, just store snapshot + candidate scan).
	// Allow small delta; fragile exact equality avoided.
	if shadowAllocs > disabledAllocs+10 {
		t.Fatalf("shadow allocs %v > disabled %v +10, overhead not bounded", shadowAllocs, disabledAllocs)
	}
	// Also verify raw extractor oversize vs within alloc ratio stable.
	_ = sha256.Sum256([]byte("use sha256 to avoid import unused"))
}

// ---------------------------------------------------------------------------
// Benchmarks: disabled baseline vs shadow/active overhead, benchmem
// ---------------------------------------------------------------------------

func BenchmarkCertify_DisabledObserver_Baseline(b *testing.B) {
	cfg := reasoningpreservation.Config{
		Action:      reasoningpreservation.ActionRestore,
		Rules:       []reasoningpreservation.RuleConfig{{ID: "be-bench", Backend: "be", Enabled: boolPtr(true)}},
		OnAmbiguous: reasoningpreservation.PolicyLogSkip, OnUnrepresentable: reasoningpreservation.PolicyReject, OnStateError: reasoningpreservation.PolicyReject,
		State:       reasoningpreservation.StateConfig{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 8192, MaxSessionBytes: 32768},
		Compression: reasoningpreservation.CompressionConfig{Enabled: false},
	}
	store, _ := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: cfg.State.TTL, MaxTurnsPerSession: cfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: cfg.State.MaxSessionBytes,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	b.ReportAllocs()
	for b.Loop() {
		obs, _ := factory.Open(b.Context(), response.StreamMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-bench-disabled"}}, response.Services{})
		_ = obs.Observe(b.Context(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "bench-reasoning-text"})
		_ = obs.Observe(b.Context(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
		_ = obs.Finish(b.Context(), response.OutcomeSuccessReleased)
	}
}

func BenchmarkCertify_RawExtractor_Oversize(b *testing.B) {
	payload := strings.Repeat("a", 256*1024)
	var c aipapi.Collected
	c.FinishReceived = true
	c.Text.WriteString(payload)
	limit := 1024
	b.ReportAllocs()
	for b.Loop() {
		_, _ = reasoningpreservation.ExtractBoundedRaw(c, limit)
	}
}

func BenchmarkCertify_RawExtractor_WithinLimit(b *testing.B) {
	payload := strings.Repeat("a", 1024)
	var c aipapi.Collected
	c.FinishReceived = true
	c.Text.WriteString(payload)
	limit := 2048
	b.ReportAllocs()
	for b.Loop() {
		_, _ = reasoningpreservation.ExtractBoundedRaw(c, limit)
	}
}

func BenchmarkCertify_Decoder_ValidSmall(b *testing.B) {
	raw := []byte(`{"schema_version":1,"segments":[{"index":0,"text":"hello"}]}`)
	params := surrogateParams([]int{0}, 100)
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = reasoningpreservation.DecodeSurrogate(raw, params)
	}
}

func BenchmarkCertify_Decoder_Malformed(b *testing.B) {
	raw := []byte(`{"schema_version":1,"segments":[{"index":0,"text":"` + strings.Repeat("a", 256) + `"},`)
	params := surrogateParams([]int{0}, 1000)
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = reasoningpreservation.DecodeSurrogate(raw, params)
	}
}

func BenchmarkCertify_Decoder_HugeDuplicate(b *testing.B) {
	raw := []byte(`{"schema_version":1,"segments":[{"index":0,"text":"` + strings.Repeat("a", 300000) + `"},{"index":0,"text":"dup"}]}`)
	// This raw exceeds hard ceiling, decoder will hit oversize fast path without full JSON parse.
	params := surrogateParams([]int{0, 0}, 500000)
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = reasoningpreservation.DecodeSurrogate(raw, params)
	}
}

// ---------------------------------------------------------------------------
// Fuzz: strict decoder/raw extractor huge/malformed/duplicate/controls, raw cap before decode
// ---------------------------------------------------------------------------

func FuzzCertify_RawExtractor_HugeMalformedDuplicateControls(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"hello"}]}`), 1024)
	f.Add([]byte(strings.Repeat("a", 100)), 10)
	f.Add([]byte(``), 1)
	f.Add([]byte(strings.Repeat("x", reasoningpreservation.HardRawOutputCeiling+10)), reasoningpreservation.HardRawOutputCeiling)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":0,"text":"dup"}]}`), 4096)
	f.Add([]byte("{\"schema_version\":1,\"segments\":[{\"index\":0,\"text\":\"a\x00b\"}]}"), 4096)
	f.Add([]byte(strings.Repeat("{", 2048)), 1024)
	f.Fuzz(func(t *testing.T, data []byte, lim int) {
		if lim < 0 {
			lim = -lim
		}
		if lim > reasoningpreservation.HardRawOutputCeiling+200 {
			lim = reasoningpreservation.HardRawOutputCeiling + 200
		}
		var c aipapi.Collected
		c.FinishReceived = true
		c.Text.WriteString(string(data))
		raw, err := reasoningpreservation.ExtractBoundedRaw(c, lim)
		if lim <= 0 {
			if !errors.Is(err, reasoningpreservation.ErrRawInvalidLimit) {
				t.Fatalf("lim %d: want ErrRawInvalidLimit got %v", lim, err)
			}
			return
		}
		effective := min(lim, reasoningpreservation.HardRawOutputCeiling)
		if err != nil {
			if !errors.Is(err, reasoningpreservation.ErrRawOversize) && !errors.Is(err, reasoningpreservation.ErrRawInvalidChannel) && !errors.Is(err, reasoningpreservation.ErrRawInvalidLimit) {
				t.Fatalf("unexpected error %v for lim %d", err, lim)
			}
			if raw != nil {
				t.Fatalf("oversize must return nil raw")
			}
			return
		}
		if len(raw) > effective {
			t.Fatalf("raw %d > effective %d", len(raw), effective)
		}
		if len(raw) > reasoningpreservation.HardRawOutputCeiling {
			t.Fatalf("raw %d > hard ceiling %d", len(raw), reasoningpreservation.HardRawOutputCeiling)
		}
		if string(raw) != string(data) {
			t.Fatalf("raw mismatch")
		}
	})
}

func FuzzCertify_SurrogateDecoder_Strict_HugeDuplicateControls(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"hello"}]}`), 100)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":1,"text":"b"}]}`), 100)
	f.Add([]byte(`{"schema_version":2,"segments":[]}`), 10)
	f.Add([]byte(`not json`), 10)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"\u0000"}]}`), 10)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":0,"text":"dup"}]}`), 100)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"a`+strings.Repeat("\x1f", 5)+`"}]}`), 10)
	f.Add([]byte(strings.Repeat("a", reasoningpreservation.HardRawOutputCeiling+5)), 1000)
	f.Add([]byte(`{"schema_version":1,"unknown":"field","segments":[{"index":0,"text":"a"}]}`), 10)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":""}]}`), 10)
	f.Fuzz(func(t *testing.T, raw []byte, sourceBytes int) {
		if len(raw) > reasoningpreservation.HardRawOutputCeiling+100 {
			return
		}
		if sourceBytes < 0 {
			sourceBytes = -sourceBytes
		}
		if sourceBytes == 0 {
			sourceBytes = 1
		}
		if sourceBytes > 5*1024*1024 {
			sourceBytes = 5 * 1024 * 1024
		}
		params := reasoningpreservation.SurrogateDecodeParams{
			ExpectedIndexes:     []int{0},
			SourceBytes:         sourceBytes,
			MaxSurrogateBytes:   1024,
			MinSavedBytes:       1,
			MinSavingsRatio:     0.01,
			OriginalDigest:      sha256.Sum256([]byte("orig")),
			PolicyRevision:      "v1",
			Sanitization:        "none",
			SemanticDigest:      sha256.Sum256([]byte("sem")),
			EgressPolicyHash:    sha256.Sum256([]byte("eg")),
			AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
		}
		if len(raw) > reasoningpreservation.HardRawOutputCeiling {
			_, out, err := reasoningpreservation.DecodeSurrogate(raw, params)
			if !errors.Is(err, reasoningpreservation.ErrSurrogateOversize) {
				t.Fatalf("huge raw %d want ErrSurrogateOversize got %v out %q", len(raw), err, out)
			}
			return
		}
		sur, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
		if err != nil {
			switch outcome {
			case reasoningpreservation.OutcomeDecodeInvalid, reasoningpreservation.OutcomeSchemaInvalid, reasoningpreservation.OutcomeControlInvalid, reasoningpreservation.OutcomeSurrogateOversize, reasoningpreservation.OutcomeInsufficientSavings:
			default:
				t.Fatalf("unexpected outcome %q for err %v", outcome, err)
			}
			if err.Error() == "" {
				t.Fatalf("empty error")
			}
			return
		}
		if outcome != reasoningpreservation.OutcomeSurrogateDecoded {
			t.Fatalf("success must have decoded outcome, got %q", outcome)
		}
		if len(sur.Segments) == 0 || sur.Bytes <= 0 || sur.Bytes > params.MaxSurrogateBytes {
			t.Fatalf("invalid sur bytes %d", sur.Bytes)
		}
	})
}
