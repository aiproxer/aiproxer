package sessionclassification

import (
	"sort"
	"sync"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusCollector exports bounded process state and bounded classification
// observations for session classification.
//
// Label discipline is the collector's core contract (requirements 9.4, 9.5):
//
//   - only closed-enum values and this feature's static evidence codes can
//     become labels; every observation is validated before it is counted and a
//     rejected observation is dropped rather than exported;
//   - only observed label combinations are exported, so a deployment that never
//     classifies presents no classification-specific series at all and only the
//     static store-readiness gauge (requirements 9.6, 10.8);
//   - no session ID, A-leg, raw User-Agent, filename, path, prompt, client
//     metadata, or remote result string is ever retained or exported.
type PrometheusCollector struct {
	mu          sync.RWMutex
	storeReady  bool
	dropped     uint64
	evaluations map[evaluationKey]uint64
	transitions map[transitionKey]uint64
	remotes     map[remoteKey]*remoteSeries
	stores      map[storeKey]uint64

	storeDesc       *prometheus.Desc
	evaluationsDesc *prometheus.Desc
	transitionsDesc *prometheus.Desc
	remoteDesc      *prometheus.Desc
	remoteSeconds   *prometheus.Desc
	storesDesc      *prometheus.Desc
}

type evaluationKey struct {
	mode    featurestate.Mode
	outcome featurestate.EvaluationOutcome
}

type transitionKey struct {
	source     session.ClassificationSource
	confidence session.ConfidenceBand
	evidence   session.EvidenceCode
}

type remoteKey struct {
	outcome featurestate.RemoteOutcome
}

type storeKey struct {
	operation featurestate.StoreOperation
	outcome   featurestate.StoreOutcome
}

type remoteSeries struct {
	count   uint64
	sum     float64
	buckets []uint64
}

var (
	// remoteLatencyBuckets is a fixed, finite latency bound set shared by every
	// remote outcome series: 5ms through 20.48s.
	remoteLatencyBuckets = prometheus.ExponentialBuckets(0.005, 2, 13)

	_ prometheus.Collector  = (*PrometheusCollector)(nil)
	_ featurestate.Observer = (*PrometheusCollector)(nil)
)

// NewPrometheusCollector constructs an unregistered feature-owned collector.
// Registration lifetime stays with generic metrics infrastructure; the collector
// itself belongs to this feature because it owns the bounded vocabulary.
func NewPrometheusCollector() *PrometheusCollector {
	return &PrometheusCollector{
		evaluations: make(map[evaluationKey]uint64),
		transitions: make(map[transitionKey]uint64),
		remotes:     make(map[remoteKey]*remoteSeries),
		stores:      make(map[storeKey]uint64),
		storeDesc: prometheus.NewDesc(
			"aip_session_classification_store_ready",
			"Whether the process-owned session classification store has been initialized.",
			nil,
			nil,
		),
		evaluationsDesc: prometheus.NewDesc(
			"aip_session_classification_evaluations_total",
			"Bounded classification evaluations by closed mode and closed outcome.",
			[]string{"mode", "outcome"},
			nil,
		),
		transitionsDesc: prometheus.NewDesc(
			"aip_session_classification_transitions_total",
			"First accepted positive transitions by bounded source, confidence band, and decisive evidence code.",
			[]string{"source", "confidence", "evidence"},
			nil,
		),
		remoteDesc: prometheus.NewDesc(
			"aip_session_classification_remote_total",
			"Bounded remote classification outcomes. The remote request and response bodies are never recorded.",
			[]string{"outcome"},
			nil,
		),
		remoteSeconds: prometheus.NewDesc(
			"aip_session_classification_remote_seconds",
			"Bounded remote classification latency by closed outcome. The remote request and response bodies are never recorded.",
			[]string{"outcome"},
			nil,
		),
		storesDesc: prometheus.NewDesc(
			"aip_session_classification_store_total",
			"Bounded durable classification state operations by closed operation and closed outcome.",
			[]string{"operation", "outcome"},
			nil,
		),
	}
}

// ObserveEvaluation counts one bounded evaluation outcome.
func (c *PrometheusCollector) ObserveEvaluation(observation featurestate.EvaluationObservation) {
	if c == nil || !featurestate.ValidEvaluationObservation(observation) {
		c.countDropped()
		return
	}
	key := evaluationKey{mode: observation.Mode, outcome: observation.Outcome}
	c.mu.Lock()
	c.evaluations[key]++
	c.mu.Unlock()
}

// ObserveTransition counts one accepted first positive transition.
func (c *PrometheusCollector) ObserveTransition(observation featurestate.TransitionObservation) {
	if c == nil || !featurestate.ValidTransitionObservation(observation) {
		c.countDropped()
		return
	}
	key := transitionKey{
		source:     observation.Source,
		confidence: observation.Confidence,
		evidence:   observation.Evidence,
	}
	c.mu.Lock()
	c.transitions[key]++
	c.mu.Unlock()
}

// ObserveRemote counts one bounded remote outcome and records its bounded
// latency sample.
func (c *PrometheusCollector) ObserveRemote(observation featurestate.RemoteObservation) {
	if c == nil || !featurestate.ValidRemoteObservation(observation) {
		c.countDropped()
		return
	}
	seconds := observation.Latency.Seconds()
	key := remoteKey{outcome: observation.Outcome}
	c.mu.Lock()
	series, ok := c.remotes[key]
	if !ok {
		series = &remoteSeries{buckets: make([]uint64, len(remoteLatencyBuckets))}
		c.remotes[key] = series
	}
	series.count++
	series.sum += seconds
	for i, upper := range remoteLatencyBuckets {
		if seconds <= upper {
			series.buckets[i]++
		}
	}
	c.mu.Unlock()
}

// ObserveStore counts one bounded durable operation result.
func (c *PrometheusCollector) ObserveStore(observation featurestate.StoreObservation) {
	if c == nil || !featurestate.ValidStoreObservation(observation) {
		c.countDropped()
		return
	}
	key := storeKey{operation: observation.Operation, outcome: observation.Outcome}
	c.mu.Lock()
	c.stores[key]++
	c.mu.Unlock()
}

// DroppedObservations reports how many observations were rejected because a
// value fell outside the closed vocabularies. It is a plain counter, not a
// metric family: the rejected value itself is deliberately never retained.
func (c *PrometheusCollector) DroppedObservations() uint64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dropped
}

// countDropped records a rejected observation. A nil collector is ignored so a
// process that never constructed one cannot fail while unwinding.
func (c *PrometheusCollector) countDropped() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.dropped++
	c.mu.Unlock()
}

func (c *PrometheusCollector) setStoreReady(ready bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.storeReady = ready
	c.mu.Unlock()
}

func (c *PrometheusCollector) Describe(ch chan<- *prometheus.Desc) {
	if c == nil {
		return
	}
	for _, desc := range []*prometheus.Desc{
		c.storeDesc, c.evaluationsDesc, c.transitionsDesc, c.remoteDesc, c.remoteSeconds, c.storesDesc,
	} {
		if desc != nil {
			ch <- desc
		}
	}
}

func (c *PrometheusCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil {
		return
	}
	c.mu.RLock()
	ready := c.storeReady
	evaluations := sortedPairs(c.evaluations)
	transitions := sortedTransitions(c.transitions)
	remotes := sortedRemotes(c.remotes)
	stores := sortedStorePairs(c.stores)
	c.mu.RUnlock()

	value := 0.0
	if ready {
		value = 1
	}
	ch <- prometheus.MustNewConstMetric(c.storeDesc, prometheus.GaugeValue, value)
	for _, entry := range evaluations {
		ch <- prometheus.MustNewConstMetric(c.evaluationsDesc, prometheus.CounterValue,
			float64(entry.count), string(entry.key.mode), string(entry.key.outcome))
	}
	for _, entry := range transitions {
		ch <- prometheus.MustNewConstMetric(c.transitionsDesc, prometheus.CounterValue,
			float64(entry.count), string(entry.key.source), string(entry.key.confidence), string(entry.key.evidence))
	}
	for _, entry := range remotes {
		ch <- prometheus.MustNewConstMetric(c.remoteDesc, prometheus.CounterValue,
			float64(entry.series.count), string(entry.key.outcome))
		buckets := make(map[float64]uint64, len(remoteLatencyBuckets))
		for i, upper := range remoteLatencyBuckets {
			buckets[upper] = entry.series.buckets[i]
		}
		ch <- prometheus.MustNewConstHistogram(c.remoteSeconds, entry.series.count, entry.series.sum,
			buckets, string(entry.key.outcome))
	}
	for _, entry := range stores {
		ch <- prometheus.MustNewConstMetric(c.storesDesc, prometheus.CounterValue,
			float64(entry.count), string(entry.key.operation), string(entry.key.outcome))
	}
}

type evaluationEntry struct {
	key   evaluationKey
	count uint64
}

type transitionEntry struct {
	key   transitionKey
	count uint64
}

type remoteEntry struct {
	key    remoteKey
	series remoteSeries
}

type storeEntry struct {
	key   storeKey
	count uint64
}

func sortedPairs(counts map[evaluationKey]uint64) []evaluationEntry {
	entries := make([]evaluationEntry, 0, len(counts))
	for key, count := range counts {
		entries = append(entries, evaluationEntry{key: key, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key.mode != entries[j].key.mode {
			return entries[i].key.mode < entries[j].key.mode
		}
		return entries[i].key.outcome < entries[j].key.outcome
	})
	return entries
}

func sortedTransitions(counts map[transitionKey]uint64) []transitionEntry {
	entries := make([]transitionEntry, 0, len(counts))
	for key, count := range counts {
		entries = append(entries, transitionEntry{key: key, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i].key, entries[j].key
		switch {
		case left.source != right.source:
			return left.source < right.source
		case left.confidence != right.confidence:
			return left.confidence < right.confidence
		default:
			return left.evidence < right.evidence
		}
	})
	return entries
}

func sortedRemotes(series map[remoteKey]*remoteSeries) []remoteEntry {
	entries := make([]remoteEntry, 0, len(series))
	for key, value := range series {
		entries = append(entries, remoteEntry{key: key, series: remoteSeries{
			count: value.count, sum: value.sum, buckets: append([]uint64(nil), value.buckets...),
		}})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key.outcome < entries[j].key.outcome })
	return entries
}

func sortedStorePairs(counts map[storeKey]uint64) []storeEntry {
	entries := make([]storeEntry, 0, len(counts))
	for key, count := range counts {
		entries = append(entries, storeEntry{key: key, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key.operation != entries[j].key.operation {
			return entries[i].key.operation < entries[j].key.operation
		}
		return entries[i].key.outcome < entries[j].key.outcome
	})
	return entries
}
