package siwc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// fileSync and dirSync are the durability primitives the publication and the
// durability confirmation rely on.
//
// They are variables so package tests can prove the sync-before-publish
// ordering and the failure handling deterministically; production code always
// uses the OS calls.
var (
	fileSync = func(f *os.File) error { return f.Sync() }
	dirSync  = syncDirectory
)

// errDurabilityUnconfirmed reports that a document is IN PLACE and that its
// persistence is UNPROVEN.
//
// The two facts are kept apart on purpose. The publication happened - the
// rename replaced the directory entry, or the link created it - and a reader
// sees the document. What failed is the confirmation that the entry
// survives a crash.
//
// A caller that reads this failure as "nothing was published" reports a
// state that is not the one on the disk, and a caller that reads it as
// "the write is durable" acknowledges a document whose persistence is
// unproven. The sentinel lets the caller tell the two apart: the document is
// published, and its durability is not confirmed.
var errDurabilityUnconfirmed = errors.New(
	"siwc: the document is in place but its durability was not confirmed",
)

// protectedFile is a JSON document on the local disk that is protected
// against other local users and published atomically.
//
// The durability contract is the one the shared connector-support store
// applies to credential files: the bytes are written to a temporary file in
// the target directory, flushed, closed and only then published, and the
// directory entry is flushed after the publication. A reader therefore never
// observes a half-written document, and a crash cannot leave a truncated
// record behind.
//
// The publication is exclusive: publishExclusive creates the document when it
// is absent and refuses to replace one that already exists.
//
// The state directory is a trust boundary, and the check covers exactly one
// level of it. The document's own mode protects its contents, but a
// principal that can write the directory can unlink the document and replace
// it.
//
// The enforced policy is: the directory that holds the document must not be
// writable by other users. A group-writable directory is accepted as an
// operator's deliberate trust-domain choice - a shared administrative group,
// for example - and the store keeps working there, but the guarantee then
// holds only inside that group, because another member of it can replace the
// document.
//
// The store creates the directory it owns with owner-only permissions and
// never changes the permissions of a directory it did not create: an
// operator-supplied parent keeps the permissions the operator chose, and the
// store refuses an untrusted one instead of silently tightening or relaxing
// it.
//
// The directory the policy is checked against is the one the operating
// system will really use - the resolved parent of the document, not a
// lexically derived one. The path-identity contract of pathid.go owns that
// resolution, and every operation below works on the resolved location.
type protectedFile struct {
	path string
}

// newProtectedFile returns the store of the document at path.
func newProtectedFile(path string) (*protectedFile, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("siwc: the store path is empty")
	}
	return &protectedFile{path: trimmed}, nil
}

// load reads the document at the resolved location into v.
//
// A document that is missing is reported with the not-exist error, so that
// the caller can tell "there is nothing stored yet" from "the stored
// document cannot be trusted".
//
// The location is the caller's: it resolves the configured path once and
// hands the same identity to every operation of that call, so the load, the
// publication and the durability confirmation cannot disagree about which
// file the document is.
func (f *protectedFile) load(loc recordLocation, v any) error {
	if err := checkProtectedFile(loc.path()); err != nil {
		return err
	}
	if err := verifySameDirectory(loc.rawParent, loc.dir); err != nil {
		return err
	}
	return f.loadDocument(loc.dir, loc.name, v)
}

// loadFrom reads the document named name in dir into v.
//
// It is the file-level half of load, for a store that owns a directory of
// documents instead of a single record: the caller resolves the configured
// spelling of that directory once and hands the same physical directory to
// every operation of the call, and the protection rules are the ones the
// single-record store applies - the document must be a protected regular
// file, and the directory that holds it must be a trusted state directory.
//
// A document that is missing is reported with the not-exist error, so that
// the caller can tell "there is nothing stored yet" from "the stored
// document cannot be trusted".
func (f *protectedFile) loadFrom(dir, name string, v any) error {
	if err := checkProtectedFile(filepath.Join(dir, name)); err != nil {
		return err
	}
	return f.loadDocument(dir, name, v)
}

// loadDocument reads the document named name in dir into v, after the
// trusted-directory check.
//
// A document that cannot be decoded is reported as a CLASSIFICATION, never as
// the decoder's own message: the decoder's messages are not value-free. A
// timestamp field's unmarshaler quotes the string it rejected, a syntax
// error quotes the character it stumbled on, and a stored document holds
// credential material. A malformed, imported or manually edited document
// could therefore carry a token into a support-facing error, so the cause is
// classified instead of quoted.
//
// ACCEPTED RESIDUAL - the repeated-JSON-key rule. A document that repeats a
// key keeps the LAST value of that key, exactly as the token response and
// the token error of the exchange do: encoding/json overwrites the field it
// decoded before. The rule is ACCEPTED for a protected owner-local
// document, and it is stated here so that the next reader does not re-open
// it as an unknown at one of the three decode sites. The document is written
// by this build, it lives in the owner's own protected state directory, and
// a document that repeats a key was edited or imported rather than
// produced. The record is replaced as a whole on every save, so a repeated
// key cannot survive a write of this build.
func (f *protectedFile) loadDocument(dir, name string, v any) error {
	if err := checkTrustedStateDir(dir); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("siwc: read %s: %w", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("siwc: invalid %s JSON: %s", name, safeJSONCause(err))
	}
	return nil
}

// safeJSONCause classifies a JSON failure without repeating any byte of the
// document.
//
// The decoder's own messages carry values: a timestamp field's unmarshaler
// quotes the rejected string, and a syntax error quotes the character it
// stumbled on. A stored document holds credential material, so the failure
// is reported as the kind of problem, and never as a value.
//
// The classification is deliberately coarse. It names the KIND of failure -
// a malformed document, a field of the wrong type, a timestamp that is not a
// timestamp - and never the content that failed.
//
// The host record shares this boundary. It has no timestamp field, so its
// decoder can only report a malformed document or a field of the wrong
// type, but the same rule applies to it: a document that was read from the
// local disk is never quoted back into an error.
func safeJSONCause(err error) string {
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("the document is malformed at byte offset %d", syntax.Offset)
	}
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		if typeErr.Field == "" {
			return "the document does not hold the expected type"
		}
		return fmt.Sprintf("the field %q does not hold the expected type", typeErr.Field)
	}
	if _, ok := errors.AsType[*time.ParseError](err); ok {
		return "a timestamp field does not hold a valid timestamp"
	}
	if _, ok := errors.AsType[*json.UnsupportedTypeError](err); ok {
		return "the document holds a value of an unsupported type"
	}
	return "the document does not match the expected shape"
}

// replace writes v to the document named name in dir, replacing the document
// that is already there.
//
// The mechanism is the durable write-once-per-generation pattern: the bytes
// are written to a temporary file in the same directory, flushed, closed and
// only then renamed over the previous document, and the directory entry is
// flushed after the rename.
//
// The rename replaces the previous document in one step, so a reader
// observes either the previous document or the new one, never a mixture and
// never a half-written document. A failure before the rename leaves the
// previous document in place, so a replacement that cannot be completed does
// not lose the record it was replacing.
//
// The rename replaces the previous document's directory entry rather than
// writing through it, so a document that was replaced by a symlink cannot
// redirect the write to another file.
//
// The temporary file is created in the publication directory - the resolved
// one - so it is covered by the same durability chain, and the rename never
// crosses a filesystem boundary.
//
// A failure of the durability confirmation is reported even though the
// document is in place: the caller must not acknowledge a document whose
// persistence is unproven. The failure carries the sentinel that keeps the
// two facts apart: the document IS published, and its durability is NOT
// confirmed.
func (f *protectedFile) replace(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("siwc: create %s: %w", filepath.Base(dir), err)
	}
	if err := checkTrustedStateDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// The encoder's own message can quote the value it cannot
		// represent, so the failure is reported without a cause.
		return fmt.Errorf("siwc: cannot encode %s: the document holds a value that cannot be encoded", name)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return fmt.Errorf("siwc: create a temporary %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("siwc: protect the temporary %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("siwc: write the temporary %s: %w", name, err)
	}
	// Flush the bytes before they become reachable under the final name:
	// without this a crash after the rename can leave the record present but
	// empty, which the load path rejects as corrupt.
	if err := fileSync(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("siwc: sync the temporary %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("siwc: close the temporary %s: %w", name, err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("siwc: publish %s: %w", name, err)
	}
	// Make the published name durable too, so a crash cannot roll the record
	// back to the previous one. The confirmation covers the whole directory
	// chain, not only the leaf.
	if err := confirmDurable(dir); err != nil {
		return err
	}
	return nil
}

// publishExclusive writes v to the document only when the document does not
// exist yet, and reports whether it created it.
//
// The document is written and flushed into a temporary file first and only
// then linked into place. Linking is the atomic create-if-absent primitive:
// it fails with fs.ErrExist when another writer published the document
// first, and it never exposes the path with partial content. A concurrent
// first start therefore either publishes its own record or observes the
// winner's complete record - never a truncated one, and never two different
// records.
//
// The mechanism needs a filesystem that supports hard links, which every
// mainstream local filesystem does (ext4, xfs, btrfs, apfs, NTFS, tmpfs). A
// filesystem without hard link support reports the failure explicitly
// instead of falling back to a publication that a concurrent reader could
// observe half-written.
//
// The directory chain is flushed after the publication: the leaf that holds
// the document and every ancestor that contains the next one. A failure of
// any of those flushes is reported even though the document is in place: the
// caller must not acknowledge a document whose persistence is unproven. The
// created result reports the publication itself, so a caller can tell a
// published document from a failed durability confirmation.
//
// The publication happens in the resolved directory: the temporary file is
// created there, so it is covered by the same durability chain, and it is
// linked to the record in the same directory, so the link can never cross a
// filesystem boundary.
//
// The location is the caller's, for the same reason as in load: one call
// resolves the configured path once and hands the same identity to every
// operation of that call.
func (f *protectedFile) publishExclusive(loc recordLocation, v any) (bool, error) {
	if err := os.MkdirAll(loc.dir, 0o700); err != nil {
		return false, fmt.Errorf("siwc: create %s: %w", loc.dir, err)
	}
	if err := verifySameDirectory(loc.rawParent, loc.dir); err != nil {
		return false, err
	}
	if err := checkTrustedStateDir(loc.dir); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return false, fmt.Errorf("siwc: marshal %s: %w", loc.name, err)
	}
	tmp, err := os.CreateTemp(loc.dir, "."+loc.name+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("siwc: create a temporary %s: %w", loc.name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("siwc: protect the temporary %s: %w", loc.name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("siwc: write the temporary %s: %w", loc.name, err)
	}
	// Flush the bytes before they become reachable under the final name:
	// without this a crash after the publication can leave the record
	// present but empty, which the load path rejects as corrupt.
	if err := fileSync(tmp); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("siwc: sync the temporary %s: %w", loc.name, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("siwc: close the temporary %s: %w", loc.name, err)
	}
	if err := os.Link(tmpName, loc.path()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("siwc: publish %s: %w", loc.name, err)
	}
	// Make the published name durable too, so a crash cannot lose the record
	// that the host id lives in. The confirmation covers the whole directory
	// chain, not only the leaf: the directories that hold the record may
	// have been created moments earlier, and their own entries live in
	// their parents.
	if err := confirmDurable(loc.dir); err != nil {
		return true, err
	}
	return true, nil
}

// confirmDurable establishes that the document's whole directory chain is
// durable.
//
// Flushing a directory persists the entries inside it; it does not confirm
// the directory's own entry in its parent. A record whose parent chain was
// created moments earlier therefore needs more than the leaf confirmed: the
// leaf that holds the record, and every ancestor directory that contains the
// next one. Otherwise a crash can lose an ancestor entry, the next start can
// find no record at all, and it generates a different host id.
//
// The chain starts at the resolved directory that holds the record. That
// directory holds no symlink component, so its lexical ancestors are its
// physical ancestors and the walk covers the directories the operating
// system will really use.
//
// The chain is path-determined, not state-determined: a caller that finds an
// existing record must confirm the same chain the creator confirmed, so it
// cannot depend on knowing which directories the creator made. The
// confirmation therefore holds between independent connector processes, and
// a caller that finds the path before the creator finished confirming the
// ancestors cannot acknowledge it either.
//
// A failure of any confirmation in the chain is reported: the document is
// not acknowledged while its persistence is unproven. The failure carries
// the sentinel that says the document IS in place: the confirmation is what
// failed, not the publication.
//
// The mechanism needs two things from the filesystem, and both are stated
// here because their absence is an explicit failure rather than a silent
// weakening. Every directory of the chain must be openable for reading, so
// an ancestor that grants traversal but not reading - mode 0111, for
// example - makes the confirmation fail with a permission error instead of
// an acknowledged host id. And the filesystem must support flushing a
// directory; a filesystem that does not reports the failure explicitly.
func confirmDurable(dir string) error {
	for {
		if err := dirSync(dir); err != nil {
			return fmt.Errorf(
				"%w: confirm the durability of %s: %w",
				errDurabilityUnconfirmed, filepath.Base(dir), err,
			)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// checkProtectedFile refuses a document that is not a protected regular
// file.
//
// On Unix a record that other local users can read or write is refused, and
// a path that is not a regular file - a symlink, a device, a directory - is
// refused as well. The symlink refusal matters: a record that resolves to
// another file would let a local attacker feed this connector a document it
// did not write.
//
// The check is not a filesystem confinement mechanism: it inspects the path
// and then reopens it by name, so a directory another principal can write to
// remains a hole between those two steps. The state directory is therefore
// required to be trusted, which checkTrustedStateDir enforces.
//
// On Windows the mode bits carry no meaning, so the permission check is a
// no-op there. The protection is then the one the platform offers to a
// portable Go program: the record is created under the user's own
// configuration directory and inherits its access control list.
func checkProtectedFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("siwc: stat %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("siwc: %s is not a regular file (mode %v)", filepath.Base(path), info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("siwc: %s is group/other accessible (mode %o); expected 0600", filepath.Base(path), info.Mode().Perm())
	}
	return nil
}

// checkTrustedStateDir refuses a state directory that another principal can
// write to.
//
// The document's own owner-only mode protects its contents, but a principal
// that can write the directory can unlink the document and replace it with
// its own. The state directory is therefore a trust boundary: it must not be
// writable by principals outside the owner's own trust domain.
//
// A world-writable directory is refused, because any local user could
// replace the record there. A group-writable directory is an operator's
// deliberate choice - a shared administrative group, for example - and the
// store keeps working there, but the guarantee then holds only inside that
// group, because another member of it can replace the record.
//
// The scope of the check is bounded: only the immediate parent is checked,
// and the ancestors of the record path are a trusted precondition, because a
// principal that can write an ancestor can replace the parent directory
// itself. The directory that is checked is the resolved parent - the
// directory the operating system will really use - and it is passed in by
// the caller that resolved it.
//
// The store creates the directory it owns with owner-only permissions and
// never changes the permissions of a directory it did not create. An
// operator-supplied parent keeps the permissions the operator chose: the
// store refuses an untrusted one instead of silently chmodding it.
//
// On Windows the mode bits carry no meaning, so the check is a no-op there
// and the inherited access control list of the user's own configuration
// directory is the protection.
func checkTrustedStateDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("siwc: stat the state directory %s: %w", filepath.Base(dir), err)
	}
	if !info.IsDir() {
		return fmt.Errorf("siwc: the state directory %s is not a directory", filepath.Base(dir))
	}
	if info.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf(
			"siwc: the state directory %s is writable by other users (mode %o); the host record must live in a directory that other users cannot write to",
			filepath.Base(dir), info.Mode().Perm(),
		)
	}
	return nil
}
