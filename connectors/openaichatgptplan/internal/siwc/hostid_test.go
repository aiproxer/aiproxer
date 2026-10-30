// Tests for the stable agent-host identity of the openai-chatgpt-plan
// connector.
//
// The contract under test is requirement 3 of the migration specification:
// one stable, opaque, non-PII ext_agent_host_id per installation, persisted
// protected and atomically, created exactly once even when two first starts
// race, and never replaced by an imported profile.
//
// The proofs live at the store boundary - the only place where the guarantee
// is observable - instead of mirroring the internal call graph.
package siwc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// hostIDPattern is the documented opaque form of the host id: the
// "urn:uuid:" prefix followed by the hyphenated RFC 9562 form of a version 4
// UUID. Nothing else may appear in the value.
var hostIDPattern = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The read-only view is what the import path depends on. It exposes no
// write operation, so binding an imported profile can neither create nor
// replace the host record.
var _ HostIDReader = (*HostStore)(nil)

// TestEnsureHostID_ExistingRecordRequiresItsOwnDurabilityConfirmation proves
// that a first-start which finds an existing record confirms the record's
// durability itself.
//
// The publisher of a record confirms the durability of its own publication.
// A second first-start that finds the published record must not rely on that
// publisher: the publisher may still be blocked before its directory sync,
// or its sync may fail. The second first-start therefore performs its own
// directory sync, and a failed confirmation is reported instead of an
// acknowledged host id.
//
// The regression is deterministic. The injected directory sync blocks the
// publisher and then fails, and the second first-start observes that same
// failure. Nothing here depends on timing, and the second first-start cannot
// acknowledge the id while the confirmation is unproven.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_ExistingRecordRequiresItsOwnDurabilityConfirmation(t *testing.T) {
	store, err := NewHostStore(filepath.Join(t.TempDir(), "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	blocked := true
	original := dirSync
	t.Cleanup(func() { dirSync = original })
	dirSync = func(string) error {
		mu.Lock()
		first := blocked
		blocked = false
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return errors.New("siwc: injected directory sync failure")
	}

	published := make(chan error, 1)
	go func() {
		_, err := store.EnsureHostID()
		published <- err
	}()

	// The publisher has published the record and reached its directory sync.
	select {
	case <-entered:
	case err := <-published:
		t.Fatalf("the publisher failed before it reached its directory sync: %v", err)
	}
	id, secondErr := store.EnsureHostID()
	close(release)
	publisherErr := <-published

	if publisherErr == nil {
		t.Fatal("the publisher must report the injected directory sync failure")
	}
	if secondErr == nil {
		t.Fatalf(
			"a first-start that found an existing record acknowledged the host id %q without its own successful durability confirmation; the publisher then failed: %v",
			id, publisherErr,
		)
	}
}

// TestEnsureHostID_PublisherReportsItsOwnDurabilityFailure proves the
// publisher does not acknowledge a host id whose directory entry could not
// be flushed.
//
// The publication is in place when the flush fails, so the failure has to
// reach the caller: acknowledging the id would let a registration use an
// identity whose persistence is unproven.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_PublisherReportsItsOwnDurabilityFailure(t *testing.T) {
	store, err := NewHostStore(filepath.Join(t.TempDir(), "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	dirSync = func(string) error { return errors.New("siwc: injected directory sync failure") }

	if _, err := store.EnsureHostID(); err == nil {
		t.Fatal("EnsureHostID must report a failed directory flush instead of acknowledging the host id")
	}
}

// TestEnsureHostID_ConfirmsEveryNewDirectoryInTheChain proves that the
// first start confirms the durability of the whole directory chain it
// created.
//
// The default location nests the record under directories that need not
// exist yet. Flushing only the leaf would leave the new directories' own
// entries unconfirmed: a crash could lose an ancestor entry, the next start
// would find no record at all, and it would generate a different host id.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_ConfirmsEveryNewDirectoryInTheChain(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "new-installation", "new-connector")
	store, err := NewHostStore(filepath.Join(dir, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	var mu sync.Mutex
	confirmed := map[string]bool{}
	dirSync = func(path string) error {
		mu.Lock()
		confirmed[filepath.Clean(path)] = true
		mu.Unlock()
		return original(path)
	}

	if _, err := store.EnsureHostID(); err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	for _, want := range []string{base, filepath.Dir(dir), dir} {
		mu.Lock()
		ok := confirmed[filepath.Clean(want)]
		mu.Unlock()
		if !ok {
			t.Errorf("the first start acknowledged the host id without confirming the durability of %s", want)
		}
	}
}

// TestEnsureHostID_PropagatesAnAncestorSyncFailure proves that a failure in
// the chain confirmation is reported instead of acknowledged.
//
// The record's own directory can be durable while the directory that holds
// it is not. An acknowledged host id in that state can be lost by a crash,
// and the next start would generate a different one.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_PropagatesAnAncestorSyncFailure(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "new-installation", "new-connector")
	store, err := NewHostStore(filepath.Join(dir, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	dirSync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(base) {
			return errors.New("siwc: injected ancestor sync failure")
		}
		return original(path)
	}

	if _, err := store.EnsureHostID(); err == nil {
		t.Fatal("EnsureHostID must report a failed ancestor confirmation instead of acknowledging the host id")
	} else if !strings.Contains(err.Error(), "injected ancestor sync failure") {
		t.Fatalf("error %q must report the failed ancestor confirmation", err)
	}
}

// TestEnsureHostID_ContenderConfirmsTheWholeChainItself proves that a
// second first start confirms the whole chain itself.
//
// The creator of the record may still be inside its own confirmation. A
// contender that finds the record must not acknowledge it on the creator's
// word: it confirms the same path-determined chain, so a caller that finds
// the path before the creator finished cannot acknowledge it either.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_ContenderConfirmsTheWholeChainItself(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "new-installation", "new-connector")
	store, err := NewHostStore(filepath.Join(dir, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	original := dirSync
	t.Cleanup(func() { dirSync = original })
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	creator := true
	contenderPhase := false
	contenderConfirmed := map[string]bool{}
	dirSync = func(path string) error {
		mu.Lock()
		isCreator := creator
		creator = false
		if !isCreator && contenderPhase {
			contenderConfirmed[filepath.Clean(path)] = true
		}
		mu.Unlock()
		if isCreator {
			close(entered)
			<-release
		}
		return original(path)
	}

	created := make(chan error, 1)
	go func() {
		_, err := store.EnsureHostID()
		created <- err
	}()

	// The creator published the record and is inside its own confirmation.
	<-entered
	mu.Lock()
	contenderPhase = true
	mu.Unlock()
	contenderID, contenderErr := store.EnsureHostID()
	mu.Lock()
	contenderPhase = false
	synced := make(map[string]bool, len(contenderConfirmed))
	maps.Copy(synced, contenderConfirmed)
	mu.Unlock()
	close(release)
	creatorErr := <-created

	if contenderErr != nil {
		t.Fatalf("the contender failed its own confirmation: %v", contenderErr)
	}
	if contenderID == "" {
		t.Fatal("the contender must resolve the host id after its own confirmation")
	}
	if creatorErr != nil {
		t.Fatalf("the creator failed its own confirmation: %v", creatorErr)
	}
	for _, want := range []string{base, filepath.Dir(dir), dir} {
		if !synced[filepath.Clean(want)] {
			t.Errorf("the contender acknowledged the host id without confirming the durability of %s", want)
		}
	}
}

// TestEnsureHostID_LosingPublisherPropagatesItsOwnDurabilityFailure proves
// that the publisher that lost the race confirms the record's durability
// itself.
//
// Two first starts race for the publication. The one that wins flushes the
// chain; the one that loses must not take the winner's word for it. The
// loser's own confirmation fails here, and the loser must report that
// instead of acknowledging the host id.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestEnsureHostID_LosingPublisherPropagatesItsOwnDurabilityFailure(t *testing.T) {
	store, err := NewHostStore(filepath.Join(t.TempDir(), "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}

	originalFile, originalDir := fileSync, dirSync
	t.Cleanup(func() { fileSync, dirSync = originalFile, originalDir })

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	fileCalls := 0
	loserPhase := false
	fileSync = func(f *os.File) error {
		mu.Lock()
		fileCalls++
		first := fileCalls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return originalFile(f)
	}
	dirSync = func(path string) error {
		mu.Lock()
		inLoserPhase := loserPhase
		mu.Unlock()
		if inLoserPhase {
			return errors.New("siwc: injected loser confirmation failure")
		}
		return originalDir(path)
	}

	blocked := make(chan error, 1)
	go func() {
		_, err := store.EnsureHostID()
		blocked <- err
	}()

	// The first caller is still before its own publication, so the second
	// caller wins it.
	<-entered
	winnerID, winnerErr := store.EnsureHostID()
	if winnerErr != nil {
		t.Fatalf("the winning first start failed: %v", winnerErr)
	}
	if winnerID == "" {
		t.Fatal("the winning first start must resolve the host id")
	}
	mu.Lock()
	loserPhase = true
	mu.Unlock()
	close(release)
	loserErr := <-blocked
	if loserErr == nil {
		t.Fatal("the losing publisher acknowledged the host id without its own durability confirmation")
	}
	if !strings.Contains(loserErr.Error(), "injected loser confirmation failure") {
		t.Fatalf("loser error %q must report the failed durability confirmation", loserErr)
	}
}

// TestNewHostStore_RejectsARelativePath proves the host record path must be
// absolute.
//
// A relative path would be resolved against the working directory of
// whichever process started the connector, so the same configuration would
// address a different file after a restart with a different working
// directory - a silent change of the host identity. Resolving it against the
// current working directory would not fix that, so the path is refused.
func TestNewHostStore_RejectsARelativePath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"host.json", "state/host.json", "./host.json"} {
		if _, err := NewHostStore(path); err == nil {
			t.Errorf("NewHostStore(%q) must refuse a relative host record path", path)
		}
	}
}

// TestResolveHostRecordPath_RejectsARelativeConfiguredPath proves the
// configuration boundary refuses a relative host record path.
func TestResolveHostRecordPath_RejectsARelativeConfiguredPath(t *testing.T) {
	t.Parallel()

	if _, err := ResolveHostRecordPath("state/host.json"); err == nil {
		t.Fatal("ResolveHostRecordPath must refuse a relative configured host record path")
	}
}

// TestHostID_RejectionDoesNotEchoTheRejectedValue proves a rejected host id
// never reaches a diagnostic.
//
// A record that was populated by mistake can carry content that must not
// reach a log or a support error, so the validation reports the constraint
// that failed and not the value it rejected.
func TestHostID_RejectionDoesNotEchoTheRejectedValue(t *testing.T) {
	t.Parallel()

	const sentinel = "sentinel-user@example.com"
	path := filepath.Join(t.TempDir(), "host.json")
	writeFile(t, path, []byte(`{"version":1,"ext_agent_host_id":"`+sentinel+`"}`), 0o600)

	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	if _, err := store.HostID(); err == nil {
		t.Fatal("HostID must refuse a record with an unusable host id")
	} else if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("the rejection echoed the rejected value: %v", err)
	}
	if _, err := store.EnsureHostID(); err == nil {
		t.Fatal("EnsureHostID must refuse a record with an unusable host id")
	} else if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("the rejection echoed the rejected value: %v", err)
	}
}

// TestStore_KeepsAnOperatorSuppliedStateDirectoryAsItIs proves the store
// never changes the permissions of a directory it did not create.
//
// The store creates the directory it owns with owner-only permissions. An
// operator-supplied parent keeps the permissions the operator chose: the
// store refuses an untrusted directory instead of silently chmodding it.
func TestStore_KeepsAnOperatorSuppliedStateDirectoryAsItIs(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits carry no meaning on Windows")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod the state directory: %v", err)
	}
	store, err := NewHostStore(filepath.Join(dir, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	if _, err := store.EnsureHostID(); err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the state directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("the store changed the operator-supplied state directory to mode %o, want the unchanged 0755", got)
	}
}

// TestStore_RefusesAnOtherWritableStateDirectory proves the state-directory
// trust precondition is enforced.
//
// The record's own permissions protect its contents, but a principal that can
// write the directory can unlink the record and replace it with its own. The
// store therefore refuses a directory that other users can write to.
func TestStore_RefusesAnOtherWritableStateDirectory(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits carry no meaning on Windows")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod the state directory: %v", err)
	}
	store, err := NewHostStore(filepath.Join(dir, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	if _, err := store.EnsureHostID(); err == nil {
		t.Fatal("EnsureHostID must refuse a state directory that other users can write to")
	}
}

// TestEnsureHostID_RestartReusesTheStoredHostID proves requirement 3.2: a
// restart reuses the stored host id.
//
// A restart is modelled the way it happens in production: a new store
// instance over the same installation state, with no in-memory carry-over
// from the previous process. The stored record must not be rewritten either,
// because a rewritten record is a host id that could have changed.
func TestEnsureHostID_RestartReusesTheStoredHostID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.json")
	first, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	hostID, err := first.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record: %v", err)
	}

	restarted, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q) after restart: %v", path, err)
	}
	again, err := restarted.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID after restart: %v", err)
	}
	if again != hostID {
		t.Fatalf("host id after restart = %q, want the stored %q", again, hostID)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record after restart: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the restart rewrote the host record:\nbefore %s\nafter  %s", before, after)
	}
}

// TestEnsureHostID_ConcurrentFirstStartResolvesOneHostID proves requirement
// 3.6: two concurrent first starts resolve one host id, not two.
//
// The race is real: every racer starts from an empty installation and tries
// to create the record. The proof is that all racers observe the same value
// and that the installation ends up with exactly one record document and no
// leftover temporary file.
func TestEnsureHostID_ConcurrentFirstStartResolvesOneHostID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.json")
	const racers = 24

	ids := make([]string, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store, err := NewHostStore(path)
			if err != nil {
				errs[i] = err
				return
			}
			<-start
			ids[i], errs[i] = store.EnsureHostID()
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d failed: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("racer %d resolved host id %q, want the single id %q", i, ids[i], ids[0])
		}
	}

	rec := readHostRecord(t, path)
	if rec.HostID != ids[0] {
		t.Fatalf("the persisted host id %q is not the resolved id %q", rec.HostID, ids[0])
	}
	if rec.Version != hostRecordVersion {
		t.Fatalf("persisted host record version = %d, want %d", rec.Version, hostRecordVersion)
	}
	entries := readDirNames(t, filepath.Dir(path))
	if !slices.Equal(entries, []string{"host.json"}) {
		t.Fatalf("the installation must hold exactly the host record, got %v", entries)
	}
}

// TestEnsureHostID_PersistsAProtectedAtomicRecord proves the protection and
// atomicity half of requirement 3.6.
//
// The record must be an owner-only regular file in an owner-only directory,
// it must carry nothing but the version and the opaque host id, and the
// publication must be exclusive: a second publication attempt must not
// replace the record that is already there.
func TestEnsureHostID_PersistsAProtectedAtomicRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "state", "host.json")
	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	hostID, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat host record: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("the host record must be a regular file, got mode %v", info.Mode())
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("host record mode = %o, want 0600 (owner-only)", got)
		}
		// The directory the store creates for its own state is owner-only.
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat host record directory: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("host record directory mode = %o, want no group/other access", got)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record: %v", err)
	}
	var rec HostRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the persisted host record is not valid JSON: %v", err)
	}
	if rec.Version != hostRecordVersion {
		t.Fatalf("persisted host record version = %d, want %d", rec.Version, hostRecordVersion)
	}
	if rec.HostID != hostID {
		t.Fatalf("persisted host id %q is not the resolved %q", rec.HostID, hostID)
	}
	// The document carries the version and the host id and nothing else, so
	// the file discloses nothing about the person who signed in.
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("the persisted host record is not a JSON object: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("the host record must carry only the version and the host id, got %v", fields)
	}
}

// TestPublishExclusive_NeverReplacesAnExistingRecord proves the exclusivity
// of the first-start publication.
//
// This is the property the concurrent first start rests on: the record is
// published exactly once, and a later publisher observes the existing record
// instead of replacing it.
func TestPublishExclusive_NeverReplacesAnExistingRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.json")
	file, err := newProtectedFile(path)
	if err != nil {
		t.Fatalf("newProtectedFile(%q): %v", path, err)
	}
	loc, err := locate(path)
	if err != nil {
		t.Fatalf("locate(%q): %v", path, err)
	}
	const first = "urn:uuid:11111111-1111-4111-8111-111111111111"
	const second = "urn:uuid:22222222-2222-4222-8222-222222222222"

	created, err := file.publishExclusive(loc, HostRecord{Version: hostRecordVersion, HostID: first})
	if err != nil {
		t.Fatalf("publishExclusive: %v", err)
	}
	if !created {
		t.Fatal("the first publication must create the record")
	}
	created, err = file.publishExclusive(loc, HostRecord{Version: hostRecordVersion, HostID: second})
	if err != nil {
		t.Fatalf("publishExclusive (second): %v", err)
	}
	if created {
		t.Fatal("a second publication must not replace the published record")
	}
	rec := readHostRecord(t, path)
	if rec.HostID != first {
		t.Fatalf("the published record was replaced: %q", rec.HostID)
	}
}

// TestEnsureHostID_RejectsAnUnusableRecord proves the load half of
// requirement 3.6: a record that cannot be trusted is refused explicitly
// instead of being replaced or silently reinterpreted.
//
// A corrupt document, a document of an unknown version, a document that
// carries something other than an opaque host id, a record that other users
// can read, and a record path that is not a regular file are all refused -
// and none of them may be overwritten in the process.
func TestEnsureHostID_RejectsAnUnusableRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{
			name: "truncated document",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, []byte(`{"version":1,"ext_agent_host_id":`), 0o600)
			},
		},
		{
			name: "empty document",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, nil, 0o600)
			},
		},
		{
			name: "unknown version",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, []byte(`{"version":2,"ext_agent_host_id":"urn:uuid:11111111-1111-4111-8111-111111111111"}`), 0o600)
			},
		},
		{
			name: "missing host id",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, []byte(`{"version":1}`), 0o600)
			},
		},
		{
			name: "host id that carries a user identity",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				writeFile(t, path, []byte(`{"version":1,"ext_agent_host_id":"user@example.com"}`), 0o600)
			},
		},
		{
			name: "group accessible record",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("unix permission bits carry no meaning on Windows")
				}
				writeFile(t, path, []byte(`{"version":1,"ext_agent_host_id":"urn:uuid:11111111-1111-4111-8111-111111111111"}`), 0o644)
			},
		},
		{
			name: "symlink at the record path",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "elsewhere.json")
				writeFile(t, target, []byte(`{"version":1,"ext_agent_host_id":"urn:uuid:11111111-1111-4111-8111-111111111111"}`), 0o600)
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks are not available here: %v", err)
				}
			},
		},
		{
			name: "directory at the record path",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create directory at the record path: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "host.json")
			tc.prepare(t, path)
			before := snapshotPath(t, path)

			store, err := NewHostStore(path)
			if err != nil {
				t.Fatalf("NewHostStore(%q): %v", path, err)
			}
			if _, err := store.EnsureHostID(); err == nil {
				t.Fatal("EnsureHostID must refuse an unusable host record")
			}
			if _, err := store.HostID(); err == nil {
				t.Fatal("HostID must refuse an unusable host record")
			}
			if after := snapshotPath(t, path); before != after {
				t.Fatalf("a refused host record was rewritten:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// TestHostID_IsOpaqueAndCarriesNoUserIdentity proves requirement 3.4: the
// host id is an opaque value that encodes no user identity.
//
// The value is a random UUID behind a URI prefix, so it holds no email, no
// account id, no workspace name and no machine or path information. Two
// installations must not share one.
func TestHostID_IsOpaqueAndCarriesNoUserIdentity(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	hostID, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	if !hostIDPattern.MatchString(hostID) {
		t.Fatalf("host id %q is not the documented opaque UUID URI form", hostID)
	}
	body := strings.TrimPrefix(hostID, hostIDURIPrefix)
	if strings.ContainsAny(body, "@/\\ \t") {
		t.Fatalf("host id %q carries characters that could encode a user identity", hostID)
	}
	if strings.Contains(hostID, store.Path()) {
		t.Fatalf("host id %q encodes the record path", hostID)
	}

	other := newTestStore(t)
	otherID, err := other.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID (other host): %v", err)
	}
	if otherID == hostID {
		t.Fatalf("two installations resolved the same host id %q", hostID)
	}
}

// TestHostIDForImportedProfile_KeepsTheDestinationHostID proves requirement
// 3.5: importing a profile onto another host must not overwrite the
// destination host's own host id.
//
// The imported record carries the host id of the machine it was created on.
// That value describes the source host, so it is never adopted: the
// destination host binds the imported profile to its own id and reports the
// rebinding, and its host record is not touched.
func TestHostIDForImportedProfile_KeepsTheDestinationHostID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.json")
	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	hostID, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record: %v", err)
	}

	const foreignHostID = "urn:uuid:9f8e7d6c-5b4a-4392-8180-7f6e5d4c3b2a"
	imported, rebound, err := BindImportedProfile(store, foreignHostID)
	if err != nil {
		t.Fatalf("BindImportedProfile: %v", err)
	}
	if imported != hostID {
		t.Fatalf("an imported profile must be bound to this host's id %q, got %q", hostID, imported)
	}
	if !rebound {
		t.Error("the import must report that the imported host id belonged to another host")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record after the import: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the import rewrote the host record:\nbefore %s\nafter  %s", before, after)
	}
	current, err := store.HostID()
	if err != nil {
		t.Fatalf("HostID after the import: %v", err)
	}
	if current != hostID {
		t.Fatalf("the host id changed to %q after an import, want %q", current, hostID)
	}

	// A profile that already carries this host's own id is not a rebinding.
	imported, rebound, err = BindImportedProfile(store, hostID)
	if err != nil {
		t.Fatalf("BindImportedProfile (same host): %v", err)
	}
	if imported != hostID {
		t.Fatalf("HostIDForImportedProfile (same host) = %q, want %q", imported, hostID)
	}
	if rebound {
		t.Error("a profile that already carries this host's id must not be reported as rebound")
	}

	// Binding without a host identity is a programming error, not a silent
	// adoption of the imported value.
	if _, _, err := BindImportedProfile(nil, foreignHostID); err == nil {
		t.Error("binding an imported profile without a host identity must fail")
	}
}

// TestHostID_DoesNotCreateTheRecord proves the read path stays read-only.
//
// A lookup must never be able to change the host identity: only the first
// start may create the record, and the profile-import path depends on that
// separation.
func TestHostID_DoesNotCreateTheRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "host.json")
	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	if _, err := store.HostID(); err == nil {
		t.Fatal("a read-only lookup must not create the host record")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a read-only lookup must not create the host record, got %v", err)
	}
}

// TestResolveHostRecordPath proves the path decision.
//
// An operator may point the host record somewhere else, and the default
// lives in the per-user configuration directory under a connector-scoped
// name. The default never depends on the profile store path: the host
// identity belongs to the installation, not to a store file that an
// operator may relocate or replace with an imported one.
func TestResolveHostRecordPath(t *testing.T) {
	t.Parallel()

	configured := filepath.Join(t.TempDir(), "host.json")
	got, err := ResolveHostRecordPath(configured)
	if err != nil {
		t.Fatalf("ResolveHostRecordPath(%q): %v", configured, err)
	}
	if got != configured {
		t.Fatalf("ResolveHostRecordPath(%q) = %q, want the configured path", configured, got)
	}

	defaultPath, err := ResolveHostRecordPath("")
	if err != nil {
		t.Fatalf("ResolveHostRecordPath(\"\"): %v", err)
	}
	again, err := ResolveHostRecordPath("")
	if err != nil {
		t.Fatalf("ResolveHostRecordPath(\"\"): %v", err)
	}
	if defaultPath != again {
		t.Fatalf("the default host record path is not stable: %q then %q", defaultPath, again)
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user configuration directory on this host: %v", err)
	}
	if !strings.HasPrefix(defaultPath, userConfig) {
		t.Fatalf("the default host record path %q must live under the user configuration directory %q", defaultPath, userConfig)
	}
	if filepath.Base(defaultPath) != "host.json" {
		t.Fatalf("the default host record path %q must be named host.json", defaultPath)
	}
	if strings.Contains(defaultPath, "siwc") || strings.Contains(defaultPath, "profile") {
		t.Fatalf("the default host record path %q must not be tied to the profile store", defaultPath)
	}
}

// newTestStore returns a store over a private temporary directory.
func newTestStore(t *testing.T) *HostStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "host.json")
	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	return store
}

// readHostRecord reads and decodes the persisted host record.
func readHostRecord(t *testing.T, path string) HostRecord {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host record: %v", err)
	}
	var rec HostRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the persisted host record is not valid JSON: %v", err)
	}
	return rec
}

// writeFile writes data to path with the given permissions.
func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %q: %v", path, err)
	}
}

// readDirNames returns the sorted names of the entries in dir.
func readDirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// snapshotPath describes the state of path so that a refused record can be
// compared before and after the refusal. A path that is not a regular file
// is described by its kind instead of its bytes.
func snapshotPath(t *testing.T, path string) string {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return "not-a-regular-file:" + info.Mode().String()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(raw)
}
