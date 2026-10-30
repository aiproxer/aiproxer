// Tests for the SIWC profile record and its protected, atomic store.
//
// The contract under test is requirement 2.11, 2.12 and 3.6 of the
// migration specification, and the slice of requirement 14.1 that belongs
// to this task: the record, the store, the profile identity and the
// selection.
//
// The proofs live at the store boundary - the only place where the
// guarantees are observable - instead of mirroring the internal call graph.
package siwc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The secrets the proofs plant in a record. They are the values that must
// not appear in a listing, in a summary or in an error.
const (
	testAccessToken  = "test-access-token-material"
	testRefreshToken = "test-refresh-token-material"
	testIDToken      = "test-id-token-material"
	testEmailHint    = "person@example.invalid"
)

// testProfileRecord returns a valid record for the store proofs.
func testProfileRecord(id string) ProfileRecord {
	return ProfileRecord{
		ID:             id,
		Label:          "work",
		Subject:        "subject-" + id,
		EmailHint:      testEmailHint,
		IssuedClientID: "issued-client-" + id,
		HostID:         "urn:uuid:11111111-2222-4333-8444-555555555555",
		GrantedScopes:  []string{"openid", "chatgpt.tokens.use.direct"},
		AccessToken:    testAccessToken,
		RefreshToken:   testRefreshToken,
		IDToken:        testIDToken,
		Expiry:         time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}
}

// newTestProfileStore returns a store in a fresh temporary directory.
func newTestProfileStore(t *testing.T) *ProfileStore {
	t.Helper()
	store, err := NewProfileStore(filepath.Join(t.TempDir(), "profiles"))
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	return store
}

// requireSameRecord compares two records field by field.
//
// A record holds credentials, so a failure message names the field that
// differs and never repeats a value.
func requireSameRecord(t *testing.T, got, want ProfileRecord) {
	t.Helper()
	if got.Version != want.Version {
		t.Errorf("version = %d, want %d", got.Version, want.Version)
	}
	if got.ID != want.ID {
		t.Errorf("id = %q, want %q", got.ID, want.ID)
	}
	if got.Label != want.Label {
		t.Errorf("label = %q, want %q", got.Label, want.Label)
	}
	if got.Subject != want.Subject {
		t.Errorf("subject = %q, want %q", got.Subject, want.Subject)
	}
	if got.EmailHint != want.EmailHint {
		t.Errorf("email hint = %q, want %q", got.EmailHint, want.EmailHint)
	}
	if got.IssuedClientID != want.IssuedClientID {
		t.Errorf("issued client id = %q, want %q", got.IssuedClientID, want.IssuedClientID)
	}
	if got.HostID != want.HostID {
		t.Errorf("host id = %q, want %q", got.HostID, want.HostID)
	}
	if !reflect.DeepEqual(got.GrantedScopes, want.GrantedScopes) {
		t.Errorf("granted scopes = %v, want %v", got.GrantedScopes, want.GrantedScopes)
	}
	if got.AccessToken != want.AccessToken {
		t.Error("the access token does not round-trip")
	}
	if got.RefreshToken != want.RefreshToken {
		t.Error("the refresh token does not round-trip")
	}
	if got.IDToken != want.IDToken {
		t.Error("the retained ID token does not round-trip")
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("expiry = %s, want %s", got.Expiry, want.Expiry)
	}
	if got.NeedsReauth != want.NeedsReauth {
		t.Errorf("needs reauth = %t, want %t", got.NeedsReauth, want.NeedsReauth)
	}
}

// writeStoreFile writes a raw document into the store's directory.
//
// The file is written with the store's own protections, so a proof about the
// store's own behavior is not confused with a proof about an unprotected
// file.
func writeStoreFile(t *testing.T, store *ProfileStore, name, content string) {
	t.Helper()
	path := filepath.Join(store.Path(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// requireSameDirectory proves that two paths name the same directory.
//
// The comparison goes through the operating system, because a path that
// contains a symlink - the temporary directory of a test on macOS, for
// example - names the same directory as its resolved spelling.
func requireSameDirectory(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Errorf("stat %q: %v", got, err)
		return
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Errorf("stat %q: %v", want, err)
		return
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Errorf("%q and %q are not the same directory", got, want)
	}
}

// TestProfileStore_SaveAndLoadRoundTrip proves that a saved profile is read
// back unchanged.
//
// The record is the state a registration persists: the validated subject,
// the issued client id, the granted scopes, the token set, the expiry, the
// retained identity hints, the local label and the reauthorization state
// must all survive the round trip.
func TestProfileStore_SaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	want := testProfileRecord("alpha")
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want.Version = profileRecordVersion

	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	requireSameRecord(t, got, want)
}

// TestProfileStore_PersistsTheDesignDocumentShape proves that the stored
// document is the shape the design fixes.
//
// The design names the persisted fields, and a later build must be able to
// read what this build wrote: the field names are part of the contract.
func TestProfileStore_PersistsTheDesignDocumentShape(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	record := testProfileRecord("alpha")
	// The design omits the optional fields when they carry no value, so the
	// shape is pinned with every field set.
	record.NeedsReauth = true
	if err := store.Save(record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the stored record is not a JSON object: %v", err)
	}
	for _, field := range []string{
		"version",
		"id",
		"label",
		"subject",
		"email_hint",
		"issued_client_id",
		"host_id",
		"granted_scopes",
		"access_token",
		"refresh_token",
		"id_token",
		"expiry",
		"needs_reauth",
	} {
		if _, ok := document[field]; !ok {
			t.Errorf("the stored record has no %q field", field)
		}
	}
}

// TestProfileStore_AnUnwrittenStoreHoldsNoProfiles proves that a store that
// was never written is empty instead of an error.
//
// A connector that starts before the first registration has no profiles to
// list, and that is not a failure.
func TestProfileStore_AnUnwrittenStoreHoldsNoProfiles(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("the store lists %d profiles, want none", len(summaries))
	}
	if summaries == nil {
		t.Error("the listing of an unwritten store must be empty, not absent")
	}
}

// TestProfileStore_StoresEveryProfileSeparately proves that the store holds
// more than one profile.
//
// The manager lists and selects the saved registrations explicitly, and the
// email is never the key: two registrations that share an email hint are
// two profiles.
func TestProfileStore_StoresEveryProfileSeparately(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	second := testProfileRecord("beta")
	second.Subject = "subject-beta"
	second.IssuedClientID = "issued-client-beta"
	if first.EmailHint != second.EmailHint {
		t.Fatal("the proof needs two registrations that share an email hint")
	}
	for _, record := range []ProfileRecord{first, second} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%s): %v", record.ID, err)
		}
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("the store lists %d profiles, want 2", len(summaries))
	}
	if summaries[0].ID != "alpha" || summaries[1].ID != "beta" {
		t.Errorf("the listing is %q, %q; want the profiles ordered by id", summaries[0].ID, summaries[1].ID)
	}
	if summaries[0].Subject != first.Subject || summaries[1].Subject != second.Subject {
		t.Error("the listing does not report the validated subject of every profile")
	}
}

// TestProfileStore_ListCarriesNoTokenMaterial proves that the listing
// surface carries no credential material.
//
// The listing is what a diagnostic, a listing command or a support
// transcript may carry, so it must not repeat a token, and it must not
// repeat the retained identity hint either.
func TestProfileStore_ListCarriesNoTokenMaterial(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal the listing: %v", err)
	}
	text := string(encoded) + fmt.Sprintf("%v", summaries)
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint} {
		if strings.Contains(text, secret) {
			t.Errorf("the listing carries secret material %q", secret)
		}
	}
	if summaries[0].ID != "alpha" {
		t.Errorf("the listing reports the profile id %q, want %q", summaries[0].ID, "alpha")
	}
	if summaries[0].Label != "work" {
		t.Errorf("the listing reports the label %q, want %q", summaries[0].Label, "work")
	}
	if !reflect.DeepEqual(summaries[0].GrantedScopes, []string{"openid", "chatgpt.tokens.use.direct"}) {
		t.Errorf("the listing reports the scopes %v", summaries[0].GrantedScopes)
	}
	if !summaries[0].Expiry.Equal(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("the listing reports the expiry %s", summaries[0].Expiry)
	}
	if summaries[0].NeedsReauth {
		t.Error("the listing reports a reauthorization state that was never set")
	}
}

// TestProfileStore_ReauthStateIsPersisted proves that the reauthorization
// state survives the store, because a terminal refresh failure has to
// survive a restart.
func TestProfileStore_ReauthStateIsPersisted(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	record := testProfileRecord("alpha")
	record.NeedsReauth = true
	if err := store.Save(record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.NeedsReauth {
		t.Error("the reauthorization state did not survive the store")
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !summaries[0].NeedsReauth {
		t.Error("the listing does not report the reauthorization state")
	}
}

// TestProfileStore_LoadRefusesAnUnknownVersion proves that a record written
// by another build is refused instead of being interpreted with the wrong
// schema.
func TestProfileStore_LoadRefusesAnUnknownVersion(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the stored record is not a JSON object: %v", err)
	}
	for _, version := range []float64{0, 2, 99} {
		// The document is otherwise the COMPLETE, VALID one the store wrote,
		// so the version is the only reason it is refused: a skeleton that
		// were invalid for other reasons would keep the proof green after the
		// version check was removed.
		document["version"] = version
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("encode the record with version %v: %v", version, err)
		}
		writeStoreFile(t, store, "profile-alpha.json", string(encoded))
		if _, err := store.Load("alpha"); err == nil {
			t.Errorf("Load must refuse a stored record with version %v", version)
		} else if !strings.Contains(err.Error(), "version") {
			t.Errorf("error %q must state that the version is unusable", err)
		}
	}
}

// TestProfileStore_SaveRefusesAForeignVersion proves that the store owns the
// persisted format version: a record that names a different version is
// refused instead of being rewritten.
func TestProfileStore_SaveRefusesAForeignVersion(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	record := testProfileRecord("alpha")
	record.Version = 99
	err := store.Save(record)
	if err == nil {
		t.Fatal("Save must refuse a record that names a foreign version")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q must state that the version is unusable", err)
	}
}

// TestProfileStore_LoadRefusesACorruptDocument proves that a stored record
// that cannot be parsed is an explicit failure.
//
// The record holds the only copy of a refresh token, so a corrupt record is
// never silently replaced or skipped.
func TestProfileStore_LoadRefusesACorruptDocument(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, content := range []string{
		"",
		"{",
		"not json at all",
		`{"version":1,"id":"alpha","subject":`,
	} {
		writeStoreFile(t, store, "profile-alpha.json", content)
		_, err := store.Load("alpha")
		if err == nil {
			t.Errorf("Load must refuse the corrupt document %q", content)
		} else if !strings.Contains(err.Error(), "invalid profile-alpha.json JSON") {
			// The refusal must be the parse failure, not an incidental one:
			// a record that could not be decoded must not be reported as a
			// record that was decoded and then rejected for its content.
			t.Errorf("error %q must classify profile-alpha.json as unparseable", err)
		}
	}
}

// TestProfileStore_LoadRefusesASymlinkedRecord proves that a record that is
// a symlink is refused.
//
// A record that resolves to another file would let a local attacker feed
// the connector a document it did not write.
//
// The document the symlink points at is the COMPLETE, VALID document the
// store itself wrote, so the symlink is the only reason the record is
// refused: a fixture that were invalid for other reasons would keep the
// proof green after the symlink protection was removed.
func TestProfileStore_LoadRefusesASymlinkedRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs privileges on Windows")
	}
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stored := filepath.Join(store.Path(), "profile-alpha.json")
	complete, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(elsewhere, complete, 0o600); err != nil {
		t.Fatalf("write the foreign document: %v", err)
	}
	if err := os.Remove(stored); err != nil {
		t.Fatalf("remove the stored record: %v", err)
	}
	if err := os.Symlink(elsewhere, stored); err != nil {
		t.Fatalf("link the foreign document: %v", err)
	}
	_, err = store.Load("alpha")
	if err == nil {
		t.Fatal("Load must refuse a record that is a symlink")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error %q must state that the record is not a regular file", err)
	}
}

// TestProfileStore_LoadRefusesAMismatchedProfileID proves that a record
// whose identity does not match the file it was read from is refused.
//
// A file that was copied under another name would otherwise change which
// profile a read returns.
//
// The copied document is the COMPLETE, VALID document the store itself
// wrote, so the identity mismatch is the only reason it is refused.
func TestProfileStore_LoadRefusesAMismatchedProfileID(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	complete, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	writeStoreFile(t, store, "profile-beta.json", string(complete))

	_, err = store.Load("beta")
	if err == nil {
		t.Fatal("Load must refuse a record that carries another profile id")
	}
	if !strings.Contains(err.Error(), "different profile id") {
		t.Errorf("error %q must state that the record carries a different profile id", err)
	}
}

// TestProfileStore_ListRefusesARecordWhoseIdentityDoesNotMatchItsFile proves
// that the listing refuses a record whose identity does not match the file it
// was read from.
//
// The listing is a view of the store's profiles, and a record that the store
// refuses to load must not appear in it under another name either.
func TestProfileStore_ListRefusesARecordWhoseIdentityDoesNotMatchItsFile(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	complete, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	writeStoreFile(t, store, "profile-beta.json", string(complete))

	_, err = store.List()
	if err == nil {
		t.Fatal("List must refuse a record that carries another profile id")
	}
	if !strings.Contains(err.Error(), "different profile id") {
		t.Errorf("error %q must state that the record carries a different profile id", err)
	}
}

// TestProfileStore_SelectRefusesAProfileWhoseIdentityDoesNotMatchItsFile
// proves that the selection refuses a profile whose record the store refuses
// to load.
//
// A selection names the profile the connector uses. A selection of a record
// that carries another profile's identity would point the connector at a
// credential it cannot read, so the selection must fail the same way the
// read fails.
func TestProfileStore_SelectRefusesAProfileWhoseIdentityDoesNotMatchItsFile(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	complete, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	writeStoreFile(t, store, "profile-beta.json", string(complete))

	err = store.Select("beta")
	if err == nil {
		t.Fatal("Select must refuse a profile whose record carries another profile id")
	}
	if !strings.Contains(err.Error(), "different profile id") {
		t.Errorf("error %q must state that the record carries a different profile id", err)
	}
}

// TestProfileStore_AFailedSelectionKeepsThePreviousSelection proves that a
// refused selection does not replace the selection that was already stored.
//
// The selection is the pointer the connector uses, so a failed selection
// must leave the previous, valid pointer in place.
func TestProfileStore_AFailedSelectionKeepsThePreviousSelection(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Select("alpha"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	complete, err := os.ReadFile(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	writeStoreFile(t, store, "profile-beta.json", string(complete))

	if err := store.Select("beta"); err == nil {
		t.Fatal("Select must refuse a profile whose record carries another profile id")
	}
	selected, err := store.Selected()
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "alpha" {
		t.Errorf("the refused selection replaced the stored selection: %q, want %q", selected, "alpha")
	}
}

// TestProfileStore_LoadRefusesAnAccessibleRecord proves that a record other
// local users can read is refused.
//
// The record holds the tokens of a subscription credential, so a readable
// record is a leaked credential.
func TestProfileStore_LoadRefusesAnAccessibleRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix mode bits carry no meaning on Windows")
	}
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(store.Path(), "profile-alpha.json")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("relax the record's mode: %v", err)
	}
	if _, err := store.Load("alpha"); err == nil {
		t.Fatal("Load must refuse a record that other users can read")
	}
}

// TestProfileStore_RecordsAreOwnerOnly proves that the store writes the
// record and the directory with owner-only permissions.
func TestProfileStore_RecordsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix mode bits carry no meaning on Windows")
	}
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	record, err := os.Stat(filepath.Join(store.Path(), "profile-alpha.json"))
	if err != nil {
		t.Fatalf("stat the stored record: %v", err)
	}
	if record.Mode().Perm() != 0o600 {
		t.Errorf("the stored record has mode %o, want 0600", record.Mode().Perm())
	}
	directory, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat the store directory: %v", err)
	}
	if directory.Mode().Perm() != 0o700 {
		t.Errorf("the store directory has mode %o, want 0700", directory.Mode().Perm())
	}
}

// TestProfileStore_RefusesAWorldWritableStoreDirectory proves that the
// store refuses a directory another local user can write to.
//
// The record's own mode protects its contents, but a principal that can
// write the directory can unlink the record and replace it with its own.
func TestProfileStore_RefusesAWorldWritableStoreDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix mode bits carry no meaning on Windows")
	}
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the store directory: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("relax the store directory's mode: %v", err)
	}
	store, err := NewProfileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(testProfileRecord("alpha")); err == nil {
		t.Error("Save must refuse a store directory that other users can write to")
	}
	// A stored record in that directory is refused as well: the directory
	// is the trust boundary, not only the record's own permissions. The
	// document is the COMPLETE, VALID one the store writes, so the directory
	// is the only reason it is refused: a skeleton that were invalid for
	// other reasons would keep the proof green after the directory check was
	// removed.
	planted := testProfileRecord("alpha")
	planted.Version = profileRecordVersion
	encoded, err := json.Marshal(planted)
	if err != nil {
		t.Fatalf("marshal the planted record: %v", err)
	}
	writeStoreFile(t, store, "profile-alpha.json", string(encoded))
	if _, err := store.Load("alpha"); err == nil {
		t.Error("Load must refuse a store directory that other users can write to")
	}
}

// TestProfileStore_SaveReplacesTheStoredRecord proves that one profile's
// record can be replaced.
//
// A refresh token rotation replaces the stored record, so the store has to
// be able to replace one and not only to create one.
func TestProfileStore_SaveReplacesTheStoredRecord(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rotated := first
	rotated.RefreshToken = "rotated-refresh-token-material"
	rotated.Expiry = first.Expiry.Add(time.Hour)
	if err := store.Save(rotated); err != nil {
		t.Fatalf("Save (replacement): %v", err)
	}

	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RefreshToken != rotated.RefreshToken {
		t.Error("the replaced record did not take effect")
	}
	if !got.Expiry.Equal(rotated.Expiry) {
		t.Error("the replaced record kept the previous expiry")
	}

	// The replacement must not leave a second copy of the profile behind.
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Errorf("the store lists %d profiles after the replacement, want 1", len(summaries))
	}
}

// TestProfileStore_AFailedReplacementKeepsThePreviousRecord proves that a
// replacement that cannot be completed does not lose the record it was
// replacing.
//
// The refresh token in the previous record is the only copy the connector
// has, so a failed write must leave it in place.
func TestProfileStore_AFailedReplacementKeepsThePreviousRecord(t *testing.T) {
	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	original := fileSync
	t.Cleanup(func() { fileSync = original })
	fileSync = func(*os.File) error { return errors.New("siwc: injected file sync failure") }

	rotated := first
	rotated.RefreshToken = "rotated-refresh-token-material"
	if err := store.Save(rotated); err == nil {
		t.Fatal("Save must report the failed flush")
	}

	// The failed replacement must not have damaged the previous record.
	// The seam is restored first, because the load does not flush anything
	// but the record must still be readable.
	fileSync = original
	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load after the failed replacement: %v", err)
	}
	if got.RefreshToken != first.RefreshToken {
		t.Error("the failed replacement lost the previous record")
	}
}

// TestProfileStore_FlushesTheRecordBeforeTheRenamePublishesIt proves that
// the bytes are durable before the record becomes reachable under its final
// name.
//
// Without that order a crash after the publication can leave the record
// present but empty, which the load path rejects as corrupt.
func TestProfileStore_FlushesTheRecordBeforeTheRenamePublishesIt(t *testing.T) {
	original := fileSync
	t.Cleanup(func() { fileSync = original })

	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	target := filepath.Join(store.Path(), "profile-alpha.json")

	published := ""
	fileSync = func(f *os.File) error {
		data, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		published = string(data)
		return original(f)
	}
	rotated := first
	rotated.RefreshToken = "rotated-refresh-token-material"
	if err := store.Save(rotated); err != nil {
		t.Fatalf("Save (replacement): %v", err)
	}
	if !strings.Contains(published, testRefreshToken) {
		t.Error("the replacement was published before the new record was flushed")
	}
	if strings.Contains(published, "rotated-refresh-token-material") {
		t.Error("the replacement reached the final name before it was flushed")
	}
}

// TestProfileStore_ReplacesThroughTheResolvedDirectory proves that the
// replacement is published in the physical directory the operator's spelling
// resolves to.
//
// The temporary file has to be created in the publication directory: a
// temporary file in a lexically derived directory would make the rename
// cross a filesystem boundary, and its directory entry would fall outside
// the durability chain.
func TestProfileStore_ReplacesThroughTheResolvedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs privileges on Windows")
	}
	original := fileSync
	t.Cleanup(func() { fileSync = original })

	base := t.TempDir()
	physical := filepath.Join(base, "physical")
	if err := os.MkdirAll(physical, 0o700); err != nil {
		t.Fatalf("create the physical directory: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("link the physical directory: %v", err)
	}

	store, err := NewProfileStore(filepath.Join(link, "profiles"))
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}

	temporary := ""
	fileSync = func(f *os.File) error {
		temporary = filepath.Dir(f.Name())
		return original(f)
	}
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	resolved := filepath.Join(physical, "profiles")
	requireSameDirectory(t, temporary, resolved)
	if _, err := os.Stat(filepath.Join(resolved, "profile-alpha.json")); err != nil {
		t.Errorf("the record was not published in the resolved directory: %v", err)
	}
}

// TestProfileStore_PathIsASnapshotWhileOperationsReResolve proves the two
// halves of the store's path contract.
//
// Path reports the directory the configured spelling resolved to when the
// store was constructed, so it is a SNAPSHOT. Every operation resolves the
// spelling again at the moment it runs: an operator who retargets a symlink
// moves where the records are written, while the accessor still reports the
// location that was resolved at construction time.
//
// The fixture makes the two diverge: a spelling that is merely followed to
// the same directory cannot tell the snapshot from a fresh resolution.
func TestProfileStore_PathIsASnapshotWhileOperationsReResolve(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs privileges on Windows")
	}
	t.Parallel()

	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o700); err != nil {
			t.Fatalf("create the %s store directory: %v", filepath.Base(dir), err)
		}
	}
	link := filepath.Join(base, "alias")
	if err := os.Symlink(first, link); err != nil {
		t.Fatalf("link the first directory: %v", err)
	}

	store, err := NewProfileStore(filepath.Join(link, "profiles"))
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	constructed := filepath.Join(first, "profiles")
	requireSameDirectory(t, store.Path(), constructed)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(constructed, "profile-alpha.json")); err != nil {
		t.Errorf("the record was not published in the resolved directory: %v", err)
	}

	// Retarget the operator's spelling, then read the accessor again and
	// write one more record. The accessor keeps the construction-time
	// location, and the operation follows the spelling as it is now.
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove the link: %v", err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatalf("retarget the link: %v", err)
	}
	requireSameDirectory(t, store.Path(), constructed)
	if err := store.Save(testProfileRecord("beta")); err != nil {
		t.Fatalf("Save after the retarget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "profiles", "profile-beta.json")); err != nil {
		t.Errorf("the operation did not follow the retargeted spelling: %v", err)
	}
	if _, err := os.Stat(filepath.Join(constructed, "profile-beta.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the operation wrote into the construction-time location: %v", err)
	}
}

// TestProfileStore_ErrorsDoNotEchoRejectedValues proves that a rejected
// record produces an error that names the constraint and never repeats the
// rejected value.
//
// A record that was populated by mistake can carry an email address and
// three credentials, and the error is a support-facing surface.
func TestProfileStore_ErrorsDoNotEchoRejectedValues(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	record := testProfileRecord("alpha")
	record.ID = "../escape"
	err := store.Save(record)
	if err == nil {
		t.Fatal("Save must refuse a profile id that could name another file")
	}
	// The rejected value is the profile id: an error that echoed it would
	// repeat the one value this call was given, and the record it came from
	// holds three credentials and an email hint.
	for _, rejected := range []string{record.ID, testAccessToken, testRefreshToken, testIDToken, testEmailHint, record.Subject} {
		if strings.Contains(err.Error(), rejected) {
			t.Errorf("the error %q repeats a rejected value", err)
		}
	}
	if !strings.Contains(err.Error(), "profile id") {
		t.Errorf("error %q must name the constraint that failed", err)
	}
}

// TestProfileStore_LoadReportsAMissingProfile proves that a profile that is
// not stored is distinguishable from a profile that cannot be trusted.
func TestProfileStore_LoadReportsAMissingProfile(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	_, err := store.Load("missing")
	if err == nil {
		t.Fatal("Load must report a profile that is not stored")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %q must be the not-exist error", err)
	}
}

// TestProfileStore_RefusesAMalformedExpiryWithoutEchoingItsValue proves that
// a document the decoder refuses does not carry its content into the error.
//
// The expiry is a timestamp, and the standard decoder's message for a
// malformed timestamp quotes the string it rejected. A stored document holds
// credential material, so a document whose expiry was corrupted - or planted
// - with a token would carry that token into the error that a caller may log
// or attach to a support transcript.
//
// The document is otherwise the complete, valid one the store wrote, so the
// malformed expiry is the only reason it is refused.
func TestProfileStore_RefusesAMalformedExpiryWithoutEchoingItsValue(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeStoreFile(t, store, "profile-alpha.json", storedDocumentWithExpiry(t, store, "profile-alpha.json", testRefreshToken))

	for name, call := range map[string]func() error{
		"Load":   func() error { _, err := store.Load("alpha"); return err },
		"List":   func() error { _, err := store.List(); return err },
		"Select": func() error { return store.Select("alpha") },
	} {
		err := call()
		if err == nil {
			t.Errorf("%s must refuse a document with a malformed expiry", name)
			continue
		}
		for _, secret := range []string{testRefreshToken, testAccessToken, testIDToken, testEmailHint} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s exposed the rejected value in its error: %s", name, err)
			}
		}
	}
}

// TestProfileStore_RefusesAMalformedExpiryWithoutEchoingTheIDToken proves the
// same for a retained ID token planted in the expiry field.
//
// The retained ID token is an identity hint, and requirement 2.12 keeps it
// out of support-facing errors exactly like the tokens themselves.
func TestProfileStore_RefusesAMalformedExpiryWithoutEchoingTheIDToken(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeStoreFile(t, store, "profile-alpha.json", storedDocumentWithExpiry(t, store, "profile-alpha.json", testIDToken))

	_, err := store.Load("alpha")
	if err == nil {
		t.Fatal("Load must refuse a document with a malformed expiry")
	}
	if strings.Contains(err.Error(), testIDToken) {
		t.Errorf("the error exposed the rejected value: %s", err)
	}
}

// storedDocumentWithExpiry returns the stored document with its expiry
// replaced by value.
//
// The document is otherwise the one the store wrote, so the replaced expiry
// is the only reason the document is refused.
func storedDocumentWithExpiry(t *testing.T, store *ProfileStore, name, value string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(store.Path(), name))
	if err != nil {
		t.Fatalf("read the stored record: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the stored record is not a JSON object: %v", err)
	}
	document["expiry"] = value
	corrupt, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode the corrupt record: %v", err)
	}
	return string(corrupt)
}

// TestProfileStore_ReportsAFailedDurabilityConfirmation proves that a
// replacement whose directory entry could not be flushed is reported instead
// of acknowledged.
//
// The rename has already published the record when the confirmation runs, so
// the record is in place - but its persistence is unproven, and the caller
// must not learn that the write succeeded. The failure is classified with
// errDurabilityUnconfirmed, which is what the refresh path uses to tell a
// record that is in place from one that was never published.
func TestProfileStore_ReportsAFailedDurabilityConfirmation(t *testing.T) {
	original := dirSync
	t.Cleanup(func() { dirSync = original })

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dirSync = func(string) error { return errors.New("siwc: injected directory sync failure") }
	rotated := testProfileRecord("alpha")
	rotated.AccessToken = "rotated-access-token-material"
	err := store.Save(rotated)
	if err == nil {
		t.Fatal("Save must report a failed durability confirmation instead of acknowledging the record")
	}
	// The caller distinguishes "published, durability unproven" from
	// "nothing was published" with errors.Is (see classifyPublication).
	if !errors.Is(err, errDurabilityUnconfirmed) {
		t.Errorf("error %q must classify the failure as an unconfirmed durability", err)
	}
	// The publication itself happened: only its persistence is unproven.
	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load after the failed confirmation: %v", err)
	}
	if got.AccessToken != rotated.AccessToken {
		t.Error("the failed confirmation must leave the published record in place")
	}
}

// TestProfileStore_ResolvesADotDotSpellingToThePhysicalDirectory proves that
// the store resolves the operator's spelling the way the operating system
// resolves it.
//
// The operating system follows a symlink FIRST and applies the ".." after
// it, while a lexical cleanup applies the ".." first, so the two name
// different directories. The spelling below is built by raw concatenation on
// purpose: filepath.Join would clean the ".." away, and the proof would then
// describe a different path than the one the store was given.
func TestProfileStore_ResolvesADotDotSpellingToThePhysicalDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows kernel resolves a '..' naively, so the store refuses such a spelling by design")
	}
	t.Parallel()

	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatalf("create the target directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(base, "alias")); err != nil {
		t.Fatalf("link the target directory: %v", err)
	}
	spelling := base + string(os.PathSeparator) + "alias" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "profiles"

	store, err := NewProfileStore(spelling)
	if err != nil {
		t.Fatalf("NewProfileStore(%q): %v", spelling, err)
	}
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The operating system resolves the ".." after the symlink, so the
	// records belong to the target's directory and not to the base.
	if _, err := os.Stat(filepath.Join(target, "profiles", "profile-alpha.json")); err != nil {
		t.Errorf("the record was not published in the resolved directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "profiles", "profile-alpha.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the record was published in the lexically cleaned directory: %v", err)
	}
	requireSameDirectory(t, store.Path(), filepath.Join(target, "profiles"))
}

// TestProfileStore_ListReportsARecordFileWhoseIDIsNotCanonical proves that a
// store-framed file this build cannot read is reported, not silently skipped.
//
// A name that carries the store's record frame but a profile id outside the
// canonical alphabet is an entry the store does not recognise. Presenting it
// as if it did not exist would hide it from the operator, so the listing
// refuses with a diagnostic that names the constraint and never the name.
func TestProfileStore_ListReportsARecordFileWhoseIDIsNotCanonical(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A complete, otherwise valid document under a framed name whose id is
	// not canonical. This build never writes such a name.
	//
	// The name must not case-fold onto an existing file: on a
	// case-insensitive filesystem "profile-Alpha.json" IS "profile-alpha.json",
	// and writing it would replace the record this proof needs. The planted
	// name therefore folds onto nothing in the store.
	planted := testProfileRecord("alpha")
	planted.Version = profileRecordVersion
	encoded, err := json.Marshal(planted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeStoreFile(t, store, "profile-Unsupported.json", string(encoded))

	summaries, err := store.List()
	if err == nil {
		t.Fatalf("List must report a record file whose profile id is not canonical; got %d summaries", len(summaries))
	}
	if !strings.Contains(err.Error(), "not a canonical lowercase id") {
		t.Errorf("the diagnostic %q must name the constraint that failed", err)
	}
	if strings.Contains(err.Error(), "profile-Unsupported.json") || strings.Contains(err.Error(), "Unsupported") {
		t.Errorf("the diagnostic %q must not repeat the rejected name", err)
	}
}

// TestProfileStore_ARefusedOperationLeavesTheStoreDirectoryAbsent proves that
// the key policy is enforced BEFORE any I/O.
//
// A refused id must not create the store directory either: the policy is
// stated as a before-I/O rule, and a store directory that appears because an
// id was refused is the observable sign that the rule was not applied.
func TestProfileStore_ARefusedOperationLeavesTheStoreDirectoryAbsent(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "profiles")
	store, err := NewProfileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	record := testProfileRecord("alpha")
	record.ID = "Alpha"
	if err := store.Save(record); err == nil {
		t.Fatal("Save must refuse the non-canonical profile id")
	}
	if _, err := store.Load("Alpha"); err == nil {
		t.Fatal("Load must refuse the non-canonical profile id")
	}
	if err := store.Select("Alpha"); err == nil {
		t.Fatal("Select must refuse the non-canonical profile id")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused operation must leave the store directory absent; stat returned %v", err)
	}
}

// TestProfileStore_ReplacingOneProfileLeavesTheOtherIntact proves the
// combination the per-profile layout is for.
//
// Two profiles are stored with distinct credentials; replacing one must leave
// the other's record and listing untouched. The separate proofs that two
// profiles exist and that one can be replaced do not by themselves show that
// a replacement reaches only its own profile.
func TestProfileStore_ReplacingOneProfileLeavesTheOtherIntact(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	alpha := testProfileRecord("alpha")
	beta := testProfileRecord("beta")
	beta.AccessToken = "beta-access-token"
	beta.RefreshToken = "beta-refresh-token"
	if err := store.Save(alpha); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}
	if err := store.Save(beta); err != nil {
		t.Fatalf("Save beta: %v", err)
	}

	replacement := testProfileRecord("alpha")
	replacement.AccessToken = "alpha-access-token-2"
	if err := store.Save(replacement); err != nil {
		t.Fatalf("Save the replacement: %v", err)
	}

	stored, err := store.Load("beta")
	if err != nil {
		t.Fatalf("Load beta: %v", err)
	}
	if stored.AccessToken != "beta-access-token" || stored.RefreshToken != "beta-refresh-token" {
		t.Error("replacing alpha must leave beta's credentials untouched")
	}
	if stored.Subject != beta.Subject {
		t.Errorf("beta's subject = %q, want %q", stored.Subject, beta.Subject)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("the store holds %d profiles, want 2", len(summaries))
	}
}

// TestProfileStore_RefusesANonCanonicalProfileID proves that the store
// refuses a profile id that is not a lowercase ASCII token.
//
// The id becomes a file name, and the filesystem's case sensitivity is not
// the store's to choose. Two accepted ids that differ only in case would
// name the same file on a case-insensitive filesystem, and the second
// registration would replace the first one's document - the only copy of
// its refresh token. The store refuses such an id before it touches the
// disk, so two accepted ids always name two different files.
func TestProfileStore_RefusesANonCanonicalProfileID(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	for _, id := range []string{"Alpha", "ALPHA", "aLpha", "alpha.beta", "alpha/beta", "..", ".hidden"} {
		record := testProfileRecord("alpha")
		record.ID = id
		if err := store.Save(record); err == nil {
			t.Errorf("Save must refuse the non-canonical profile id %q", id)
		}
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load must refuse the non-canonical profile id %q", id)
		}
		if err := store.Select(id); err == nil {
			t.Errorf("Select must refuse the non-canonical profile id %q", id)
		}
	}
}

// TestProfileStore_AcceptedProfileIDsMapToCanonicalFileNames proves the key
// policy that makes the id-to-file-name mapping injective on a
// case-insensitive filesystem.
//
// The file name of every accepted id is ALREADY in its case-folded form, so
// two distinct accepted ids cannot name the same file: the names differ in a
// character whose case carries no meaning. The proof is deterministic and
// needs no case-insensitive filesystem: a name that is not canonical is a
// name that can fold onto another one.
func TestProfileStore_AcceptedProfileIDsMapToCanonicalFileNames(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"alpha", "beta", "a-b_c", "1", "profile-1"} {
		if err := validateProfileID(id); err != nil {
			t.Fatalf("the id %q must be accepted: %v", id, err)
		}
		name := profileFileName(id)
		if name != strings.ToLower(name) {
			t.Errorf("the file name %q of an accepted id is not canonical: it could fold onto another id's name", name)
		}
	}
	// Two distinct accepted ids must name two distinct files.
	names := map[string]string{}
	for _, id := range []string{"alpha", "beta", "a-b_c", "1", "profile-1"} {
		name := profileFileName(id)
		if other, ok := names[name]; ok {
			t.Errorf("the ids %q and %q name the same file %q", other, id, name)
		}
		names[name] = id
	}
}

// TestProfileStore_ADifferentlyCasedIDCannotDestroyARegistration proves that
// a differently cased id cannot take over another profile's record.
//
// The first registration and its selection must survive the attempt, and the
// attempt itself must be refused rather than silently merged into the first
// registration's key.
func TestProfileStore_ADifferentlyCasedIDCannotDestroyARegistration(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Select("alpha"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	second := testProfileRecord("Alpha")
	second.Subject = "subject-other"
	second.RefreshToken = "rotated-refresh-token-material"
	if err := store.Save(second); err == nil {
		t.Fatal("Save must refuse a differently cased profile id")
	}

	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RefreshToken != first.RefreshToken {
		t.Error("the first registration lost its refresh token")
	}
	if got.Subject != first.Subject {
		t.Error("the first registration lost its subject")
	}
	selected, err := store.Selected()
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "alpha" {
		t.Errorf("the first registration lost its selection: %q", selected)
	}
}

// TestProfileStore_AFailedEncodeKeepsThePreviousRecord proves that a record
// the encoder refuses does not damage the record it was replacing.
//
// The expiry is a timestamp, and the encoder refuses a timestamp it cannot
// represent. The failure must be reported as a classification - a
// user-defined marshaler may put a value in its cause - and the previously
// stored token and expiry must survive it.
func TestProfileStore_AFailedEncodeKeepsThePreviousRecord(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	original := testProfileRecord("alpha")
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	unencodable := original
	unencodable.Expiry = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := store.Save(unencodable)
	if err == nil {
		t.Fatal("Save must refuse a record the encoder cannot represent")
	}
	if strings.Contains(err.Error(), "MarshalJSON") {
		t.Errorf("the error must classify the failure instead of quoting the encoder's cause: %s", err)
	}
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error carries secret material %q", secret)
		}
	}

	got, err := store.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RefreshToken != original.RefreshToken {
		t.Error("the failed encoding damaged the stored refresh token")
	}
	if !got.Expiry.Equal(original.Expiry) {
		t.Error("the failed encoding damaged the stored expiry")
	}
}

// TestProfileStore_PrepareRefusesARetargetedSpelling proves that the
// directory preparation refuses a directory that the configured spelling no
// longer names.
//
// The operator's spelling is resolved once per call, and the preparation
// confirms that the operating system still resolves it to the same
// directory. An operator who retargets the symlink between the resolution
// and the preparation must be refused, because the records would otherwise
// be written into a directory the spelling no longer names.
//
// The proof is deterministic on any filesystem: it needs a symlink and two
// existing directories, and no timing.
func TestProfileStore_PrepareRefusesARetargetedSpelling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs privileges on Windows")
	}
	t.Parallel()

	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	link := filepath.Join(base, "alias")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", filepath.Base(dir), err)
		}
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatalf("link the first directory: %v", err)
	}
	store, err := NewProfileStore(link)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	resolved, err := store.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove the link: %v", err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatalf("retarget the link: %v", err)
	}
	if err := store.prepare(resolved); err == nil {
		t.Fatal("the preparation must refuse a directory the configured spelling no longer names")
	}
}

// TestProfileStore_SelectAndSelected proves that the explicit selection is
// stored and read back.
//
// The selection is a local pointer to a profile the store holds; it carries
// no credential material.
func TestProfileStore_SelectAndSelected(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	selected, err := store.Selected()
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "" {
		t.Errorf("Selected = %q, want the empty value of an unselected store", selected)
	}
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Select("alpha"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	selected, err = store.Selected()
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "alpha" {
		t.Errorf("Selected = %q, want %q", selected, "alpha")
	}
	if err := store.Select("missing"); err == nil {
		t.Error("Select must refuse a profile the store does not hold")
	}
}

// TestProfileStore_TheSelectionCarriesNoCredentialMaterial proves that the
// selection file holds nothing but the pointer.
func TestProfileStore_TheSelectionCarriesNoCredentialMaterial(t *testing.T) {
	t.Parallel()

	store := newTestProfileStore(t)
	if err := store.Save(testProfileRecord("alpha")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Select("alpha"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Path(), selectionFileName))
	if err != nil {
		t.Fatalf("read the selection: %v", err)
	}
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the selection carries secret material %q", secret)
		}
	}
}

// TestResolveProfileStorePath proves the path decision.
//
// An operator may point the profile store somewhere else, and the default
// lives in the per-user configuration directory under a connector-scoped
// name. The default is a directory next to the host record, not the host
// record itself: the host identity belongs to the installation and must not
// follow the store around.
func TestResolveProfileStorePath(t *testing.T) {
	t.Parallel()

	configured := filepath.Join(t.TempDir(), "profiles")
	got, err := ResolveProfileStorePath(configured)
	if err != nil {
		t.Fatalf("ResolveProfileStorePath(%q): %v", configured, err)
	}
	if got != configured {
		t.Fatalf("ResolveProfileStorePath(%q) = %q, want the configured path", configured, got)
	}

	defaultPath, err := ResolveProfileStorePath("")
	if err != nil {
		t.Fatalf("ResolveProfileStorePath(\"\"): %v", err)
	}
	again, err := ResolveProfileStorePath("")
	if err != nil {
		t.Fatalf("ResolveProfileStorePath(\"\"): %v", err)
	}
	if defaultPath != again {
		t.Fatalf("the default profile store path is not stable: %q then %q", defaultPath, again)
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user configuration directory on this host: %v", err)
	}
	if !strings.HasPrefix(defaultPath, userConfig) {
		t.Fatalf("the default profile store path %q must live under the user configuration directory %q", defaultPath, userConfig)
	}
	if filepath.Base(defaultPath) != "profiles" {
		t.Fatalf("the default profile store path %q must be a directory named profiles", defaultPath)
	}
	hostRecord, err := DefaultHostRecordPath()
	if err != nil {
		t.Fatalf("DefaultHostRecordPath: %v", err)
	}
	if defaultPath == hostRecord {
		t.Fatal("the default profile store path must not be the host record path")
	}
	if filepath.Dir(defaultPath) != filepath.Dir(hostRecord) {
		t.Fatalf("the default profile store path %q must live next to the host record %q", defaultPath, hostRecord)
	}
}

// TestNewProfileStore_RefusesARelativePath proves that the store refuses a
// relative path.
//
// A relative path would be resolved against the working directory of
// whichever process started the connector, so the same configuration would
// address a different directory after a restart with a different working
// directory.
func TestNewProfileStore_RefusesARelativePath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"profiles", "state/profiles", "./profiles"} {
		if _, err := NewProfileStore(path); err == nil {
			t.Errorf("NewProfileStore must refuse the relative path %q", path)
		} else if !strings.Contains(err.Error(), "absolute") {
			t.Errorf("error %q must state that the store path must be absolute", err)
		}
	}
	if _, err := NewProfileStore("  "); err == nil {
		t.Error("NewProfileStore must refuse an empty path")
	}
}

// TestProfileRecord_StringOmitsTokenMaterial proves that the record's own
// summary does not print its credentials.
//
// The summary is the one Format renders for every verb it dispatches, so a
// token that reached it would reach a log line, a test message or a support
// transcript through every formatting path. The proof asks for the summary
// directly: "%v" is dispatched to Format while the record implements
// fmt.Formatter, so it proves nothing about this method.
func TestProfileRecord_StringOmitsTokenMaterial(t *testing.T) {
	t.Parallel()

	record := testProfileRecord("alpha")
	text := record.String()
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint} {
		if strings.Contains(text, secret) {
			t.Errorf("the formatted record carries secret material %q", secret)
		}
	}
	if !strings.Contains(text, "alpha") {
		t.Errorf("the formatted record %q must identify the profile by its local id", text)
	}
}

// TestProfileRecord_GoStringOmitsTokenMaterial proves that the record's
// Go-syntax form does not print the credentials either.
//
// The Go-syntax verb does not reach this method while the record implements
// fmt.Formatter: "%#v" is dispatched to Format. A caller that asks for the
// Go-syntax form directly must still not receive a token, so the proof asks
// for that form directly.
func TestProfileRecord_GoStringOmitsTokenMaterial(t *testing.T) {
	t.Parallel()

	record := testProfileRecord("alpha")
	text := record.GoString()
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint} {
		if strings.Contains(text, secret) {
			t.Errorf("the Go-syntax formatted record carries secret material %q", secret)
		}
	}
	if !strings.Contains(text, "alpha") {
		t.Errorf("the Go-syntax formatted record %q must identify the profile by its local id", text)
	}
}

// TestProfileRecord_NoFormattingVerbPrintsTokenMaterial proves that EVERY
// formatting verb the record can control is safe, not only the common ones.
//
// Go's default rendering for a struct satisfies the numeric, boolean and
// pointer verbs by printing a raw field, which for this type means printing
// a token. The record therefore implements fmt.Formatter and renders its
// secret-free content for every verb, so a mis-chosen verb in a diagnostic
// cannot leak a credential.
//
// The one exception is "%p" on a record VALUE, which fmt handles before it
// consults a Formatter and whose error path prints the raw struct. The
// record's own comment names that limitation, and the pointer case - which
// is the correct way to use "%p" - is proved here.
func TestProfileRecord_NoFormattingVerbPrintsTokenMaterial(t *testing.T) {
	t.Parallel()

	record := testProfileRecord("alpha")
	secrets := []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint}
	for _, verb := range []string{
		"%v", "%+v", "%#v", "%s", "%q", "%x", "%X",
		"%d", "%b", "%o", "%c", "%t", "%e", "%f", "%g", "%U",
		"%20v", "%-20v", "%.3v", "%#20v",
	} {
		text := fmt.Sprintf(verb, record)
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("the %s formatted record carries secret material %q", verb, secret)
			}
		}
		if !strings.Contains(text, "alpha") {
			t.Errorf("the %s formatted record %q must identify the profile by its local id", verb, text)
		}
	}
	// The pointer verb on a POINTER prints the address, not the record.
	pointer := fmt.Sprintf("%p", &record)
	for _, secret := range secrets {
		if strings.Contains(pointer, secret) {
			t.Errorf("the pointer-verb formatted record carries secret material %q", secret)
		}
	}
}

// TestProfileStore_ConcurrentReadersNeverSeeAPartialRecord proves that a
// reader never observes a half-written record.
//
// The replacement is published by a rename, so a reader observes the
// previous record or the new one, never a mixture.
//
// The reader reads CONTINUOUSLY while the writer publishes, so its reads fall
// inside the publications themselves. A reader that waited for a completed
// replacement between two reads would read outside the window this proof is
// about: a publication that truncated the record in place would be
// invisible to it.
//
// The start barrier parks the reader on the start signal before the writer's
// first publication, and the writer marks every publication with the active
// flag, so the overlap is coordinated rather than hoped for: the proof
// asserts that reads completed WHILE the writer was publishing, and that the
// reader observed BOTH generations, so at least one rotated credential
// crossed the reader while replacements were running. A replacements counter
// alone cannot show that: it records successful Save returns, not observed
// changes to the stored record.
//
// Windows sharing semantics: Go opens a file without FILE_SHARE_DELETE, so a
// rename of the destination while a reader holds it open fails with a sharing
// violation, and a read that opens the destination while the rename commits
// fails the same way. That is a platform limitation of the concurrent
// open/rename pair, not a torn write: no successful Load ever returns a
// mixture. On Windows the proof therefore retries the sharing-violation
// failures of both sides and counts them, while every other failure - an
// undecodable document, a wrong id, a partial token - still fails the proof.
// On POSIX no failure is tolerated: the rename is atomic against concurrent
// opens, so any error is a real one.
//
// This is a file-level atomicity proof, not race-detector evidence.
//
//nolint:paralleltest // drives concurrent readers against one store.
func TestProfileStore_ConcurrentReadersNeverSeeAPartialRecord(t *testing.T) {
	store := newTestProfileStore(t)
	first := testProfileRecord("alpha")
	second := first
	second.RefreshToken = "rotated-refresh-token-material"
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	readerReady := make(chan struct{})
	start := make(chan struct{})
	done := make(chan struct{})
	readerDone := make(chan error, 1)
	var reads, overlapReads, loadTransient atomic.Int64
	var sawFirst, sawSecond atomic.Bool
	var writerActive atomic.Bool
	go func() {
		close(readerReady)
		<-start
		for {
			select {
			case <-done:
				readerDone <- nil
				return
			default:
			}
			inWindow := writerActive.Load()
			got, err := store.Load("alpha")
			if err != nil {
				if isWindowsSharingViolation(err) {
					loadTransient.Add(1)
					runtime.Gosched()
					continue
				}
				readerDone <- fmt.Errorf("Load: %w", err)
				return
			}
			if got.ID != "alpha" {
				readerDone <- errors.New("a reader observed a partial record")
				return
			}
			switch got.RefreshToken {
			case testRefreshToken:
				sawFirst.Store(true)
			case "rotated-refresh-token-material":
				sawSecond.Store(true)
			default:
				readerDone <- errors.New("a reader observed a partial record")
				return
			}
			reads.Add(1)
			if inWindow {
				overlapReads.Add(1)
			}
		}
	}()

	<-readerReady
	writerActive.Store(true)
	close(start)

	const maxTransientRetries = 100
	var replacements, saveTransient atomic.Int64
	var writerErr error
	save := func(rec ProfileRecord) bool {
		for attempt := 0; ; attempt++ {
			if err := store.Save(rec); err != nil {
				if isWindowsSharingViolation(err) {
					saveTransient.Add(1)
					if attempt >= maxTransientRetries {
						writerErr = fmt.Errorf("the writer's replacement kept failing: %w", err)
						return false
					}
					runtime.Gosched()
					continue
				}
				writerErr = err
				return false
			}
			replacements.Add(1)
			return true
		}
	}
	for range 200 {
		if !save(second) {
			break
		}
		if !save(first) {
			break
		}
	}
	writerActive.Store(false)
	close(done)

	if err := <-readerDone; err != nil {
		t.Fatal(err)
	}
	if writerErr != nil {
		t.Fatalf("the writer's replacement failed: %v", writerErr)
	}
	if reads.Load() == 0 {
		t.Fatal("no read completed while the writer was publishing")
	}
	if overlapReads.Load() == 0 {
		t.Fatal("no read overlapped the writer's publications; the proof did not observe the replacement window")
	}
	if !sawFirst.Load() || !sawSecond.Load() {
		t.Fatal("the reader did not observe both credential generations; the proof did not see a rotation cross the writer's window")
	}
	if replacements.Load() != 400 {
		t.Fatalf("the writer completed %d replacements, want 400", replacements.Load())
	}
	if runtime.GOOS == "windows" {
		t.Logf("tolerated %d transient sharing violations on reads and %d on writes",
			loadTransient.Load(), saveTransient.Load())
	} else if loadTransient.Load() != 0 || saveTransient.Load() != 0 {
		t.Fatalf("tolerated %d read and %d write transients off Windows; POSIX must not need them",
			loadTransient.Load(), saveTransient.Load())
	}
}

// isWindowsSharingViolation reports whether err is the sharing violation of
// a concurrent open/rename pair on Windows.
//
// Go opens files without FILE_SHARE_DELETE, so on Windows a rename of the
// destination while a reader holds it open - or a read that opens the
// destination while the rename commits - fails instead of proceeding
// atomically as on POSIX. The failure carries ERROR_ACCESS_DENIED (5),
// ERROR_SHARING_VIOLATION (32) or ERROR_LOCK_VIOLATION (33), usually wrapped
// in a *PathError or *LinkError. Only that failure is transient: anything
// else - an undecodable document, a refused record, a partial token - is a
// real proof failure and must not be retried.
//
// The helper returns false off Windows, so the POSIX proof stays strict: any
// error there fails the test.
func isWindowsSharingViolation(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		if errno == 5 || errno == 32 || errno == 33 {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sharing violation") ||
		strings.Contains(msg, "being used by another process") ||
		strings.Contains(msg, "access is denied")
}
