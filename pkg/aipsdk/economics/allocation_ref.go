package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

const MaxAllocationRefs = 1024

var ErrInvalidAllocation = errors.New("economics: invalid allocation")

// AllocationRef points to one immutable allocation revision.
type AllocationRef struct {
	StoreID      string `json:"store_id"`
	AllocationID string `json:"allocation_id"`
	Version      uint64 `json:"version"`
	PayloadHash  string `json:"payload_hash"`
}

func (r AllocationRef) normalize() error {
	for name, value := range map[string]string{"allocation ref store_id": r.StoreID, "allocation ref id": r.AllocationID, "allocation ref payload_hash": r.PayloadHash} {
		if err := ValidateSafeRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
	}
	if r.Version == 0 {
		return fmt.Errorf("%w: allocation ref version required", ErrInvalidAllocation)
	}
	if decoded, err := hex.DecodeString(r.PayloadHash); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: allocation ref payload hash must be SHA-256 hex", ErrInvalidAllocation)
	}
	return nil
}

// Validate checks an immutable allocation reference.
func (r AllocationRef) Validate() error { return r.normalize() }

func allocationRefSortKey(ref AllocationRef) string {
	return ref.StoreID + "\x00" + ref.AllocationID + "\x00" + strconv.FormatUint(ref.Version, 10) + "\x00" + ref.PayloadHash
}
