package siwc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The cross-process lock of this package.
//
// The lock is an OS ADVISORY lock over a file in the protected state
// directory, and the operating system owns it. That is the whole point,
// and every property the lock needs comes from it:
//
//   - The ownership does NOT depend on elapsed time. A holder that takes
//     longer than any bound keeps the lock, and no other process can take
//     it away from it.
//   - The operating system RELEASES the lock when the holding process
//     dies. A connector that is killed while it holds the lock does not
//     block its registration forever.
//   - The file is NEVER unlinked. There is no recovery step that could
//     remove the lock of a live holder, and no step that could remove the
//     lock of another process's new holder.
//
// The alternative - a lock file that is created exclusively and removed by
// its holder - needs a STALE-RECOVERY rule, and every such rule has the
// same two defects:
//
//   - it infers death from elapsed time, so a slow but LIVE holder loses
//     its lock to a process that decides the holder is gone; and
//   - it unlinks a PATH by name, so two competing recoverers can remove
//     each other's lock: the first removes the abandoned file, a new
//     holder creates its own, and the second recoverer - which still holds
//     the stale stat result - removes the new holder's file.
//
// Neither defect is repaired by a longer age or a careful read: the first
// needs the owner's death to be observable, and the second needs the
// unlink to be coupled to the file that was observed. The operating system
// answers both, and it answers them at the moment they matter.

// The tuning of the lock wait.
const (
	// lockPollInterval is how often the lock is polled while another
	// holder has it.
	//
	// The poll is short, because the lock is held for the duration of one
	// provider request and the waiters are the callers of the same
	// operation.
	lockPollInterval = 20 * time.Millisecond

	// defaultLockTimeout bounds the wait for a lock.
	//
	// The bound is a wall-clock bound, because it bounds a wait for
	// another process: a wait that never ends is a registration that can
	// never be refreshed again.
	defaultLockTimeout = 30 * time.Second
)

// The classified failures of a lock.
var (
	// errLockHeld reports that another holder has the lock and did not
	// release it inside the bound.
	//
	// The failure names the constraint and not a value: the other holder
	// is another process, and its identity is not this connector's to
	// report.
	errLockHeld = errors.New(
		"siwc: another holder of this registration's lock did not release it in time",
	)

	// errLockUntrustedDirectory reports a lock file in a directory that
	// another principal can write to.
	//
	// The state directory is a trust boundary, and the lock file is
	// opened before the store's own protection check would run. A lock
	// file in an untrusted directory is a file an attacker can replace,
	// and the trust is therefore checked HERE, before the path is opened.
	errLockUntrustedDirectory = errors.New(
		"siwc: the state directory of the registration's lock is not a trusted state directory",
	)

	// errLockNotARegularFile reports a lock path that is not a regular
	// file.
	//
	// A symlink at the lock path is refused: the connector would lock
	// (or create) a file that another principal chose, and a lock on such
	// a file protects nothing.
	errLockNotARegularFile = errors.New(
		"siwc: the registration's lock file is not a regular file",
	)
)

// The lock files of a registration.
//
// The names do NOT carry the store's record frame: the store lists its
// directory and classifies every name it finds, and a lock file is not a
// record.
//
// There are TWO locks per registration, and they guard two different
// things:
//
//   - the ROTATION lock is held for the duration of one provider request,
//     so that two refreshes of the same rotating token cannot run at the
//     same time; and
//   - the PUBLICATION lock is held for the duration of one publication, so
//     that no two writers of the same registration's credentials can lose
//     each other's update.
//
// The two are separate on purpose. A writer that waits for a provider -
// the refresh - holds the rotation lock for the provider's latency, and a
// writer that must not wait for that provider - the reauthorization, whose
// flow includes an interactive browser wait - must still be able to
// publish. A single lock would make the reauthorization wait for a
// refresh that is in flight.
const (
	rotationLockPrefix = "refresh-"
	rotationLockSuffix = ".lock"

	publicationLockPrefix = "publish-"
	publicationLockSuffix = ".lock"
)

// The kinds of a registration's lock.
//
// The kind names the ADMISSION of the lock and the file it lives in. The
// two locks of a registration are independent: a writer may hold the
// rotation lock while it waits for the publication lock, and the order is
// always rotation-then-publication, so the two cannot deadlock.
const (
	// rotationLockKind guards the credential rotation.
	rotationLockKind = "rotation"

	// publicationLockKind guards the publication of a record.
	publicationLockKind = "publication"
)

// rotationLockName returns the name of a registration's rotation lock file.
func rotationLockName(id string) string {
	return rotationLockPrefix + id + rotationLockSuffix
}

// publicationLockName returns the name of a registration's publication lock
// file.
func publicationLockName(id string) string {
	return publicationLockPrefix + id + publicationLockSuffix
}

// lockFileName returns the name of a registration's lock file of one kind.
func lockFileName(kind, id string) string {
	if kind == rotationLockKind {
		return rotationLockName(id)
	}
	return publicationLockName(id)
}

// fileLock is a held advisory lock.
//
// The lock is released when the file is closed, and the operating system
// closes the file when the process dies. The release is therefore a
// CLOSE, not an unlink: the file stays where it is, and a process that
// takes the lock afterwards locks the SAME file.
type fileLock struct {
	file  *os.File
	state lockState
}

// acquireFileLock takes the exclusive lock of the file at path.
//
// The wait is BOUNDED and CANCELLABLE: the caller gets a classified
// failure instead of an endless wait, and a cancelled context ends the
// wait at once.
//
// The state directory is checked for TRUST BEFORE the path is opened: the
// lock file is opened before the store's own protection check would run,
// and a lock file in a directory another principal can write to is a file
// that principal can replace.
//
// The file is opened with the create-exclusive flag FIRST: a path that
// already exists is not created, and a symlink at the path is therefore
// not followed. An existing file is then checked to be a regular file -
// the same discipline the store applies to its documents - and opened
// without the create flag.
//
// The file is opened once and the lock is polled on that one descriptor:
// reopening it for every attempt would leave a descriptor behind for
// every failed attempt.
func acquireFileLock(ctx context.Context, dir, path string, timeout time.Duration) (*fileLock, error) {
	if err := checkTrustedStateDir(dir); err != nil {
		return nil, errLockUntrustedDirectory
	}
	// The caller's context is checked FIRST: a caller that was cancelled
	// before it asked must not open the lock file at all.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("siwc: the wait for the lock was cancelled: %w", err)
	}
	file, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		state, locked, err := tryLockFile(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("siwc: take the lock: %w", err)
		}
		if locked {
			return &fileLock{file: file, state: state}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("siwc: the wait for the lock was cancelled: %w", ctx.Err())
		case <-time.After(lockPollInterval):
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, errLockHeld
		}
	}
}

// openLockFile opens the lock file without following a symlink.
//
// The create-exclusive open comes first: it fails when the path exists -
// including when the path is a symlink - and it never opens a symlink's
// target. An existing path is then checked to be a regular file and is
// opened WITHOUT the create flag.
//
// The check and the open are two steps, and a principal that can write the
// directory could swap the path between them. That is the same limitation
// the store's own protection check has, and the same reason the state
// directory is a trust boundary: checkTrustedStateDir has refused the
// directory before this point.
func openLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("siwc: create the lock file: %w", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return nil, fmt.Errorf("siwc: stat the lock file: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		return nil, errLockNotARegularFile
	}
	file, err = os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("siwc: open the lock file: %w", err)
	}
	return file, nil
}

// release gives the lock up.
//
// The lock is released by unlocking the descriptor and CLOSING it. The
// file itself stays: a file that was unlinked on release would let a
// process that already holds the descriptor believe that it still owns
// the lock while another process creates a new file with the same name.
func (l *fileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockFile(l.file, l.state)
	_ = l.file.Close()
	l.file = nil
}

// profileAdmission is the in-process admission of one registration's lock.
//
// The admission is a CHANNEL semaphore, and its wait is a select on the
// caller's context and on the deadline. A sync.Mutex is not: its Lock
// accepts neither a context nor a timeout, so a caller that waits behind
// a live holder cannot be withdrawn - a cancelled caller would wait for
// the holder's whole operation, and a caller with a deadline would wait
// past it. An admission that a cancelled caller cannot leave is a LIVENESS
// defect, not a convenience one.
//
// The admission is per ProfileStore and per registration, and it is shared
// by every writer of that registration, so that ONE descriptor of the lock
// file is open in this store at a time.
//
// That is what makes the Windows lock safe, and the reason is the
// release-on-close rule of the operating system: a byte-range lock there
// is owned by the PROCESS, and closing a file that carries the process's
// outstanding locks makes the operating system release them. A store that
// kept TWO descriptors of the same lock file open would therefore release
// the lock of the registration when it closed the second one, and another
// writer could take it while the first one still held it.
//
// The admission is PER STORE, and the argument above assumes ONE store per
// process: two stores over the same directory in one process keep two
// descriptors open and defeat the property. A process-wide registry is NOT
// an option here - the repository's guardrails forbid globals - and the
// assumption is therefore a property of how the store is used: the flows of
// this package create one store per process.
type profileAdmission struct {
	slots chan struct{}
}

// newProfileAdmission returns the admission of one registration.
func newProfileAdmission() *profileAdmission {
	return &profileAdmission{slots: make(chan struct{}, 1)}
}

// acquire takes the admission.
//
// The wait is bounded by the deadline and honours the caller's context: a
// caller that is cancelled or that runs out of time is told so, and it does
// not wait for the holder.
func (a *profileAdmission) acquire(ctx context.Context, deadline time.Time) error {
	// The caller's context is checked FIRST. A select would pick
	// RANDOMLY between a free slot and a context that is already done,
	// and a caller that was cancelled before it asked must not take the
	// admission.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf(
			"siwc: the wait for the registration's lock was cancelled: %w",
			err,
		)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errLockHeld
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case a.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf(
			"siwc: the wait for the registration's lock was cancelled: %w",
			ctx.Err(),
		)
	case <-timer.C:
		return errLockHeld
	}
}

// release gives the admission up.
//
// The receive is exact: the holder put a token into the channel, and the
// pairing is the whole of the bookkeeping. A non-blocking receive would
// hide a mistake and leak the admission, and a leaked admission blocks the
// registration forever.
func (a *profileAdmission) release() {
	<-a.slots
}

// lockPath returns the path of a registration's lock file of one kind.
func lockPath(dir, kind, id string) string {
	return filepath.Join(dir, lockFileName(kind, id))
}
