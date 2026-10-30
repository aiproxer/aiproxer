package reasoningpreservation

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Telemetry accumulates content-safe aggregate outcome counters for one feature instance.
// All byte counters are content-free, outcome-whitelisted via isKnownOutcome, and
// individually clamped to hard ceilings to prevent unbounded aggregation.
// Concurrency: single registry keyed by SafeOutcome to *outcomeBuckets with
// lock-free per-outcome atomic updates; accumulation is saturation-safe.
type Telemetry struct {
	buckets sync.Map // SafeOutcome -> *outcomeBuckets
}

// outcomeBuckets holds per-outcome counters as independent atomics.
type outcomeBuckets struct {
	count        atomic.Int64
	rawBytes     atomic.Int64
	decodedBytes atomic.Int64
	savedBytes   atomic.Int64
	sourceBytes  atomic.Int64
	latencyNanos atomic.Int64
}

// bucketView is a point-in-time copy of one outcome's counters.
type bucketView struct {
	count   int64
	raw     int64
	decoded int64
	saved   int64
	source  int64
	latency int64
}

// CompressionMeasurements is a content-free snapshot of per-outcome byte measurements.
type CompressionMeasurements struct {
	Counts       map[SafeOutcome]int64
	RawBytes     map[SafeOutcome]int64
	DecodedBytes map[SafeOutcome]int64
	SavedBytes   map[SafeOutcome]int64
}

// ShadowEvaluation is the bounded content-free evaluation snapshot for shadow mode.
// It aggregates source/raw/decoded/saved bytes and ratios, plus per-outcome counts and latency.
// No economic/money calculation is performed; billing remains authoritative via existing
// auxiliary BillingCallID / usage surfaces. All numeric fields are bounded.
type ShadowEvaluation struct {
	Counts       map[SafeOutcome]int64
	SourceBytes  map[SafeOutcome]int64
	RawBytes     map[SafeOutcome]int64
	DecodedBytes map[SafeOutcome]int64
	SavedBytes   map[SafeOutcome]int64
	Latency      map[SafeOutcome]time.Duration
	TotalCount   int64
	TotalSource  int64
	TotalRaw     int64
	TotalDecoded int64
	TotalSaved   int64
	// Ratio is hypothetical saved/source when TotalSource>0, else 0. No claim of semantic equivalence.
	SavingsRatio     float64
	CompressionRatio float64
	// AvgLatency is TotalLatency / TotalCount for outcomes with latency if TotalCount>0.
	AvgLatency time.Duration
}

func NewTelemetry() *Telemetry {
	return &Telemetry{}
}

// discardBuckets absorbs writes if getBucket ever observes a foreign value
// stored under an outcome key, which cannot happen today: buckets is unexported
// and only getBucket writes to it, always *outcomeBuckets.
var discardBuckets outcomeBuckets

// getBucket returns the bucket for outcome, creating if absent.
func (t *Telemetry) getBucket(outcome SafeOutcome) *outcomeBuckets {
	v, _ := t.buckets.LoadOrStore(outcome, &outcomeBuckets{})
	if b, ok := v.(*outcomeBuckets); ok && b != nil {
		return b
	}
	// Never overwrite the existing entry: replacing it here would drop its
	// accumulated counters. Writes to discardBuckets are counted by nothing
	// and harm no one.
	return &discardBuckets
}

// addSaturating adds delta to addr with saturation at MaxInt64/MinInt64.
func addSaturating(addr *atomic.Int64, delta int64) {
	if delta == 0 {
		return
	}
	for {
		cur := addr.Load()
		next := saturatingAddInt64(cur, delta)
		if addr.CompareAndSwap(cur, next) {
			return
		}
	}
}

// views returns a point-in-time copy of all buckets; single registry Range site.
func (t *Telemetry) views() map[SafeOutcome]bucketView {
	out := make(map[SafeOutcome]bucketView)
	if t == nil {
		return out
	}
	t.buckets.Range(func(key, value any) bool {
		o, ok := key.(SafeOutcome)
		if !ok {
			return true
		}
		b, ok := value.(*outcomeBuckets)
		if !ok || b == nil {
			return true
		}
		out[o] = bucketView{
			count:   b.count.Load(),
			raw:     b.rawBytes.Load(),
			decoded: b.decodedBytes.Load(),
			saved:   b.savedBytes.Load(),
			source:  b.sourceBytes.Load(),
			latency: b.latencyNanos.Load(),
		}
		return true
	})
	return out
}

func (t *Telemetry) Record(outcome SafeOutcome, counts map[string]int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	b := t.getBucket(outcome)
	addSaturating(&b.count, 1)
	if counts != nil {
		if v, ok := counts["bytes"]; ok && v > 0 {
			if v > HardRawOutputCeiling {
				v = HardRawOutputCeiling
			}
			addSaturating(&b.rawBytes, int64(v))
		}
		if v, ok := counts["sourceBytes"]; ok && v > 0 {
			if v > HardRawOutputCeiling {
				v = HardRawOutputCeiling
			}
			addSaturating(&b.sourceBytes, int64(v))
		}
	}
}

// RecordCompression records a compression-safe outcome with content-free byte count.
// It records raw bytes once in the dedicated rawBytes bucket (single count, bounded to HardRawOutputCeiling).
// BytesSnapshot is an alias to RawBytesSnapshot for backward compat.
// New code should prefer RecordCompressionMeasurement or RecordShadowMeasurement.
func (t *Telemetry) RecordCompression(outcome SafeOutcome, rawBytes int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	// Delegate to measurement path to share clamping and saturation logic.
	t.RecordCompressionMeasurement(outcome, rawBytes, 0, 0)
}

// RecordCompressionMeasurement records an outcome with explicit raw/decoded/saved bytes, each clamped.
// Raw is stored once in rawBytes; Decoded bounded to HardCompressionMaxSurrogateBytes, saved to HardRawOutputCeiling.
// It is content-free and outcome-whitelisted via isKnownOutcome.
func (t *Telemetry) RecordCompressionMeasurement(outcome SafeOutcome, rawBytes, decodedBytes, savedBytes int) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	b := t.getBucket(outcome)
	addSaturating(&b.count, 1)
	if rawBytes > 0 {
		if rawBytes > HardRawOutputCeiling {
			rawBytes = HardRawOutputCeiling
		}
		addSaturating(&b.rawBytes, int64(rawBytes))
	}
	if decodedBytes > 0 {
		if decodedBytes > HardCompressionMaxSurrogateBytes {
			decodedBytes = HardCompressionMaxSurrogateBytes
		}
		addSaturating(&b.decodedBytes, int64(decodedBytes))
	}
	if savedBytes > 0 {
		if savedBytes > HardRawOutputCeiling {
			savedBytes = HardRawOutputCeiling
		}
		addSaturating(&b.savedBytes, int64(savedBytes))
	}
}

// RecordShadowMeasurement records a full shadow evaluation sample with source/raw/decoded/saved and latency.
// All byte values are bounded; latency is bounded to HardCompressionTimeout per sample.
// No money calculation. Content-free via isKnownOutcome.
func (t *Telemetry) RecordShadowMeasurement(outcome SafeOutcome, sourceBytes, rawBytes, decodedBytes, savedBytes int, latency time.Duration) {
	if t == nil || !isKnownOutcome(outcome) {
		return
	}
	t.RecordCompressionMeasurement(outcome, rawBytes, decodedBytes, savedBytes)
	if sourceBytes > 0 {
		if sourceBytes > HardRawOutputCeiling {
			sourceBytes = HardRawOutputCeiling
		}
		b := t.getBucket(outcome)
		addSaturating(&b.sourceBytes, int64(sourceBytes))
	}
	if latency > 0 {
		if latency > HardCompressionTimeout {
			latency = HardCompressionTimeout
		}
		b := t.getBucket(outcome)
		addSaturating(&b.latencyNanos, int64(latency))
	}
}

func (t *Telemetry) Snapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.count > 0 {
			out[o] = v.count
		}
	}
	return out
}

// BytesSnapshot returns aggregate raw bytes per outcome (alias to RawBytesSnapshot, single count, legacy compat).
func (t *Telemetry) BytesSnapshot() map[SafeOutcome]int64 {
	return t.RawBytesSnapshot()
}

// RawBytesSnapshot returns raw bytes per outcome (dedicated, bounded).
func (t *Telemetry) RawBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.raw > 0 {
			out[o] = v.raw
		}
	}
	return out
}

// DecodedBytesSnapshot returns decoded bytes per outcome.
func (t *Telemetry) DecodedBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.decoded > 0 {
			out[o] = v.decoded
		}
	}
	return out
}

// SavedBytesSnapshot returns saved bytes per outcome.
func (t *Telemetry) SavedBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.saved > 0 {
			out[o] = v.saved
		}
	}
	return out
}

// SourceBytesSnapshot returns source bytes per outcome (bounded).
func (t *Telemetry) SourceBytesSnapshot() map[SafeOutcome]int64 {
	out := make(map[SafeOutcome]int64)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.source > 0 {
			out[o] = v.source
		}
	}
	return out
}

// LatencySnapshot returns total latency per outcome (bounded).
func (t *Telemetry) LatencySnapshot() map[SafeOutcome]time.Duration {
	out := make(map[SafeOutcome]time.Duration)
	if t == nil {
		return out
	}
	for o, v := range t.views() {
		if v.latency > 0 {
			out[o] = time.Duration(v.latency)
		}
	}
	return out
}

// CompressionMeasurementsSnapshot returns per-outcome counts and byte measurements.
func (t *Telemetry) CompressionMeasurementsSnapshot() CompressionMeasurements {
	if t == nil {
		return CompressionMeasurements{
			Counts:       map[SafeOutcome]int64{},
			RawBytes:     map[SafeOutcome]int64{},
			DecodedBytes: map[SafeOutcome]int64{},
			SavedBytes:   map[SafeOutcome]int64{},
		}
	}
	vw := t.views()
	counts := make(map[SafeOutcome]int64)
	raw := make(map[SafeOutcome]int64)
	dec := make(map[SafeOutcome]int64)
	saved := make(map[SafeOutcome]int64)
	for o, v := range vw {
		if v.count > 0 {
			counts[o] = v.count
		}
		if v.raw > 0 {
			raw[o] = v.raw
		}
		if v.decoded > 0 {
			dec[o] = v.decoded
		}
		if v.saved > 0 {
			saved[o] = v.saved
		}
	}
	return CompressionMeasurements{
		Counts:       counts,
		RawBytes:     raw,
		DecodedBytes: dec,
		SavedBytes:   saved,
	}
}

// ShadowEvaluationSnapshot returns a bounded evaluation computing source/raw/decoded/saved totals and ratios.
// Ratios are hypothetical savings only; no money calculation; latency is averaged if available.
func (t *Telemetry) ShadowEvaluationSnapshot() ShadowEvaluation {
	if t == nil {
		return ShadowEvaluation{
			Counts:       map[SafeOutcome]int64{},
			SourceBytes:  map[SafeOutcome]int64{},
			RawBytes:     map[SafeOutcome]int64{},
			DecodedBytes: map[SafeOutcome]int64{},
			SavedBytes:   map[SafeOutcome]int64{},
			Latency:      map[SafeOutcome]time.Duration{},
		}
	}
	vw := t.views()
	counts := make(map[SafeOutcome]int64)
	src := make(map[SafeOutcome]int64)
	raw := make(map[SafeOutcome]int64)
	dec := make(map[SafeOutcome]int64)
	saved := make(map[SafeOutcome]int64)
	lat := make(map[SafeOutcome]time.Duration)
	var totalCount, totalSource, totalRaw, totalDecoded, totalSaved int64
	var totalLatency time.Duration
	for o, v := range vw {
		if v.count > 0 {
			counts[o] = v.count
			totalCount = saturatingAddInt64(totalCount, v.count)
		}
		if v.source > 0 {
			src[o] = v.source
			totalSource = saturatingAddInt64(totalSource, v.source)
		}
		if v.raw > 0 {
			raw[o] = v.raw
			totalRaw = saturatingAddInt64(totalRaw, v.raw)
		}
		if v.decoded > 0 {
			dec[o] = v.decoded
			totalDecoded = saturatingAddInt64(totalDecoded, v.decoded)
		}
		if v.saved > 0 {
			saved[o] = v.saved
			totalSaved = saturatingAddInt64(totalSaved, v.saved)
		}
		if v.latency > 0 {
			d := time.Duration(v.latency)
			lat[o] = d
			totalLatency = saturatingAddDuration(totalLatency, d)
		}
	}
	// Totals already saturating, ratios clamp 0..1.
	var savingsRatio, compressionRatio float64
	if totalSource > 0 {
		savingsRatio = float64(totalSaved) / float64(totalSource)
		compressionRatio = float64(totalDecoded) / float64(totalSource)
		if savingsRatio < 0 {
			savingsRatio = 0
		}
		if savingsRatio > 1 {
			savingsRatio = 1
		}
		if compressionRatio < 0 {
			compressionRatio = 0
		}
		if compressionRatio > 1 {
			compressionRatio = 1
		}
	}
	var avgLatency time.Duration
	if totalCount > 0 {
		avgLatency = time.Duration(int64(totalLatency) / totalCount)
	}
	return ShadowEvaluation{
		Counts:           counts,
		SourceBytes:      src,
		RawBytes:         raw,
		DecodedBytes:     dec,
		SavedBytes:       saved,
		Latency:          lat,
		TotalCount:       totalCount,
		TotalSource:      totalSource,
		TotalRaw:         totalRaw,
		TotalDecoded:     totalDecoded,
		TotalSaved:       totalSaved,
		SavingsRatio:     savingsRatio,
		CompressionRatio: compressionRatio,
		AvgLatency:       avgLatency,
	}
}

func saturatingAddInt64(a, b int64) int64 {
	if b > 0 && a > (1<<63-1)-b {
		return 1<<63 - 1
	}
	if b < 0 && a < (-1<<63)-b {
		return -1 << 63
	}
	return a + b
}

func saturatingAddDuration(a, b time.Duration) time.Duration {
	ai, bi := int64(a), int64(b)
	if bi > 0 && ai > (1<<63-1)-bi {
		return time.Duration(1<<63 - 1)
	}
	if bi < 0 && ai < (-1<<63)-bi {
		return time.Duration(-1 << 63)
	}
	return a + b
}

// SafeInventory is the process-local, content-safe diagnostics projection for one enabled instance.
type SafeInventory struct {
	Enabled            bool             `json:"enabled"`
	Action             string           `json:"action"`
	CatalogVersion     string           `json:"catalog_version,omitempty"`
	RuleIDs            []string         `json:"rule_ids,omitempty"`
	RuleCount          int              `json:"rule_count"`
	TTL                string           `json:"ttl"`
	MaxTurnsPerSession int              `json:"max_turns_per_session"`
	MaxBytesPerTurn    int              `json:"max_reasoning_bytes_per_turn"`
	MaxSessionBytes    int              `json:"max_session_bytes"`
	ProcessLocal       bool             `json:"process_local"`
	AggregateCounters  map[string]int64 `json:"aggregate_counters"`
}

// BuildSafeInventory projects config + aggregate counters without payloads, anchors, or session partitions.
func BuildSafeInventory(cfg Config, tel *Telemetry) SafeInventory {
	if strings.TrimSpace(cfg.Action) == "" {
		return SafeInventory{AggregateCounters: map[string]int64{}}
	}
	ids := make([]string, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if id := sanitizeRuleID(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	agg := map[string]int64{}
	if tel != nil {
		for o, n := range tel.Snapshot() {
			agg[string(o)] = n
		}
	}
	inv := SafeInventory{
		Enabled:            true,
		Action:             cfg.Action,
		RuleIDs:            ids,
		RuleCount:          len(ids),
		TTL:                cfg.State.TTL.String(),
		MaxTurnsPerSession: cfg.State.MaxTurnsPerSession,
		MaxBytesPerTurn:    cfg.State.MaxReasoningBytesPerTurn,
		MaxSessionBytes:    cfg.State.MaxSessionBytes,
		ProcessLocal:       true,
		AggregateCounters:  agg,
	}
	if cfg.UseBuiltinCatalog {
		inv.CatalogVersion = BuiltinCatalogVersion
	}
	return inv
}
