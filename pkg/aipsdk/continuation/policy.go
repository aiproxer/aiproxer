package continuation

import "time"

// PersistenceMode selects whether a record survives reconnect/process boundaries.
type PersistenceMode string

const (
	PersistencePersistent PersistenceMode = "persistent"
	PersistenceConnection PersistenceMode = "connection_local"
)

// StoragePolicy governs reservation and retention for one continuation chain link.
type StoragePolicy struct {
	Mode            PersistenceMode
	TTL             time.Duration
	AllowIncomplete bool
	Limits          StorageLimits
}

// StorageLimits bounds one store. Zero values select the store defaults.
type StorageLimits struct {
	MaxRecords     int
	MaxBytes       int64
	MaxRecordBytes int64
	MaxChainDepth  int
}

// DefaultStorageLimits returns finite production-safe storage limits.
func DefaultStorageLimits() StorageLimits {
	return StorageLimits{
		MaxRecords:     10_000,
		MaxBytes:       256 << 20,
		MaxRecordBytes: 16 << 20,
		MaxChainDepth:  64,
	}
}

// Validate rejects policies that could weaken scope or retention guarantees.
func (p StoragePolicy) Validate() error {
	if p.Mode != "" && p.Mode != PersistencePersistent && p.Mode != PersistenceConnection {
		return ErrInvalidPolicy
	}
	if p.TTL < 0 {
		return ErrInvalidPolicy
	}
	if p.Limits.MaxRecords < 0 || p.Limits.MaxBytes < 0 || p.Limits.MaxRecordBytes < 0 || p.Limits.MaxChainDepth < 0 {
		return ErrInvalidPolicy
	}
	return nil
}

// Bounds configures deterministic traversal and materialization limits.
type Bounds struct {
	MaxChainDepth        int
	MaxMaterializedItems int
	MaxMaterializedBytes int64
}

// DefaultBounds returns conservative Task 1.5 contract defaults aligned with design.
func DefaultBounds() Bounds {
	return Bounds{
		MaxChainDepth:        64,
		MaxMaterializedItems: 100_000,
		MaxMaterializedBytes: 64 << 20, // 64 MiB
	}
}
