package siwc

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The wait and the trust proofs of this slice.
//
// The schedules below are the ones a caller can be stuck behind:
//
//   - a caller that waits for a registration another caller holds, and
//     that cannot be withdrawn (the J5 schedule: a context deadline and a
//     lock budget, both of which the wait must honour); and
//   - a lock path in a directory another principal can write to, which the
//     connector must refuse BEFORE it opens the path.
//
// The proofs are at the boundary where the property is observable: the
// wait's return, the provider's traffic, and the filesystem.

// TestRefresh_SameInstanceWaitHonorsCancellation proves that a waiting
// caller is withdrawn by its own deadline.
//
// The first refresh is held in the provider, and a second caller waits
// behind it. The second caller has a short deadline, and it must be told
// that its deadline passed - it must not wait for the first refresh, and
// it must not report a success.
func TestRefresh_SameInstanceWaitHonorsCancellation(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(
		t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
	)
	// The first refresh is held in the provider.
	release := fake.gateToken()
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), record.ID)
		firstDone <- err
	}()
	fake.waitForTokenRequest(t)

	// The second caller has a deadline of its own, and the lock budget is
	// long enough that only the deadline can end the wait.
	context40, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context40, record.ID)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the waiting caller reported a success after its deadline passed")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the failure is not the caller's deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("the waiting caller was not withdrawn by its deadline")
	}
	// The waiting caller did not reach the provider: the one-provider-
	// request invariant holds.
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1", requests)
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("the first refresh failed: %v", err)
	}
}

// TestRefresh_SameInstanceWaitHonorsTimeout proves that a waiting caller is
// withdrawn by the lock budget.
//
// The context has no deadline of its own, so the budget is the only bound
// the wait has - and the wait must honour it.
func TestRefresh_SameInstanceWaitHonorsTimeout(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresherWithLockTimeout(
		t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(), 40*time.Millisecond,
	)
	// The first refresh is held in the provider.
	release := fake.gateToken()
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), record.ID)
		firstDone <- err
	}()
	fake.waitForTokenRequest(t)

	done := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), record.ID)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the waiting caller reported a success after the lock budget passed")
		}
	case <-time.After(time.Second):
		t.Error("the waiting caller was not withdrawn by the lock budget")
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1", requests)
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("the first refresh failed: %v", err)
	}
}

// TestFileLock_ContendsBetweenDistinctDescriptorsOfOneProcess proves that
// the operating system's lock excludes a second descriptor of the same file
// in the SAME process.
//
// The property is what the cross-process serialization rests on: on the
// platforms whose lock belongs to the open file description (Unix), two
// descriptors of one file are two holders. The property is proved here, on
// the platform the proof runs on, instead of being assumed.
//
// The platforms differ, and the difference is stated rather than assumed: a
// Windows byte-range lock belongs to the PROCESS, so a second descriptor of
// the same file in the same process is NOT a second holder there. The
// in-process admission of the store is what makes that path safe: it keeps
// one descriptor of the lock file open in this process at a time.
func TestFileLock_ContendsBetweenDistinctDescriptorsOfOneProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registration.lock")
	first, err := acquireFileLock(context.Background(), dir, path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	// A second descriptor of the same file, in the same process.
	refusedContext, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	second, err := acquireFileLock(refusedContext, dir, path, time.Second)
	if err == nil {
		second.release()
		if runtime.GOOS == "windows" {
			// A Windows byte-range lock belongs to the process, so a
			// second descriptor of the same file is the SAME holder
			// there. The store's in-process admission is what keeps the
			// Windows path exclusive, and the difference is stated in
			// the lock's documentation.
			t.Log("the byte-range lock of this platform belongs to the process")
		} else {
			t.Fatal("two descriptors of one process took the same lock")
		}
	}
	first.release()
	// After the release, a new holder takes the same file.
	holder, err := acquireFileLock(context.Background(), dir, path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock after the release: %v", err)
	}
	holder.release()
}

// TestFileLock_RefusesAnUntrustedDirectory proves that the trust of the
// state directory is established BEFORE the lock path is opened.
//
// The lock file is opened before the store's own protection check would
// run, and a directory another principal can write to is a directory whose
// lock file that principal can replace.
//
// The mode bits carry no meaning on Windows, and the trust check is a
// documented no-op there: the protection is the inherited access control
// list. The lock is therefore TAKEN on that platform, and the proof is
// about the platforms whose mode bits mean something.
func TestFileLock_RefusesAnUntrustedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the mode bits carry no meaning on Windows; the trust check is a no-op there")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("relax the directory: %v", err)
	}
	path := filepath.Join(dir, "registration.lock")
	if _, err := acquireFileLock(context.Background(), dir, path, time.Second); err == nil {
		t.Fatal("the lock was taken in a directory another principal can write to")
	}
	// The path was not created either: the trust is checked first.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the lock file was created in an untrusted directory")
	}
}

// TestFileLock_RefusesASymlinkedLockPath proves that a symlink at the lock
// path is not followed.
//
// A lock on a symlink's target protects nothing: the target is a file
// another principal chose, and the connector would create it.
func TestFileLock_RefusesASymlinkedLockPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege the test runner may not have")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write the target: %v", err)
	}
	path := filepath.Join(dir, "registration.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}
	if _, err := acquireFileLock(context.Background(), dir, path, time.Second); err == nil {
		t.Fatal("the lock followed a symlink")
	}
}

// TestFileLock_AnAgedLockFileIsNotTakenFromItsHolder proves that elapsed
// time is not evidence of anything.
//
// The lock file is aged while its holder is alive, and a second acquisition
// must be refused. A protocol that reads the age of the file as the death
// of its holder takes the lock away from a live holder here - and a
// rotating token that two processes present at once is a session that the
// provider invalidates.
func TestFileLock_AnAgedLockFileIsNotTakenFromItsHolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registration.lock")
	holder, err := acquireFileLock(context.Background(), dir, path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	defer holder.release()
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the lock file: %v", err)
	}
	refusedContext, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	second, err := acquireFileLock(refusedContext, dir, path, time.Second)
	if err == nil {
		second.release()
		t.Fatal("the aged lock file was taken from its live holder")
	}
}

// TestStore_SaveIsNotUsedByTheCredentialWriters proves that no production
// file of the package replaces a registration's record wholesale.
//
// The store's Save replaces the WHOLE record, and a writer that read it
// before another writer changed it loses that other writer's update. The
// writers of a registration's credentials publish through Publish, which
// applies the change to the record that is stored now, and this guard
// keeps it that way: a production file that calls Save fails the build.
//
// The guard covers the package's own production files. The connector's
// command package never writes a profile record: it drives the manager.
func TestStore_SaveIsNotUsedByTheCredentialWriters(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if wholeRecordReplacement(string(data)) {
			t.Errorf(
				"%s replaces a record with Save; a writer of a registration's credentials must publish through Publish",
				name,
			)
		}
	}
}

// TestStore_TheSaveGuardDetectsAWholeRecordReplacement proves that the
// guard DETECTS the escape hatch.
//
// The guard reads the package's source files, so a proof of the guard
// itself cannot be a proof about a file: the check is proved on the text
// it reads, with a replacement and with a publication.
func TestStore_TheSaveGuardDetectsAWholeRecordReplacement(t *testing.T) {
	if !wholeRecordReplacement("return m.profiles.Save(record)") {
		t.Error("the guard did not detect a whole-record replacement")
	}
	if wholeRecordReplacement("return m.profiles.Publish(ctx, id, change)") {
		t.Error("the guard reported a publication as a whole-record replacement")
	}
}

// TestRefresh_LockWaitHonoursTheCallersContext is the boundary proof of the
// wait: the refresh reports the caller's own cancellation.
//
// The wait is a wait for another writer, and a caller that is withdrawn
// from it is told why.
func TestRefresh_LockWaitHonoursTheCallersContext(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(
		t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
	)
	release := fake.gateToken()
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), record.ID)
		firstDone <- err
	}()
	fake.waitForTokenRequest(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(cancelled, record.ID)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled caller reported a success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the failure is not the caller's cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("the cancelled caller was not withdrawn")
	}
	release()
	<-firstDone
}

// newTestRefresherWithLockTimeout returns a refresher with a lock budget of
// the caller's own.
func newTestRefresherWithLockTimeout(
	t *testing.T,
	store *ProfileStore,
	endpoint string,
	client *http.Client,
	timeout time.Duration,
) *Refresher {
	t.Helper()
	refresher, err := NewRefresher(RefresherConfig{
		Profiles:      store,
		TokenEndpoint: endpoint,
		HTTPClient:    client,
		Now:           time.Now,
		LockTimeout:   timeout,
	})
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	return refresher
}

// The environment of the cross-process holder.
const (
	rotationHolderDir     = "AIP_SIWC_ROTATION_HOLDER_DIR"
	rotationHolderProfile = "AIP_SIWC_ROTATION_HOLDER_PROFILE"
)

// TestRotationLockHolderHelper is the helper process of the cross-process
// proof.
//
// The helper takes the rotation lock of one registration and holds it. The
// parent kills it.
func TestRotationLockHolderHelper(t *testing.T) {
	dir := os.Getenv(rotationHolderDir)
	profile := os.Getenv(rotationHolderProfile)
	if dir == "" || profile == "" {
		t.Skip("not the rotation lock holder")
	}
	store, err := NewProfileStore(dir)
	if err != nil {
		os.Exit(2)
	}
	release, err := store.acquireProfileLock(
		context.Background(), dir, profile, rotationLockKind, 5*time.Second,
	)
	if err != nil {
		os.Exit(3)
	}
	defer release()
	_, _ = os.Stdout.WriteString(lockHolderReady + "\n")
	time.Sleep(2 * time.Minute)
}

// TestRefresh_CrossProcessHolderExcludesTheRefresh proves that the rotation
// lock excludes a holder in ANOTHER process.
//
// The holder is a separate process, and the refresh must wait for it: a
// rotating token is single-use, and two refreshes of one token invalidate
// each other. The holder is then killed, and the operating system releases
// the lock - which is what lets the refresh proceed.
func TestRefresh_CrossProcessHolderExcludesTheRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("the helper process is not run in the short mode")
	}
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(
		t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
	)

	command := exec.Command(os.Args[0], "-test.run=^TestRotationLockHolderHelper$")
	command.Env = append(os.Environ(),
		rotationHolderDir+"="+store.Path(),
		rotationHolderProfile+"="+record.ID,
	)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open the helper's output: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	scanner := bufio.NewScanner(output)
	held := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), lockHolderReady) {
			held = true
			break
		}
	}
	if !held {
		t.Fatal("the helper did not take the rotation lock")
	}

	// The refresh must wait for the holder in the other process.
	done := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), record.ID)
		done <- err
	}()
	select {
	case err := <-done:
		t.Errorf("the refresh did not wait for the holder in the other process: %v", err)
	case <-time.After(time.Second):
	}
	// The holder is killed: the operating system releases the lock.
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	_ = command.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the refresh failed after the holder died: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the refresh did not proceed after the holder died")
	}
}
