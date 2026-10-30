// Package siwc owns the Sign in with ChatGPT (SIWC) state of the
// openai-chatgpt-plan connector.
//
// The package is connector-local on purpose. SIWC is provider
// authentication: its records, policies and lifecycle belong to the backend
// plugin, not to the host's canonical contracts and not to the generic core.
//
// This file owns the stable agent-host identity that requirement 3 of the
// migration specification defines: the opaque ext_agent_host_id that OpenAI
// uses to attribute ChatGPT plan usage to one agent host, the protected
// record that persists it, and the rule that an imported profile never
// replaces it.
//
// # Path identity
//
// The store has two notions of a path, and they are not interchangeable.
//
// The CONFIGURED path is the operator's spelling: the absolute path the
// operator wrote, with its symlinks and its ".." components, exactly as it
// was configured. ConfiguredPath reports it.
//
// The RESOLVED path is the physical location the operating system will
// use: the location the configured spelling resolves to, component by
// component, the way the kernel resolves it. Path reports it.
//
// The store RESOLVES the operator's spelling, it never REWRITES it: no
// component of a filename is dropped, translated or normalised. A directory
// whose name starts with a character whose CODE POINT, narrowed to a byte,
// aliases the separator - "Я" (U+042F) and "į" (U+012F) both have a low
// byte of 0x2f, which is "/" - keeps that character, and the record is
// published in the directory the operator named.
//
// The two accessors resolve at different times:
//
//   - ConfiguredPath reports the spelling as it was configured. It performs
//     no resolution at all, so it also reports a spelling that no longer
//     resolves to the record's location.
//
//   - Path reports the resolution that was made when the store was
//     constructed. It is a snapshot of that moment: an operator who
//     retargets a symlink in the spelling afterwards moves the record, and
//     the file system reflects the new location while this accessor still
//     reports the location that was resolved at construction time. A
//     caller that needs the current location creates a new store.
//
// The record's ACTUAL parent - the resolved directory the operating system
// will really use - is the directory that is checked for other-write, the
// directory that receives the record's temporary file, and the directory
// whose chain is confirmed before a host id is acknowledged. The ancestors
// of that directory are a trusted precondition: a principal that can write
// an ancestor can replace the directory itself.
//
// The durability confirmation walks the RESOLVED directory's chain, from the
// leaf to the filesystem root. The resolved directory holds no symlink
// component, so its lexical ancestors ARE its physical ancestors, and the
// walk therefore covers the directories the operating system will really
// use.
//
// A path that does not name a file, or one that does not resolve to a
// single directory, is refused before any state is created.
package siwc

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// hostRecordVersion is the version of the persisted host record this
	// build reads and writes. A record with any other version is refused
	// instead of being interpreted with the wrong schema.
	hostRecordVersion = 1

	// hostIDURIPrefix is the URI prefix of the UUID-based host id format.
	hostIDURIPrefix = "urn:uuid:"

	// hostStateDirName is the per-installation state directory of this
	// connector under the user configuration directory.
	hostStateDirName = "aiproxer"

	// hostStateSubdir is the connector-scoped subdirectory inside it.
	hostStateSubdir = "openai-chatgpt-plan"

	// hostRecordFileName is the name of the host record inside the state
	// directory.
	hostRecordFileName = "host.json"
)

// HostRecord is the persisted host identity record.
//
// The shape is fixed by the design: a versioned document that carries the
// opaque ext_agent_host_id. The record holds nothing else - no account id,
// no workspace name, no user identity - so the file can be inspected without
// disclosing anything about the person who signed in.
type HostRecord struct {
	Version int    `json:"version"`
	HostID  string `json:"ext_agent_host_id"`
}

// HostStore is the protected, atomic store of the host identity record.
//
// The store owns the only write path to the host record: EnsureHostID.
// Everything that must not change this host's identity - a profile import,
// for example - depends on the read-only HostIDReader view instead, so the
// rule is enforced by the types and not by convention.
type HostStore struct {
	file *protectedFile
	loc  recordLocation
}

// HostIDReader is the read-only view of this host's identity.
//
// The profile-import path depends on this interface instead of on the
// concrete store, so that importing a profile can neither create nor
// replace the host record.
type HostIDReader interface {
	// HostID returns this host's stable agent-host identity.
	HostID() (string, error)
}

// ReconcileProfileHost returns the host id a profile must carry on this
// host, and reports whether the recorded value belonged to another host.
//
// The rule is ONE rule in ONE place, and it is a rule about PROVENANCE
// rather than about values. A profile that was created on another host
// carries that host's identity, and that value describes the SOURCE host.
// The identity that the flow acts under is the identity of the host that
// runs the flow, and the host store is the authority for it. The recorded
// value is therefore never adopted: it is only REPORTED, so that the
// reconciliation is observable instead of silent.
//
// An empty recorded value is not a foreign host: a registration that was
// written before this host had an identity is simply bound to this host's
// identity, and there is nothing to reconcile.
func ReconcileProfileHost(hostID, recordedHostID string) (string, bool) {
	recorded := strings.TrimSpace(recordedHostID)
	if recorded == "" || recorded == hostID {
		return hostID, false
	}
	return hostID, true
}

// BindImportedProfile returns the host id a profile imported onto this host
// must be bound to, and reports whether the imported value belonged to
// another host.
//
// Requirement 3.5: importing a profile onto another host must not overwrite
// the destination host's own host id. An imported record carries the host id
// of the machine it was created on; that value describes the source host, so
// it is never adopted. The destination host binds the imported profile to
// its own id instead.
//
// The reader is the read-only view of the identity, so this function cannot
// create or replace the host record: the record is written exactly once,
// by the first start, and never replaced afterwards.
func BindImportedProfile(reader HostIDReader, importedHostID string) (string, bool, error) {
	if reader == nil {
		return "", false, errors.New("siwc: the host identity reader is nil")
	}
	hostID, err := reader.HostID()
	if err != nil {
		return "", false, err
	}
	resolved, rebound := ReconcileProfileHost(hostID, importedHostID)
	return resolved, rebound, nil
}

// NewHostStore returns the store of the host record at path.
//
// The path names the record file itself, not a directory, and it must be
// absolute. A relative path would be resolved against the working directory
// of whichever process started the connector, so the same configuration
// would address a different file after a restart with a different working
// directory - a silent change of the host identity. The store therefore
// refuses a relative path instead of resolving it against an unstable base.
//
// The path may contain symlinks and ".." components: the store resolves it
// the way the operating system does and manages the record at the resolved
// location, it does not refuse such a spelling. A path that does not name a
// file, or one that does not resolve to a single directory, is refused
// here, before any state is created.
//
// The parent directory is created on demand with owner-only permissions.
//
// The state-directory policy is the one the store documents: the directory
// that holds the record must not be writable by other users. A
// group-writable directory is accepted as an operator's deliberate
// trust-domain choice, and the guarantee then holds only inside that group.
// Only the immediate parent is checked, and that parent is the resolved one
// - the directory the operating system will really use - so the ancestors
// of the resolved record path are a trusted precondition: a principal that
// can write an ancestor can replace the directory itself.
func NewHostStore(path string) (*HostStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("siwc: the host record path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return nil, fmt.Errorf(
			"siwc: the host record path %q must be absolute; a relative path would address a different file after a restart with a different working directory",
			filepath.Base(trimmed),
		)
	}
	// Resolve the path before anything is created: an unusable path is
	// refused before any state mutation, not after one.
	loc, err := locate(trimmed)
	if err != nil {
		return nil, err
	}
	file, err := newProtectedFile(trimmed)
	if err != nil {
		return nil, err
	}
	return &HostStore{file: file, loc: loc}, nil
}

// Path returns the physical path of the host record the store manages.
//
// The store resolves the configured path the way the operating system does,
// so this is a physical location - not the operator's spelling of it, and not
// the lexically cleaned spelling either.
//
// The resolution is the one made when the store was constructed, so this
// accessor is a snapshot of that moment. Every operation of the store
// resolves the configured spelling again, at the moment it runs: if an
// operator retargets a symlink in the spelling after the store was
// constructed, the record follows the new location while this accessor
// still reports the location that was resolved at construction time. Use
// ConfiguredPath when the operator's spelling is what a diagnostic needs,
// and create a new store to obtain a fresh resolution.
func (s *HostStore) Path() string { return s.loc.path() }

// ConfiguredPath returns the operator's spelling of the host record path.
//
// The spelling is the configured path itself, trimmed of surrounding
// whitespace. It is not resolved: it may still name symlinks and ".."
// components, and it keeps every character the operator wrote. It is kept
// for diagnostics only: the record itself lives at the location an
// operation resolves when it runs, which Path reports only as of the
// store's construction.
func (s *HostStore) ConfiguredPath() string { return s.loc.configured }

// EnsureHostID returns this host's stable agent-host identity, creating the
// record on the first start.
//
// The record is created exactly once. The publication is exclusive, so two
// concurrent first starts resolve one host id instead of two, and a restart
// reuses the stored value without rewriting it.
//
// Every path that returns a host id confirms that the record's whole
// directory chain is durable before it acknowledges the id. The record may
// have been published by another connector process whose own confirmation
// has not completed yet, or has failed, and a caller must not depend on that
// other publisher: the chain confirmation flushes the leaf that holds the
// record and every ancestor that contains the next one, which covers the
// entries of directories that were created moments earlier. The chain is
// the resolved record location's own ancestors, so the confirmation holds
// between independent processes and not only between goroutines of one
// process. A failed confirmation is reported instead of an acknowledged host
// id.
//
// This is the only writer of the host record. It runs before the first SIWC
// authorization, because the host id is part of the registration request.
func (s *HostStore) EnsureHostID() (string, error) {
	// One call resolves the configured path once, and every operation of
	// this call works on that one identity, so the load, the publication
	// and the confirmation cannot disagree about which file the record is.
	loc, err := locate(s.file.path)
	if err != nil {
		return "", err
	}
	rec, err := s.loadRecord(loc)
	switch {
	case err == nil:
		// The record exists. Its durability is not yet established by this
		// process, so confirm it here before acknowledging the host id.
		if err := confirmDurable(loc.dir); err != nil {
			return "", err
		}
		return rec.HostID, nil
	case !errors.Is(err, fs.ErrNotExist):
		// A record that exists but cannot be trusted is an explicit failure.
		// Replacing it would silently change the identity of this host.
		return "", err
	}

	hostID, err := newHostID()
	if err != nil {
		return "", err
	}
	created, err := s.file.publishExclusive(loc, HostRecord{Version: hostRecordVersion, HostID: hostID})
	if err != nil {
		return "", err
	}
	if created {
		// The publication flushed the directory entry itself.
		return hostID, nil
	}
	// Another first start published the record first. Its durability
	// confirmation is not ours to rely on: confirm it here.
	rec, err = s.loadRecord(loc)
	if err != nil {
		return "", err
	}
	if err := confirmDurable(loc.dir); err != nil {
		return "", err
	}
	return rec.HostID, nil
}

// HostID returns this host's stable agent-host identity.
//
// The lookup never creates the record: a read-only caller must not be able
// to change the host identity, and the profile-import path depends on that
// separation.
func (s *HostStore) HostID() (string, error) {
	loc, err := locate(s.file.path)
	if err != nil {
		return "", err
	}
	rec, err := s.loadRecord(loc)
	if err != nil {
		return "", err
	}
	return rec.HostID, nil
}

// loadRecord reads and validates the persisted host record at the resolved
// location.
func (s *HostStore) loadRecord(loc recordLocation) (HostRecord, error) {
	var rec HostRecord
	if err := s.file.load(loc, &rec); err != nil {
		return HostRecord{}, err
	}
	if rec.Version != hostRecordVersion {
		return HostRecord{}, fmt.Errorf("siwc: the host record has version %d, want %d", rec.Version, hostRecordVersion)
	}
	if err := validateHostID(rec.HostID); err != nil {
		return HostRecord{}, fmt.Errorf("siwc: the host record carries an unusable host id: %w", err)
	}
	return rec, nil
}

// DefaultHostRecordPath returns the default location of the host record.
//
// The host record belongs to the installation, not to a profile store. An
// operator may point the profile store at another directory, or replace it
// with one imported from another host, and the host identity of this
// installation must not change with it. The default therefore lives in the
// per-user configuration directory, in a connector-scoped subdirectory that
// no other tool writes to.
//
// The file is named host.json. The record holds an identifier rather than a
// credential - OpenAI states that a host id is not an authentication
// credential - but it is protected like the rest of the connector's local
// state, because the same directory will hold the SIWC profile store.
func DefaultHostRecordPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("siwc: resolve the user configuration directory: %w", err)
	}
	return filepath.Join(dir, hostStateDirName, hostStateSubdir, hostRecordFileName), nil
}

// ResolveHostRecordPath returns the effective host record path.
//
// An operator may configure the path explicitly; an empty configuration
// means the per-installation default.
//
// A configured path must be absolute. The resolution base of a relative path
// would be the working directory of whichever process started the
// connector, so the same configuration would address a different file after
// a restart with a different working directory - a silent change of the host
// identity. Resolving a relative path against the current working directory
// would not fix that, so the path is refused instead.
//
// A configured path must also name a file. The host record is a file, and a
// path whose last component is empty, "." or ".." does not name one, so it
// is refused here instead of later by the store.
//
// The path may name the record through symlinks and ".." components: the
// store resolves it the way the operating system does and manages the
// record at the resolved location.
//
// The function deliberately takes no profile store path: the host identity is
// a property of the installation and must not follow the profile store
// around.
func ResolveHostRecordPath(configured string) (string, error) {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		if !filepath.IsAbs(trimmed) {
			return "", fmt.Errorf(
				"siwc: the configured host record path %q must be absolute; a relative path would address a different file after a restart with a different working directory",
				filepath.Base(trimmed),
			)
		}
		if _, _, err := splitRecordPath(trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	path, err := DefaultHostRecordPath()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf(
			"siwc: the default host record path %q is not absolute; the user configuration directory must be an absolute path",
			filepath.Base(path),
		)
	}
	return path, nil
}

// newHostID returns a fresh opaque host id.
//
// Format decision (requirement 3.3). OpenAI documents three accepted
// ext_agent_host_id formats: an RFC 9278 JWK-thumbprint URI
// (urn:ietf:params:oauth:jwk-thumbprint:...), a urn:uuid: URI, and a did:key
// URI. Its documentation calls the JWK thumbprint the recommended format, and
// it states that the recommendation is optional, that "UUID-based host IDs
// remain supported", and that a public-key-derived host id "is currently an
// identifier only" because OpenAI does not verify possession of the private
// key as part of the flow. Its reference credential record carries a
// urn:uuid: value.
//
// This connector therefore uses the urn:uuid: form, for a concrete reason
// rather than a stronger claim: the SIWC flow has no host key. The connector
// authenticates as a public client with PKCE and never signs anything with a
// host key, so a thumbprint would mean generating a key pair whose private
// half the protocol never uses and persisting that unused key material on
// disk - a key lifecycle the flow would not exercise and OpenAI would not
// verify. The UUID keeps the host record one small protected document with
// no key material to protect, rotate or back up.
//
// The value is a RFC 9562 version 4 UUID in its hyphenated form behind the
// "urn:uuid:" prefix: 122 random bits that encode no email, no account id,
// no workspace name and no host or path information.
func newHostID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("siwc: generate the host id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 9562 variant
	return hostIDURIPrefix + formatUUID(raw), nil
}

// formatUUID renders the bytes as the hyphenated UUID form.
func formatUUID(raw [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// validateHostID refuses a host id that is not the opaque form this
// connector persists.
//
// The accepted shape is the UUID-based URI that OpenAI documents as a
// supported ext_agent_host_id format: the "urn:uuid:" prefix followed by the
// hyphenated RFC 9562 form of a version 4 UUID. The check is deliberately
// strict. A record that carries anything else - an email, an account id, a
// workspace name, a thumbprint of a format this connector does not
// implement - is refused instead of being forwarded to the provider.
//
// The diagnostics name the constraint that failed and never the rejected
// value: a record that was populated by mistake can carry content that must
// not reach a log or a support error.
func validateHostID(hostID string) error {
	if !strings.HasPrefix(hostID, hostIDURIPrefix) {
		return errors.New("the host id is not a urn:uuid: URI")
	}
	raw := strings.TrimPrefix(hostID, hostIDURIPrefix)
	if len(raw) != 36 {
		return errors.New("the host id is not a 36 character UUID")
	}
	offset := 0
	for index, size := range []int{8, 4, 4, 4, 12} {
		if index > 0 {
			if offset >= len(raw) || raw[offset] != '-' {
				return errors.New("the host id is not a hyphenated UUID")
			}
			offset++
		}
		if offset+size > len(raw) {
			return errors.New("the host id is not a hyphenated UUID")
		}
		for index := range size {
			if !isLowerHex(raw[offset+index]) {
				return errors.New("the host id is not a lower case hexadecimal UUID")
			}
		}
		offset += size
	}
	if offset != len(raw) {
		return errors.New("the host id is not a hyphenated UUID")
	}
	if raw[14] != '4' {
		return errors.New("the host id is not a version 4 UUID")
	}
	switch raw[19] {
	case '8', '9', 'a', 'b':
	default:
		return errors.New("the host id does not use the RFC 9562 variant")
	}
	return nil
}

// isLowerHex reports whether the byte is a lower case hexadecimal digit.
func isLowerHex(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}
