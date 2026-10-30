package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// deterministicShadowBackend is a test-only deterministic fake auxiliary backend for shadow evaluation.
// It returns a bounded surrogate payload deterministically compressed to demonstrate
// hypothetical savings. It tracks SubmitCollect calls and provides synthetic fixture
// token usage evidence (tokens only, no billing CostNano/CostSource) for observability.
type deterministicShadowBackend struct {
	ratio     float64
	calls     int
	lastJobID auxiliary.JobID
}

func newDeterministicShadowBackend(ratio float64) *deterministicShadowBackend {
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.4
	}
	return &deterministicShadowBackend{ratio: ratio}
}

func (b *deterministicShadowBackend) SubmitCollect(_ context.Context, _ auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	b.calls++
	b.lastJobID = auxiliary.JobID("shadow-job-1")
	if opts.OnCoalesced != nil {
		opts.OnCoalesced(false)
	}
	return b.lastJobID, nil
}

func (b *deterministicShadowBackend) Await(_ context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (b *deterministicShadowBackend) Forget(_ auxiliary.JobID) {}

func (b *deterministicShadowBackend) Poll(_ context.Context, _ auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollPending}, nil
}

func (b *deterministicShadowBackend) PollWithSource(sourceText string, maxOutputBytes int) auxiliary.PollResult {
	srcLen := len(sourceText)
	decLen := max(int(float64(srcLen)*b.ratio), 1)
	if decLen >= srcLen {
		decLen = max(srcLen-1, 1)
	}
	surText := strings.Repeat("c", decLen)
	obj := map[string]any{"schema_version": 1, "segments": []map[string]any{{"index": 0, "text": surText}}}
	raw, _ := json.Marshal(obj)
	if len(raw) > maxOutputBytes && maxOutputBytes > 0 {
		raw = raw[:maxOutputBytes]
	}
	var c aipapi.Collected
	c.Text.WriteString(string(raw))
	c.FinishReceived = true
	c.InputTokens = 10
	c.OutputTokens = 5
	c.TotalTokens = 15
	return auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}
}

// shadowEvidence is the deterministic bounded evaluation result for a shadow round.
// Synthetic fixture token counts only, no billing; hypothetical savings only.
type shadowEvidence struct {
	SourceBytes      int
	DecodedBytes     int
	SavedBytes       int
	RawBytes         int
	SavingsRatio     float64
	CompressionRatio float64
	Evaluation       ShadowEvaluation
	FixtureTokens    struct {
		InputTokens  int
		OutputTokens int
		TotalTokens  int
	}
}

func runDeterministicShadowRound(cfg Config, sourceText string, ratio float64) shadowEvidence {
	store, _ := NewMemoryTurnStore(StoreOptions{
		TTL:                      cfg.State.TTL,
		MaxTurnsPerSession:       cfg.State.MaxTurnsPerSession,
		MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:          cfg.State.MaxSessionBytes,
		CompressionLimits:        cfg.Compression.ToLimits(),
	})
	tel := NewTelemetry()
	backend := newDeterministicShadowBackend(ratio)
	cs, _ := store.(CompressionStore)
	p := NewSessionPartition("shadow-fixture-partition")
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	_ = visible
	anchor, _ := ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	part := &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: sourceText}
	art := TurnArtifact{
		ID:             "shadow-art-1",
		Anchor:         anchor,
		SourceBackend:  "be",
		SourceModel:    "m",
		Reasoning:      []PlacedReasoning{{Part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: part}, BeforeNonReasoningPart: 0}},
		CreatedAt:      time.Now().UTC(),
		ReasoningBytes: len(sourceText),
	}
	_, _ = cs.Append(context.Background(), p, art)
	segs := ExtractSemanticSegments(art.Reasoning)
	srcBytes := 0
	for _, s := range segs {
		srcBytes += len(s.Text)
	}
	semDigest := sha256.Sum256([]byte("semantic-" + sourceText))
	egHash := sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef))
	claim, err := cs.ReserveCompression(context.Background(), p, art.ID, anchor, cfg.Compression.EgressPolicyRef, semDigest, egHash)
	if err == nil {
		auth := ComputeEgressPolicyHash(CompressionEgressDecision{Action: EgressAllow, PolicyVersion: cfg.Compression.EgressPolicyRef}, cfg.Compression.Route)
		routeHash := sha256.Sum256([]byte(cfg.Compression.Route))
		_ = cs.UpdateReservationPolicyHash(context.Background(), claim, egHash, semDigest, auth, SanitizationNone, routeHash)
		jobID := auxiliary.JobID("shadow-job-1")
		_ = cs.BindCompressionJob(context.Background(), claim, jobID)
		tel.RecordShadowMeasurement(OutcomeEligible, srcBytes, 0, 0, 0, 0)
		tel.RecordShadowMeasurement(OutcomeEgressAllow, srcBytes, 0, 0, 0, 0)
		tel.RecordShadowMeasurement(OutcomeSubmitted, srcBytes, 0, 0, 0, 0)
		pr := backend.PollWithSource(sourceText, cfg.Compression.MaxOutputBytes)
		raw, _ := ExtractBoundedRaw(pr.Collected, cfg.Compression.MaxOutputBytes)
		params := SurrogateDecodeParams{
			ExpectedIndexes:     []int{0},
			SourceBytes:         srcBytes,
			MaxSurrogateBytes:   cfg.Compression.MaxSurrogateBytes,
			MinSavedBytes:       cfg.Compression.MinSavedBytes,
			MinSavingsRatio:     cfg.Compression.MinSavingsRatio,
			OriginalDigest:      anchor,
			PolicyRevision:      cfg.Compression.EgressPolicyRef,
			Sanitization:        SanitizationNone,
			SemanticDigest:      semDigest,
			EgressPolicyHash:    auth,
			AuthorizedRouteHash: routeHash,
		}
		sur, _, _ := DecodeSurrogate(raw, params)
		_ = cs.AttachSurrogate(context.Background(), claim, jobID, sur)
		decBytes := sur.Bytes
		saved := max(srcBytes-decBytes, 0)
		tel.RecordShadowMeasurement(OutcomeSurrogateAttached, srcBytes, len(raw), decBytes, saved, 5*time.Millisecond)
		tel.RecordShadowMeasurement(OutcomeShadowReady, srcBytes, 0, decBytes, saved, 0)
		tel.RecordShadowMeasurement(OutcomeOriginalFallback, srcBytes, 0, 0, 0, 0)
		eval := tel.ShadowEvaluationSnapshot()
		return shadowEvidence{
			SourceBytes:      srcBytes,
			DecodedBytes:     decBytes,
			SavedBytes:       saved,
			RawBytes:         len(raw),
			SavingsRatio:     eval.SavingsRatio,
			CompressionRatio: eval.CompressionRatio,
			Evaluation:       eval,
			FixtureTokens: struct {
				InputTokens  int
				OutputTokens int
				TotalTokens  int
			}{InputTokens: pr.Collected.InputTokens, OutputTokens: pr.Collected.OutputTokens, TotalTokens: pr.Collected.TotalTokens},
		}
	}
	return shadowEvidence{Evaluation: tel.ShadowEvaluationSnapshot()}
}

//go:fix inline
func boolPtrShadow(b bool) *bool { return new(b) }

func TestShadowFixture_DeterministicEvidence(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Action:      ActionRestore,
		Rules:       []RuleConfig{{ID: "test-be", Backend: "be", Enabled: new(true)}},
		OnAmbiguous: PolicyLogSkip, OnUnrepresentable: PolicyReject, OnStateError: PolicyReject,
		State: StateConfig{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768},
		Compression: CompressionConfig{
			Enabled: true, Mode: CompressionShadow, Route: "comp-route", Timeout: 5 * time.Second,
			MaxInputTokens: 1000, MaxInputBytes: 10000, MaxOutputTokens: 1000, MaxOutputBytes: 4096,
			MaxSurrogateBytes: 1024, MinSourceBytes: 10, MinSavedBytes: 5, MinSavingsRatio: 0.1,
			MaxPendingPerSession: 8, MaxSurrogateBytesPerSession: 32768, MaxPendingTotal: 100, MaxSurrogateBytesTotal: 262144,
			EgressPolicyRef: "v1",
		},
	}
	source := strings.Repeat("a", 100)
	ev1 := runDeterministicShadowRound(cfg, source, 0.3)
	ev2 := runDeterministicShadowRound(cfg, source, 0.3)
	if ev1.SavedBytes != ev2.SavedBytes || ev1.SavingsRatio != ev2.SavingsRatio {
		t.Fatalf("non deterministic %v vs %v", ev1, ev2)
	}
	if ev1.FixtureTokens.TotalTokens == 0 {
		t.Fatal("fixture tokens evidence missing")
	}
	if ev1.Evaluation.TotalSaved == 0 {
		t.Fatal("evaluation saved zero")
	}
	// Ensure no billing CostNano/CostSource leaked from fixture (synthetic only)
	if ev1.Evaluation.TotalSaved > ev1.Evaluation.TotalSource {
		t.Fatal("saved exceeds source")
	}
	// Content-free: evaluation string must not contain source text
	evalStr := string(rune(ev1.Evaluation.TotalSaved)) + string(rune(ev1.Evaluation.TotalSource))
	if strings.Contains(evalStr, source) {
		t.Fatal("evaluation leaked content")
	}
}

func BenchmarkDisabledObserverNoTelemetry(b *testing.B) {
	cfg := Config{
		Action:      ActionRestore,
		Rules:       []RuleConfig{{ID: "test-be", Backend: "be", Enabled: new(true)}},
		OnAmbiguous: PolicyLogSkip, OnUnrepresentable: PolicyReject, OnStateError: PolicyReject,
		State:       StateConfig{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768},
		Compression: CompressionConfig{Enabled: false},
	}
	store, _ := NewMemoryTurnStore(StoreOptions{
		TTL: cfg.State.TTL, MaxTurnsPerSession: cfg.State.MaxTurnsPerSession, MaxReasoningBytesPerTurn: cfg.State.MaxReasoningBytesPerTurn, MaxSessionBytes: cfg.State.MaxSessionBytes,
	})
	factory := NewStreamObserverFactory(cfg, store)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs, _ := factory.Open(b.Context(), response.StreamMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-bench"}}, response.Services{})
		_ = obs.Observe(b.Context(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "bench-reasoning"})
		_ = obs.Observe(b.Context(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
		_ = obs.Finish(b.Context(), response.OutcomeSuccessReleased)
	}
}
