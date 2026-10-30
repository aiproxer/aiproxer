package traffic

import (
	"context"
	"slices"
)

// Redactor runs on the observation path after privileged raw capture and before general
// observers (design §11). Implementations must be deterministic for tests; errors are ignored
// by ApplyRedactors (fail-open) so request execution is not blocked.
type Redactor interface {
	ID() string
	Redact(ctx context.Context, leg Leg, meta CaptureMeta, body []byte) ([]byte, error)
}

// ChainObservers returns an Observer that invokes each non-nil child in order. Handler errors
// are ignored (fail-open; design §17 traffic observation).
func ChainObservers(obs ...Observer) Observer {
	var flat []Observer
	for _, o := range obs {
		if o == nil {
			continue
		}
		flat = append(flat, o)
	}
	switch len(flat) {
	case 0:
		return NoopObserver{}
	case 1:
		return flat[0]
	default:
		return chainObserver(flat)
	}
}

type chainObserver []Observer

func (c chainObserver) OnObservation(ctx context.Context, ev Observation) error {
	for _, o := range c {
		if o == nil {
			continue
		}
		_ = o.OnObservation(ctx, ev)
	}
	return nil
}

// MultiRawCapture fans out verbatim payloads to each non-nil sink. Errors are ignored (fail-open).
func MultiRawCapture(sinks ...RawCaptureSink) RawCaptureSink {
	var flat []RawCaptureSink
	for _, s := range sinks {
		if s == nil {
			continue
		}
		if _, ok := s.(DisabledRawCapture); ok {
			continue
		}
		flat = append(flat, s)
	}
	switch len(flat) {
	case 0:
		return DisabledRawCapture{}
	case 1:
		return flat[0]
	default:
		return multiRawSink(flat)
	}
}

type multiRawSink []RawCaptureSink

func (m multiRawSink) WriteRaw(ctx context.Context, leg Leg, meta CaptureMeta, payload []byte) error {
	for _, s := range m {
		if s == nil {
			continue
		}
		_ = s.WriteRaw(ctx, leg, meta, payload)
	}
	return nil
}

// ApplyRedactors runs redactors in registration order. On error the last good payload is kept
// (fail-open). Nil redactors are skipped.
func ApplyRedactors(ctx context.Context, leg Leg, meta CaptureMeta, body []byte, redactors []Redactor) []byte {
	out := slices.Clone(body)
	for _, r := range redactors {
		if r == nil {
			continue
		}
		next, err := r.Redact(ctx, leg, meta, out)
		if err != nil {
			continue
		}
		out = next
	}
	return out
}

// MaterializeSortedRedactors returns a defensive copy sorted by the same contract as other
// extension stages: ascending Priority, then ID, then registration index (design §17).
func MaterializeSortedRedactors(redactors []Redactor) []Redactor {
	type tagged struct {
		r        Redactor
		priority int
		id       string
		idx      int
	}
	var xs []tagged
	for i, r := range redactors {
		if r == nil {
			continue
		}
		pri, id := 0, r.ID()
		if o, ok := r.(interface{ Priority() int }); ok {
			pri = o.Priority()
		}
		xs = append(xs, tagged{r: r, priority: pri, id: id, idx: i})
	}
	slices.SortFunc(xs, func(a, b tagged) int {
		if a.priority != b.priority {
			return a.priority - b.priority
		}
		if a.id != b.id {
			if a.id < b.id {
				return -1
			}
			if a.id > b.id {
				return 1
			}
		}
		return a.idx - b.idx
	})
	out := make([]Redactor, 0, len(xs))
	for _, t := range xs {
		out = append(out, t.r)
	}
	return out
}
