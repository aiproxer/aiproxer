package codex

import (
	"fmt"
	"sync"
	"time"
)

type nativeTelemetryReason string

const (
	reasoningTelemetryRequested nativeTelemetryReason = "requested"
)

type checkpointTelemetryOutcome string

const (
	checkpointTelemetryHit      checkpointTelemetryOutcome = "hit"
	checkpointTelemetryReuse    checkpointTelemetryOutcome = "reuse"
	checkpointTelemetryMiss     checkpointTelemetryOutcome = "miss"
	checkpointTelemetryMismatch checkpointTelemetryOutcome = "mismatch"
	checkpointTelemetryEvicted  checkpointTelemetryOutcome = "evicted"
	checkpointTelemetryCooldown checkpointTelemetryOutcome = "cooldown"
)

type compactionTelemetryOutcome string

const (
	compactionTelemetryAttempt       compactionTelemetryOutcome = "attempt"
	compactionTelemetrySecondAttempt compactionTelemetryOutcome = "second_attempt"
	compactionTelemetrySuccess       compactionTelemetryOutcome = "success"
	compactionTelemetryProtocol      compactionTelemetryOutcome = "protocol_failure"
	compactionTelemetryCanceled      compactionTelemetryOutcome = "canceled"
	compactionTelemetryHardFail      compactionTelemetryOutcome = "hard_failure"
	compactionTelemetryRewriteFail   compactionTelemetryOutcome = "rewrite_mismatch"
	compactionTelemetryCommit        compactionTelemetryOutcome = "checkpoint_commit"
)

// nativeContextTelemetry is a connector-private bounded snapshot. It is kept
// local because independent connector modules cannot use root runtime metrics.
type nativeContextTelemetry struct {
	mu  sync.Mutex
	now func() time.Time

	ContextBeforeTokens int64
	ContextAfterTokens  int64
	ContextBeforeBytes  int64
	ContextAfterBytes   int64

	ReasoningRequested int64

	CheckpointHits       int64
	CheckpointReuseHits  int64
	CheckpointMisses     int64
	CheckpointMismatches int64
	CheckpointEvictions  int64
	CooldownSkips        int64
	HardFailures         int64

	CompactionAttempts       int64
	CompactionSecondAttempts int64
	CompactionSuccesses      int64
	CompactionProtocolFails  int64
	CompactionCanceled       int64
	CompactionHardFailures   int64
	CompactionRewriteFails   int64
	CheckpointCommits        int64
	CompactionUsageTokens    int64
	CompactionLatencyMillis  int64
}

func newNativeContextTelemetry() *nativeContextTelemetry {
	return newNativeContextTelemetryWithClock(time.Now)
}

func newNativeContextTelemetryWithClock(now func() time.Time) *nativeContextTelemetry {
	if now == nil {
		now = time.Now
	}
	return &nativeContextTelemetry{now: now}
}

func (t *nativeContextTelemetry) recordContext(beforeTokens, afterTokens, beforeBytes, afterBytes int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ContextBeforeTokens, t.ContextAfterTokens = nonNegative(beforeTokens), nonNegative(afterTokens)
	t.ContextBeforeBytes, t.ContextAfterBytes = nonNegative(beforeBytes), nonNegative(afterBytes)
	t.mu.Unlock()
}

func (t *nativeContextTelemetry) recordReasoning(outcome nativeTelemetryReason) {
	if t == nil {
		return
	}
	t.mu.Lock()
	switch outcome {
	case reasoningTelemetryRequested:
		t.ReasoningRequested++
	}
	t.mu.Unlock()
}

func (t *nativeContextTelemetry) recordCheckpoint(outcome checkpointTelemetryOutcome) {
	if t == nil {
		return
	}
	t.mu.Lock()
	switch outcome {
	case checkpointTelemetryHit:
		t.CheckpointHits++
	case checkpointTelemetryReuse:
		t.CheckpointReuseHits++
	case checkpointTelemetryMiss:
		t.CheckpointMisses++
	case checkpointTelemetryMismatch:
		t.CheckpointMismatches++
	case checkpointTelemetryEvicted:
		t.CheckpointEvictions++
	case checkpointTelemetryCooldown:
		t.CooldownSkips++
	}
	t.mu.Unlock()
}

func (t *nativeContextTelemetry) recordCompaction(outcome compactionTelemetryOutcome, usageTokens int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	switch outcome {
	case compactionTelemetryAttempt:
		t.CompactionAttempts++
	case compactionTelemetrySecondAttempt:
		t.CompactionSecondAttempts++
	case compactionTelemetrySuccess:
		t.CompactionSuccesses++
	case compactionTelemetryProtocol:
		t.CompactionProtocolFails++
	case compactionTelemetryCanceled:
		t.CompactionCanceled++
	case compactionTelemetryHardFail:
		t.CompactionHardFailures++
		t.HardFailures++
	case compactionTelemetryRewriteFail:
		t.CompactionRewriteFails++
	case compactionTelemetryCommit:
		t.CheckpointCommits++
	}
	if usageTokens > 0 {
		t.CompactionUsageTokens += usageTokens
	}
	t.mu.Unlock()
}

func (t *nativeContextTelemetry) recordLatency(start time.Time) {
	if t == nil {
		return
	}
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	latency := max(now.Sub(start).Milliseconds(), 0)
	t.mu.Lock()
	t.CompactionLatencyMillis = latency
	t.mu.Unlock()
}

type nativeContextTelemetrySnapshot struct {
	ContextBeforeTokens, ContextAfterTokens        int64
	ContextBeforeBytes, ContextAfterBytes          int64
	ReasoningRequested                             int64
	CheckpointHits, CheckpointReuseHits            int64
	CheckpointMisses                               int64
	CheckpointMismatches, CheckpointEvictions      int64
	CooldownSkips, HardFailures                    int64
	CompactionAttempts, CompactionSecondAttempts   int64
	CompactionSuccesses                            int64
	CompactionProtocolFails, CompactionCanceled    int64
	CompactionHardFailures, CompactionRewriteFails int64
	CheckpointCommits, CompactionUsageTokens       int64
	CompactionLatencyMillis                        int64
}

func (t *nativeContextTelemetry) snapshot() nativeContextTelemetrySnapshot {
	if t == nil {
		return nativeContextTelemetrySnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return nativeContextTelemetrySnapshot{
		ContextBeforeTokens: t.ContextBeforeTokens, ContextAfterTokens: t.ContextAfterTokens,
		ContextBeforeBytes: t.ContextBeforeBytes, ContextAfterBytes: t.ContextAfterBytes,
		ReasoningRequested: t.ReasoningRequested, CheckpointHits: t.CheckpointHits, CheckpointReuseHits: t.CheckpointReuseHits,
		CheckpointMisses: t.CheckpointMisses, CheckpointMismatches: t.CheckpointMismatches,
		CheckpointEvictions: t.CheckpointEvictions, CooldownSkips: t.CooldownSkips,
		HardFailures: t.HardFailures, CompactionAttempts: t.CompactionAttempts, CompactionSecondAttempts: t.CompactionSecondAttempts,
		CompactionSuccesses: t.CompactionSuccesses, CompactionProtocolFails: t.CompactionProtocolFails,
		CompactionCanceled: t.CompactionCanceled, CompactionHardFailures: t.CompactionHardFailures,
		CompactionRewriteFails: t.CompactionRewriteFails, CheckpointCommits: t.CheckpointCommits,
		CompactionUsageTokens: t.CompactionUsageTokens, CompactionLatencyMillis: t.CompactionLatencyMillis,
	}
}

func (s nativeContextTelemetrySnapshot) String() string {
	return fmt.Sprintf("context_before_tokens=%d context_after_tokens=%d context_before_bytes=%d context_after_bytes=%d reasoning_requested=%d checkpoint_hits=%d checkpoint_reuse_hits=%d checkpoint_misses=%d checkpoint_mismatches=%d checkpoint_evictions=%d cooldown_skips=%d hard_failures=%d compaction_attempts=%d compaction_second_attempts=%d compaction_successes=%d compaction_protocol_failures=%d compaction_canceled=%d compaction_hard_failures=%d compaction_rewrite_mismatches=%d checkpoint_commits=%d compaction_usage_tokens=%d compaction_latency_ms=%d", s.ContextBeforeTokens, s.ContextAfterTokens, s.ContextBeforeBytes, s.ContextAfterBytes, s.ReasoningRequested, s.CheckpointHits, s.CheckpointReuseHits, s.CheckpointMisses, s.CheckpointMismatches, s.CheckpointEvictions, s.CooldownSkips, s.HardFailures, s.CompactionAttempts, s.CompactionSecondAttempts, s.CompactionSuccesses, s.CompactionProtocolFails, s.CompactionCanceled, s.CompactionHardFailures, s.CompactionRewriteFails, s.CheckpointCommits, s.CompactionUsageTokens, s.CompactionLatencyMillis)
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
