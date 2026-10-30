package configsource

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// DefaultMaxBytes is the startup-fixed upper bound for one accepted source snapshot.
const DefaultMaxBytes int64 = config.DefaultConfigMaxBytes

// Category aliases the core secret-safe load category so driving adapters and
// typed decoding report one stable vocabulary without reversing dependencies.
type Category = config.LoadCategory

const (
	CategoryOK                = config.CategoryOK
	CategoryMissing           = config.CategoryMissing
	CategoryEmpty             = config.CategoryEmpty
	CategoryWhitespace        = config.CategoryWhitespace
	CategoryOversize          = config.CategoryOversize
	CategoryUnstable          = config.CategoryUnstable
	CategoryNonAtomicUpdate   = config.CategoryNonAtomicUpdate
	CategoryUnsupportedType   = config.CategoryUnsupportedType
	CategoryMalformedYAML     = config.CategoryMalformedYAML
	CategoryMultipleDocuments = config.CategoryMultipleDocuments
	CategoryTrailingContent   = config.CategoryTrailingContent
	CategoryUnknownCoreField  = config.CategoryUnknownCoreField
	CategoryPartialUnreadable = config.CategoryPartialUnreadable
)

// FileIdentity scheme names. A scheme identifies which metadata produced
// Opaque. Identities from different schemes are never comparable: comparing
// them must fail closed, because a coarser scheme (device+inode only) cannot
// prove that a candidate is not the same physical file as the accepted one.
const (
	identitySchemeStatxBirthTime = "statx-btime" // retired Linux scheme rejected against lease identities
	identitySchemeDeviceInode    = "dev-ino"     // portable Unix fallback: device + inode
	identitySchemeFileID         = "win-fileid"  // Windows: volume serial + file index
	identitySchemeLinuxExt4Lease = "linux-ext4-dev-ino-lease-v1"
)

// FileIdentity is a platform-stable handle identity used to prove atomic replacement.
// Scheme records the provenance of Opaque so identities derived from different
// metadata can never be mistaken for the same physical file.
type FileIdentity struct {
	Platform string
	Scheme   string
	Opaque   [32]byte
}

// SourceSnapshot is one accepted bounded read of the fixed startup path.
type SourceSnapshot struct {
	SourceID         string
	HandleIdentity   FileIdentity
	Size             int64
	ModTime          time.Time
	PrivateDigest    [32]byte
	Bytes            []byte
	ReadAt           time.Time
	ownerSlot        *SourceOwnerSlot
	leaseCore        *sourceLeaseCore
	leaseRequired    bool
	leaseUnavailable bool
	mountID          uint64
	devMajor         uint32
	devMinor         uint32
	changeTime       time.Time
	hasChangeTime    bool
}

// ActiveSourceVersion is the last accepted source identity used for no-op /
// non-atomic comparisons (published generation or effective no-op baseline).
type ActiveSourceVersion struct {
	HandleIdentity   FileIdentity
	PrivateDigest    [32]byte
	leaseCore        *sourceLeaseCore
	leaseRequired    bool
	leaseUnavailable bool
	mountID          uint64
	devMajor         uint32
	devMinor         uint32
	size             int64
	modTime          time.Time
	changeTime       time.Time
	hasChangeTime    bool
}

// RequiresLease reports whether this version depends on Linux's retained
// descriptor identity scheme.
func (v *ActiveSourceVersion) RequiresLease() bool {
	return v != nil && v.leaseRequired
}

type sourceLeaseEvidence struct {
	identity FileIdentity
	mountID  uint64
	devMajor uint32
	devMinor uint32
}

// TakeBaseline creates comparison metadata and moves any accepted owner into a
// fresh destructive slot. The metadata itself has no close authority.
func (s *SourceSnapshot) TakeBaseline() (*ActiveSourceVersion, *SourceOwnerSlot) {
	if s == nil {
		return nil, nil
	}
	version := &ActiveSourceVersion{
		HandleIdentity: s.HandleIdentity, PrivateDigest: s.PrivateDigest,
		leaseCore: s.leaseCore, leaseRequired: s.leaseRequired,
		leaseUnavailable: s.leaseUnavailable,
		mountID:          s.mountID, devMajor: s.devMajor, devMinor: s.devMinor,
		size: s.Size, modTime: s.ModTime, changeTime: s.changeTime, hasChangeTime: s.hasChangeTime,
	}
	var owner *SourceLeaseOwner
	if s.ownerSlot != nil {
		owner = s.ownerSlot.Take()
		s.ownerSlot = nil
	}
	return version, NewSourceOwnerSlot(owner)
}

// Close releases an owner still held by this snapshot, if one was not moved
// with TakeBaseline.
func (s *SourceSnapshot) Close(ctx context.Context) error {
	if s == nil || s.ownerSlot == nil {
		return nil
	}
	return s.ownerSlot.Close(ctx)
}

// SetCleanupObserver attaches the bounded runtime cleanup observer to the
// snapshot's owner, if a supported-platform lease exists.
func (s *SourceSnapshot) SetCleanupObserver(fn func(error)) {
	if s != nil && s.ownerSlot != nil {
		s.ownerSlot.SetCleanupObserver(fn)
	}
}

// Borrow acquires a non-owning validated lease borrow. Native non-Linux
// identity schemes do not require a retained source descriptor.
func (v *ActiveSourceVersion) Borrow() (*SourceBorrow, error) {
	if v == nil {
		return nil, leaseIntegrityErr("lease_unavailable")
	}
	if v.HandleIdentity.Platform != "linux" && !v.leaseRequired && !v.leaseUnavailable && v.leaseCore == nil && nativeIdentity(v.HandleIdentity) {
		return &SourceBorrow{}, nil
	}
	if !v.leaseRequired || v.leaseUnavailable || v.HandleIdentity.Platform != "linux" ||
		v.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease || v.leaseCore == nil {
		return nil, leaseIntegrityErr("lease_unavailable")
	}
	if v.mountID != v.leaseCore.mountID || v.devMajor != v.leaseCore.devMajor || v.devMinor != v.leaseCore.devMinor {
		return nil, leaseIntegrityErr("lease_provenance")
	}
	return v.leaseCore.borrow(v.HandleIdentity)
}

func (s *SourceSnapshot) borrowCandidate() (*SourceBorrow, error) {
	if s == nil {
		return nil, leaseIntegrityErr("lease_unavailable")
	}
	if s.HandleIdentity.Platform != "linux" && !s.leaseRequired && !s.leaseUnavailable && s.leaseCore == nil && nativeIdentity(s.HandleIdentity) {
		return &SourceBorrow{}, nil
	}
	if !s.leaseRequired || s.leaseUnavailable || s.HandleIdentity.Platform != "linux" ||
		s.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease || s.leaseCore == nil ||
		s.ownerSlot == nil || !s.ownerSlot.Matches(&ActiveSourceVersion{
		HandleIdentity: s.HandleIdentity, leaseCore: s.leaseCore, leaseRequired: true,
	}) {
		return nil, leaseIntegrityErr("lease_provenance")
	}
	return s.leaseCore.borrow(s.HandleIdentity)
}
