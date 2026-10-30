package terminal

// AccumulatorSnapshot is an opaque immutable copy of mutable stream
// accumulators taken by the CAS claim winner.
type AccumulatorSnapshot struct {
	raw             []byte
	outputCommitted bool
}

// NewAccumulatorSnapshot copies data into an opaque snapshot.
func NewAccumulatorSnapshot(data []byte, outputCommitted bool) AccumulatorSnapshot {
	return AccumulatorSnapshot{
		raw:             append([]byte(nil), data...),
		outputCommitted: outputCommitted,
	}
}

// Bytes returns a defensive copy of the snapshot bytes.
func (s AccumulatorSnapshot) Bytes() []byte {
	return append([]byte(nil), s.raw...)
}

// OutputCommitted reports whether visible output was committed when snapped.
func (s AccumulatorSnapshot) OutputCommitted() bool {
	return s.outputCommitted
}

// Clone returns a snapshot equivalent to s.
//
// A snapshot is immutable once constructed and [AccumulatorSnapshot.Bytes]
// copies on read, so the published value can share the original read-only
// payload instead of duplicating it. Terminal decisions clone snapshots on
// every claim, and duplicating the whole encoded accumulator payload each time
// is pure work: nothing can mutate or observe the difference.
func (s AccumulatorSnapshot) Clone() AccumulatorSnapshot {
	return s
}

// Equal reports whether two snapshots have identical bytes and flags.
func (s AccumulatorSnapshot) Equal(o AccumulatorSnapshot) bool {
	if s.outputCommitted != o.outputCommitted || len(s.raw) != len(o.raw) {
		return false
	}
	for i := range s.raw {
		if s.raw[i] != o.raw[i] {
			return false
		}
	}
	return true
}
