package largebody

import (
	"errors"
	"fmt"
	"sync"
)

// ErrSpoolBudgetExhausted is returned when a logical spool reservation cannot
// be granted or grown because the global in-flight spool budget
// (max_inflight_spool_bytes) has been reached or the requested byte count
// exceeds available capacity.
//
// Per Requirement 1.5 and Requirement 20.5 (design section 5), budget exhaustion
// is an optimization decline, NOT a client error (do NOT map to 413 Request Entity
// Too Large). The request must decline to canonical processing.
var ErrSpoolBudgetExhausted = errors.New("largebody: spool budget exhausted (optimization decline)")

// ErrInvalidReservation is returned when a reservation parameter violates
// bounds or checked int64 arithmetic, such as negative byte counts or overflow.
var ErrInvalidReservation = errors.New("largebody: invalid spool reservation parameter")

// ErrReservationClosed is returned when attempting to mutate or grow a
// spool reservation that has already been released.
var ErrReservationClosed = errors.New("largebody: spool reservation already released")

// IsSpoolBudgetExhausted reports whether err is or wraps ErrSpoolBudgetExhausted.
func IsSpoolBudgetExhausted(err error) bool {
	return errors.Is(err, ErrSpoolBudgetExhausted)
}

// SpoolBudgetConfig configures the logical spool ledger budgets
// (Requirements 1, 20, 21; design sections 3, 5).
type SpoolBudgetConfig struct {
	// MemorySpoolBytes bounds retained request bytes in Go heap per capture
	// before spilling to secondary storage.
	MemorySpoolBytes int64
	// MaxInflightSpoolBytes bounds global logical spool reservations across
	// all concurrent in-flight captures. This is an optimization budget only.
	MaxInflightSpoolBytes int64
}

// SpoolLedger tracks global in-flight logical spool reservations (Task 4.1).
// It acts as the pure reservation accounting ledger for large-payload capture.
// All operations use checked int64 math and are safe for concurrent use.
type SpoolLedger struct {
	mu                    sync.Mutex
	memorySpoolBytes      int64
	maxInflightSpoolBytes int64
	inflightBytes         int64
	activeReservations    int64
	observer              SpoolObserver
	seq                   uint64
}

// SetObserver sets an optional observer to receive active spool byte updates.
// If obs is non-nil, it is immediately notified of the current active spool bytes.
func (l *SpoolLedger) SetObserver(obs SpoolObserver) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.observer = obs
	seq := l.seq
	inflight := l.inflightBytes
	l.mu.Unlock()
	if obs != nil {
		obs.OnActiveSpoolBytes(seq, inflight)
	}
}

// NewSpoolLedger validates budget configuration and constructs a SpoolLedger.
// MaxInflightSpoolBytes must be > 0.
// MemorySpoolBytes must be >= 0 and <= MaxInflightSpoolBytes.
func NewSpoolLedger(cfg SpoolBudgetConfig) (*SpoolLedger, error) {
	if cfg.MaxInflightSpoolBytes <= 0 {
		return nil, fmt.Errorf("%w: max_inflight_spool_bytes must be > 0, got %d",
			ErrInvalidReservation, cfg.MaxInflightSpoolBytes)
	}
	if cfg.MemorySpoolBytes < 0 {
		return nil, fmt.Errorf("%w: memory_spool_bytes must be >= 0, got %d",
			ErrInvalidReservation, cfg.MemorySpoolBytes)
	}
	if cfg.MemorySpoolBytes > cfg.MaxInflightSpoolBytes {
		return nil, fmt.Errorf("%w: memory_spool_bytes (%d) must be <= max_inflight_spool_bytes (%d)",
			ErrInvalidReservation, cfg.MemorySpoolBytes, cfg.MaxInflightSpoolBytes)
	}
	return &SpoolLedger{
		memorySpoolBytes:      cfg.MemorySpoolBytes,
		maxInflightSpoolBytes: cfg.MaxInflightSpoolBytes,
	}, nil
}

// MaxInflightSpoolBytes reports the configured global in-flight budget limit.
func (l *SpoolLedger) MaxInflightSpoolBytes() int64 {
	if l == nil {
		return 0
	}
	return l.maxInflightSpoolBytes
}

// MemorySpoolBytes reports the configured per-capture memory ceiling.
func (l *SpoolLedger) MemorySpoolBytes() int64 {
	if l == nil {
		return 0
	}
	return l.memorySpoolBytes
}

// InflightBytes reports the total bytes currently reserved across all active captures.
func (l *SpoolLedger) InflightBytes() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflightBytes
}

// AvailableBytes reports remaining available capacity before budget exhaustion.
func (l *SpoolLedger) AvailableBytes() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	avail := l.maxInflightSpoolBytes - l.inflightBytes
	if avail < 0 {
		return 0
	}
	return avail
}

// ActiveReservations reports the count of currently unreleased reservations.
func (l *SpoolLedger) ActiveReservations() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeReservations
}

// Reserve allocates an initial logical spool reservation for a known body length
// (e.g. Content-Length for identity JSON) or 0 for an unknown/chunked stream.
//
// If requested bytes exceed available capacity, ErrSpoolBudgetExhausted is returned
// without allocating. Negative bytes or int64 overflow return ErrInvalidReservation.
func (l *SpoolLedger) Reserve(knownBytes int64) (*SpoolReservation, error) {
	if l == nil {
		return nil, ErrSpoolBudgetExhausted
	}
	if knownBytes < 0 {
		return nil, fmt.Errorf("%w: requested reservation bytes must be >= 0, got %d",
			ErrInvalidReservation, knownBytes)
	}

	l.mu.Lock()

	newInflight, err := checkedAdd(l.inflightBytes, knownBytes)
	if err != nil {
		l.mu.Unlock()
		return nil, fmt.Errorf("%w: reservation overflows int64: %v", ErrInvalidReservation, err)
	}
	if newInflight > l.maxInflightSpoolBytes {
		l.mu.Unlock()
		return nil, ErrSpoolBudgetExhausted
	}

	l.seq++
	seq := l.seq
	l.inflightBytes = newInflight
	l.activeReservations++
	obs := l.observer
	l.mu.Unlock()
	if obs != nil {
		obs.OnActiveSpoolBytes(seq, newInflight)
	}

	return &SpoolReservation{
		ledger:           l,
		reserved:         knownBytes,
		memorySpoolBytes: l.memorySpoolBytes,
	}, nil
}

// BeginReservation is a convenience alias for Reserve(0) for unknown/chunked captures.
func (l *SpoolLedger) BeginReservation() (*SpoolReservation, error) {
	return l.Reserve(0)
}

// SpoolReservation represents an active logical reservation against a SpoolLedger.
// It tracks reserved bytes, provides checked incremental growth, and guarantees
// exact-once release on fallback/success/cancel/error (Requirement 20.4).
type SpoolReservation struct {
	mu               sync.Mutex
	ledger           *SpoolLedger
	reserved         int64
	memorySpoolBytes int64
	released         bool
}

// ReservedBytes reports the total bytes currently reserved by this reservation.
func (r *SpoolReservation) ReservedBytes() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserved
}

// MemorySpoolBytes reports the per-capture memory budget threshold for this reservation.
func (r *SpoolReservation) MemorySpoolBytes() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memorySpoolBytes
}

// MemoryBytes reports how many of the reserved bytes reside within the in-memory window.
func (r *SpoolReservation) MemoryBytes() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reserved <= r.memorySpoolBytes {
		return r.reserved
	}
	return r.memorySpoolBytes
}

// SpillBytes reports how many of the reserved bytes exceed the memory ceiling and spill.
func (r *SpoolReservation) SpillBytes() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reserved > r.memorySpoolBytes {
		return r.reserved - r.memorySpoolBytes
	}
	return 0
}

// ReserveMore increments the reservation by additionalBytes using checked int64 math.
// Used as chunks arrive during streaming/chunked capture.
//
// If the requested growth exceeds the ledger's budget, ErrSpoolBudgetExhausted is returned.
// Crucially, previously reserved bytes remain held so that retained prefix memory/spill
// remains accounted for during canonical continuation (Requirements 1.4, 20.6, 20.7).
func (r *SpoolReservation) ReserveMore(additionalBytes int64) error {
	if r == nil {
		return fmt.Errorf("%w: nil reservation", ErrInvalidReservation)
	}
	if additionalBytes < 0 {
		return fmt.Errorf("%w: additional bytes must be >= 0, got %d",
			ErrInvalidReservation, additionalBytes)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.released {
		return ErrReservationClosed
	}
	if additionalBytes == 0 {
		return nil
	}

	newReserved, err := checkedAdd(r.reserved, additionalBytes)
	if err != nil {
		return fmt.Errorf("%w: reservation growth overflows int64: %v", ErrInvalidReservation, err)
	}

	l := r.ledger
	if l == nil {
		return ErrSpoolBudgetExhausted
	}

	l.mu.Lock()

	newInflight, err := checkedAdd(l.inflightBytes, additionalBytes)
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("%w: inflight bytes overflow int64: %v", ErrInvalidReservation, err)
	}
	if newInflight > l.maxInflightSpoolBytes {
		l.mu.Unlock()
		return ErrSpoolBudgetExhausted
	}

	l.seq++
	seq := l.seq
	l.inflightBytes = newInflight
	r.reserved = newReserved
	obs := l.observer
	l.mu.Unlock()
	if obs != nil {
		obs.OnActiveSpoolBytes(seq, newInflight)
	}
	return nil
}

// ShrinkTo reduces the reservation down to finalBytes using checked int64 math,
// returning the difference to the ledger.
// finalBytes must be >= 0 and <= ReservedBytes().
func (r *SpoolReservation) ShrinkTo(finalBytes int64) error {
	if r == nil {
		return fmt.Errorf("%w: nil reservation", ErrInvalidReservation)
	}
	if finalBytes < 0 {
		return fmt.Errorf("%w: target bytes must be >= 0, got %d", ErrInvalidReservation, finalBytes)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.released {
		return ErrReservationClosed
	}
	if finalBytes > r.reserved {
		return fmt.Errorf("%w: cannot shrink to %d bytes (currently reserved %d)",
			ErrInvalidReservation, finalBytes, r.reserved)
	}

	delta := r.reserved - finalBytes
	if delta == 0 {
		return nil
	}

	l := r.ledger
	if l != nil {
		l.mu.Lock()
		l.seq++
		seq := l.seq
		l.inflightBytes -= delta
		if l.inflightBytes < 0 {
			l.inflightBytes = 0
		}
		inflight := l.inflightBytes
		obs := l.observer
		l.mu.Unlock()
		if obs != nil {
			obs.OnActiveSpoolBytes(seq, inflight)
		}
	}

	r.reserved = finalBytes
	return nil
}

// Release frees all reserved bytes held by this reservation back to the ledger
// and decrements the active reservation count.
//
// Release is idempotent and safe for concurrent use: exactly one call frees
// the reservation and returns the number of bytes released. Subsequent calls
// return 0.
func (r *SpoolReservation) Release() int64 {
	if r == nil {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.released {
		return 0
	}
	r.released = true

	freed := r.reserved
	r.reserved = 0

	l := r.ledger
	if l != nil {
		l.mu.Lock()
		l.seq++
		seq := l.seq
		l.inflightBytes -= freed
		if l.inflightBytes < 0 {
			l.inflightBytes = 0
		}
		l.activeReservations--
		if l.activeReservations < 0 {
			l.activeReservations = 0
		}
		inflight := l.inflightBytes
		obs := l.observer
		l.mu.Unlock()
		if obs != nil {
			obs.OnActiveSpoolBytes(seq, inflight)
		}
	}

	return freed
}
