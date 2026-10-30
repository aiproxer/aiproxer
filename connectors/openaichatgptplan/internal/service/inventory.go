package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// The account-scoped model inventory.
//
// The inventory is the list-visible catalog of the EXPLICITLY SELECTED SIWC
// registration, read from the documented public /v1/models endpoint. The
// authority for the selection and for the credential is the local
// registration store: the inventory holds no credential of its own, and it
// never carries one from another registration.
//
// # The cache is keyed by the registration it describes
//
// A catalog belongs to one account. The cache therefore stores an entry
// under the local, non-secret id of the selected registration, and every
// entry carries the IDENTITY of the registration it was read from: the
// subject, the issued client id, the host id, the granted scopes, the
// reauthorization state and a fingerprint of the credential generation.
//
// The identity is checked EVERY TIME the cache is consulted, against the
// registration that is stored NOW. An entry whose identity does not match is
// DROPPED, not merely bypassed: a bypassed entry would be waiting under the
// same key for a later state that happened to match again, and a logout
// followed by a reauthorization of the same account would then serve the
// catalog of the session that was signed out. The drop is the invalidation
// policy in one place, and it covers a registration that changed identity, a
// registration whose credential was cleared or replaced, and a registration
// that was removed from the store.
//
// The credential FINGERPRINT is deliberately part of the identity. A token
// rotation and a reauthorization both produce a new credential generation,
// and the cache cannot tell a harmless rotation from the replacement of a
// session it must not serve. Dropping on either is the conservative half of
// the choice; the alternative - assuming that the account is unchanged
// because the subject is - is exactly the assumption that would keep a
// catalog across a logout.
//
// The POST-READ check is about attribution and not about the cache, so it
// compares the registration WITHOUT the credential generation: the client
// takes the credential from the authority at the moment it reads, and a
// renewal during the read is the reading of the current credential, not a
// change of whose catalog the answer is.
//
// The cache entry is nevertheless stamped with the credential generation the
// request ACTUALLY PRESENTED, carried back through the catalog seam from the
// token acquisition. A generation that is merely observed after the answer
// arrived is never used as a stamp: a session replacement can land while the
// old-session request is in flight, and the old catalog must not then be
// published as the replacement session's catalog. When the acquired
// generation no longer matches the stored registration at publication time,
// the answer is returned as the live answer of the call that dispatched it,
// but no entry is published for the replacement.
//
// # The cache is bounded and explicitly marked
//
// The cache holds at most maxProfileInventories entries and serves an entry
// for at most defaultInventoryTTL. A cache hit is reported with
// inventorySourceCache instead of the live source, so an operator or a
// diagnostic can tell a catalog read now from a catalog read earlier. An
// entry is never a FALLBACK for a failed read: a failed read is an explicit
// failure even when a matching entry exists, because a provider-side change
// would otherwise be hidden behind an old catalog that still looks fetched.
//
// # The authority is read, not copied
//
// The selected id, the registration record and the access token are all read
// from the registration authority at the moment they are used. The inventory
// keeps no selection of its own, and it never takes the first of several
// registrations: a nil or empty selection is a failure, never a default.

const (
	// inventorySourceLive marks a catalog read from the provider for this
	// call.
	inventorySourceLive = FactoryKind
	// inventorySourceCache marks a catalog served from the profile-local
	// cache. The suffix is part of the observable provenance, not a
	// diagnostic flourish: requirement 7.6 asks for a bounded last-known
	// cache to be explicitly marked.
	inventorySourceCache = FactoryKind + "/profile-cache"
	// defaultInventoryTTL bounds how long one cached catalog may be served.
	defaultInventoryTTL = 5 * time.Minute
	// maxProfileInventories bounds the number of cached catalogs. The
	// entries are per registration, so the bound also bounds the memory a
	// store with many saved profiles can cost the connector.
	maxProfileInventories = 8
)

// The failures of the account-scoped inventory.
//
// ErrInventoryUnavailable is the connector-local sentinel every inventory
// failure wraps. The host turns a ListModels failure into an unavailable
// discovery for this backend and keeps the other backends' inventories, so
// the sentinel is operational rather than fatal. It is never a successful
// empty answer: the host reads a zero-model success as a catalog that was
// fetched and found no models.
var (
	// ErrInventoryUnavailable reports that the account-scoped model
	// inventory could not be produced.
	ErrInventoryUnavailable = errors.New(
		"openai-chatgpt-plan: the account-scoped model inventory is unavailable",
	)

	// errInventoryNoStore reports an instance that was configured without
	// a registration store. There is no account whose catalog could be
	// read, and no credential that could read it.
	errInventoryNoStore = errors.New(
		"the model inventory needs a configured profile store",
	)

	// errInventoryNoSelection reports a store that holds no selected
	// registration.
	errInventoryNoSelection = errors.New(
		"the model inventory needs a selected registration",
	)

	// errInventoryRegistrationUnusable reports a selected registration
	// that cannot supply an inventory: it needs a new authorization, its
	// granted scopes do not authorize direct plan usage, or it carries no
	// credential at all.
	errInventoryRegistrationUnusable = errors.New(
		"the selected registration cannot supply a model inventory",
	)

	// errInventorySelectionChanged reports that the registration the read
	// was about is no longer the selected one, or no longer has the
	// identity the read started with. The answer is refused rather than
	// attributed to the registration that is selected now.
	errInventorySelectionChanged = errors.New(
		"the selected registration changed during the model inventory read",
	)

	// errInventoryCatalogMismatch reports a client answer that names
	// another registration than the one the read was about.
	errInventoryCatalogMismatch = errors.New(
		"the model inventory does not describe the selected registration",
	)
)

// registrationAuthority is the persisted registration state the inventory
// reads.
//
// Selected is asked every time, including after a provider read: the
// inventory must never assume that the selection it observed before the read
// is still the selection when the answer arrives.
type registrationAuthority interface {
	// Selected returns the id of the explicitly selected registration. An
	// empty id with no error means that no registration is selected.
	Selected(ctx context.Context) (string, error)
	// Load returns the record of the named registration. A registration
	// that is not stored is reported with the not-exist error.
	Load(profileID string) (siwc.ProfileRecord, error)
}

// modelCatalog reads the public model inventory of one named registration.
type modelCatalog interface {
	// ListFor reads the list-visible models of the named registration.
	// The returned inventory names the registration it describes.
	ListFor(ctx context.Context, profileID string) (siwc.ModelInventory, error)
}

// inventoryConfig is the construction of one profile-aware inventory.
type inventoryConfig struct {
	// Authority is the persisted registration state. It is required.
	Authority registrationAuthority
	// Catalog reads the public inventory. It is required.
	Catalog modelCatalog
	// Now is the clock the cache age is read with. Empty means time.Now.
	Now func() time.Time
	// TTL bounds how long one entry is served. Zero means
	// defaultInventoryTTL.
	TTL time.Duration
}

// profileInventory is the profile-aware cache of the model inventory.
//
// The inventory is safe for concurrent use. The mutex protects the cache
// map, and it is not held across a provider read: two concurrent reads may
// fetch the same catalog, and both write a complete entry, which is harmless
// because an entry is only served when its identity still matches.
type profileInventory struct {
	authority registrationAuthority
	catalog   modelCatalog
	now       func() time.Time
	ttl       time.Duration

	mu      sync.Mutex
	entries map[string]inventoryEntry
}

// inventoryEntry is one cached catalog and the registration it describes.
type inventoryEntry struct {
	identity  registrationIdentity
	models    []siwc.Model
	fetchedAt time.Time
}

// registrationIdentity is the registration a cached catalog belongs to.
//
// The identity is the comparison the cache makes on every consultation. It
// carries no token: the credential is represented by a one-way fingerprint,
// so a cache entry cannot become a second copy of a credential.
type registrationIdentity struct {
	profileID      string
	subject        string
	issuedClientID string
	hostID         string
	scopes         string
	needsReauth    bool
	hasCredentials bool
	credentials    [sha256.Size]byte
}

// sameRegistration reports whether two identities describe the same
// registration, ignoring the credential generation.
//
// The credential generation is reconciled separately, against the generation
// the request actually presented. It is NOT part of the attribution of a read:
// the inventory client takes the credential from the authority at the moment
// it reads, so a renewal that happens during the read is the reading of the
// CURRENT credential, not a change of whose catalog the answer is. Comparing
// the whole identity after the read would refuse a perfectly good answer
// merely because the access token was renewed just before it was used.
//
// A change that is NOT a renewal - another subject, host, issued client id,
// grant or reauthorization state - is still refused, and so is a selection
// that moved.
func sameRegistration(left, right registrationIdentity) bool {
	left.hasCredentials = false
	left.credentials = [sha256.Size]byte{}
	right.hasCredentials = false
	right.credentials = [sha256.Size]byte{}
	return left == right
}

// registrationIdentityOf reads the identity of a stored registration.
func registrationIdentityOf(record siwc.ProfileRecord) registrationIdentity {
	fingerprint, hasCredentials := record.CredentialGeneration()
	scopes := make([]string, 0, len(record.GrantedScopes))
	for _, scope := range record.GrantedScopes {
		if trimmed := strings.TrimSpace(scope); trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	slices.Sort(scopes)
	return registrationIdentity{
		profileID:      record.ID,
		subject:        record.Subject,
		issuedClientID: record.IssuedClientID,
		hostID:         record.HostID,
		scopes:         strings.Join(scopes, "\x00"),
		needsReauth:    record.NeedsReauth,
		hasCredentials: hasCredentials,
		credentials:    fingerprint,
	}
}

// inventoryUnusableReason reports why a selected registration cannot supply
// an inventory. A nil result means the registration may be used.
//
// The checks are about the registration's stated state, not about a copy of
// its credential: the credential itself is read and renewed by the
// registration authority when the catalog is actually read.
func inventoryUnusableReason(record siwc.ProfileRecord) error {
	switch {
	case record.NeedsReauth:
		return fmt.Errorf(
			"%w: the registration needs a new authorization", errInventoryRegistrationUnusable,
		)
	case !record.AllowsPlanUsage():
		return fmt.Errorf(
			"%w: the granted scopes do not authorize direct ChatGPT plan usage",
			errInventoryRegistrationUnusable,
		)
	case strings.TrimSpace(record.AccessToken) == "" && strings.TrimSpace(record.RefreshToken) == "":
		return fmt.Errorf(
			"%w: the registration carries no credential", errInventoryRegistrationUnusable,
		)
	}
	return nil
}

// newProfileInventory returns the profile-aware inventory of one instance.
func newProfileInventory(config inventoryConfig) (*profileInventory, error) {
	if config.Authority == nil {
		return nil, errors.New("openai-chatgpt-plan: the model inventory needs the registration authority")
	}
	if config.Catalog == nil {
		return nil, errors.New("openai-chatgpt-plan: the model inventory needs the public catalog client")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.TTL <= 0 {
		config.TTL = defaultInventoryTTL
	}
	return &profileInventory{
		authority: config.Authority,
		catalog:   config.Catalog,
		now:       config.Now,
		ttl:       config.TTL,
		entries:   map[string]inventoryEntry{},
	}, nil
}

// newProfileAuthority resolves the configured registration store into the
// authority that both the inventory and the execution path read.
//
// The authority is the single owner of the selection and of the credential:
// the inventory names the selected registration's catalog, and an attempt is
// authorized by the same selection, so neither can act on a registration the
// other would not accept. A configuration without a store builds no
// authority: the inventory then reports an explicit unavailable inventory and
// execution refuses the attempt instead of reading a store the operator did
// not configure.
func newProfileAuthority(cfg Config) (*selectedProfiles, error) {
	if strings.TrimSpace(cfg.ProfileStorePath) == "" {
		return nil, nil
	}
	store, err := siwc.NewProfileStore(cfg.ProfileStorePath)
	if err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: open the profile store: %w", err)
	}
	refresher, err := siwc.NewRefresher(siwc.RefresherConfig{Profiles: store})
	if err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: build the credential refresher: %w", err)
	}
	manager, err := siwc.NewManager(siwc.ManagerConfig{
		Profiles:  store,
		Refresher: refresher,
	})
	if err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: build the profile manager: %w", err)
	}
	return &selectedProfiles{
		store:   store,
		manager: manager,
		pinned:  cfg.ProfileID,
	}, nil
}

// newInventoryFromConfig builds the inventory of a configured instance.
//
// The authority is the selected registration state; without one there is no
// account whose catalog could be read, and the instance reports an explicit
// unavailable inventory.
func newInventoryFromConfig(authority *selectedProfiles) (*profileInventory, error) {
	if authority == nil {
		return nil, nil
	}
	client, err := siwc.NewModelsClient(siwc.ModelsClientConfig{Tokens: authority})
	if err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: build the model inventory client: %w", err)
	}
	return newProfileInventory(inventoryConfig{
		Authority: authority,
		Catalog:   client,
	})
}

// ListModels returns the account-scoped model inventory of the selected SIWC
// registration.
//
// The catalog is read through the public /v1/models client with the selected
// registration's credential, and the cache that serves a repeated call is
// keyed by the registration it describes. A missing store, a missing
// selection, an unusable registration, a failed read and a selection that
// moved while the read was in flight are all EXPLICIT failures through the
// error channel. The method never answers with an empty catalog it did not
// read: the host reads a zero-model success as a catalog that was fetched and
// found no models.
//
// The failure is marked operational as a connector-local contract, so the
// connector's own callers can tell a temporary unavailability from a hard
// failure. The marker does not survive the gRPC boundary, where the SDK
// returns the failure as a plain RPC error, so the host-visible outcome does
// not depend on it: the host's inventory aggregation fails soft on any load
// failure for this backend and reports an unavailable discovery for it while
// keeping the other backends' inventories.
func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if i == nil || i.inventory == nil {
		return backendplugin.ListModelsResponse{}, inventoryOperationalError(errInventoryNoStore)
	}
	response, err := i.inventory.list(ctx, limit)
	if err != nil {
		return backendplugin.ListModelsResponse{}, inventoryOperationalError(err)
	}
	return response, nil
}

// selectedProfiles is the registration authority of one configured instance.
//
// The configured profile id, when present, is an EXPLICIT selection: it names
// one saved registration, and the inventory reads that registration only.
// Without it, the store's own selection is the authority. The adapter never
// falls back from one to the other, and it never chooses among several saved
// registrations.
type selectedProfiles struct {
	store   *siwc.ProfileStore
	manager *siwc.Manager
	pinned  string
}

// Selected returns the explicitly selected registration.
func (p *selectedProfiles) Selected(ctx context.Context) (string, error) {
	if pinned := strings.TrimSpace(p.pinned); pinned != "" {
		return pinned, nil
	}
	return p.manager.Selected(ctx)
}

// Load returns the stored record of the named registration.
func (p *selectedProfiles) Load(profileID string) (siwc.ProfileRecord, error) {
	return p.store.Load(profileID)
}

// Token returns a usable access token of the named registration, with the
// one-way generation fingerprint of the credential pair it was acquired from.
//
// The method makes the adapter the client's SelectedTokenProvider: the
// catalog client asks for the token of the registration the inventory named,
// and the manager renews it when it is near its expiry. The generation is
// taken from the same record the manager returned the token from, so it names
// the credential the request presents and not a later replacement.
func (p *selectedProfiles) Token(ctx context.Context, profileID string) (siwc.TokenGrant, error) {
	return p.manager.Token(ctx, profileID)
}

// list returns the inventory of the selected registration.
//
// The order is the authority's: the selected id is read first, the
// registration it names is read next, and the cache is consulted only after
// both. A read that started for one registration is checked AGAIN after the
// provider answered, so an answer that arrives after the operator switched
// profiles is refused instead of being attributed to the new selection.
func (p *profileInventory) list(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if ctx == nil {
		return backendplugin.ListModelsResponse{}, errors.New(
			"openai-chatgpt-plan: the model inventory needs a context",
		)
	}
	if err := ctx.Err(); err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	profileID, err := p.authority.Selected(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf(
			"openai-chatgpt-plan: read the selected registration: %w", err,
		)
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return backendplugin.ListModelsResponse{}, errInventoryNoSelection
	}
	record, err := p.authority.Load(profileID)
	if err != nil {
		// A registration that cannot be loaded is not a registration with
		// an old catalog: the entry that described it is dropped, and the
		// failure is explicit.
		p.forget(profileID)
		if errors.Is(err, fs.ErrNotExist) {
			return backendplugin.ListModelsResponse{}, fmt.Errorf(
				"openai-chatgpt-plan: the selected registration %q is not stored: %w",
				profileID, err,
			)
		}
		return backendplugin.ListModelsResponse{}, fmt.Errorf(
			"openai-chatgpt-plan: load the selected registration %q: %w", profileID, err,
		)
	}
	if err := inventoryUnusableReason(record); err != nil {
		// An unusable registration that ARRIVED is not an absent one, and
		// it is not a reason to serve the catalog of the session that is
		// gone. The entry is dropped and the failure is explicit.
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, err
	}
	identity := registrationIdentityOf(record)
	if entry, ok := p.cached(profileID, identity, p.now()); ok {
		return p.response(entry, inventorySourceCache, limit), nil
	}
	inventory, err := p.catalog.ListFor(ctx, profileID)
	if err != nil {
		// A failed read is never answered from an entry: a last-known
		// catalog that substituted itself for the failure would hide a
		// provider-side change behind a successful answer.
		return backendplugin.ListModelsResponse{}, err
	}
	if inventory.ProfileID != profileID {
		// The catalog owner comes from the registration the read was about,
		// not from the answer's claim about itself.
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, errInventoryCatalogMismatch
	}
	// The selection and the registration are checked AGAIN after the
	// provider answered. The authority may have moved while the read was in
	// flight, and an answer that describes the previous registration must
	// not be returned as the new selection's catalog.
	selectedAfter, err := p.authority.Selected(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf(
			"openai-chatgpt-plan: read the selected registration after the inventory read: %w", err,
		)
	}
	if strings.TrimSpace(selectedAfter) != profileID {
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, errInventorySelectionChanged
	}
	recordAfter, err := p.authority.Load(profileID)
	if err != nil {
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, fmt.Errorf(
			"openai-chatgpt-plan: load the selected registration after the inventory read: %w", err,
		)
	}
	if err := inventoryUnusableReason(recordAfter); err != nil {
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, err
	}
	identityAfter := registrationIdentityOf(recordAfter)
	if !sameRegistration(identity, identityAfter) {
		p.forget(profileID)
		return backendplugin.ListModelsResponse{}, errInventorySelectionChanged
	}
	// The entry is stamped with the credential generation the request
	// PRESENTED - carried back from the token acquisition - not with the
	// generation the store holds after the answer arrived. The two are equal
	// on the ordinary path and on a renewal that happened before the fetch.
	// When a replacement landed while the request was in flight they are
	// not, and the old session's catalog must never be published under the
	// replacement session: the next call must fetch with the replacement.
	entry := inventoryEntry{
		identity:  identityAfter,
		models:    inventory.Models,
		fetchedAt: p.now(),
	}
	entry.identity.credentials = inventory.CredentialGeneration
	entry.identity.hasCredentials = true
	if !identityAfter.hasCredentials ||
		identityAfter.credentials != inventory.CredentialGeneration {
		p.forget(profileID)
		return p.response(entry, inventorySourceLive, limit), nil
	}
	p.remember(profileID, entry)
	return p.response(entry, inventorySourceLive, limit), nil
}

// cached returns the entry of the named registration when it still describes
// the registration that is stored now and is still inside its lifetime.
//
// An entry that no longer matches is DELETED. That is the whole invalidation
// policy: the identity check is the invalidation, and a mismatched entry is
// never left under the key for a later state to match again.
func (p *profileInventory) cached(
	profileID string,
	identity registrationIdentity,
	now time.Time,
) (inventoryEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[profileID]
	if !ok {
		return inventoryEntry{}, false
	}
	if entry.identity != identity {
		delete(p.entries, profileID)
		return inventoryEntry{}, false
	}
	if !now.Before(entry.fetchedAt.Add(p.ttl)) {
		delete(p.entries, profileID)
		return inventoryEntry{}, false
	}
	return entry, true
}

// forget drops the entry of one registration.
func (p *profileInventory) forget(profileID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, profileID)
}

// remember stores one catalog under the registration it describes.
//
// The cache is bounded: when a new registration would exceed the bound, the
// entry that was fetched longest ago is dropped. The eviction is by age and
// not by identity, so it cannot confuse two registrations.
func (p *profileInventory) remember(profileID string, entry inventoryEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.entries[profileID]; !exists && len(p.entries) >= maxProfileInventories {
		p.evictOldestLocked()
	}
	p.entries[profileID] = entry
}

// evictOldestLocked drops the entry that was fetched longest ago.
//
// The caller holds the mutex.
func (p *profileInventory) evictOldestLocked() {
	oldestID := ""
	var oldest time.Time
	for profileID, entry := range p.entries {
		if oldestID == "" || entry.fetchedAt.Before(oldest) ||
			(entry.fetchedAt.Equal(oldest) && profileID < oldestID) {
			oldestID = profileID
			oldest = entry.fetchedAt
		}
	}
	if oldestID != "" {
		delete(p.entries, oldestID)
	}
}

// response renders one entry as the backend-plugin inventory answer.
//
// The provider's order is preserved and the supplied limit truncates the
// answer without reordering it. The canonical id is the connector's own
// route prefix plus the provider's native slug, so the model a client selects
// is the model the provider named.
func (p *profileInventory) response(
	entry inventoryEntry,
	source string,
	limit uint32,
) backendplugin.ListModelsResponse {
	models := make([]backendplugin.ModelDescriptor, 0, len(entry.models))
	for _, model := range entry.models {
		if limit > 0 && uint32(len(models)) >= limit {
			break
		}
		models = append(models, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + model.Slug,
			NativeModelID:    model.Slug,
			DisplayName:      model.DisplayName,
			RoutePrefix:      FactoryKind,
			FactoryKind:      FactoryKind,
			Capabilities: backendplugin.CapabilitySummary{
				Streaming: true,
				Tools:     true,
			},
		})
	}
	return backendplugin.ListModelsResponse{
		Models:          models,
		InventorySource: source,
		FetchedUnixMS:   entry.fetchedAt.UnixMilli(),
	}
}

// inventoryOperationalError marks an inventory failure as fail-soft.
//
// The host turns a ListModels error into an unavailable discovery for this
// backend while keeping the other backends' inventories, and the stable code
// tells a timeout or a cancellation apart from an unavailable source. The
// raw error is retained for the connector's own diagnostics; it never
// carries a token, a provider document or an account identity.
func inventoryOperationalError(err error) error {
	if err == nil {
		return nil
	}
	code := modelinventory.ErrorCodeUnavailable
	switch {
	case errors.Is(err, context.Canceled):
		code = modelinventory.ErrorCodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = modelinventory.ErrorCodeTimeout
	}
	if !errors.Is(err, ErrInventoryUnavailable) {
		err = fmt.Errorf("%w: %w", ErrInventoryUnavailable, err)
	}
	return &modelinventory.OperationalError{Code: code, Err: err}
}
