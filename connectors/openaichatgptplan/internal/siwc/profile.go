package siwc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// profileRecordVersion is the version of the persisted profile record
	// this build reads and writes. A record with any other version is
	// refused instead of being interpreted with the wrong schema.
	profileRecordVersion = 1

	// selectionRecordVersion is the version of the persisted selection.
	selectionRecordVersion = 1

	// profileStoreDirName is the name of the profile store directory
	// inside the connector's state directory.
	profileStoreDirName = "profiles"

	// profileFilePrefix and profileFileSuffix bracket the file name of a
	// stored profile record: "profile-<id>.json".
	//
	// The prefix is not decoration: it keeps the store's own files
	// distinguishable from the other files an operator may keep in the
	// store directory. The host record, for example, is a file of its own,
	// and a store directory that holds it must still list its profiles.
	profileFilePrefix = "profile-"
	profileFileSuffix = ".json"

	// selectionFileName is the name of the file that holds the explicitly
	// selected profile.
	selectionFileName = "selection.json"

	// maxProfileIDLength bounds a local profile id.
	maxProfileIDLength = 64

	// profileIDPrefix is the frame of a generated profile id. The frame
	// keeps a generated id readable in a directory listing without
	// revealing anything about the account it names.
	profileIDPrefix = "acct-"
)

// ProfileRecord is the persisted state of one SIWC registration.
//
// The shape is fixed by the design of the migration specification: a
// versioned document that distinguishes the local profile id and label, the
// validated subject, the issued client id, the host id, the granted scopes,
// the access and refresh tokens, the retained ID token, the expiry and the
// reauthorization state.
//
// The SUBJECT is the authoritative identity of the registration. The email
// hint is a display convenience that the provider may or may not have
// returned; it is never a key, and the store never looks a profile up by it.
//
// The ID is the local key of the registration. It is a lowercase ASCII token
// - see validateProfileID - because it names the file that holds the record.
//
// The record carries secret material, so it is stored owner-only and never
// leaves the store in a summary, a listing or an error.
type ProfileRecord struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	Label          string    `json:"label,omitempty"`
	Subject        string    `json:"subject"`
	EmailHint      string    `json:"email_hint,omitempty"`
	IssuedClientID string    `json:"issued_client_id"`
	HostID         string    `json:"host_id"`
	GrantedScopes  []string  `json:"granted_scopes"`
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	IDToken        string    `json:"id_token,omitempty"`
	Expiry         time.Time `json:"expiry"`
	NeedsReauth    bool      `json:"needs_reauth,omitempty"`
}

// String renders the record without its secret material.
//
// A profile record holds three credentials, and formatting one is a normal
// thing to do in a log line, in a test message or a support transcript.
// The record therefore formats itself without its tokens, and it identifies
// the profile only through the local, non-secret profile id.
//
// The persisted document is unaffected: the JSON marshaler does not use this
// method.
func (r ProfileRecord) String() string {
	return fmt.Sprintf(
		"profile %s (version %d, %d scopes, expires %s, needs reauth %t)",
		r.ID,
		r.Version,
		len(r.GrantedScopes),
		r.Expiry.UTC().Format(time.RFC3339),
		r.NeedsReauth,
	)
}

// CredentialGeneration returns the one-way fingerprint of the record's
// credential pair.
//
// The fingerprint identifies a credential GENERATION without carrying a token:
// it is a SHA-256 digest of the access and refresh tokens, and the zero value
// is reported only for a record that carries no token at all. A caller that
// reads this value learns which credential pair a request was authorized with,
// never what the credential is; a cached catalog can therefore be stamped with
// the generation of the token that actually reached the wire.
func (r ProfileRecord) CredentialGeneration() ([sha256.Size]byte, bool) {
	access := strings.TrimSpace(r.AccessToken)
	refresh := strings.TrimSpace(r.RefreshToken)
	if access == "" && refresh == "" {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(access + "\x00" + refresh)), true
}

// GoString renders the record without its secret material.
//
// The Go-syntax verb uses this method rather than the stringer, so a record
// formatted with "%#v" would otherwise print its tokens. It renders the
// same content as String.
//
// Format takes precedence over this method for every verb fmt routes to a
// Formatter, so in practice the guarantee is Format's; this method remains
// for a caller that asks for the Go-syntax form directly.
func (r ProfileRecord) GoString() string {
	return r.String()
}

// AllowsPlanUsage reports whether the record's granted scopes authorize
// direct ChatGPT plan usage.
//
// The granted scopes are the PERSISTED FACT: a sign-in that the provider
// granted without the direct plan-use scope keeps its registration, and
// the registration then says that the plan usage is not authorized. The
// check is the one the inference path makes before it uses the
// registration for plan usage.
//
// The method reads the scopes rather than a separate flag, so the fact
// cannot be lost by a writer that updates one field and not the other.
func (r ProfileRecord) AllowsPlanUsage() bool {
	return PlanUsageEnabled(r.GrantedScopes)
}

// Format implements fmt.Formatter, so that no formatting verb can reach the
// record's fields.
//
// A profile record holds three credentials, and formatting one is a normal
// thing to do in a log line, in a test message or a support transcript. The
// stringer and the Go-syntax method cover the common verbs, but Go's default
// rendering for the remaining verbs - "%d", "%b", "%o", "%c", "%t", "%e",
// "%f", "%g", "%U" - falls back to printing a raw struct field, which for
// this type means printing a token.
//
// This method therefore renders the same secret-free content for every verb
// that fmt dispatches here. Width, precision and the padding flags are
// deliberately not reproduced: a diagnostic that needs those should format
// the explicit fields, not the record. The persisted document is unaffected:
// the JSON marshaler does not use this method.
//
// The guarantee holds when fmt can dispatch to this record, so it is worth
// being precise about the routes where it cannot:
//
//   - fmt handles some verbs before it consults a Formatter. "%T" prints the
//     type name and is safe. "%w" is rejected for a non-error operand and is
//     then rendered through the raw struct path, and "%p" takes that path for
//     a non-pointer operand. Both bypass every method on this record AND on
//     its fields. Do not format a record with "%w", and do not format a
//     record with "%p" except through a pointer.
//   - fmt can only dispatch to a Formatter it can reach through an
//     interface. A record held in an UNEXPORTED struct field, or supplied as
//     a non-interfaceable reflect.Value, is rendered from its raw fields
//     instead - for an ordinary "%v" as well.
//
// At an operational sink, prefer a structural summary over formatting the
// record at all.
func (r ProfileRecord) Format(state fmt.State, verb rune) {
	safe := r.String()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// ProfileSummary is the non-secret view of a saved profile.
//
// The summary is what a listing, a diagnostic or a support transcript may
// carry: it names the profile, its subject, its scopes, its expiry and its
// reauthorization state, and it holds no access token, no refresh token, no
// retained ID token and no email hint.
//
// The profile id and the label are the non-secret local key an operator
// recognizes; the subject is the provider's stable identity for the
// registration, which the store needs to confirm that a returning
// authorization belongs to the same account.
type ProfileSummary struct {
	ID            string    `json:"id"`
	Label         string    `json:"label,omitempty"`
	Subject       string    `json:"subject"`
	GrantedScopes []string  `json:"granted_scopes"`
	Expiry        time.Time `json:"expiry"`
	NeedsReauth   bool      `json:"needs_reauth"`
}

// selectionRecord is the persisted explicit selection of one profile.
//
// It holds no credential material: the selection is a local pointer to a
// profile the store already holds.
type selectionRecord struct {
	Version   int    `json:"version"`
	ProfileID string `json:"profile_id"`
}

// ProfileStore is the protected, atomic store of the saved SIWC profiles.
//
// The store owns the on-disk state of the registrations: it publishes a
// profile's record, replaces it atomically, reads it back, lists the saved
// profiles without their secret material and records the explicitly selected
// profile.
//
// # On-disk shape
//
// The store holds one file per profile, inside one directory:
//
//	<store>/profile-<id>.json   the record of one profile
//	<store>/selection.json      the explicitly selected profile
//
// The alternative - one document that holds every profile - was rejected for
// a concrete reason: the atomic replace of ONE profile's record. A refresh
// token rotation replaces exactly one profile, and the replacement must not
// be able to lose another profile's update. With one document per store, a
// writer that replaces one profile rewrites the whole document, so a second
// writer that read the document before the first writer published its
// rotation silently drops the first writer's rotated refresh token - and a
// dropped rotated refresh token cannot be recovered, because the provider
// invalidates the previous one.
//
// With one file per profile, the replacement touches one file, and the other
// profiles' records are not even read.
//
// The race is not theoretical: the connector and its login command are
// separate processes. A registration that runs while the connector refreshes
// a profile must not undo the refresh.
//
// # Protection
//
// The store is protected the same way the host record is: the directory that
// holds the records must not be writable by other users, every record file
// is owner-only and a regular file, and every write is published atomically.
//
// # Key policy
//
// A profile id is a LOWERCASE ASCII token, and the file name of a record is
// the id inside a fixed frame. That makes the mapping from an accepted id to
// a file name injective on a filesystem whose case sensitivity the store
// does not control: on a case-insensitive filesystem - Windows NTFS and macOS
// APFS are case-insensitive by default - two ids that differ only in case
// would otherwise name the same file, and the second registration would
// replace the first one's document, which holds the only copy of its refresh
// token. With a lowercase alphabet every accepted id's file name is
// already canonical, so the two can never collide.
//
// The policy is at the KEY level. A collision check before the replacement
// would not be enough: two concurrent first writers could both pass it and
// then overwrite one another.
//
// A code that creates a profile must therefore emit a lowercase id. The
// operator-facing label is where free-form text belongs.
//
// # Secrets
//
// A record carries the access token, the refresh token and the retained ID
// token. None of them leaves the store: the listing reports the profiles
// without their token material, and no error the store returns repeats a
// rejected value.
//
// Every operation resolves the configured spelling of the store's directory
// once and works on that one physical location, exactly as the host record
// store does: the protection check, the publication and the durability
// confirmation cannot disagree about which directory the records are in.
//
// A profile is looked up by its LOCAL ID only. The subject is the
// authoritative identity of a registration, but a lookup by subject would
// let the store choose a profile on the provider's behalf, and the
// selection of a profile is always explicit.
type ProfileStore struct {
	file *protectedFile
	dir  string
	// lockTimeout bounds the wait for the publication lock of a
	// registration.
	lockTimeout time.Duration
	// admissions are the in-process admissions of the registrations, one
	// per registration and per lock kind.
	//
	// The admission is shared by every writer of the registration, so
	// that ONE descriptor of the lock file is open in this store at a
	// time. It is also the stage that makes a wait CANCELLABLE: the
	// operating system's lock wait is bounded and cancellable, and the
	// admission in front of it is bounded and cancellable in the same way.
	admissionMu sync.Mutex
	admissions  map[string]*profileAdmission
}

// NewProfileStore returns the store of the SIWC profiles in the directory at
// path.
//
// The path names the store's directory, and it must be absolute. A relative
// path would be resolved against the working directory of whichever process
// started the connector, so the same configuration would address a different
// directory after a restart with a different working directory - a silent
// change of which registrations the connector can see. The store therefore
// refuses a relative path instead of resolving it against an unstable base.
//
// The path may contain symlinks and ".." components: the store resolves it
// the way the operating system does and manages the records in the resolved
// directory, it does not refuse such a spelling. A path that does not resolve
// to a single directory is refused here, before any state is created.
//
// The directory is created on demand with owner-only permissions.
func NewProfileStore(path string) (*ProfileStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("siwc: the profile store path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return nil, fmt.Errorf(
			"siwc: the profile store path %q must be absolute; a relative path would address a different directory after a restart with a different working directory",
			filepath.Base(trimmed),
		)
	}
	// Resolve the path before anything is created: an unusable path is
	// refused before any state mutation, not after one.
	dir, err := locateStateDir(trimmed)
	if err != nil {
		return nil, err
	}
	file, err := newProtectedFile(trimmed)
	if err != nil {
		return nil, err
	}
	return &ProfileStore{
		file:        file,
		dir:         dir,
		lockTimeout: defaultLockTimeout,
		admissions:  map[string]*profileAdmission{},
	}, nil
}

// Path returns the physical directory the store manages.
//
// The store resolves the configured path the way the operating system does,
// so this is a physical location - not the operator's spelling of it, and not
// the lexically cleaned spelling either.
//
// The resolution is the one made when the store was constructed, so this
// accessor is a snapshot of that moment. Every operation of the store
// resolves the configured spelling again, at the moment it runs: an operator
// who retargets a symlink in the spelling after the store was constructed
// moves the records, while this accessor still reports the location that was
// resolved at construction time. Use ConfiguredPath when the operator's
// spelling is what a diagnostic needs, and create a new store to obtain a
// fresh resolution.
func (s *ProfileStore) Path() string { return s.dir }

// ConfiguredPath returns the operator's spelling of the store path.
//
// The spelling is the configured path itself, trimmed of surrounding
// whitespace. It is not resolved: it may still name symlinks and ".."
// components, and it keeps every character the operator wrote. It is kept
// for diagnostics only.
func (s *ProfileStore) ConfiguredPath() string { return s.file.path }

// Save publishes the record of one profile, replacing the record the same
// profile held.
//
// The replacement is atomic: the record is written to a temporary file in
// the store's own directory, flushed, closed and only then renamed over the
// previous record, and the directory entry is flushed after the rename. A
// reader therefore observes either the previous record or the new one, never
// a half-written document, and a failure before the rename leaves the
// previous record in place.
//
// The rename replaces the previous record's directory entry, so a
// compromised target cannot make the store write through it.
//
// The store owns the persisted format version: a record that does not name a
// version is stamped with the current one, and a record that names a
// different version is refused instead of being rewritten.
//
// The record is validated before anything is written, and a rejected record
// produces an error that names the constraint that failed and never repeats
// the rejected value.
//
// A writer of a registration's CREDENTIALS does not use this method: it
// publishes through Publish, which applies the change to the record that is
// stored NOW. This method replaces the WHOLE record, and a writer that read
// it before another writer changed it loses that other writer's update. The
// difference matters for a rotating credential, and it is invisible at the
// file level: an atomic rename prevents a torn write, not a lost update.
func (s *ProfileStore) Save(rec ProfileRecord) error {
	if err := validateProfileRecord(rec); err != nil {
		return err
	}
	dir, err := s.resolve()
	if err != nil {
		return err
	}
	return s.saveToDir(dir, rec)
}

// SaveNew publishes the record of one registration ONLY when that
// registration does not exist yet.
//
// The publication is atomic and exclusive: the record is written to a
// temporary file in the store's own directory, flushed, closed and only
// then LINKED into place, and the link fails when the name is already
// taken. A concurrent first writer therefore either publishes its own
// record or observes the winner's complete record, and a registration that
// already exists is never replaced.
//
// That is the difference from Save, and it is what a FIRST REGISTRATION
// needs: two overlapping registrations of the same local id would
// otherwise destroy each other's record, and the destroyed record holds the
// only copy of a refresh token.
//
// A registration that already exists is REFUSED, and the existing record is
// left exactly as it was.
//
// The record is validated before anything is written, and a rejected record
// produces an error that names the constraint that failed and never repeats
// the rejected value.
func (s *ProfileStore) SaveNew(rec ProfileRecord) error {
	if err := validateProfileRecord(rec); err != nil {
		return err
	}
	stamped := rec
	stamped.Version = profileRecordVersion
	dir, err := s.resolve()
	if err != nil {
		return err
	}
	if err := s.prepare(dir); err != nil {
		return err
	}
	location := recordLocation{
		configured: s.file.path,
		rawParent:  s.file.path,
		dir:        dir,
		name:       profileFileName(stamped.ID),
	}
	created, err := s.file.publishExclusive(location, stamped)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf(
			"siwc: a registration with the id %q already exists; reauthorize it instead of registering over it",
			stamped.ID,
		)
	}
	return nil
}

// Load returns the record of one profile.
//
// A profile that is not stored is reported with the not-exist error, so that
// the caller can tell "there is no such profile" from "the stored record
// cannot be trusted". A stored record that cannot be trusted is an explicit
// failure: it is never silently replaced, because the record holds the only
// copy of a refresh token.
func (s *ProfileStore) Load(id string) (ProfileRecord, error) {
	if err := validateProfileID(id); err != nil {
		return ProfileRecord{}, fmt.Errorf("siwc: the profile id is unusable: %w", err)
	}
	dir, err := s.resolve()
	if err != nil {
		return ProfileRecord{}, err
	}
	return s.loadFromDir(dir, id)
}

// loadFromDir reads the record of one registration from a resolved
// directory.
//
// The directory is the CALLER's: the caller resolved the configured
// spelling once and hands the same physical directory to every operation of
// that call, so that a flow that reads, locks and writes a registration
// cannot disagree about which file the record is.
//
// The resolution itself is the store's: the caller resolves it with the
// store's own resolver, so the protection and the cross-check of the
// resolution are the ones every other operation applies.
func (s *ProfileStore) loadFromDir(dir, id string) (ProfileRecord, error) {
	return s.loadRecord(dir, profileFileName(id))
}

// saveToDir publishes the record of one registration into a resolved
// directory.
//
// The directory is the CALLER's, for the same reason loadFromDir's is.
func (s *ProfileStore) saveToDir(dir string, rec ProfileRecord) error {
	if err := validateProfileRecord(rec); err != nil {
		return err
	}
	stamped := rec
	stamped.Version = profileRecordVersion
	if err := s.prepare(dir); err != nil {
		return err
	}
	return s.file.replace(dir, profileFileName(stamped.ID), stamped)
}

// wholeRecordReplacement reports whether a source file replaces a
// registration's record wholesale.
//
// The escape hatch is the store's Save, and a writer of a registration's
// CREDENTIALS must not use it: Save replaces the WHOLE record, and a writer
// that read it before another writer changed it loses that other writer's
// update. The writers publish through Publish, which applies the change to
// the record that is stored now.
//
// The check is a source check, and it is deliberately narrow: it looks for
// a CALL of Save on a store, and the store's own declaration of the method
// is not a call.
func wholeRecordReplacement(content string) bool {
	// The needle is built in two parts, because the detector must not
	// match its OWN source: a guard that reports itself is a guard that
	// always fails.
	needle := "." + "Save("
	return strings.Contains(content, needle)
}

// ProfileChange is the merge of one registration's record.
//
// The change receives the record that is CURRENTLY stored and returns the
// record to publish. It runs under the registration's publication lock, so
// it must not block and must not perform I/O: it is a decision about the
// stored record and nothing else.
//
// A change that reports an error publishes NOTHING. The stored record
// stays exactly as it is, and the error is the change's own, so a caller
// can report a superseded outcome.
//
// A change that alters the profile id publishes NOTHING either, and the
// failure is the store's own: the profile id is the registration's
// identity, and a publication that moved it would write into another
// registration's file.
type ProfileChange func(current ProfileRecord) (ProfileRecord, error)

// errProfileIDChanged reports a change that would move a registration to
// another profile id.
//
// The failure is a CONSTANT and it names no id: the caller knows which
// registration it published, and the diagnostic does not repeat a value.
var errProfileIDChanged = errors.New(
	"siwc: a publication may not change the profile id of a registration",
)

// Publish applies a change to the record of one registration and publishes
// the result.
//
// # A publication is a REQUIRED COMMIT
//
// The publication records an outcome the PROVIDER has already committed: a
// rotated refresh token, a confirmed revocation, a fresh token set. The
// provider's side of that outcome cannot be taken back, and the local
// record of it is the only copy of a renewable credential. A publication is
// therefore never contingent on a caller's context, and the API does not
// accept one: a caller cannot ask for the commit to be skipped, and the
// next credential writer cannot reintroduce the defect by threading its
// context in here.
//
// The publication is BOUNDED: it waits at most the store's lock budget
// (30 s) for the registration's publication lock, and it reports a
// classified failure if the budget passes. The bound is the STORE's, and it
// is deliberately not the caller's: a caller whose own deadline has
// expired is not the reason a committed outcome may be lost.
//
// The bound is the TOTAL of one reconciliation. A publication on its own
// waits at most the store's lock budget, and a reconciliation that makes
// SEVERAL publications - a commit and its recovery, say - waits at most the
// store's lock budget IN TOTAL: the second publication has what is LEFT of
// the first one's budget, and it cannot restart the clock. A flow that
// needs that shape asks the store for a Reconciliation, and the store
// hands out one budget for it.
//
// # The caller-reporting policy
//
// The pre-provider cancellation is the CALLER's withdrawal, and it is
// reported as the caller's cancellation: the provider was not asked, and
// nothing was committed. A cancellation that arrives AFTER the provider's
// answer is NOT the caller's withdrawal: the provider has already
// committed, the obligation to record it is the connector's, and the
// publication goes ahead. The call then reports SUCCESS, because the
// obligation was discharged and the state is committed. The caller's own
// cancellation is the caller's knowledge, and the connector does not
// pretend that the caller can undo a provider's rotation.
//
// The publication is SERIALIZED with every other publication of the same
// registration, and the change is applied to the record that is CURRENTLY
// stored. A writer therefore never publishes a record it read before
// another writer changed it: the credential it holds is applied onto the
// state that is there now, and a credential that is no longer there is
// noticed instead of restored.
//
// The registration's IDENTITY is not negotiable either: a change that
// alters the profile id publishes NOTHING and is refused. The profile id
// is the registration's key - the name of its file and the key every
// other operation of the store uses - and a publication that moved it
// would write into another registration's file and leave a second record
// behind.
//
// The serialization is what makes that rule sound. An atomic rename
// prevents a TORN write; it does not prevent a LOST UPDATE, and a lost
// update of a rotating credential is what destroys a session: the writer
// that lost the update puts a token back that the provider has already
// consumed.
//
// The change is applied under the lock, and the lock is held for the
// duration of the change. A change that waits for the provider would hold
// the lock for the provider's latency, and every other publication of the
// registration would wait with it.
func (s *ProfileStore) Publish(id string, change ProfileChange) (ProfileRecord, error) {
	trimmed := strings.TrimSpace(id)
	if err := validateProfileID(trimmed); err != nil {
		return ProfileRecord{}, fmt.Errorf("siwc: the profile id is unusable: %w", err)
	}
	if change == nil {
		return ProfileRecord{}, errors.New("siwc: the publication needs a change")
	}
	dir, err := s.resolve()
	if err != nil {
		return ProfileRecord{}, err
	}
	return s.publishInDir(dir, trimmed, change)
}

// publishInDir applies a change to the record of one registration in a
// resolved directory.
//
// The directory is the CALLER's, for the same reason loadFromDir's is: a
// flow that locks one directory and publishes in another cannot decide
// which file the record is.
//
// The commit is bounded by the STORE's budget and is not the caller's: the
// reconciliation context is derived HERE, from the store's own budget, and
// not from a caller's context. That is the whole of the required-commit
// rule, and it is here - at the single chokepoint every credential writer
// goes through - rather than at the call sites.
//
// The publication is ONE reconciliation of its own: a caller that has no
// other commit to make gets the whole budget. A caller that has a commit
// AND a recovery uses a Reconciliation, and the two share one budget.
func (s *ProfileStore) publishInDir(
	dir string,
	id string,
	change ProfileChange,
) (ProfileRecord, error) {
	reconciliation := s.BeginReconciliation()
	defer reconciliation.Close()
	return reconciliation.Publish(dir, id, change)
}

// Reconciliation is ONE bounded reconciliation of the store's records.
//
// A reconciliation owns ONE budget, and every commit that belongs to it
// shares that budget. A commit and its one recovery - the quarantine that
// follows a commit that failed - are therefore bounded TOGETHER: the
// second one cannot buy a second full budget, and a caller cannot make the
// store wait twice as long by making the first commit fail.
//
// The reconciliation is the STORE's: the budget is the store's lock budget
// and the context is the store's own. A caller cannot cancel it, cannot
// extend it, and cannot ask for a second one inside it.
//
// The type is the store's own, and it is used where a flow has more than
// one commit to make under one obligation.
type Reconciliation struct {
	store  *ProfileStore
	ctx    context.Context
	cancel context.CancelFunc
}

// BeginReconciliation starts one reconciliation of the store's records.
//
// The caller closes the reconciliation when it is over, and every commit
// it makes in the meantime shares the one budget.
func (s *ProfileStore) BeginReconciliation() *Reconciliation {
	ctx, cancel := context.WithTimeout(context.Background(), s.lockTimeout)
	return &Reconciliation{store: s, ctx: ctx, cancel: cancel}
}

// Publish applies a change to the record of one registration and publishes
// the result, within the reconciliation's budget.
//
// The publication is the one of the store's chokepoint, and the budget it
// may spend is what is LEFT of the reconciliation: a commit that follows
// another one cannot restart the clock.
//
// A publication after the reconciliation is closed is refused: the budget
// is spent, and a commit that is not committed is reported as one.
func (r *Reconciliation) Publish(
	dir string,
	id string,
	change ProfileChange,
) (ProfileRecord, error) {
	return r.store.publishInDirWithin(r.ctx, r.remaining(), dir, id, change)
}

// Close ends the reconciliation and releases its budget.
//
// Closing twice is safe, and closing a reconciliation that was never
// started is safe: the reconciliation owns no resource that a second
// release could break.
func (r *Reconciliation) Close() {
	if r == nil || r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
}

// remaining returns the budget the reconciliation has left.
//
// The budget is the store's lock budget, and the reconciliation's own
// deadline is the authority for what is left of it.
func (r *Reconciliation) remaining() time.Duration {
	deadline, ok := r.ctx.Deadline()
	if !ok {
		return r.store.lockTimeout
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// publishInDirWithin applies a change to the record of one registration,
// under a context and a budget the caller owns.
//
// The context and the budget are the caller's: a reconciliation passes the
// ones it owns, and the store's own single publication passes a context
// and a budget of its own.
func (s *ProfileStore) publishInDirWithin(
	ctx context.Context,
	budget time.Duration,
	dir string,
	id string,
	change ProfileChange,
) (ProfileRecord, error) {
	if change == nil {
		return ProfileRecord{}, errors.New("siwc: the publication needs a change")
	}
	if err := s.prepare(dir); err != nil {
		return ProfileRecord{}, err
	}
	// The commit is REQUIRED: it records an outcome the provider has
	// already committed. The caller's cancellation governs the wait for
	// the provider, not this bookkeeping, and the context here is the
	// store's own bounded budget.
	release, err := s.acquireProfileLock(ctx, dir, id, publicationLockKind, budget)
	if err != nil {
		return ProfileRecord{}, err
	}
	defer release()
	current, err := s.loadFromDir(dir, id)
	if err != nil {
		return ProfileRecord{}, err
	}
	updated, err := change(current)
	if err != nil {
		// Nothing is published: the stored record stays as it is.
		return current, err
	}
	if err := checkProfileIDUnchanged(id, current.ID, updated.ID); err != nil {
		// Nothing is published: a change that would move the
		// registration to another profile id is refused, and the
		// stored record stays exactly as it is.
		return ProfileRecord{}, err
	}
	if err := s.saveToDir(dir, updated); err != nil {
		return ProfileRecord{}, err
	}
	return updated, nil
}

// checkProfileIDUnchanged refuses a change that would move a registration
// to another profile id.
//
// The publication is about ONE registration, and the profile id is the
// registration's identity: it is the name of the file the record lives in
// and the key every other operation of the store uses. A change that
// altered it would publish into ANOTHER registration's file, and the
// registration that was published would be a second record of a name that
// is already taken.
//
// The failure is a CONSTANT and it names no id: an id is the operator's
// non-secret local key, and the diagnostic does not need it - the caller
// knows which registration it published.
func checkProfileIDUnchanged(requested, current, updated string) error {
	if updated == current && updated == requested {
		return nil
	}
	return errProfileIDChanged
}

// acquireProfileLock takes the admission and the operating system's lock of
// one registration.
//
// The two stages share ONE deadline: the TOTAL wait is bounded by the
// caller's budget, and the admission in front of the operating system's
// lock is bounded and cancellable in the same way. A wait that a cancelled
// caller cannot leave is a liveness defect, and the two stages are the two
// places a caller can wait.
//
// The admission is the in-process stage, and the operating system's lock is
// the cross-process one. The admission is what keeps ONE descriptor of the
// lock file open in this store at a time, which is what makes the Windows
// byte-range lock safe: a byte-range lock there is owned by the PROCESS,
// and closing a file that carries the process's outstanding locks makes the
// operating system release them.
//
// The admission is PER STORE, and the argument above assumes ONE store per
// process: two stores over the same directory in one process keep two
// descriptors open and defeat the property. A process-wide registry is NOT
// an option here, and the assumption is a property of how the store is
// used.
//
// The wait for the operating system's lock is the wait for another
// PROCESS, and it is polled on one descriptor. The wait for the admission
// is the wait for another writer of this process, and it is a select on
// the caller's context and on the deadline.
//
// The context is the CALLER's for the ROTATION admission - a caller that
// goes away must not wait for a provider - and it is the STORE's bounded
// budget for a PUBLICATION, because a publication is a required commit.
func (s *ProfileStore) acquireProfileLock(
	ctx context.Context,
	dir string,
	id string,
	kind string,
	timeout time.Duration,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	admission := s.admissionFor(kind, id)
	if err := admission.acquire(ctx, deadline); err != nil {
		return nil, err
	}
	lock, err := acquireFileLock(
		ctx, dir, lockPath(dir, kind, id), time.Until(deadline),
	)
	if err != nil {
		admission.release()
		return nil, err
	}
	return func() {
		lock.release()
		admission.release()
	}, nil
}

// admissionFor returns the in-process admission of one registration's lock
// of one kind.
//
// The admission is shared by every writer of the registration, and it is
// created once: two writers that used two admissions would hold two
// descriptors of the same lock file, and closing one of them would make
// the operating system release the process's lock on that file.
func (s *ProfileStore) admissionFor(kind, id string) *profileAdmission {
	key := kind + ":" + id
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	admission, ok := s.admissions[key]
	if !ok {
		admission = newProfileAdmission()
		s.admissions[key] = admission
	}
	return admission
}

// List returns the non-secret summary of every saved profile.
//
// The listing holds no access token, no refresh token, no retained ID token
// and no email hint: it is the surface a diagnostic, a listing command or a
// support transcript may carry.
//
// The summaries are ordered by profile id, so two listings of the same store
// agree.
//
// A stored record that cannot be trusted is an explicit failure, not a
// silently skipped entry: a profile whose record is corrupt must not look
// like a profile that was never registered.
func (s *ProfileStore) List() ([]ProfileSummary, error) {
	dir, err := s.resolve()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A store that was never written holds no profiles. The listing
			// is empty, not absent, so that a caller renders it the same way
			// in both cases.
			return []ProfileSummary{}, nil
		}
		return nil, fmt.Errorf("siwc: read the profile store: %w", err)
	}
	summaries := make([]ProfileSummary, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		_, kind := classifyProfileFileName(name)
		switch kind {
		case profileFileForeign:
			continue
		case profileFileUnsupportedName:
			// The name carries the store's own frame but an id this build
			// does not accept. Ignoring it would present an unreadable
			// entry as if it did not exist, so it is reported instead. The
			// diagnostic names the constraint and never the name: the
			// operator can see the directory, the store does not repeat it.
			return nil, errors.New(
				"siwc: the profile store contains a record file whose profile id is not a canonical lowercase id; this build does not recognise it",
			)
		}
		rec, err := s.loadRecord(dir, name)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summaryOf(rec))
	}
	slices.SortFunc(summaries, func(left, right ProfileSummary) int {
		return strings.Compare(left.ID, right.ID)
	})
	return summaries, nil
}

// Select records the explicitly selected profile.
//
// The selection is a local pointer: it names a profile the store holds, and
// it carries no credential material. A profile that the store does not hold
// is refused, because a selection of a profile that cannot be loaded would
// point the connector at a credential it does not have.
func (s *ProfileStore) Select(id string) error {
	if err := validateProfileID(id); err != nil {
		return fmt.Errorf("siwc: the profile id is unusable: %w", err)
	}
	dir, err := s.resolve()
	if err != nil {
		return err
	}
	if _, err := s.loadRecord(dir, profileFileName(id)); err != nil {
		return err
	}
	if err := s.prepare(dir); err != nil {
		return err
	}
	return s.file.replace(dir, selectionFileName, selectionRecord{
		Version:   selectionRecordVersion,
		ProfileID: id,
	})
}

// Selected returns the explicitly selected profile id.
//
// An empty id with no error means that no profile is selected yet.
func (s *ProfileStore) Selected() (string, error) {
	dir, err := s.resolve()
	if err != nil {
		return "", err
	}
	var selection selectionRecord
	if err := s.file.loadFrom(dir, selectionFileName, &selection); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if selection.Version != selectionRecordVersion {
		return "", fmt.Errorf(
			"siwc: the selected profile has version %d, want %d",
			selection.Version,
			selectionRecordVersion,
		)
	}
	if err := validateProfileID(selection.ProfileID); err != nil {
		return "", fmt.Errorf("siwc: the selected profile id is unusable: %w", err)
	}
	return selection.ProfileID, nil
}

// loadRecord reads and validates one stored profile record.
//
// The record's own version decides whether the document may be interpreted
// with this build's schema, and the record's identity must match the file
// it was read from: a file that was copied under another name would
// otherwise change which profile a read returns.
//
// The identity check lives HERE, in the one helper every read path uses, so
// that the load, the listing and the selection cannot disagree about it. A
// selection that named a record the store refuses to load would point the
// connector at a credential it cannot read.
func (s *ProfileStore) loadRecord(dir, name string) (ProfileRecord, error) {
	var rec ProfileRecord
	if err := s.file.loadFrom(dir, name, &rec); err != nil {
		return ProfileRecord{}, err
	}
	if err := validateStoredRecord(rec); err != nil {
		return ProfileRecord{}, err
	}
	expected, ok := profileIDFromFileName(name)
	if !ok || rec.ID != expected {
		return ProfileRecord{}, fmt.Errorf(
			"siwc: the stored profile %s carries a different profile id",
			name,
		)
	}
	return rec, nil
}

// resolve returns the physical directory of this call, and confirms that the
// operating system agrees with the resolution.
//
// One call resolves the configured spelling once, and every operation of the
// call works on that one identity, so the load, the publication and the
// durability confirmation cannot disagree about which directory the records
// are in.
//
// The cross-check asks the operating system whether the configured spelling
// and the resolved directory are the same directory. The resolver implements
// the POSIX rule for a ".." after a symlink, while the Windows kernel
// resolves a ".." naively, so a disagreement is refused explicitly instead
// of silently reading or writing the records somewhere else.
//
// The cross-check needs the directory to exist, so it is made for a store
// that is already there. A store that is not there yet has nothing to
// disagree about, and the write path confirms the agreement once it has
// created the directory.
func (s *ProfileStore) resolve() (string, error) {
	dir, err := locateStateDir(s.file.path)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		if err := verifySameDirectory(s.file.path, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// prepare creates the store directory and confirms that the operating system
// resolves the configured spelling to it.
//
// The directory is created with owner-only permissions. A directory the
// store did not create keeps the permissions the operator chose: the store
// refuses an untrusted one instead of silently tightening or relaxing it.
func (s *ProfileStore) prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("siwc: create %s: %w", filepath.Base(dir), err)
	}
	return verifySameDirectory(s.file.path, dir)
}

// summaryOf returns the non-secret view of a record.
func summaryOf(rec ProfileRecord) ProfileSummary {
	return ProfileSummary{
		ID:            rec.ID,
		Label:         rec.Label,
		Subject:       rec.Subject,
		GrantedScopes: slices.Clone(rec.GrantedScopes),
		Expiry:        rec.Expiry,
		NeedsReauth:   rec.NeedsReauth,
	}
}

// profileFileName returns the file name of a profile's record.
func profileFileName(id string) string {
	return profileFilePrefix + id + profileFileSuffix
}

// profileFileKind classifies a name found in the store directory.
type profileFileKind int

const (
	// profileFileForeign is a name that is not one of the store's own files:
	// the operator's own file, or the temporary file of an interrupted write.
	profileFileForeign profileFileKind = iota
	// profileFileRecord is a record file whose profile id is canonical.
	profileFileRecord
	// profileFileUnsupportedName is a name that carries the store's record
	// frame but whose profile id is not canonical. This build never writes
	// such a name, so it is an entry the store does not recognise - and
	// recognising that it cannot read it is different from not seeing it.
	profileFileUnsupportedName
)

// classifyProfileFileName says what a name in the store directory is.
//
// The three outcomes are deliberately distinct. A foreign file is ignored: the
// operator's own files and an interrupted write's temporary file are none of
// the store's business. A name that carries the store's record frame but an
// id this build does not accept is reported, because silently skipping it
// would present an unreadable entry as if it did not exist.
func classifyProfileFileName(name string) (string, profileFileKind) {
	if !strings.HasPrefix(name, profileFilePrefix) || !strings.HasSuffix(name, profileFileSuffix) {
		return "", profileFileForeign
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, profileFilePrefix), profileFileSuffix)
	if err := validateProfileID(id); err != nil {
		return "", profileFileUnsupportedName
	}
	return id, profileFileRecord
}

// profileIDFromFileName returns the profile id a record file name stands
// for.
//
// The name is the store's own naming of a record, so the id it stands for
// is derived from the name and never from the document's content: a record
// that names another profile is refused by the caller.
func profileIDFromFileName(name string) (string, bool) {
	if !strings.HasPrefix(name, profileFilePrefix) || !strings.HasSuffix(name, profileFileSuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, profileFilePrefix), profileFileSuffix)
	if err := validateProfileID(id); err != nil {
		return "", false
	}
	return id, true
}

// validateProfileRecord refuses a record that this build cannot persist.
//
// A record that does not name a version is accepted here: the store owns
// the persisted format version and stamps it on the way in. A record that
// names a different version is refused instead of being rewritten.
//
// The diagnostics name the constraint that failed and never the rejected
// value: a record that was populated by mistake can carry content that must
// not reach a log or a support error.
func validateProfileRecord(rec ProfileRecord) error {
	if rec.Version != 0 && rec.Version != profileRecordVersion {
		return fmt.Errorf("siwc: the profile record has version %d, want %d", rec.Version, profileRecordVersion)
	}
	return validateProfileIdentity(rec)
}

// validateStoredRecord refuses a stored record that this build cannot
// interpret.
//
// The version of a stored document decides the schema it is read with, so a
// document with any other version is refused instead of being interpreted
// with the wrong schema.
func validateStoredRecord(rec ProfileRecord) error {
	if rec.Version != profileRecordVersion {
		return fmt.Errorf("siwc: the profile record has version %d, want %d", rec.Version, profileRecordVersion)
	}
	return validateProfileIdentity(rec)
}

// validateProfileIdentity refuses a record that does not identify a
// registration.
//
// The subject is the authoritative identity of the registration, and the
// issued client id and the host id are the binding a returning
// authorization is matched against.
func validateProfileIdentity(rec ProfileRecord) error {
	if err := validateProfileID(rec.ID); err != nil {
		return fmt.Errorf("siwc: the profile record carries an unusable profile id: %w", err)
	}
	if strings.TrimSpace(rec.Subject) == "" {
		return errors.New("siwc: the profile record carries no validated subject")
	}
	if strings.TrimSpace(rec.IssuedClientID) == "" {
		return errors.New("siwc: the profile record carries no issued client id")
	}
	if strings.TrimSpace(rec.HostID) == "" {
		return errors.New("siwc: the profile record carries no host id")
	}
	return nil
}

// validateProfileID refuses a profile id that cannot be a store file name.
//
// The id becomes the name of the profile's file inside the store, so it must
// not be able to name a different file: a separator, a "..", a leading dot
// and every character outside the accepted alphabet are refused.
//
// The alphabet is LOWERCASE ASCII, and that is not cosmetic. A profile id
// becomes a file name on a filesystem whose case sensitivity the store does
// not control: Windows NTFS and macOS APFS are case-insensitive by default.
// Two distinct ids that differ only in case would name the same file there,
// and the second registration would replace the first one's document - the
// only copy of its refresh token. A lowercase ASCII alphabet makes the file
// name of every accepted id canonical, so no two accepted ids can name the
// same file on any filesystem: the key policy holds at the KEY level, not
// through a collision check that two concurrent first writers could both
// pass.
//
// The alphabet is plain ASCII as well, so the Unicode normalization that
// macOS applies to file names cannot fold two distinct ids into one name
// either.
//
// The consequence for the code that creates a profile is that it must emit a
// lowercase id. The operator-facing label is where free-form text belongs.
//
// The diagnostics name the constraint that failed and never the rejected
// value.
func validateProfileID(id string) error {
	if id == "" {
		return errors.New("the profile id is empty")
	}
	if len(id) > maxProfileIDLength {
		return fmt.Errorf("the profile id is longer than %d characters", maxProfileIDLength)
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z',
			char >= '0' && char <= '9',
			char == '_', char == '-':
		default:
			return errors.New("the profile id must consist of lowercase letters, digits, '_' and '-'")
		}
	}
	return nil
}

// NewProfileID returns a fresh canonical local profile id.
//
// The id is the local key of a registration and it becomes the name of the
// file that holds the record, so it must satisfy the store's key policy: a
// lowercase ASCII token, injective on a case-insensitive filesystem.
//
// The id is opaque on purpose. The account it names is an account
// identifier, and the file that holds the registration is visible in a
// directory listing; the operator-facing label is where the human-readable
// name belongs.
//
// The id is derived from random bytes, so two registrations of the SAME
// account - which the provider allows, for example per workspace - still
// get distinct records.
func NewProfileID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("siwc: generate the profile id: %w", err)
	}
	return profileIDPrefix + hex.EncodeToString(raw), nil
}

// DefaultProfileStorePath returns the default location of the profile store.
//
// The store belongs to the installation, next to the host record: both are
// the connector's own local state, and both live in a connector-scoped
// subdirectory that no other tool writes to.
//
// The default is a DIRECTORY, one file per profile. The host record is a
// separate file above it, so importing a profile or replacing the store
// never touches the host identity.
func DefaultProfileStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("siwc: resolve the user configuration directory: %w", err)
	}
	return filepath.Join(dir, hostStateDirName, hostStateSubdir, profileStoreDirName), nil
}

// ResolveProfileStorePath returns the effective profile store path.
//
// An operator may configure the path explicitly; an empty configuration
// means the per-installation default.
//
// A configured path must be absolute, for the same reason the host record
// path must be absolute: the resolution base of a relative path would be the
// working directory of whichever process started the connector.
//
// The path names a directory. It may name it through symlinks and ".."
// components: the store resolves the path the way the operating system does
// and manages the records in the resolved directory.
func ResolveProfileStorePath(configured string) (string, error) {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		if !filepath.IsAbs(trimmed) {
			return "", fmt.Errorf(
				"siwc: the configured profile store path %q must be absolute; a relative path would address a different directory after a restart with a different working directory",
				filepath.Base(trimmed),
			)
		}
		return trimmed, nil
	}
	path, err := DefaultProfileStorePath()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf(
			"siwc: the default profile store path %q is not absolute; the user configuration directory must be an absolute path",
			filepath.Base(path),
		)
	}
	return path, nil
}
