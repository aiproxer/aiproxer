package siwc

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

// The state of the local record after a failed refresh.
//
// The store's publication boundary separates THREE facts, and the operator's
// diagnostic has to keep them apart:
//
//   - the PUBLICATION: the record was written, and a reader sees it;
//   - the DURABILITY: the write was confirmed, and a crash cannot roll it
//     back; and
//   - the SUPERSESSION: a different, newer credential is stored.
//
// A publication whose durability was NOT confirmed is neither a committed
// quarantine nor an unpublished record: the rename happened, the record is in
// place, and only its persistence is unproven. A diagnostic that calls that
// a committed quarantine claims that the tokens are cleared; one that calls
// it a failed quarantine claims that the registration is NOT marked. Both
// claims are false, and the operator cannot act on either of them.
//
// The proofs below drive the REAL refresh over the REAL store and fault the
// operating system's directory flush - the package's own durability seam -
// AFTER the atomic rename has already happened.

// TestRefresh_ThePostRenameFlushFailureOfARotationIsNotACommittedQuarantine
// proves that a rotation whose DURABILITY was not confirmed is not reported
// as a committed quarantine.
//
// The provider rotated the token and the connector published the
// replacement: the rename happened, and the record on the disk holds the
// replacement. Only the confirmation of the directory entry failed.
//
// The stored credential is therefore the connector's OWN replacement: it is
// neither stale nor superseded, and there is nothing to quarantine. The
// recovery must not run - it would reload that very replacement and mistake
// it for a superseding credential - and the diagnostic must not claim that
// the quarantine was committed.
//
//nolint:paralleltest // swaps the package-level dirSync durability seam; parallel siblings would race.
func TestRefresh_ThePostRenameFlushFailureOfARotationIsNotACommittedQuarantine(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	flushes := 0
	dirSync = func(string) error {
		flushes++
		return errors.New("siwc: injected directory flush failure")
	}

	transport := &cannedTransport{status: http.StatusOK, body: `{"access_token":"refreshed-access-token",` +
		`"refresh_token":"rotated-refresh-token","token_type":"bearer",` +
		`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	_, err := refresher.Refresh(context.Background(), record.ID)
	if err == nil {
		t.Fatalf("a refresh whose durability was not confirmed reported SUCCESS")
	}
	rendered := err.Error()
	t.Logf("err=%q flushes=%d", rendered, flushes)
	// The quarantine was NOT committed: the replacement is stored and the
	// registration is not marked.
	if strings.Contains(rendered, "quarantine was committed") {
		t.Errorf("the diagnostic claims a committed quarantine although the replacement is stored: %q", rendered)
	}
	if strings.Contains(rendered, "NOT marked") {
		t.Errorf("the diagnostic claims that the registration is NOT marked although the replacement is stored: %q", rendered)
	}
	// The credential is not unusable: the record on the disk holds the
	// replacement, and the failure must not ask for a new authorization.
	if errors.Is(err, ErrRefreshTerminal) {
		t.Errorf("the diagnostic claims that the credential is unusable although the replacement is stored: %q", rendered)
	}
	var classified *RefreshError
	if !errors.As(err, &classified) {
		t.Fatalf("the failure is not a classified refresh failure: %v", err)
	}
	// The commit outcome names the state that was established: the record
	// was published and its durability was not confirmed. The value is
	// asserted as a literal, because the VALUE is the operator-facing
	// contract and not the name of the constant that carries it.
	if classified.Commit != "unconfirmed" {
		t.Errorf("the commit outcome = %q, want %q", classified.Commit, "unconfirmed")
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.RefreshToken != "rotated-refresh-token" {
		t.Errorf("the published replacement is not on the disk: %q", stored.RefreshToken)
	}
	if stored.NeedsReauth {
		t.Errorf("the registration was marked although the replacement was published")
	}
	// The recovery must not run: it would reload the connector's own
	// just-published replacement.
	if flushes != 1 {
		t.Errorf("the connector attempted %d directory flushes, want 1 (the recovery must not run)", flushes)
	}
}

// TestRefresh_ThePostRenameFlushFailureOfTheTerminalClearingIsNotReportedAsNotMarked
// proves that a quarantine whose DURABILITY was not confirmed does not claim
// that the registration is NOT marked.
//
// The provider refused the token, and the connector published the clearing
// and the mark: the rename happened, and the record on the disk is marked
// and holds no token. Only the confirmation of the directory entry failed.
//
// The registration IS marked, so a diagnostic that says "NOT marked" states
// a local fact that is false, and the operator is told to do something the
// store has already done.
//
//nolint:paralleltest // swaps the package-level dirSync durability seam; parallel siblings would race.
func TestRefresh_ThePostRenameFlushFailureOfTheTerminalClearingIsNotReportedAsNotMarked(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	dirSync = func(string) error {
		return errors.New("siwc: injected directory flush failure")
	}

	transport := &cannedTransport{status: http.StatusBadRequest, body: `{"error":"refresh_token_reused"}`}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	_, err := refresher.Refresh(context.Background(), record.ID)
	if err == nil {
		t.Fatalf("a refused token whose quarantine was not confirmed reported SUCCESS")
	}
	rendered := err.Error()
	t.Logf("err=%q", rendered)
	if strings.Contains(rendered, "NOT marked") {
		t.Errorf("the diagnostic claims that the registration is NOT marked although the rename marked it: %q", rendered)
	}
	if strings.Contains(rendered, "cleared") {
		t.Errorf("the diagnostic claims that the tokens are cleared although the durability was not confirmed: %q", rendered)
	}
	var classified *RefreshError
	if !errors.As(err, &classified) {
		t.Fatalf("the failure is not a classified refresh failure: %v", err)
	}
	if classified.Commit != "unconfirmed" {
		t.Errorf("the commit outcome = %q, want %q", classified.Commit, "unconfirmed")
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	// The rename published the clearing and the mark: the record on the
	// disk IS marked and holds no token.
	if stored.RefreshToken != "" {
		t.Errorf("the unusable token was kept although the rename cleared it: %q", stored.RefreshToken)
	}
	if !stored.NeedsReauth {
		t.Errorf("the registration is not marked although the rename marked it")
	}
}

// TestRefresh_TheGenuineSupersessionIsNotReportedAsACommittedQuarantine proves
// that a recovery which finds a NEWER credential reports the supersession
// and not a committed quarantine.
//
// The provider rotated the token, and the publication of the replacement
// fails before the rename: the saved credential is stale, and the recovery
// has to quarantine it. A newer credential is stored by then - a
// reauthorization that completed in the meantime - and it is NOT stale.
//
// The recovery is therefore a no-op, and a no-op is not a committed
// quarantine: the registration holds a newer credential, and the caller is
// handed the credential that is there now. The newer authorization must not
// be erased.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestRefresh_TheGenuineSupersessionIsNotReportedAsACommittedQuarantine(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	// The newer credential. It is published with the store's whole-record
	// write, because the refresh holds the registration's publication lock
	// while it publishes: a concurrent writer would have to wait for it,
	// and the proof needs the newer credential to be on the disk BEFORE
	// the recovery reads it.
	newer := record
	newer.AccessToken = "newer-access-token"
	newer.RefreshToken = "newer-refresh-token"

	originalFile, originalDir := fileSync, dirSync
	t.Cleanup(func() { fileSync, dirSync = originalFile, originalDir })
	var mutex sync.Mutex
	flushes := 0
	fileSync = func(*os.File) error {
		mutex.Lock()
		flushes++
		first := flushes == 1
		mutex.Unlock()
		if !first {
			return nil
		}
		if err := store.Save(newer); err != nil {
			t.Errorf("publish the newer credential: %v", err)
		}
		return errors.New("siwc: injected file sync failure")
	}

	transport := &cannedTransport{status: http.StatusOK, body: `{"access_token":"refreshed-access-token",` +
		`"refresh_token":"rotated-refresh-token","token_type":"bearer",` +
		`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	updated, err := refresher.Refresh(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("the refresh failed although the registration holds a newer credential: %v", err)
	}
	t.Logf("updated refresh=%q needsReauth=%v", updated.RefreshToken, updated.NeedsReauth)
	if updated.RefreshToken != newer.RefreshToken {
		t.Errorf("the caller was not handed the credential that is there now: %q", updated.RefreshToken)
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	// The newer authorization is not erased.
	if stored.RefreshToken != newer.RefreshToken {
		t.Errorf("the newer credential was erased: %q", stored.RefreshToken)
	}
	if stored.NeedsReauth {
		t.Errorf("the newer credential was marked as unusable")
	}
}
