package configsource

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
)

// SourceLeaseOwner is the sole close-capable token for an accepted source
// handle. Copies of this pointer share one synchronized close authority.
type SourceLeaseOwner struct {
	core *sourceLeaseCore
}

type sourceLeaseCore struct {
	mu              sync.Mutex
	file            *os.File
	identity        FileIdentity
	mountID         uint64
	devMajor        uint32
	devMinor        uint32
	borrows         int
	closeRequested  bool
	closing         bool
	closed          bool
	closeDone       chan struct{}
	closeErr        error
	closeFile       func(*os.File) error
	cleanupSink     func(error)
	cleanupSent     bool
	captureMetadata func(*os.File) (sourceFileMetadata, error)
}

// SourceOwnerSlot is a destructive handoff slot. Take atomically clears the
// slot, so copied metadata or outcome records cannot close a moved owner.
type SourceOwnerSlot struct {
	owner atomic.Pointer[SourceLeaseOwner]
}

type sourceBorrowRelease struct {
	core *sourceLeaseCore
	// All token state is protected by core.mu. Retired tokens keep their
	// borrow reference until the last admitted handle operation finishes.
	released   bool
	operations int
}

// SourceBorrow keeps a validated source core open until Release. It exposes no
// descriptor and carries no close authority. Copies share one release token.
type SourceBorrow struct {
	release *sourceBorrowRelease
}

func newSourceLeaseOwner(file *os.File, identity FileIdentity) *SourceLeaseOwner {
	return newSourceLeaseOwnerWithCloser(file, identity, func(f *os.File) error { return f.Close() })
}

func newSourceLeaseOwnerWithCloser(file *os.File, identity FileIdentity, closeFile func(*os.File) error) *SourceLeaseOwner {
	if file == nil {
		return nil
	}
	if closeFile == nil {
		closeFile = func(f *os.File) error { return f.Close() }
	}
	return &SourceLeaseOwner{core: &sourceLeaseCore{
		file: file, identity: identity, closeDone: make(chan struct{}), closeFile: closeFile,
	}}
}

// NewSourceOwnerSlot creates a takeable slot for owner handoff between the
// configsource, runtimehost, and runtimebundle packages.
func NewSourceOwnerSlot(owner *SourceLeaseOwner) *SourceOwnerSlot {
	s := &SourceOwnerSlot{}
	if owner != nil {
		s.owner.Store(owner)
	}
	return s
}

// Take moves the slot's owner to the caller and leaves the slot empty.
func (s *SourceOwnerSlot) Take() *SourceLeaseOwner {
	if s == nil {
		return nil
	}
	return s.owner.Swap(nil)
}

// MoveTo atomically transfers this slot's owner into an already allocated
// destination slot and returns the destination's displaced owner.
func (s *SourceOwnerSlot) MoveTo(destination *SourceOwnerSlot) *SourceLeaseOwner {
	if s == nil || destination == nil || s == destination {
		return nil
	}
	owner := s.owner.Swap(nil)
	if owner == nil {
		return nil
	}
	return destination.owner.Swap(owner)
}

// Close takes and closes the owner remaining in the slot. A caller that
// already moved the owner gets a harmless nil result.
func (s *SourceOwnerSlot) Close(ctx context.Context) error {
	owner := s.Take()
	if owner == nil {
		return nil
	}
	return owner.Close(ctx)
}

// SetCleanupObserver installs the bounded runtime cleanup sink on the owner
// still held in this slot. It does not move or otherwise expose the owner.
func (s *SourceOwnerSlot) SetCleanupObserver(fn func(error)) {
	if s == nil || fn == nil {
		return
	}
	if owner := s.owner.Load(); owner != nil {
		owner.setCleanupObserver(fn)
	}
}

// Matches reports whether this slot contains the owner whose private core
// backs version. It does not transfer close authority.
func (s *SourceOwnerSlot) Matches(version *ActiveSourceVersion) bool {
	if s == nil || version == nil {
		return false
	}
	owner := s.owner.Load()
	return owner != nil && owner.core != nil && version.leaseCore == owner.core &&
		version.HandleIdentity == owner.core.identity
}

// IsEmpty reports whether the slot currently contains no close authority.
func (s *SourceOwnerSlot) IsEmpty() bool {
	return s == nil || s.owner.Load() == nil
}

// ValidFor reports whether the slot and version describe one valid lease pair.
// Native and unsupported-Linux versions have no lease owner; supported Linux
// versions must retain the exact core recorded in the version.
func (s *SourceOwnerSlot) ValidFor(version *ActiveSourceVersion) bool {
	if version == nil {
		return false
	}
	if s == nil {
		return sourceOwnerMatchesVersion(nil, version)
	}
	return sourceOwnerMatchesVersion(s.owner.Load(), version)
}

func sourceOwnerMatchesVersion(owner *SourceLeaseOwner, version *ActiveSourceVersion) bool {
	if version == nil {
		return false
	}
	id := version.HandleIdentity
	if id.Platform != "linux" {
		return owner == nil && version.leaseCore == nil && !version.leaseRequired &&
			!version.leaseUnavailable && nativeIdentity(id)
	}
	if !version.leaseRequired {
		return false
	}
	if version.leaseUnavailable {
		return owner == nil && version.leaseCore == nil && id.Scheme == identitySchemeDeviceInode
	}
	if id.Scheme != identitySchemeLinuxExt4Lease || owner == nil || owner.core == nil || owner.core != version.leaseCore {
		return false
	}
	c := owner.core
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity == id && c.mountID == version.mountID && c.devMajor == version.devMajor &&
		c.devMinor == version.devMinor && c.file != nil && !c.closeRequested && !c.closing && !c.closed
}

func nativeIdentity(id FileIdentity) bool {
	if id.Platform == "windows" {
		return id.Scheme == identitySchemeFileID
	}
	switch id.Platform {
	case "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "netbsd", "openbsd", "solaris":
		return id.Scheme == identitySchemeDeviceInode
	default:
		return false
	}
}

// MoveMatchingTo atomically consumes this slot only if it still contains the
// lease paired with version. An ownerless native version is valid and clears
// the destination owner so metadata and close authority stay aligned.
func (s *SourceOwnerSlot) MoveMatchingTo(destination *SourceOwnerSlot, version *ActiveSourceVersion) (*SourceLeaseOwner, bool) {
	if destination == nil || destination == s || version == nil {
		return nil, false
	}
	if s == nil {
		if !sourceOwnerMatchesVersion(nil, version) {
			return nil, false
		}
		return destination.owner.Swap(nil), true
	}
	for {
		owner := s.owner.Load()
		if !sourceOwnerMatchesVersion(owner, version) {
			return nil, false
		}
		if owner == nil {
			return destination.owner.Swap(nil), true
		}
		if s.owner.CompareAndSwap(owner, nil) {
			return destination.owner.Swap(owner), true
		}
	}
}

// RequestClose prevents future borrows and closes the file immediately when
// no borrow is active. If a borrow remains, its final Release performs close.
func (o *SourceLeaseOwner) RequestClose() error {
	if o == nil || o.core == nil {
		return nil
	}
	c := o.core
	c.mu.Lock()
	c.closeRequested = true
	f := c.claimCloseLocked()
	c.mu.Unlock()
	if f != nil {
		c.finishClose(f)
	}
	return c.completionError()
}

// Close requests closure and waits for completion only until ctx expires. The
// underlying Close call is synchronous and is never retried.
func (o *SourceLeaseOwner) Close(ctx context.Context) error {
	if o == nil || o.core == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := o.RequestClose(); err != nil {
		return err
	}
	c := o.core
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		return err
	}
	done := c.closeDone
	c.mu.Unlock()
	select {
	case <-done:
		return c.completionError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitClosed waits for the owner close requested by RequestClose. It does not
// initiate close itself, which lets shutdown remain nonblocking while readers
// still hold borrows.
func (o *SourceLeaseOwner) WaitClosed(ctx context.Context) error {
	if o == nil || o.core == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c := o.core
	select {
	case <-c.closeDone:
		return c.completionError()
	default:
	}
	select {
	case <-c.closeDone:
		return c.completionError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *SourceLeaseOwner) setCleanupObserver(fn func(error)) {
	if o == nil || o.core == nil || fn == nil {
		return
	}
	c := o.core
	c.mu.Lock()
	if !c.cleanupSent {
		c.cleanupSink = fn
	}
	c.mu.Unlock()
}

func (c *sourceLeaseCore) borrow(identity FileIdentity) (*SourceBorrow, error) {
	if c == nil {
		return nil, leaseIntegrityErr("lease_unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.identity != identity {
		return nil, leaseIntegrityErr("lease_provenance")
	}
	if c.closeRequested || c.closing || c.closed || c.file == nil {
		return nil, leaseIntegrityErr("lease_closed")
	}
	c.borrows++
	return &SourceBorrow{release: &sourceBorrowRelease{core: c}}, nil
}

func (b *SourceBorrow) Release() {
	if b == nil || b.release == nil || b.release.core == nil {
		return
	}
	token := b.release
	c := token.core
	c.mu.Lock()
	if token.released {
		c.mu.Unlock()
		return
	}
	token.released = true
	var f *os.File
	if token.operations == 0 {
		f = token.releaseLocked()
	}
	c.mu.Unlock()
	if f != nil {
		c.finishClose(f)
	}
}

func (r *sourceBorrowRelease) releaseLocked() *os.File {
	r.core.borrows--
	if r.core.closeRequested {
		return r.core.claimCloseLocked()
	}
	return nil
}

// beginOperation admits handle access using this token's shared authority.
// Release retires it immediately, but an admitted operation pins the borrow
// through its last syscall without holding the mutex during I/O.
func (b *SourceBorrow) beginOperation() (*sourceLeaseCore, error) {
	if b == nil || b.release == nil || b.release.core == nil {
		return nil, leaseIntegrityErr("lease_provenance")
	}
	r := b.release
	c := r.core
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.released || c.file == nil || c.closing || c.closed {
		return nil, leaseIntegrityErr("lease_closed")
	}
	r.operations++
	return c, nil
}

func (b *SourceBorrow) endOperation() {
	r := b.release
	c := r.core
	c.mu.Lock()
	r.operations--
	var f *os.File
	if r.released && r.operations == 0 {
		f = r.releaseLocked()
	}
	c.mu.Unlock()
	if f != nil {
		c.finishClose(f)
	}
}

func (c *sourceLeaseCore) capture(f *os.File) (sourceFileMetadata, error) {
	if c.captureMetadata != nil {
		return c.captureMetadata(f)
	}
	return captureLeaseMetadata(f)
}

// validateBaseline confirms that the borrowed descriptor still carries the
// accepted identity and stable metadata captured with the active baseline.
func (b *SourceBorrow) validateBaseline(version *ActiveSourceVersion) error {
	if b == nil || b.release == nil || b.release.core == nil || version == nil {
		return leaseIntegrityErr("lease_provenance")
	}
	c, err := b.beginOperation()
	if err != nil {
		return err
	}
	defer b.endOperation()
	if version.leaseCore != c || !version.leaseRequired || version.leaseUnavailable ||
		version.HandleIdentity.Platform != "linux" || version.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		return leaseIntegrityErr("lease_provenance")
	}
	c.mu.Lock()
	f := c.file
	identity := c.identity
	mountID, devMajor, devMinor := c.mountID, c.devMajor, c.devMinor
	c.mu.Unlock()
	if f == nil || identity != version.HandleIdentity || mountID != version.mountID ||
		devMajor != version.devMajor || devMinor != version.devMinor {
		return leaseIntegrityErr("lease_provenance")
	}
	metadata, err := c.capture(f)
	if err != nil || metadata.identity != version.HandleIdentity || metadata.size != version.size ||
		!metadata.modTime.Equal(version.modTime) || metadata.changeTime != version.changeTime ||
		!metadata.hasChangeTime || !version.hasChangeTime || metadata.mountID != version.mountID ||
		metadata.devMajor != version.devMajor || metadata.devMinor != version.devMinor {
		return leaseIntegrityErr("lease_unavailable")
	}
	return nil
}

func (b *SourceBorrow) validateProvenance(version *ActiveSourceVersion) error {
	if b == nil || b.release == nil || b.release.core == nil || version == nil {
		return leaseIntegrityErr("lease_provenance")
	}
	c, err := b.beginOperation()
	if err != nil {
		return err
	}
	defer b.endOperation()
	if version.leaseCore != c || !version.leaseRequired || version.leaseUnavailable ||
		version.HandleIdentity.Platform != "linux" || version.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		return leaseIntegrityErr("lease_provenance")
	}
	c.mu.Lock()
	f, identity := c.file, c.identity
	mountID, devMajor, devMinor := c.mountID, c.devMajor, c.devMinor
	c.mu.Unlock()
	if f == nil || identity != version.HandleIdentity || mountID != version.mountID ||
		devMajor != version.devMajor || devMinor != version.devMinor {
		return leaseIntegrityErr("lease_provenance")
	}
	metadata, err := c.capture(f)
	if err != nil || metadata.identity != version.HandleIdentity || metadata.mountID != version.mountID ||
		metadata.devMajor != version.devMajor || metadata.devMinor != version.devMinor {
		return leaseIntegrityErr("lease_unavailable")
	}
	return nil
}

// ValidateProvenance confirms that the borrow belongs to the live pinned
// owner for version. It intentionally does not compare current content times:
// a rejected in-place edit may later be repaired by an atomic replacement.
func (b *SourceBorrow) ValidateProvenance(version *ActiveSourceVersion) error {
	return b.validateProvenance(version)
}

// ValidateBaseline confirms that this borrow matches the supplied source
// baseline and that its pinned handle has not changed stable metadata.
func (b *SourceBorrow) ValidateBaseline(version *ActiveSourceVersion) error {
	return b.validateBaseline(version)
}

func (b *SourceBorrow) validateSnapshot(snapshot *SourceSnapshot) error {
	if b == nil || b.release == nil || b.release.core == nil || snapshot == nil {
		return leaseIntegrityErr("lease_provenance")
	}
	c, err := b.beginOperation()
	if err != nil {
		return err
	}
	defer b.endOperation()
	if snapshot.leaseCore != c || !snapshot.leaseRequired || snapshot.leaseUnavailable ||
		snapshot.HandleIdentity.Platform != "linux" || snapshot.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		return leaseIntegrityErr("lease_provenance")
	}
	c.mu.Lock()
	f := c.file
	identity := c.identity
	mountID, devMajor, devMinor := c.mountID, c.devMajor, c.devMinor
	c.mu.Unlock()
	if f == nil || identity != snapshot.HandleIdentity || mountID != snapshot.mountID ||
		devMajor != snapshot.devMajor || devMinor != snapshot.devMinor {
		return leaseIntegrityErr("lease_provenance")
	}
	metadata, err := c.capture(f)
	if err != nil || metadata.identity != snapshot.HandleIdentity || metadata.size != snapshot.Size ||
		!metadata.modTime.Equal(snapshot.ModTime) || metadata.changeTime != snapshot.changeTime ||
		!metadata.hasChangeTime || !snapshot.hasChangeTime || metadata.mountID != snapshot.mountID ||
		metadata.devMajor != snapshot.devMajor || metadata.devMinor != snapshot.devMinor {
		return leaseIntegrityErr("lease_unavailable")
	}
	return nil
}

func (c *sourceLeaseCore) claimCloseLocked() *os.File {
	if c.closed || c.closing || c.borrows != 0 || c.file == nil {
		return nil
	}
	c.closing = true
	f := c.file
	c.file = nil
	return f
}

func (c *sourceLeaseCore) finishClose(file *os.File) {
	var closeErr error
	func() {
		defer func() {
			if recover() != nil {
				closeErr = sourceCleanupError{cause: errors.New("close panic")}
			}
		}()
		if c.closeFile != nil {
			closeErr = sourceCleanupFailure(c.closeFile(file))
		} else {
			closeErr = sourceCleanupFailure(file.Close())
		}
	}()

	c.mu.Lock()
	c.closeErr = closeErr
	c.closing, c.closed = false, true
	close(c.closeDone)
	sink := c.cleanupSink
	if closeErr != nil && sink != nil && !c.cleanupSent {
		c.cleanupSent = true
	} else {
		sink = nil
	}
	c.mu.Unlock()
	if sink != nil {
		func() {
			defer func() { _ = recover() }()
			sink(closeErr)
		}()
	}
}

func (c *sourceLeaseCore) completionError() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		return nil
	}
	return c.closeErr
}

type sourceCleanupError struct{ cause error }

func (e sourceCleanupError) Error() string { return "configsource: cleanup failed" }
func (e sourceCleanupError) Unwrap() error { return e.cause }

func sourceCleanupFailure(err error) error {
	if err == nil {
		return nil
	}
	return sourceCleanupError{cause: err}
}

// MarkSourceCleanupFailure wraps a cleanup cause with the bounded private
// marker consumed by runtime cleanup telemetry and keeps the cause available
// through errors.Is/errors.As. Its string form never includes the cause.
func MarkSourceCleanupFailure(err error) error {
	return sourceCleanupFailure(err)
}

// IsSourceCleanupError reports whether err contains a source close failure.
func IsSourceCleanupError(err error) bool {
	var target sourceCleanupError
	return errors.As(err, &target)
}
