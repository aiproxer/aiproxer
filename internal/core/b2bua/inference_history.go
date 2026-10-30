package b2bua

import (
	"context"
	"errors"
)

// ErrBLegAllocationAuthorityUnsupported rejects stores without allocation authority.
var ErrBLegAllocationAuthorityUnsupported = errors.New("b2bua: allocation authority unsupported")

// BLegAllocationAuthority serializes a bounded, synchronous memory operation with
// NextBLeg. apply must not perform I/O, call continuity, or notify observers.
type BLegAllocationAuthority interface {
	WithBLegAllocationAuthority(context.Context, string, func(hasAllocated bool) error) error
}

var _ BLegAllocationAuthority = (*MemoryStore)(nil)

// WithBLegAllocationAuthority holds the actual NextBLeg mutex, including the
// allocation increment that precedes ID generation and attempt recording.
func (s *MemoryStore) WithBLegAllocationAuthority(ctx context.Context, aLegID string, apply func(bool) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if apply == nil {
		return ErrBLegAllocationAuthorityUnsupported
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	st, ok := s.legs[aLegID]
	if !ok {
		return ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return ErrALegNotFound
	}
	return apply(st.nextSeq > 0)
}
