package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	ErrNativeContextHardLimit = errors.New("native context hard limit")
	ErrNativeContextAuthority = errors.New("native context authority unavailable")
	ErrNativeContextHistory   = errors.New("native context history unavailable")
)

type nativeContextAccountError struct {
	status int
	cause  error
}

func (e *nativeContextAccountError) Error() string { return "native context account unavailable" }
func (e *nativeContextAccountError) Unwrap() error { return e.cause }

func nativeContextStatus(err error) int {
	if accountErr, ok := errors.AsType[*nativeContextAccountError](err); ok {
		return accountErr.status
	}
	return compactionStatus(err)
}

type nativeCompactionClient interface {
	Compact(context.Context, CompactionRequest) (CompactionResult, error)
}

// NativeContextCoordinator owns only connector-local native context state. The
// caller supplies the already-selected account and model for every attempt.
type NativeContextCoordinator struct {
	config     Config
	instanceID string
	store      *nativeCheckpointStore
	compactor  nativeCompactionClient
	telemetry  *nativeContextTelemetry
}

var nativeConnectorInstanceCounter atomic.Uint64

type NativeContextPrepared struct {
	Payload              Payload
	Compacted            bool
	ReusedCheckpoint     bool
	FallbackToFull       bool
	CompactionUsage      *NativeUsageEvidence
	Outcome              string
	EstimatedInputTokens int64
}

// NativeUsage returns compaction usage as bounded provider evidence for the
// connector's accounting seam; request/response bodies are never retained.
func (p NativeContextPrepared) NativeUsage() *NativeUsageEvidence {
	return cloneUsageEvidence(p.CompactionUsage)
}

type NativeContextPrepareInput struct {
	Call               aipapi.Call
	Payload            Payload
	Account            Config
	Model              string
	MarkerEligible     bool
	ClientFamily       string
	ConversationID     string
	OnCheckpointCommit func()
}

type nativeContextPreflight struct {
	history  NativeHistory
	profile  CompactionModelProfile
	estimate CompactionEstimate
	isReady  bool
}

func newNativeContextCoordinator(cfg Config, instanceID string) *NativeContextCoordinator {
	if cfg.NativeContext == nil || !cfg.NativeContext.Compaction.Enabled || !cfg.NativeContext.Enabled || cfg.DisableNativeCompactionWithoutAccounting {
		return nil
	}
	client := cfg.HTTPClient
	if client == nil {
		client = httpClientForNativeContext()
	}
	return newNativeContextCoordinatorWithCompactor(cfg, instanceID,
		newCompactionClient(client, responsesCompactionEndpoint(cfg.BaseURL)))
}

func newNativeContextCoordinatorWithCompactor(cfg Config, instanceID string, compactor nativeCompactionClient) *NativeContextCoordinator {
	if cfg.NativeContext == nil || !cfg.NativeContext.Compaction.Enabled || !cfg.NativeContext.Enabled || cfg.DisableNativeCompactionWithoutAccounting {
		return nil
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		instanceID = fmt.Sprintf("codex-instance-%d", nativeConnectorInstanceCounter.Add(1))
	}
	telemetry := newNativeContextTelemetry()
	store := newNativeCheckpointStore(cfg.NativeContext.Compaction.StateTTL, cfg.NativeContext.Compaction.MaxEntries, cfg.NativeContext.Compaction.MaxEntryBytes)
	store.setEvictionHook(func() { telemetry.recordCheckpoint(checkpointTelemetryEvicted) })
	return &NativeContextCoordinator{
		config: cfg, instanceID: instanceID,
		store:     store,
		compactor: compactor,
		telemetry: telemetry,
	}
}

func httpClientForNativeContext() *http.Client { return &http.Client{} }

func (c *NativeContextCoordinator) Close() {
	if c != nil && c.store != nil {
		c.store.Close()
	}
}

func (c *NativeContextCoordinator) keyFor(in NativeContextPrepareInput, resolvedCompHash string) CheckpointKey {
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(in.Payload.Model)
	}
	session := strings.TrimSpace(in.Call.Session.AuthoritativeSessionID)
	cache := strings.TrimSpace(in.Payload.PromptCacheKey)
	if cache == "" {
		cache = strings.TrimSpace(in.ConversationID)
	}
	if cache == "" && session != "" {
		cache = "session:" + session
	}
	clientFamily := strings.TrimSpace(in.ClientFamily)
	if clientFamily == "" {
		clientFamily = "codex"
	}
	accountID := strings.TrimSpace(in.Account.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(c.config.AccountID)
	}
	if accountID == "" {
		accountID = "static"
	}
	compHash := ""
	compHash = strings.TrimSpace(resolvedCompHash)
	if compHash == "" {
		compHash = "unknown"
	}
	continuity := ContinuityDisabled
	if c != nil && c.config.NativeContext != nil && c.config.NativeContext.ReasoningContinuity != "" {
		continuity = c.config.NativeContext.ReasoningContinuity
	}
	return CheckpointKey{
		ConnectorInstanceID: c.instanceID,
		SessionID:           session,
		AccountID:           accountID,
		Model:               model,
		PromptCacheKey:      cache,
		ClientFamily:        clientFamily,
		CompHash:            compHash,
		InstructionsFP:      fingerprintJSON(in.Payload.Instructions),
		ToolsFP:             fingerprintJSON(in.Payload.Tools),
		ContinuityMode:      string(continuity),
	}
}

func (c *NativeContextCoordinator) Prepare(ctx context.Context, in NativeContextPrepareInput) (NativeContextPrepared, error) {
	if ctx == nil {
		return NativeContextPrepared{}, aipapi.ErrNilContext
	}
	if c == nil {
		return NativeContextPrepared{Payload: in.Payload, Outcome: "disabled"}, nil
	}
	if c.store == nil || c.config.NativeContext == nil {
		return NativeContextPrepared{Payload: in.Payload, Outcome: "disabled"}, nil
	}
	start := time.Now()
	defer c.telemetry.recordLatency(start)
	if err := ctx.Err(); err != nil {
		return NativeContextPrepared{}, err
	}
	if in.MarkerEligible && c.config.NativeContext.RequestEncryptedReasoning {
		c.telemetry.recordReasoning(reasoningTelemetryRequested)
	}
	if c.config.NativeContext.ReasoningContinuity == ContinuityDisabled {
		in.MarkerEligible = false
	}
	if strings.TrimSpace(in.Call.Session.AuthoritativeSessionID) == "" && len(in.Call.Messages) == 0 {
		return NativeContextPrepared{}, ErrNativeContextAuthority
	}
	if c.compactor == nil {
		return NativeContextPrepared{Payload: in.Payload, Outcome: "compactor_unavailable"}, nil
	}
	if !in.MarkerEligible {
		// Best-effort/compaction-only evaluation may still compact, but without a
		// continuity marker it must not request automatic encrypted reasoning.
		// Exact client-supplied items remain part of the history baseline.
		in.Account.NativeContext = cloneNativeContextConfig(in.Account.NativeContext)
		if in.Account.NativeContext != nil {
			in.Account.NativeContext.RequestEncryptedReasoning = false
		}
	}
	history, err := buildNativeHistory(&in.Call)
	if err != nil {
		return NativeContextPrepared{}, fmt.Errorf("%w: %w", ErrNativeContextHistory, err)
	}
	profile, err := ResolveCompactionModelProfile(c.config.ModelCatalog, in.Model, c.config.NativeContext.Compaction)
	if err != nil {
		return NativeContextPrepared{}, err
	}
	estimate, err := estimateHistory(ctx, deterministicHistoryEstimator{}, history)
	if err != nil {
		return NativeContextPrepared{}, err
	}
	preflight := nativeContextPreflight{history: history, profile: profile, estimate: estimate, isReady: true}
	// A trusted marker proves the feature transform ran, but the typed session
	// authority is still the required partition key. Never let a client hint
	// become checkpoint authority.
	if strings.TrimSpace(in.Call.Session.AuthoritativeSessionID) == "" {
		// The ABI currently carries the proxy-owned session id on aipapi.Call.
		// ClientSessionID/ContinuityKey are deliberately not authority fallbacks.
		return c.fallback(ctx, in, ErrNativeContextAuthority, preflight)
	}
	// The marker is emitted by the trusted reasoning-preservation transform.
	// Required continuity must never be inferred from client history alone.
	if c.config.NativeContext.ReasoningContinuity == ContinuityRequired && !in.MarkerEligible {
		return c.fallback(ctx, in, nil, preflight)
	}
	key := c.keyFor(in, profile.CompHash)
	var storedCheckpoint *NativeCheckpoint
	var checkpoint *CheckpointView
	if stored, ok := c.store.Get(key); ok {
		c.telemetry.recordCheckpoint(checkpointTelemetryHit)
		storedCheckpoint = &stored
		checkpoint = &CheckpointView{Model: stored.Key.Model, CompHash: stored.Key.CompHash, SourcePrefixFP: append([]string(nil), stored.SourcePrefixFP...), Replacement: cloneInputItems(stored.Replacement), CompactionUsage: cloneUsageEvidence(stored.CompactionUsage)}
	} else {
		c.telemetry.recordCheckpoint(checkpointTelemetryMiss)
	}
	plan := PlanCompaction(CompactionPlanInput{
		Context: ctx, History: history, Profile: profile, Config: *c.config.NativeContext, FullEstimate: &preflight.estimate,
		MarkerEligible: in.MarkerEligible, Checkpoint: checkpoint,
	})
	afterTokens := max(plan.EffectiveTokens-plan.ExpectedSavings, 0)
	c.telemetry.recordContext(plan.EffectiveTokens, afterTokens, nativeHistoryBytes(history), nativeHistoryBytes(plan.EffectiveHistory))
	if checkpoint != nil && plan.Kind == DecisionCreate {
		c.telemetry.recordCheckpoint(checkpointTelemetryMismatch)
	}
	switch plan.Kind {
	case DecisionBypass:
		return c.fallback(ctx, in, nil, preflight)
	case DecisionHardFailure:
		c.telemetry.recordCompaction(compactionTelemetryHardFail, 0)
		return c.fallback(ctx, in, ErrNativeContextHardLimit, preflight)
	case DecisionReuse:
		if storedCheckpoint == nil {
			return c.fallback(ctx, in, nil, preflight)
		}
		return c.reuse(ctx, in, *storedCheckpoint, history, key, preflight)
	case DecisionCreate:
		return c.create(ctx, in, history, profile, key, plan, preflight)
	default:
		return c.fallback(ctx, in, ErrNativeContextHardLimit, preflight)
	}
}

func cloneNativeContextConfig(src *NativeContextConfig) *NativeContextConfig {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Compaction = src.Compaction
	return &dst
}

func (c *NativeContextCoordinator) reuse(ctx context.Context, in NativeContextPrepareInput, stored NativeCheckpoint, history NativeHistory, key CheckpointKey, preflight nativeContextPreflight) (NativeContextPrepared, error) {
	rewritten, matched, err := rewriteNativeHistoryWithKey(history, key, stored)
	if err != nil || !matched {
		return c.fallback(ctx, in, nil, preflight)
	}
	in.Payload.Input = rewritten.Items
	c.telemetry.recordCheckpoint(checkpointTelemetryReuse)
	return NativeContextPrepared{Payload: in.Payload, ReusedCheckpoint: true, Outcome: "checkpoint_reuse"}, nil
}

func (c *NativeContextCoordinator) create(ctx context.Context, in NativeContextPrepareInput, history NativeHistory, profile CompactionModelProfile, key CheckpointKey, plan CompactionPlan, preflight nativeContextPreflight) (NativeContextPrepared, error) {
	// Keep a second guard at the I/O boundary. A malformed or stale plan must
	// never turn the current user turn into dedicated compaction input.
	if !compactionPrefixExcludesLatestUserTail(plan.EffectiveHistory, plan.PrefixEnd) {
		return c.fallback(ctx, in, nil, preflight)
	}
	reservation, ok := c.store.Reserve(key)
	if !ok {
		if c.store.InCooldown(key) {
			c.telemetry.recordCheckpoint(checkpointTelemetryCooldown)
		}
		return c.fallback(ctx, in, nil, preflight)
	}
	attemptOutcome := compactionTelemetryAttempt
	if plan.ExistingCheckpoint != nil {
		attemptOutcome = compactionTelemetrySecondAttempt
	}
	c.telemetry.recordCompaction(attemptOutcome, 0)
	abort := true
	defer func() {
		if abort {
			c.store.Abort(reservation)
		}
	}()
	prefixHistory := historySlice(plan.EffectiveHistory, 0, plan.PrefixEnd)
	prefix := prefixHistory.Items
	compactionRequest, err := buildCompactionRequest(in.Payload, prefix, in.Account, in.ConversationID, nil)
	if err != nil {
		return c.compactionFailure(ctx, in, key, reservation, err, preflight)
	}
	result, err := c.compactor.Compact(ctx, compactionRequest)
	if err != nil {
		return c.compactionFailure(ctx, in, key, reservation, err, preflight)
	}
	replacement, err := authoritativeReplacement(prefixHistory, result)
	if err != nil {
		return c.compactionFailure(ctx, in, key, reservation, err, preflight)
	}
	if plan.SourcePrefixEnd <= 0 || plan.SourcePrefixEnd > len(history.Fingerprints) {
		return c.compactionFailure(ctx, in, key, reservation, ErrNativeContextHardLimit, preflight)
	}
	compactionUsage := compactionUsageEvidence(result.Usage, plan, replacement)
	candidate := NativeCheckpoint{
		Key: key, SourcePrefixFP: append([]string(nil), history.Fingerprints[:plan.SourcePrefixEnd]...),
		Replacement: cloneInputItems(replacement.Items), SourceEstimatedTokens: replacement.SourceEstimatedTokens,
		ResultEstimatedTokens: replacement.ResultEstimatedTokens,
		CompactionInputTokens: plan.EffectiveTokens, CompactionUsage: compactionUsage,
	}
	rewritten, matched, err := rewriteNativeHistoryWithKey(history, key, candidate)
	if err != nil || !matched {
		c.telemetry.recordCompaction(compactionTelemetryRewriteFail, 0)
		return c.compactionFailure(ctx, in, key, reservation, ErrNativeContextHardLimit, preflight)
	}
	if err := c.store.Commit(reservation, candidate); err != nil {
		return c.compactionFailure(ctx, in, key, reservation, err, preflight)
	}
	abort = false
	c.telemetry.recordCompaction(compactionTelemetryCommit, 0)
	if in.OnCheckpointCommit != nil {
		in.OnCheckpointCommit()
	}
	in.Payload.Input = rewritten.Items
	c.telemetry.recordCompaction(compactionTelemetrySuccess, compactionUsage.TotalTokens)
	return NativeContextPrepared{Payload: in.Payload, Compacted: true, Outcome: "checkpoint_created", CompactionUsage: compactionUsage, EstimatedInputTokens: plan.EffectiveTokens}, nil
}

// authoritativeReplacement keeps the dedicated unary response separate from
// the legacy streamed trigger result. Its output is already the complete
// replacement history; applying the old retention builder would discard or
// duplicate provider-retained messages.
func authoritativeReplacement(prefix NativeHistory, result CompactionResult) (ReplacementResult, error) {
	if !result.dedicated() {
		// Compatibility-only path for the old streamed trigger collector and
		// existing emulators. The dedicated endpoint never takes this branch.
		return buildReplacement(prefix, result.Item, ReplacementConfig{Version: replacementPredicateV1})
	}
	if _, err := buildNativeHistoryFromItems(result.Output); err != nil {
		return ReplacementResult{}, compactionProtocolError("invalid_compaction_item")
	}
	source, err := estimateReplacementSource(prefix)
	if err != nil {
		return ReplacementResult{}, err
	}
	resultEstimate, err := estimateHistory(context.Background(), deterministicHistoryEstimator{}, NativeHistory{Items: cloneInputItems(result.Output)})
	if err != nil {
		return ReplacementResult{}, err
	}
	return ReplacementResult{
		Items: cloneInputItems(result.Output), SourceEstimatedTokens: source,
		ResultEstimatedTokens: resultEstimate.Tokens,
	}, nil
}

func (c *NativeContextCoordinator) compactionFailure(ctx context.Context, in NativeContextPrepareInput, key CheckpointKey, reservation Reservation, cause error, preflight nativeContextPreflight) (NativeContextPrepared, error) {
	c.store.Abort(reservation)
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		cooldown := c.config.NativeContext.Compaction.FailureCooldown
		if cooldown <= 0 {
			cooldown = DefaultFailureCooldown
		}
		c.store.MarkFailure(key, c.store.clockNow().Add(cooldown))
	}
	status := compactionStatus(cause)
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		c.telemetry.recordCompaction(compactionTelemetryCanceled, 0)
	} else {
		c.telemetry.recordCompaction(compactionTelemetryProtocol, 0)
	}
	if in.Account.ManagedOAuthEnabled && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests) {
		return NativeContextPrepared{}, &nativeContextAccountError{status: status, cause: cause}
	}
	return c.fallback(ctx, in, cause, preflight)
}

func nativeHistoryBytes(history NativeHistory) int64 {
	if history.TotalBytes > 0 {
		return history.TotalBytes
	}
	var total int64
	for _, item := range history.Items {
		body, err := nativeItemJSON(item)
		if err == nil {
			total += int64(len(body))
		}
	}
	return total
}

func (c *NativeContextCoordinator) fallback(ctx context.Context, in NativeContextPrepareInput, cause error, preflight nativeContextPreflight) (NativeContextPrepared, error) {
	if !preflight.isReady {
		var historyErr error
		preflight.history, historyErr = buildNativeHistory(&in.Call)
		if historyErr != nil {
			if cause != nil {
				return NativeContextPrepared{}, cause
			}
			return NativeContextPrepared{}, historyErr
		}
	}
	if preflight.profile.ModelSlug == "" {
		var profileErr error
		preflight.profile, profileErr = ResolveCompactionModelProfile(c.config.ModelCatalog, in.Model, c.config.NativeContext.Compaction)
		if profileErr != nil {
			return NativeContextPrepared{}, profileErr
		}
	}
	if preflight.estimate.Tokens == 0 && len(preflight.history.Items) > 0 {
		var estimateErr error
		preflight.estimate, estimateErr = estimateHistory(ctx, deterministicHistoryEstimator{}, preflight.history)
		if estimateErr != nil {
			return NativeContextPrepared{}, estimateErr
		}
		preflight.isReady = true
	}
	if preflight.estimate.Tokens >= preflight.profile.HardLimit {
		return NativeContextPrepared{}, fmt.Errorf("%w: %s", ErrNativeContextHardLimit, safeNativeOutcome(cause))
	}
	return NativeContextPrepared{Payload: in.Payload, FallbackToFull: cause != nil, Outcome: "full_history"}, nil
}

func usageEvidence(usage *completedUsage) *NativeUsageEvidence {
	if usage == nil {
		return nil
	}
	evidence := &NativeUsageEvidence{
		InputTokens: int64Value(usage.InputTokens), OutputTokens: int64Value(usage.OutputTokens), TotalTokens: int64Value(usage.TotalTokens),
		UsagePresence: aipapi.UsagePresence{InputTokens: usage.InputTokens != nil, OutputTokens: usage.OutputTokens != nil, TotalTokens: usage.TotalTokens != nil},
		Source:        aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}
	if !evidence.UsagePresence.Any() {
		return nil
	}
	return evidence
}

func compactionUsageEvidence(usage *completedUsage, plan CompactionPlan, replacement ReplacementResult) *NativeUsageEvidence {
	if evidence := usageEvidence(usage); evidence != nil {
		return evidence
	}
	// Prior checkpoint usage describes an earlier provider request. It must never
	// be emitted again as if this compaction attempt incurred that charge.
	return &NativeUsageEvidence{
		InputTokens: plan.EffectiveTokens, OutputTokens: replacement.ResultEstimatedTokens,
		TotalTokens:   plan.EffectiveTokens + replacement.ResultEstimatedTokens,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Source:        aipapi.UsageSourceLocalEstimator, Authority: aipapi.UsageAuthorityEstimated,
	}
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func safeNativeOutcome(err error) string {
	if err == nil {
		return "context_limit"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "compaction_failed"
}
