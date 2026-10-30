package configsource

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// FixedSource is the startup-fixed absolute configuration path used by every
// reload. It does not watch the filesystem; callers must invoke ReadStable
// explicitly after an operator trigger.
type FixedSource struct {
	path          string
	maxBytes      int64
	leaseDetector func(*os.File) (sourceLeaseEvidence, bool)
	closeFile     func(*os.File) error
}

type sourceFileMetadata struct {
	identity      FileIdentity
	size          int64
	modTime       time.Time
	changeTime    time.Time
	hasChangeTime bool
	mountID       uint64
	devMajor      uint32
	devMinor      uint32
}

// NewFixedSource resolves path to an absolute location and stores the
// startup-fixed size limit. maxBytes <= 0 selects DefaultMaxBytes.
func NewFixedSource(path string, maxBytes int64) (*FixedSource, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("configsource: empty path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("configsource: resolve path: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &FixedSource{
		path: abs, maxBytes: maxBytes, leaseDetector: detectSourceLease,
		closeFile: func(file *os.File) error { return file.Close() },
	}, nil
}

// AbsolutePath returns the startup-resolved absolute source path.
func (s *FixedSource) AbsolutePath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// MaxBytes returns the startup-fixed read limit.
func (s *FixedSource) MaxBytes() int64 {
	if s == nil || s.maxBytes <= 0 {
		return DefaultMaxBytes
	}
	return s.maxBytes
}

// ReadStable opens the fixed path, captures handle identity, reads one bounded
// snapshot, revalidates the same candidate handle and a second path handle,
// classifies the bytes, and enforces atomic-replacement rules against active
// when non-nil. On supported Linux ext4, the accepted descriptor is retained
// as the source lease until its owner is closed.
func (s *FixedSource) ReadStable(ctx context.Context, active *ActiveSourceVersion) (snap SourceSnapshot, result AtomicResult, err error) {
	if s == nil || s.path == "" {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: nil source")
	}
	if ctx == nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: nil context")
	}
	if err = ctx.Err(); err != nil {
		return SourceSnapshot{}, "", err
	}

	var activeBorrow *SourceBorrow
	if active != nil {
		activeBorrow, err = active.Borrow()
		if err != nil {
			return SourceSnapshot{}, AtomicReject, err
		}
		defer activeBorrow.Release()
	}

	candidate, openErr := os.Open(s.path) // #nosec G304 -- fixed absolute startup path
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return SourceSnapshot{}, "", integrityErr(CategoryMissing)
		}
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, openErr)
	}
	candidateOwned := true
	defer func() {
		if candidateOwned {
			cleanupErr := sourceCleanupFailure(s.close(candidate))
			if err == nil && cleanupErr != nil {
				err = errors.Join(integrityErr(CategoryPartialUnreadable), cleanupErr)
			} else {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()

	beforeInfo, statErr := candidate.Stat()
	if statErr != nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, statErr)
	}
	if !beforeInfo.Mode().IsRegular() {
		return SourceSnapshot{}, "", integrityErr(CategoryUnsupportedType)
	}
	before, leaseEnabled, leaseUnavailable, inspectErr := s.inspect(candidate, beforeInfo)
	if inspectErr != nil {
		return SourceSnapshot{}, "", inspectErr
	}
	if runtime.GOOS == "linux" && active != nil && (!leaseEnabled || leaseUnavailable) {
		return SourceSnapshot{}, AtomicReject, leaseIntegrityErr("lease_unavailable")
	}
	if active != nil && before.identity.Platform == "linux" && before.identity.Scheme != identitySchemeLinuxExt4Lease {
		return SourceSnapshot{}, AtomicReject, leaseIntegrityErr("lease_unavailable")
	}
	if activeBorrow != nil && activeBorrow.release != nil && activeBorrow.release.core != nil {
		if !active.leaseRequired || active.leaseUnavailable || active.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
			return SourceSnapshot{}, AtomicReject, leaseIntegrityErr("lease_provenance")
		}
		if err := activeBorrow.validateProvenance(active); err != nil {
			return SourceSnapshot{}, AtomicReject, err
		}
	}

	limited := io.LimitReader(candidate, s.MaxBytes()+1)
	raw, readErr := io.ReadAll(limited)
	if readErr != nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, readErr)
	}
	if err = ctx.Err(); err != nil {
		return SourceSnapshot{}, "", err
	}

	afterInfo, statErr := candidate.Stat()
	if statErr != nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, statErr)
	}
	after, afterLease, afterUnavailable, inspectErr := s.inspect(candidate, afterInfo)
	if inspectErr != nil {
		return SourceSnapshot{}, "", inspectErr
	}
	if cat, stabilityErr := classifyMetadataStability(before, after, leaseEnabled); stabilityErr != nil {
		return SourceSnapshot{}, "", &IntegrityError{Category: cat}
	}
	if leaseEnabled && (!afterLease || afterUnavailable) {
		return SourceSnapshot{}, "", integrityErr(CategoryUnstable)
	}

	target, targetErr := os.Open(s.path) // #nosec G304 -- startup-fixed path revalidation
	if targetErr != nil {
		if os.IsNotExist(targetErr) {
			return SourceSnapshot{}, "", integrityErr(CategoryUnstable)
		}
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, targetErr)
	}
	targetOwned := true
	defer func() {
		if targetOwned {
			err = errors.Join(err, sourceCleanupFailure(s.close(target)))
		}
	}()
	targetInfo, statErr := target.Stat()
	if statErr != nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, statErr)
	}
	if !targetInfo.Mode().IsRegular() {
		return SourceSnapshot{}, "", integrityErr(CategoryUnstable)
	}
	targetMetadata, targetLease, targetUnavailable, inspectErr := s.inspect(target, targetInfo)
	if inspectErr != nil {
		return SourceSnapshot{}, "", inspectErr
	}
	if targetMetadata.identity != after.identity || targetMetadata.size != after.size ||
		!sameMetadataTime(targetMetadata, after, leaseEnabled) ||
		(leaseEnabled && (!targetLease || targetUnavailable)) {
		return SourceSnapshot{}, "", integrityErr(CategoryUnstable)
	}
	targetAfterInfo, statErr := target.Stat()
	if statErr != nil {
		return SourceSnapshot{}, "", fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, statErr)
	}
	targetAfter, targetAfterLease, targetAfterUnavailable, inspectErr := s.inspect(target, targetAfterInfo)
	if inspectErr != nil {
		return SourceSnapshot{}, "", inspectErr
	}
	if targetAfter.identity != targetMetadata.identity || targetAfter.size != targetMetadata.size ||
		!sameMetadataTime(targetMetadata, targetAfter, leaseEnabled) ||
		(leaseEnabled && (!targetAfterLease || targetAfterUnavailable)) {
		return SourceSnapshot{}, "", integrityErr(CategoryUnstable)
	}
	if closeErr := s.close(target); closeErr != nil {
		targetOwned = false
		return SourceSnapshot{}, "", errors.Join(integrityErr(CategoryPartialUnreadable), sourceCleanupFailure(closeErr))
	}
	targetOwned = false

	if int64(len(raw)) > s.MaxBytes() {
		return SourceSnapshot{}, "", oversizeErr(s.MaxBytes())
	}
	if cat, classifyErr := ClassifyBytes(raw, s.MaxBytes()); classifyErr != nil {
		return SourceSnapshot{}, "", &IntegrityError{Category: cat}
	}

	digest := sha256.Sum256(raw)
	snap = SourceSnapshot{
		SourceID: s.path, HandleIdentity: before.identity,
		Size: int64(len(raw)), ModTime: before.modTime.UTC(),
		PrivateDigest: digest, Bytes: raw, ReadAt: time.Now().UTC(),
		leaseRequired: runtime.GOOS == "linux", leaseUnavailable: runtime.GOOS == "linux" && (!leaseEnabled || leaseUnavailable),
		mountID: before.mountID, devMajor: before.devMajor, devMinor: before.devMinor,
		changeTime: before.changeTime, hasChangeTime: before.hasChangeTime,
	}
	if leaseEnabled {
		owner := newSourceLeaseOwnerWithCloser(candidate, before.identity, s.close)
		if owner != nil {
			owner.core.mountID, owner.core.devMajor, owner.core.devMinor = before.mountID, before.devMajor, before.devMinor
			snap.ownerSlot = NewSourceOwnerSlot(owner)
			snap.leaseCore = owner.core
			candidateOwned = false
		}
	}

	if active == nil {
		return snap, AtomicEligible, nil
	}
	result, cat, classifyErr := ClassifyAtomicReplacement(*active, snap)
	if classifyErr != nil {
		cleanupErr := snap.Close(ctx)
		return SourceSnapshot{}, result, errors.Join(&IntegrityError{Category: cat, reason: SafeReasonOf(classifyErr)}, cleanupErr)
	}
	return snap, result, nil
}

func (s *FixedSource) close(file *os.File) error {
	if s != nil && s.closeFile != nil {
		return s.closeFile(file)
	}
	if file == nil {
		return nil
	}
	return file.Close()
}

func (s *FixedSource) inspect(file *os.File, info os.FileInfo) (sourceFileMetadata, bool, bool, error) {
	if file == nil || info == nil {
		return sourceFileMetadata{}, false, false, integrityErr(CategoryUnsupportedType)
	}
	detector := s.leaseDetector
	if detector == nil {
		detector = detectSourceLease
	}
	if runtime.GOOS == "linux" {
		evidence, ok := detector(file)
		if ok {
			metadata, err := captureLeaseMetadata(file)
			if err != nil || metadata.identity != evidence.identity || metadata.mountID != evidence.mountID ||
				metadata.devMajor != evidence.devMajor || metadata.devMinor != evidence.devMinor {
				return sourceFileMetadata{}, false, false, leaseIntegrityErr("lease_unavailable")
			}
			return metadata, true, false, nil
		}
		identity, err := identityFromFile(file)
		if err != nil {
			return sourceFileMetadata{}, false, true, err
		}
		return sourceFileMetadata{identity: identity, size: info.Size(), modTime: info.ModTime().UTC()}, false, true, nil
	}
	identity, err := identityFromFile(file)
	if err != nil {
		return sourceFileMetadata{}, false, false, err
	}
	return sourceFileMetadata{identity: identity, size: info.Size(), modTime: info.ModTime().UTC()}, false, false, nil
}

func classifyMetadataStability(before, after sourceFileMetadata, lease bool) (Category, error) {
	if before.identity != after.identity || before.size != after.size || !sameMetadataTime(before, after, lease) {
		return CategoryUnstable, integrityErr(CategoryUnstable)
	}
	return CategoryOK, nil
}

func sameMetadataTime(a, b sourceFileMetadata, lease bool) bool {
	if !a.modTime.Equal(b.modTime) || a.hasChangeTime != b.hasChangeTime {
		return false
	}
	if lease && (!a.hasChangeTime || !b.hasChangeTime || !a.changeTime.Equal(b.changeTime) ||
		a.mountID != b.mountID || a.devMajor != b.devMajor || a.devMinor != b.devMinor) {
		return false
	}
	return true
}
