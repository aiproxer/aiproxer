// Package observability provides the content-free diagnostics seam for the
// compaction-continuity feature. It records bounded statuses and measurements
// only; queue depth, token usage, cost and accounting remain owned by the
// scheduler and billing observability surfaces.
package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Stage identifies one feature-local lifecycle seam. The finite vocabulary is
// intentional: adding a provider/model/branch value here would make metrics
// cardinality and privacy depend on request content.
type Stage string

const (
	StagePreview       Stage = "preview"
	StageEvent         Stage = "event"
	StageCarrier       Stage = "carrier"
	StageEligibility   Stage = "eligibility"
	StagePreviewIntent Stage = "preview_intent"
	StageJob           Stage = "job"
	StageCapsule       Stage = "capsule"
	StageBarrier       Stage = "barrier"
	StageAugmentation  Stage = "augmentation"
	StageReinjection   Stage = "reinjection"
	StageWatermark     Stage = "watermark"
	StageCallback      Stage = "callback"
)

// Outcome identifies a finite feature outcome. Errors are intentionally
// represented by statuses rather than error text.
type Outcome string

const (
	OutcomeObserved       Outcome = "observed"
	OutcomeCandidate      Outcome = "candidate"
	OutcomeCommitted      Outcome = "committed"
	OutcomeSkipped        Outcome = "skipped"
	OutcomeMatched        Outcome = "matched"
	OutcomeUnmatched      Outcome = "unmatched"
	OutcomeEligible       Outcome = "eligible"
	OutcomeIneligible     Outcome = "ineligible"
	OutcomeCreated        Outcome = "created"
	OutcomeBound          Outcome = "bound"
	OutcomeExpired        Outcome = "expired"
	OutcomeSubmitted      Outcome = "submitted"
	OutcomeCoalesced      Outcome = "coalesced"
	OutcomeSaturated      Outcome = "saturated"
	OutcomeCanceled       Outcome = "canceled"
	OutcomeCompleted      Outcome = "completed"
	OutcomeFailed         Outcome = "failed"
	OutcomeDenied         Outcome = "denied"
	OutcomeInvalid        Outcome = "invalid"
	OutcomeStale          Outcome = "stale"
	OutcomeConflict       Outcome = "conflict"
	OutcomeDigestMismatch Outcome = "digest_mismatch"
	OutcomeTimeout        Outcome = "timeout"
	OutcomeRollback       Outcome = "rollback"
	OutcomePanic          Outcome = "panic"
	OutcomeRestored       Outcome = "restored"
	OutcomePending        Outcome = "pending"
	OutcomeReleased       Outcome = "released"
	OutcomeOpaque         Outcome = "opaque"
	OutcomeRejected       Outcome = "rejected"
)

// Observation is deliberately content-free. CorrelationHash must be produced
// with HashID; raw BranchKey, prompt, output and capsule fields have no place
// in this contract. Count is a positive delta and the other measurements are
// bounded by the recorder.
type Observation struct {
	Stage           Stage
	Outcome         Outcome
	Evidence        string
	RuleID          string
	Phase           string
	CorrelationHash string
	Revision        uint64
	SizeBytes       int
	FactCount       int
	Duration        time.Duration
	Count           uint64
}

// Sink consumes feature-local content-free observations. Implementations must
// be non-blocking or bounded; callers isolate sink failures from primary flow.
type Sink interface {
	Observe(Observation)
}

// Func adapts a function to Sink and is convenient for deterministic tests.
type Func func(Observation)

func (f Func) Observe(sample Observation) {
	if f != nil {
		f(sample)
	}
}

// HashID creates the only correlation representation accepted by the
// recorder. It is useful for transaction/job IDs without exposing their raw
// values to diagnostics.
func HashID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// BoundedID keeps a non-secret enum or rule identifier safe for a direct Sink
// implementation. Correlation values should use HashID instead.
func BoundedID(value string) string { return boundedLabel(value) }

// Nop is the default sink. It keeps the feature's observability calls
// explicit without introducing global state when metrics are not composed.
type Nop struct{}

func (Nop) Observe(Observation) {}

// Recorder aggregates observations into a bounded set of content-free
// series. It is suitable for deterministic tests and small in-process
// diagnostics; production metric adapters may implement Sink directly.
type Recorder struct {
	mu       sync.Mutex
	max      int
	series   map[string]Observation
	sequence []string
}

// NewRecorder creates a bounded recorder. Non-positive bounds use 64 series.
func NewRecorder(maxSeries int) *Recorder {
	if maxSeries <= 0 {
		maxSeries = 64
	}
	return &Recorder{max: maxSeries, series: make(map[string]Observation, maxSeries)}
}

// Observe records one bounded sample. Invalid dimensions are normalized to
// unknown values and never cause an error or callback failure.
func (r *Recorder) Observe(sample Observation) {
	if r == nil {
		return
	}
	sample = normalize(sample)
	key := seriesKey(sample)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.max <= 0 {
		r.max = 64
	}
	if r.series == nil {
		r.series = make(map[string]Observation, r.max)
	}
	if current, ok := r.series[key]; ok {
		// One correlation cannot represent an aggregate containing multiple
		// observations. Clear it as soon as the series receives a second sample.
		current.CorrelationHash = ""
		current.Count += sample.Count
		if current.Count < sample.Count { // saturate on overflow
			current.Count = ^uint64(0)
		}
		if sample.Revision > current.Revision {
			current.Revision = sample.Revision
		}
		if sample.SizeBytes > current.SizeBytes {
			current.SizeBytes = sample.SizeBytes
		}
		if sample.FactCount > current.FactCount {
			current.FactCount = sample.FactCount
		}
		if sample.Duration > current.Duration {
			current.Duration = sample.Duration
		}
		r.series[key] = current
		return
	}
	if len(r.sequence) >= r.max {
		return
	}
	r.series[key] = sample
	r.sequence = append(r.sequence, key)
}

// Snapshot returns a deterministic copy of the currently retained series.
func (r *Recorder) Snapshot() []Observation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Observation, 0, len(r.sequence))
	for _, key := range r.sequence {
		out = append(out, r.series[key])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		if out[i].Outcome != out[j].Outcome {
			return out[i].Outcome < out[j].Outcome
		}
		if out[i].Evidence != out[j].Evidence {
			return out[i].Evidence < out[j].Evidence
		}
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Phase < out[j].Phase
	})
	return out
}

func normalize(sample Observation) Observation {
	if !validStage(sample.Stage) {
		sample.Stage = "unknown"
	}
	if !validOutcome(sample.Outcome) {
		sample.Outcome = "unknown"
	}
	sample.Evidence = boundedLabel(sample.Evidence)
	sample.RuleID = boundedLabel(sample.RuleID)
	sample.Phase = boundedLabel(sample.Phase)
	if sample.CorrelationHash != "" && !isHash(sample.CorrelationHash) {
		sample.CorrelationHash = HashID(sample.CorrelationHash)
	}
	if sample.Revision > 1_000_000_000 {
		sample.Revision = 1_000_000_000
	}
	if sample.SizeBytes < 0 {
		sample.SizeBytes = 0
	}
	if sample.SizeBytes > 1<<30 {
		sample.SizeBytes = 1 << 30
	}
	if sample.FactCount < 0 {
		sample.FactCount = 0
	}
	if sample.FactCount > 1_000_000 {
		sample.FactCount = 1_000_000
	}
	if sample.Duration < 0 {
		sample.Duration = 0
	}
	if sample.Duration > 24*time.Hour {
		sample.Duration = 24 * time.Hour
	}
	if sample.Count == 0 {
		sample.Count = 1
	}
	return sample
}

func boundedLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		end := 0
		for end < len(value) {
			_, size := utf8.DecodeRuneInString(value[end:])
			if end+size > 64 {
				break
			}
			end += size
		}
		value = value[:end]
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "unknown"
		}
	}
	return value
}

func isHash(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func validStage(value Stage) bool {
	switch value {
	case StagePreview, StageEvent, StageCarrier, StageEligibility, StagePreviewIntent, StageJob, StageCapsule, StageBarrier, StageAugmentation, StageReinjection, StageWatermark, StageCallback:
		return true
	default:
		return false
	}
}

func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeObserved, OutcomeCandidate, OutcomeCommitted, OutcomeSkipped, OutcomeMatched, OutcomeUnmatched, OutcomeEligible, OutcomeIneligible, OutcomeCreated, OutcomeBound, OutcomeExpired, OutcomeSubmitted, OutcomeCoalesced, OutcomeSaturated, OutcomeCanceled, OutcomeCompleted, OutcomeFailed, OutcomeDenied, OutcomeInvalid, OutcomeStale, OutcomeConflict, OutcomeDigestMismatch, OutcomeTimeout, OutcomeRollback, OutcomePanic, OutcomeRestored, OutcomePending, OutcomeReleased, OutcomeOpaque, OutcomeRejected:
		return true
	default:
		return false
	}
}

func seriesKey(sample Observation) string {
	return strings.Join([]string{string(sample.Stage), string(sample.Outcome), sample.Evidence, sample.RuleID, sample.Phase}, "\x00")
}
