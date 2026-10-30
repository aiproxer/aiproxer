package observability

import (
	"maps"
	"sync"
	"time"
)

type Sink interface {
	Record(Observation)
}

// Stats accumulates bounded token-accounting observability dimensions in memory.
type Stats struct {
	mu sync.Mutex

	sourceSelections   map[Source]int
	fallbackReasons    map[Reason]int
	unavailableReasons map[Reason]int
	latencies          []time.Duration
	latencySum         time.Duration
	sink               Sink
}

// Snapshot is a point-in-time copy of accumulated observation stats.
type Snapshot struct {
	SourceSelections   map[Source]int
	FallbackReasons    map[Reason]int
	UnavailableReasons map[Reason]int
	LatencyCount       int
	LatencySum         time.Duration
	Latencies          []time.Duration
}

// NewStats creates an empty in-memory stats accumulator.
func NewStats() *Stats {
	return &Stats{
		sourceSelections:   map[Source]int{},
		fallbackReasons:    map[Reason]int{},
		unavailableReasons: map[Reason]int{},
		latencies:          []time.Duration{},
	}
}

// Record adds one validated observation to the accumulator.
func (s *Stats) Record(obs Observation) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.sourceSelections == nil {
		s.sourceSelections = map[Source]int{}
	}
	if s.fallbackReasons == nil {
		s.fallbackReasons = map[Reason]int{}
	}
	if s.unavailableReasons == nil {
		s.unavailableReasons = map[Reason]int{}
	}
	s.sourceSelections[obs.Labels.Source]++
	if obs.FallbackReason != "" {
		s.fallbackReasons[obs.FallbackReason]++
	}
	if obs.UnavailableReason != "" {
		s.unavailableReasons[obs.UnavailableReason]++
	}
	s.latencies = append(s.latencies, obs.Duration)
	s.latencySum += obs.Duration
	sink := s.sink
	s.mu.Unlock()

	if sink != nil {
		sink.Record(obs)
	}
}

// SetSink forwards future observations to sink in addition to the in-memory snapshot.
func (s *Stats) SetSink(sink Sink) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// Snapshot returns a copy of accumulated stats without payload or request identity fields.
func (s *Stats) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{
			SourceSelections:   map[Source]int{},
			FallbackReasons:    map[Reason]int{},
			UnavailableReasons: map[Reason]int{},
			Latencies:          []time.Duration{},
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		SourceSelections:   cloneMap(s.sourceSelections),
		FallbackReasons:    cloneMap(s.fallbackReasons),
		UnavailableReasons: cloneMap(s.unavailableReasons),
		LatencyCount:       len(s.latencies),
		LatencySum:         s.latencySum,
		Latencies:          append([]time.Duration{}, s.latencies...),
	}
}

func cloneMap[K comparable, V any](input map[K]V) map[K]V {
	output := map[K]V{}
	maps.Copy(output, input)
	return output
}
