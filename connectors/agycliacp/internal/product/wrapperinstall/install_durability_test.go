package wrapperinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// durabilityStep is one observed durability primitive call. Recording the
// rename as its own step is what makes the advertised ordering falsifiable: a
// future implementation that publishes first and flushes the bytes afterwards
// still produces a "file" step, but not before the "rename" step.
type durabilityStep struct {
	kind string // "file", "rename" or "dir"
	path string // directory flush target, or the rename destination
}

type durabilityTrace struct {
	steps []durabilityStep

	// failOn selects a primitive to fail: "file", "dir" or "" for none.
	failOn string
	// failDirAfterRename restricts a "dir" failure to the flushes that follow
	// the first successful rename. It models the reviewer's scenario: the
	// candidate was flushed and published, then the entry could not be
	// confirmed.
	failDirAfterRename bool
	renamed            bool

	// targetExistedAtRename reports whether the rename destination already
	// existed when the rename ran.
	targetExistedAtRename bool
}

func (tr *durabilityTrace) reset() {
	tr.steps = nil
	tr.renamed = false
	tr.targetExistedAtRename = false
}

func (tr *durabilityTrace) kinds() []string {
	out := make([]string, 0, len(tr.steps))
	for _, step := range tr.steps {
		out = append(out, step.kind)
	}
	return out
}

func (tr *durabilityTrace) dirFlushes() []string {
	var out []string
	for _, step := range tr.steps {
		if step.kind == "dir" {
			out = append(out, step.path)
		}
	}
	return out
}

// withDurabilitySeams swaps the package-level durability primitives for one
// test and restores them afterwards. A wrapped call records its kind and path,
// then either fails the selected step or runs the real OS call the seam
// replaced.
func withDurabilitySeams(t *testing.T, failOn string) *durabilityTrace {
	t.Helper()
	originalFile := fileSync
	originalDir := dirSync
	originalRename := renameFile
	trace := &durabilityTrace{failOn: failOn}
	fileSync = func(f *os.File) error {
		trace.steps = append(trace.steps, durabilityStep{kind: "file", path: f.Name()})
		if trace.failOn == "file" {
			return errors.New("simulated data flush failure")
		}
		return originalFile(f)
	}
	dirSync = func(path string) error {
		trace.steps = append(trace.steps, durabilityStep{kind: "dir", path: path})
		if trace.failOn == "dir" && (!trace.failDirAfterRename || trace.renamed) {
			return errors.New("simulated directory flush failure")
		}
		return originalDir(path)
	}
	renameFile = func(oldpath, newpath string) error {
		if _, err := os.Stat(newpath); err == nil {
			trace.targetExistedAtRename = true
		}
		trace.steps = append(trace.steps, durabilityStep{kind: "rename", path: newpath})
		trace.renamed = true
		return originalRename(oldpath, newpath)
	}
	t.Cleanup(func() {
		fileSync = originalFile
		dirSync = originalDir
		renameFile = originalRename
	})
	return trace
}

// releaseFixture serves one stable release whose single platform asset is the
// wrapper archive built by testArchive, plus a matching checksums.txt. The
// returned counter reports how many asset downloads the peer served.
func releaseFixture(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	archiveName := "go-agy-acp-wrapper_1.2.3" + mustSuffix(t)
	archive := testArchive(t)
	digest := sha256.Sum256(archive)
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(release{TagName: "v1.2.3", Assets: []asset{
				{Name: archiveName, URL: serverURL(r) + "/archive"},
				{Name: "checksums.txt", URL: serverURL(r) + "/checksums"},
			}})
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
		case "/archive":
			downloads++
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	return server, &downloads
}

func cachedBinaryPath(root string) string {
	name := "go-agy-acp-wrapper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "v1.2.3", name)
}

// TestEnsureFlushesTheBinaryBeforePublishingIt proves the crash ordering: the
// verified bytes are flushed to stable storage before the rename publishes the
// final name, and the directory entry that names the binary is flushed after.
// A crash between the rename and the directory flush would leave the entry
// disappearable; a crash before the byte flush would leave truncated bytes
// under a name every later run trusts without re-verifying the release.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureFlushesTheBinaryBeforePublishingIt(t *testing.T) {
	trace := withDurabilitySeams(t, "")
	server, _ := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	// Pre-create the release directory so this test observes only the publish
	// path; new-directory durability has its own test.
	if err := os.MkdirAll(filepath.Dir(cachedBinaryPath(cache)), 0o755); err != nil {
		t.Fatal(err)
	}
	path, ensureErr := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if ensureErr != nil {
		t.Fatalf("Ensure: %v", ensureErr)
	}
	if path != cachedBinaryPath(cache) {
		t.Fatalf("published path = %q, want %q", path, cachedBinaryPath(cache))
	}
	if content, readErr := os.ReadFile(path); readErr != nil || string(content) != "wrapper" {
		t.Fatalf("published binary = %q, %v", content, readErr)
	}
	if steps := trace.kinds(); !slices.Equal(steps, []string{"file", "rename", "dir", "dir"}) {
		t.Fatalf("durability steps = %v, want the byte flush, then the rename, then the directory confirmations", steps)
	}
	if trace.targetExistedAtRename {
		t.Fatal("the rename destination existed before publication; the fixture is not proving a first install")
	}
	wantDirs := []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))}
	if dirs := trace.dirFlushes(); !slices.Equal(dirs, wantDirs) {
		t.Fatalf("directory flushes = %v, want %v", dirs, wantDirs)
	}
}

// TestEnsurePersistsNewCacheDirectories proves the install flushes the entry of
// every directory it creates, not just the directory that holds the binary. A
// crash after Ensure returns must not lose the version directory (or the cache
// root that names it), or the next start would re-download a release it had
// already verified.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsurePersistsNewCacheDirectories(t *testing.T) {
	trace := withDurabilitySeams(t, "")
	server, _ := releaseFixture(t)
	defer server.Close()

	base := t.TempDir()
	cache := filepath.Join(base, "a", "b")
	if _, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dirs := trace.dirFlushes()
	// base is the pre-existing ancestor: it must be flushed because the install
	// created "a" inside it.
	for _, want := range []string{base, filepath.Join(base, "a"), cache, filepath.Dir(cachedBinaryPath(cache))} {
		if !slices.Contains(dirs, want) {
			t.Fatalf("directory flushes = %v, want %q among them", dirs, want)
		}
	}
}

// TestEnsureNeverPublishesUnflushedBytes proves a failed byte flush is not
// repaired by publishing anyway: no file appears under the final name, so a
// later install retries from the network instead of launching a partial
// executable the cache would otherwise trust.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureNeverPublishesUnflushedBytes(t *testing.T) {
	withDurabilitySeams(t, "file")
	server, downloads := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	path, ensureErr := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if ensureErr == nil {
		t.Fatalf("Ensure published %q although the byte flush failed", path)
	}
	if !strings.Contains(ensureErr.Error(), "flush binary") {
		t.Fatalf("Ensure error = %v, want the failed byte flush named", ensureErr)
	}
	if matches, _ := filepath.Glob(filepath.Join(cache, "v1.2.3", "go-agy-acp-wrapper*")); len(matches) != 0 {
		t.Fatalf("bytes whose flush failed were published: %v", matches)
	}
	if *downloads == 0 {
		t.Fatal("the fixture never served the asset; the refusal is not attributable")
	}
}

// TestEnsureReportsAFailedDirectoryFlush proves a failure to make the
// published name durable is reported instead of acknowledged: the complete
// bytes stay (they are the same verified release), but the install is not
// claimed to be crash-safe.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureReportsAFailedDirectoryFlush(t *testing.T) {
	trace := withDurabilitySeams(t, "dir")
	trace.failDirAfterRename = true
	server, _ := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	_, ensureErr := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if ensureErr == nil {
		t.Fatal("Ensure acknowledged a binary whose directory entry could not be made durable")
	}
	if !errors.Is(ensureErr, errPublishUnconfirmed) {
		t.Fatalf("Ensure error = %v, want errPublishUnconfirmed", ensureErr)
	}
	if content, readErr := os.ReadFile(cachedBinaryPath(cache)); readErr != nil || string(content) != "wrapper" {
		t.Fatalf("published binary = %q, %v", content, readErr)
	}
}

// TestEnsureConfirmsTheDirectoryEntryOnALaterCacheHit proves the failed
// confirmation is not bypassed by the next invocation: the published file is
// present, but the cache hit re-runs the directory flush before acknowledging
// it, and does not re-download the release.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureConfirmsTheDirectoryEntryOnALaterCacheHit(t *testing.T) {
	trace := withDurabilitySeams(t, "dir")
	trace.failDirAfterRename = true
	server, downloads := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	opts := Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()}
	if _, err := Ensure(context.Background(), opts); err == nil {
		t.Fatal("the first install acknowledged an unconfirmed directory entry")
	}
	if _, err := os.Stat(cachedBinaryPath(cache)); err != nil {
		t.Fatalf("the failed install did not leave the verified bytes behind: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("asset downloads = %d, want 1", *downloads)
	}

	// A later invocation finds the file. It must redo the confirmation rather
	// than treat presence as durability.
	trace.failOn = ""
	trace.reset()
	path, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatalf("Ensure after a repaired flush: %v", err)
	}
	if path != cachedBinaryPath(cache) {
		t.Fatalf("path = %q, want the cache hit %q", path, cachedBinaryPath(cache))
	}
	if steps := trace.kinds(); !slices.Equal(steps, []string{"dir", "dir"}) {
		t.Fatalf("cache-hit steps = %v, want the release directory and its parent confirmed", steps)
	}
	if *downloads != 1 {
		t.Fatalf("the cache hit re-downloaded the release: %d downloads", *downloads)
	}
}

// TestEnsureDoesNotAcknowledgeAnUnconfirmedCacheHit proves the invariant is
// fail-closed: while the directory flush keeps failing, every invocation
// reports the error instead of returning the path the earlier one published.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureDoesNotAcknowledgeAnUnconfirmedCacheHit(t *testing.T) {
	trace := withDurabilitySeams(t, "dir")
	trace.failDirAfterRename = true
	server, downloads := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	opts := Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()}
	if _, err := Ensure(context.Background(), opts); err == nil {
		t.Fatal("the first install acknowledged an unconfirmed directory entry")
	}

	trace.failDirAfterRename = false
	trace.reset()
	path, err := Ensure(context.Background(), opts)
	if err == nil {
		t.Fatalf("the second invocation returned %q although the directory entry is still unconfirmed", path)
	}
	if !errors.Is(err, errPublishUnconfirmed) {
		t.Fatalf("second invocation error = %v, want errPublishUnconfirmed", err)
	}
	if *downloads != 1 {
		t.Fatalf("asset downloads = %d, want 1 (the second call must be a cache hit)", *downloads)
	}
}

// TestEnsureOfflineFallbackRequiresADurableDirectoryEntry proves the offline
// fallback obeys the same invariant: an unreachable peer does not license
// returning a cached name whose entry cannot be confirmed.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureOfflineFallbackRequiresADurableDirectoryEntry(t *testing.T) {
	withDurabilitySeams(t, "dir")
	cache := t.TempDir()
	cached := cachedBinaryPath(cache)
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("cached"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Ensure(context.Background(), Options{CacheDir: cache, URL: "://bad"})
	if err == nil {
		t.Fatalf("offline fallback returned %q although the directory entry could not be confirmed", path)
	}
	if !errors.Is(err, errPublishUnconfirmed) {
		t.Fatalf("offline fallback error = %v, want errPublishUnconfirmed", err)
	}
}

// TestEnsureDoesNotPublishIntoAnUnconfirmedDirectory proves a newly created
// directory whose entry cannot be made durable is not used for publication:
// no binary appears under a name a crash could take away.
//
//nolint:paralleltest // swaps the package-level durability seams; parallel siblings would race.
func TestEnsureDoesNotPublishIntoAnUnconfirmedDirectory(t *testing.T) {
	withDurabilitySeams(t, "dir")
	server, downloads := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	path, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if err == nil {
		t.Fatalf("Ensure published %q into a directory it could not make durable", path)
	}
	if matches, _ := filepath.Glob(filepath.Join(cache, "v1.2.3", "go-agy-acp-wrapper*")); len(matches) != 0 {
		t.Fatalf("binary published into an unconfirmed directory: %v", matches)
	}
	if *downloads == 0 {
		t.Fatal("the fixture never served the asset; the refusal is not attributable")
	}
}
