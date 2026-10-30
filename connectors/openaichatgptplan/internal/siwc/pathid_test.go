// Tests for the path-identity contract of the openai-chatgpt-plan
// connector's SIWC state.
//
// The contract under test is the one the store documents: a configured
// record path is an operator's spelling, the operating system resolves it
// component by component, and the store must protect, publish and confirm
// the record at the location the operating system will really use.
//
// The proofs live at the store boundary, like the proofs of the host
// identity itself.
package siwc

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestEnsureHostID_AliasWithDotDotPublishesIntoTheResolvedRecord proves
// that the record lands at the operating system's location and that the
// actual containing directory is confirmed.
//
// The configured spelling "<base>/alias/../host.json" names a directory
// through a symlink and a "..". The operating system follows the symlink
// first and applies the ".." afterwards, so the record belongs in
// "<base>/target", while a lexical cleanup would put it in "<base>". Both
// the publisher and the caller that finds the published record must
// confirm the directory that really holds it.
//
//nolint:paralleltest // swaps the package-level dirSync durability seam; parallel siblings would race.
func TestEnsureHostID_AliasWithDotDotPublishesIntoTheResolvedRecord(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatalf("create the target directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// The spelling is built by concatenation on purpose: filepath.Join would
	// clean the "alias/.." pair away, and that pair is the case under test.
	path := base + "/alias/../host.json"

	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	confirmed := captureConfirmedDirs(t)

	id, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	physical := filepath.Join(target, "host.json")
	if _, err := os.Stat(physical); err != nil {
		t.Fatalf("the record must be published at the location the operating system resolves, %s: %v", physical, err)
	}
	cleaned := filepath.Join(base, "host.json")
	if _, err := os.Stat(cleaned); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record must not be published at the lexically cleaned location %s: %v", cleaned, err)
	}
	if !confirmed.contains(target) {
		t.Errorf("the publisher acknowledged %q without confirming the actual containing directory %s", id, target)
	}

	// The caller that finds the published record confirms it itself.
	confirmed.reset()
	again, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID (existing record): %v", err)
	}
	if again != id {
		t.Fatalf("the existing record resolved %q, want the stored %q", again, id)
	}
	if !confirmed.contains(target) {
		t.Errorf("the existing-record path acknowledged %q without confirming the actual containing directory %s", again, target)
	}
}

// TestStore_RefusesAnActualOtherWritableParentThroughAnAlias proves that
// the directory checked for other-write is the record's actual parent.
//
// The protection policy applies to the directory that holds the record. A
// lexical derivation would check a different directory than the one the
// operating system uses, and an other-writable parent would then be
// accepted through the alias spelling while the direct spelling is
// correctly refused.
func TestStore_RefusesAnActualOtherWritableParentThroughAnAlias(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits carry no meaning on Windows")
	}

	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatalf("create the target directory: %v", err)
	}
	if err := os.Chmod(target, 0o777); err != nil {
		t.Fatalf("chmod the target directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	// The control: the direct spelling names the other-writable parent.
	direct, err := NewHostStore(filepath.Join(target, "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	if id, err := direct.EnsureHostID(); err == nil {
		t.Fatalf("the direct spelling must be refused in an other-writable parent, got the host id %q", id)
	}

	// The alias spelling names the same directory and must be refused too.
	alias, err := NewHostStore(base + "/alias/../host.json")
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	if id, err := alias.EnsureHostID(); err == nil {
		t.Fatalf("the alias spelling must be refused in the same other-writable parent, got the host id %q", id)
	}
}

// TestEnsureHostID_ConfirmsThePhysicalAncestorChainThroughACleanSymlink
// proves that the confirmed chain covers the record's physical ancestors.
//
// A clean spelling through a symlink names a nested state directory. The
// confirmation has to walk the ancestors of the directory the record
// really lives in, not the ancestors of the symlink's spelling.
//
//nolint:paralleltest // swaps the package-level dirSync durability seam; parallel siblings would race.
func TestEnsureHostID_ConfirmsThePhysicalAncestorChainThroughACleanSymlink(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	target := filepath.Join(realParent, "state")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	store, err := NewHostStore(filepath.Join(base, "alias", "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	confirmed := captureConfirmedDirs(t)

	id, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	if !confirmed.contains(realParent) {
		t.Errorf("the first start acknowledged %q without confirming the actual ancestor %s", id, realParent)
	}
	if !confirmed.contains(target) {
		t.Errorf("the first start acknowledged %q without confirming the actual state directory %s", id, target)
	}
}

// TestEnsureHostID_FirstStartThroughASymlinkAndDotDot proves that a first
// start publishes into the operating system's location even when the state
// directory does not exist yet.
//
// The record's directory is created by the store, so the creation has to
// happen where the operating system will look for it. A lexical derivation
// would create a different directory and the publication would fail.
func TestEnsureHostID_FirstStartThroughASymlinkAndDotDot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatalf("create the target directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// The state directory does not exist yet: this is a first start.
	path := base + "/alias/../state/host.json"

	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	id, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	physical := filepath.Join(target, "state", "host.json")
	rec := readHostRecord(t, physical)
	if rec.HostID != id {
		t.Fatalf("the published record carries %q, want the resolved %q", rec.HostID, id)
	}
	cleaned := filepath.Join(base, "state", "host.json")
	if _, err := os.Stat(cleaned); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record must not be published at the lexically cleaned location %s: %v", cleaned, err)
	}
}

// TestStore_RefusesAnUnresolvablePath proves that a path the operating
// system cannot resolve is refused, and that nothing is created for it.
//
// A ".." that follows a component that does not exist cannot be resolved
// by the operating system either, so the path names no single directory.
// The store refuses it instead of guessing one, and it refuses it before
// any state exists.
func TestStore_RefusesAnUnresolvablePath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	path := base + "/missing/../host.json"

	store, err := NewHostStore(path)
	if err == nil {
		t.Fatalf("NewHostStore(%q) must refuse a path that does not resolve to a single directory", path)
	}
	if store != nil {
		t.Fatal("a refused path must not yield a store")
	}
	if entries := readDirNames(t, base); len(entries) != 0 {
		t.Fatalf("a refused path must not create any state, got %v", entries)
	}
}

// TestNewHostStore_RefusesAPathThatDoesNotNameAFile proves that the store
// refuses a path that does not name the record file.
//
// The host record is a file. A path whose last component is empty, "." or
// ".." names a directory, and the store would otherwise publish the record
// under a name that does not exist.
func TestNewHostStore_RefusesAPathThatDoesNotNameAFile(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	for _, path := range []string{
		base + "/host.json/",
		base + "/host.json/.",
		base + "/host.json/..",
		base + "/",
	} {
		if _, err := NewHostStore(path); err == nil {
			t.Errorf("NewHostStore(%q) must refuse a path that does not name a file", path)
		}
	}
}

// TestEnsureHostID_PublishesTheTemporaryFileInThePublicationDirectory
// proves that the record's temporary file is created in the directory the
// record is published in.
//
// The temporary file has to be created in the publication directory: it is
// the directory whose durability is confirmed, and a hard link cannot cross
// a filesystem boundary. The lexically derived parent of the configured
// spelling is made unwritable here, so a temporary file created there would
// fail the publication outright.
func TestEnsureHostID_PublishesTheTemporaryFileInThePublicationDirectory(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits carry no meaning on Windows")
	}

	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatalf("create the base directory: %v", err)
	}
	physical := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(physical, "child"), 0o700); err != nil {
		t.Fatalf("create the physical directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(physical, "child"), filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// The lexically derived parent of the configured spelling is "base", and
	// it is made unwritable: the publication must not depend on it.
	if err := os.Chmod(base, 0o555); err != nil {
		t.Fatalf("chmod the base directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })

	path := base + "/alias/../host.json"
	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	id, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	rec := readHostRecord(t, filepath.Join(physical, "host.json"))
	if rec.HostID != id {
		t.Fatalf("the published record carries %q, want the resolved %q", rec.HostID, id)
	}
	for _, name := range readDirNames(t, base) {
		if strings.HasPrefix(name, ".host.json.") {
			t.Errorf("a temporary file was left in the lexically derived directory %s: %s", base, name)
		}
	}
}

// TestStore_ResolvesThePathThroughASymlinkedConfigurationDirectory proves
// that the default location keeps working under a symlinked configuration
// directory, and that the confirmed chain follows the physical ancestors.
//
// The default record location lives under the user configuration
// directory, which routinely traverses a symlink. A store that refused
// symlinked paths would fail to start on ordinary systems, so the store
// resolves them - and the chain it confirms is the physical one.
//
//nolint:paralleltest // swaps the package-level dirSync durability seam; parallel siblings would race.
func TestStore_ResolvesThePathThroughASymlinkedConfigurationDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-config")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("create the configuration directory: %v", err)
	}
	if err := os.Symlink(real, filepath.Join(base, "config")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// The shape of the default record location, under a symlinked
	// configuration directory.
	path := filepath.Join(base, "config", "aip", "openai-chatgpt-plan", "host.json")

	store, err := NewHostStore(path)
	if err != nil {
		t.Fatalf("NewHostStore(%q): %v", path, err)
	}
	confirmed := captureConfirmedDirs(t)

	id, err := store.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	physical := filepath.Join(real, "aip", "openai-chatgpt-plan", "host.json")
	rec := readHostRecord(t, physical)
	if rec.HostID != id {
		t.Fatalf("the published record carries %q, want the resolved %q", rec.HostID, id)
	}
	for _, want := range []string{
		real,
		filepath.Join(real, "aip"),
		filepath.Join(real, "aip", "openai-chatgpt-plan"),
	} {
		if !confirmed.contains(want) {
			t.Errorf("the first start acknowledged %q without confirming the actual ancestor %s", id, want)
		}
	}
}

// TestEnsureHostID_FirstStartPreservesAUnicodeDirectoryName proves that
// the resolution never rewrites a directory name.
//
// A directory name may start with a character whose CODE POINT, narrowed to
// a byte, aliases the separator - "Я" (U+042F) and "į" (U+012F) both have a
// low byte of 0x2f, which is "/" - and a decoder that narrows a code point
// to a byte reads such a character as a separator. The resolution would then
// drop the first letter of the name, create a different directory and leave
// the configured one missing: a deterministic first-start failure on a valid
// operator path.
//
// The proof is a first start on a path whose state directory does not exist
// yet: the record must appear at the exact configured path, the restart
// must resolve the same host id, and no sibling directory may be created
// next to the configured one.
func TestEnsureHostID_FirstStartPreservesAUnicodeDirectoryName(t *testing.T) {
	t.Parallel()

	// Both names start with a letter whose low byte is the separator byte:
	// "Я" is U+042F and "į" is U+012F.
	for _, name := range []string{"Яндекс", "įstate"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := t.TempDir()
			path := filepath.Join(base, name, "host.json")

			store, err := NewHostStore(path)
			if err != nil {
				t.Fatalf("NewHostStore(%q): %v", path, err)
			}
			if store.ConfiguredPath() != path {
				t.Fatalf("the configured spelling %q was rewritten to %q", path, store.ConfiguredPath())
			}
			if got := filepath.Base(filepath.Dir(store.Path())); got != name {
				t.Fatalf("the resolved state directory is %q, want the configured name %q", got, name)
			}

			hostID, err := store.EnsureHostID()
			if err != nil {
				t.Fatalf("EnsureHostID: %v", err)
			}
			rec := readHostRecord(t, path)
			if rec.HostID != hostID {
				t.Fatalf("the published record carries %q, want the resolved %q", rec.HostID, hostID)
			}
			// The store must not have created a directory of its own next to
			// the configured one.
			entries := readDirNames(t, base)
			if !slices.Equal(entries, []string{name}) {
				t.Fatalf("the first start created %v, want only the configured directory %q", entries, name)
			}

			// A restart reuses the stored identity and does not rewrite it.
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the host record: %v", err)
			}
			restarted, err := NewHostStore(path)
			if err != nil {
				t.Fatalf("NewHostStore(%q) after the restart: %v", path, err)
			}
			again, err := restarted.EnsureHostID()
			if err != nil {
				t.Fatalf("EnsureHostID after the restart: %v", err)
			}
			if again != hostID {
				t.Fatalf("the host id after the restart is %q, want the stored %q", again, hostID)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the host record after the restart: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("the restart rewrote the host record:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// TestResolvePhysicalDir_UsesTheRootOfTheVolumeAsTheFirstAncestor proves
// that the prefix walk starts at the root.
//
// Every absolute path has exactly one ancestor that exists without any of
// its components existing: the root of its volume. A walk that considered
// only the components of the path would report "no existing ancestor" for
// a path whose first component does not exist yet, and would refuse a
// first start that the operating system performs without trouble.
//
// The probe path is not created: the resolution only reads the file
// system.
func TestResolvePhysicalDir_UsesTheRootOfTheVolumeAsTheFirstAncestor(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.VolumeName(base) + string(filepath.Separator)
	probe := filepath.Join(root, "siwc-root-ancestor-probe-"+strconv.Itoa(os.Getpid()))
	if _, err := os.Lstat(probe); !errors.Is(err, fs.ErrNotExist) {
		t.Skipf("the probe path %s already exists", probe)
	}

	got, err := resolvePhysicalDir(probe)
	if err != nil {
		t.Fatalf("resolvePhysicalDir(%q): %v", probe, err)
	}
	if got != probe {
		t.Fatalf("resolvePhysicalDir(%q) = %q, want the probed path itself", probe, got)
	}
	if _, err := os.Lstat(probe); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the resolution created %s", probe)
	}
}

// confirmedDirs records the directories whose durability the store
// confirmed.
type confirmedDirs struct {
	mu   sync.Mutex
	dirs map[string]bool
}

// captureConfirmedDirs swaps the package's directory-sync seam for the
// duration of the test and returns the recorder.
func captureConfirmedDirs(t *testing.T) *confirmedDirs {
	t.Helper()

	recorder := &confirmedDirs{dirs: map[string]bool{}}
	original := dirSync
	t.Cleanup(func() { dirSync = original })
	dirSync = func(dir string) error {
		recorder.mu.Lock()
		recorder.dirs[filepath.Clean(dir)] = true
		recorder.mu.Unlock()
		return original(dir)
	}
	return recorder
}

// contains reports whether the directory was confirmed.
func (c *confirmedDirs) contains(dir string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirs[filepath.Clean(dir)]
}

// reset forgets the recorded confirmations.
func (c *confirmedDirs) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs = map[string]bool{}
}
