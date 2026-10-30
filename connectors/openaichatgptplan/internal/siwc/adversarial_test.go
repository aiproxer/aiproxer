package siwc

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The adversarial proofs of this slice.
//
// The four schedules below are the ones a rotating credential is lost on:
//
//   - a LIVE holder whose lock is taken away because it took too long
//     (the lock must not be inferred from elapsed time);
//   - TWO holders that take each other's lock by unlinking a path by name
//     (the lock must not be recovered by unlinking);
//   - a refresh that publishes a rotation over a session that was
//     reauthorized while the refresh waited for the provider (a lost
//     update); and
//   - a bounded read that samples a PREFIX of a longer body and reports it
//     as the whole body.
//
// Every proof is at the boundary where the property is observable: the
// provider's traffic, the stored record, or the operating system's lock.

// ageRefreshLock makes the rotation lock file look old.
//
// The age is what the previous implementation read as the death of a
// holder. The proof ages the file anyway: an elapsed time must not change
// what a live holder owns.
//
// A file that is not there is created with the old timestamp: the proof is
// about what a holder keeps, not about the presence of the file.
func ageRefreshLock(t *testing.T, store *ProfileStore, id string) {
	t.Helper()
	path := filepath.Join(store.Path(), rotationLockName(id))
	old := time.Now().Add(-2 * time.Hour)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if createErr != nil {
			t.Fatalf("create the rotation lock: %v", createErr)
		}
		_ = file.Close()
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the rotation lock: %v", err)
	}
}

// TestRefresh_ALiveOwnersLockIsNotStolen proves that a live holder keeps its
// lock.
//
// The first refresh is held in the provider, the lock file is aged past any
// bound, and a second refresher is started against the same store. The
// provider must receive ONE request: the second refresh may not present the
// same rotating token a second time.
func TestRefresh_ALiveOwnersLockIsNotStolen(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	endpoint := fake.endpoint("/api/accounts/oauth/token")
	first := newTestRefresher(t, store, endpoint, fake.client())
	second := newTestRefresher(t, store, endpoint, fake.client())

	// The first refresh is held in the provider.
	release := fake.gateToken()
	defer release()
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.Refresh(context.Background(), record.ID)
		firstDone <- err
	}()
	fake.waitForTokenRequest(t)

	// The lock file is aged: an elapsed time is not evidence of death.
	ageRefreshLock(t, store, record.ID)

	// The second refresher waits for the lock, and it waits only as long as
	// its own bound.
	secondContext, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := second.Refresh(secondContext, record.ID)
		secondDone <- err
	}()
	// The second refresh is bounded by its own context, and the wait for
	// it is bounded too: a proof that waits forever is a proof that
	// hangs.
	var secondErr error
	select {
	case secondErr = <-secondDone:
	case <-time.After(time.Second):
		t.Error("the second refresh was not withdrawn by its deadline")
	}
	if secondErr != nil && !strings.Contains(secondErr.Error(), "lock") &&
		!strings.Contains(secondErr.Error(), "cancelled") {
		t.Errorf("the second refresh failed unexpectedly: %v", secondErr)
	}

	// The first refresh is released and completes.
	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("the first refresh failed: %v", err)
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Fatalf("the provider was asked %d times, want 1", requests)
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != "rotated-refresh-token" {
		t.Errorf("the stored refresh token = %q, want the rotation's", stored.RefreshToken)
	}
}

// TestFileLock_TheLockIsNotUnlinked proves that a release never unlinks the
// lock file.
//
// A lock file that is unlinked on release cannot be recovered safely: a
// process that holds the descriptor believes it owns the lock while
// another process creates a NEW file with the same name, and a competing
// recovery step removes that new file by name. The lock file therefore
// stays, and every holder locks the SAME file.
func TestFileLock_TheLockIsNotUnlinked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registration.lock")
	first, err := acquireFileLock(context.Background(), filepath.Dir(path), path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock file is missing: %v", err)
	}
	// A second holder is refused while the first one holds the lock.
	refusedContext, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireFileLock(refusedContext, filepath.Dir(path), path, time.Second); err == nil {
		t.Fatal("two holders took the same lock")
	}
	first.release()
	// The file is still there: a release does not unlink it.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the release removed the lock file: %v", err)
	}
	// A new holder locks the same file.
	second, err := acquireFileLock(context.Background(), filepath.Dir(path), path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock after the release: %v", err)
	}
	second.release()
}

// TestFileLock_TheLockIsReleasedWhenTheHolderDies proves that the operating
// system releases the lock when the holder dies.
//
// The holder is a separate PROCESS, and it is killed. The lock must be
// available afterwards: a connector that is stopped by its host must not
// leave its registration locked forever.
func TestFileLock_TheLockIsReleasedWhenTheHolderDies(t *testing.T) {
	if testing.Short() {
		t.Skip("the helper process is not run in the short mode")
	}
	path := filepath.Join(t.TempDir(), "registration.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestLockHolderHelper$")
	command.Env = append(os.Environ(), lockHolderPath+"="+path)
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
	// The helper announces that it holds the lock.
	scanner := bufio.NewScanner(output)
	held := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), lockHolderReady) {
			held = true
			break
		}
	}
	if !held {
		t.Fatal("the helper did not take the lock")
	}
	// The holder is killed: the operating system closes its descriptors.
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	_ = command.Wait()
	// The lock is available again.
	lock, err := acquireFileLock(context.Background(), filepath.Dir(path), path, 5*time.Second)
	if err != nil {
		t.Fatalf("the lock was not released when the holder died: %v", err)
	}
	lock.release()
}

// The helper process of the death proof.
const (
	lockHolderPath  = "AIP_SIWC_LOCK_HOLDER_PATH"
	lockHolderReady = "SIWC_LOCK_HOLDER_READY"
)

// TestLockHolderHelper is the helper process of the death proof.
//
// The helper takes the lock and then waits. The parent kills it.
func TestLockHolderHelper(t *testing.T) {
	path := os.Getenv(lockHolderPath)
	if path == "" {
		t.Skip("not the lock holder")
	}
	lock, err := acquireFileLock(context.Background(), filepath.Dir(path), path, 5*time.Second)
	if err != nil {
		os.Exit(2)
	}
	defer lock.release()
	_, _ = os.Stdout.WriteString(lockHolderReady + "\n")
	if f, ok := any(os.Stdout).(*os.File); ok {
		_ = f.Sync()
	}
	time.Sleep(2 * time.Minute)
}

// TestLogout_DoesNotConfirmAResponseThatIsLongerThanTheBound proves that
// the confirmation is a statement about the WHOLE body.
//
// The provider answers the documented success status with a body that is
// longer than the bound: whitespace, and then an error object. A reader
// that samples the bound's worth of the body sees nothing but whitespace
// and reports a confirmed revocation, while the response is not the
// documented empty success.
func TestLogout_DoesNotConfirmAResponseThatIsLongerThanTheBound(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	body := strings.Repeat(" ", maxRevocationResponseBytes) + `{"error":"not-confirmed"}`
	fake.setRevocationResponse(http.StatusOK, body)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("a response that is not the documented success was confirmed")
	}
	if result.RevocationOutcome != RevocationOutcomeUnexpected {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeUnexpected)
	}
	if result.Guidance == "" {
		t.Error("an unconfirmed revocation carries no guidance")
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
}

// TestRefresher_RefusesAResponseThatIsLongerThanTheBound proves that an
// oversize response is refused before it is decoded.
//
// The body is a COMPLETE and VALID token set, padded with whitespace to
// the bound, and followed by a second JSON object. A reader that truncates
// at the bound decodes the complete prefix and publishes it; the connector
// must refuse the response instead.
func TestRefresher_RefusesAResponseThatIsLongerThanTheBound(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	document := `{"access_token":"a","refresh_token":"b","expires_in":3600}`
	body := document + strings.Repeat(" ", maxTokenResponseBytes-len(document)) +
		`{"access_token":"c","refresh_token":"d"}`
	fake.setTokenResponse(http.StatusOK, body)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	_, err := refresher.Refresh(context.Background(), record.ID)
	requireRefreshClass(t, err, RefreshClassUnreadable)
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.AccessToken != record.AccessToken {
		t.Error("a response that was not read whole was published")
	}
	if stored.RefreshToken != record.RefreshToken {
		t.Error("the refresh token was replaced by a response that was not read whole")
	}
}
