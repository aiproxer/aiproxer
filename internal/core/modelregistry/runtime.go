package modelregistry

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

var ErrSnapshotUnavailable = errors.New("modelregistry: snapshot unavailable")

// errStartInFlight reports concurrent Start calls. Start owns the refresh
// guard, so a second Start (or a RunRefresh racing startup) never stampedes
// LoadModels.
var errStartInFlight = errors.New("modelregistry: start already in progress")

type RefreshFailureCategory string

const (
	RefreshFailureNone  RefreshFailureCategory = ""
	RefreshFailureFetch RefreshFailureCategory = "fetch"
	RefreshFailureCache RefreshFailureCategory = "cache"
	RefreshFailureParse RefreshFailureCategory = "parse"
)

type Cache interface {
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, snap Snapshot) error
}

type RuntimeConfig struct {
	Inventories []BackendInventory
	Cache       Cache
	Now         func() time.Time
	// Log receives fail-soft inventory warnings from Build. Nil disables logging.
	Log *slog.Logger
}

// published is the atomic unit of registry visibility: Diagnostics snapshot,
// Lookup/All (reg), modelsJSON, and publication-tied discoveries always observe
// the same generation. fingerprint skips allowlist rebuilds when catalog content
// is unchanged. Live last-refresh failures stay on Runtime.mu separately.
type published struct {
	reg                *Registry
	snap               Snapshot
	modelsJSON         []byte
	fingerprint        uint64
	backendModelCounts map[string]int
	discoveries        []BackendDiscovery
}

type Runtime struct {
	cfg RuntimeConfig

	published  atomic.Pointer[published]
	refreshing atomic.Bool

	mu          sync.Mutex
	lastFail    RefreshFailureCategory
	cacheFail   RefreshFailureCategory
	discoveries []BackendDiscovery // live mirror for Runtime.Diagnostics; published holds frozen copy
	// acceptedUnion retains every accepted native row for this Runtime/config
	// generation so provider gates stay monotonic across refreshes (req 9.5/9.9).
	acceptedUnion map[string]map[string]modelinventory.Model // backendID -> nativeID -> model
}

type Diagnostics struct {
	Active                   bool
	Generation               string
	RefreshedAt              time.Time
	ModelCount               int
	BackendModelCounts       map[string]int
	BackendDiscoveries       []BackendDiscovery
	LastRefreshErrorCategory RefreshFailureCategory
	LastCacheErrorCategory   RefreshFailureCategory
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Inventories = slices.Clone(cfg.Inventories)
	return &Runtime{cfg: cfg}
}

func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	// One refresh at a time across Start and RunRefresh: a background refresh
	// racing startup must not stampede LoadModels/CLI spawns.
	if !r.refreshing.CompareAndSwap(false, true) {
		return errStartInFlight
	}
	defer r.refreshing.Store(false)
	// Publish a valid cache immediately so boot never advertises worse than
	// the last-known inventory; the discovery-first refresh below replaces it
	// with live enumeration whenever enumeration succeeds. Fingerprint
	// equality keeps any redundant re-publish free of generation churn and
	// provider allowlist callbacks.
	cached, cacheOK := r.loadValidCache(ctx)
	if cacheOK {
		_ = r.publishSnapshot(cached, discoveriesFromCache(r.cfg.Inventories, cached))
	}
	// Discovery first: live enumeration is authoritative. The cache published
	// above (if any) is the fallback when enumeration cannot produce a usable
	// registry.
	if err := r.refresh(ctx); err != nil {
		if cacheOK {
			if perr := r.publishSnapshot(cached, discoveriesFromCache(r.cfg.Inventories, cached)); perr == nil {
				r.setCacheOK()
				return nil
			}
		}
		if r.ActiveRegistry() != nil {
			// Degraded but usable (e.g. cold-start total failure published an
			// empty registry): boot succeeds; failures stay visible in
			// diagnostics.
			return nil
		}
		return err
	}
	return nil
}

// loadValidCache returns the cached snapshot when present and structurally
// valid for the configured inventories. It records the cache health category
// and never publishes: Start publishes the cache only as a fallback after a
// failed refresh.
func (r *Runtime) loadValidCache(ctx context.Context) (Snapshot, bool) {
	if r.cfg.Cache == nil {
		return Snapshot{}, false
	}
	snap, err := r.cfg.Cache.Load(ctx)
	if err != nil {
		if !errors.Is(err, ErrSnapshotUnavailable) {
			r.setCacheFailure(RefreshFailureCache)
		}
		return Snapshot{}, false
	}
	if err := r.validateSnapshotBackends(snap); err != nil {
		r.setCacheFailure(RefreshFailureParse)
		return Snapshot{}, false
	}
	registeredPrefixes, err := validateInventoryPrefixes(r.cfg.Inventories)
	if err != nil {
		r.setCacheFailure(RefreshFailureParse)
		return Snapshot{}, false
	}
	if _, err := newRegistryFromBackendModels(snap.Models, registeredPrefixes); err != nil {
		r.setCacheFailure(RefreshFailureParse)
		return Snapshot{}, false
	}
	r.setCacheOK()
	return snap, true
}

// RunRefresh runs a background inventory refresh. Concurrent calls while a
// refresh is already in flight are skipped (no stampede of LoadModels/CLI spawns).
func (r *Runtime) RunRefresh(ctx context.Context) {
	if ctx == nil {
		r.setFailure(RefreshFailureFetch)
		return
	}
	if !r.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer r.refreshing.Store(false)

	if err := r.refresh(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if refreshErr, ok := errors.AsType[refreshFailureError](err); ok {
			r.setFailure(refreshErr.category)
			return
		}
		r.setFailure(RefreshFailureFetch)
	}
}

func (r *Runtime) Lookup(canonicalID string) ([]BackendModel, bool) {
	reg := r.ActiveRegistry()
	if reg == nil {
		return nil, false
	}
	return reg.Lookup(canonicalID)
}

func (r *Runtime) All() []BackendModel {
	reg := r.ActiveRegistry()
	if reg == nil {
		return []BackendModel{}
	}
	return reg.All()
}

// ModelsJSON returns the precomputed OpenAI /v1/models JSON body and generation
// ETag for the published snapshot. ok is false when nothing is published.
func (r *Runtime) ModelsJSON() (body []byte, generation string, ok bool) {
	if r == nil {
		return nil, "", false
	}
	pub := r.published.Load()
	if pub == nil {
		return nil, "", false
	}
	return pub.modelsJSON, pub.snap.Generation, true
}

func (r *Runtime) Diagnostics() Diagnostics {
	if r == nil {
		return Diagnostics{
			BackendModelCounts: map[string]int{},
			BackendDiscoveries: []BackendDiscovery{},
		}
	}
	out := Diagnostics{
		BackendModelCounts: map[string]int{},
		BackendDiscoveries: []BackendDiscovery{},
	}
	pub := r.published.Load()
	r.mu.Lock()
	out.LastRefreshErrorCategory = r.lastFail
	out.LastCacheErrorCategory = r.cacheFail
	liveDisc := slices.Clone(r.discoveries)
	r.mu.Unlock()
	if pub != nil {
		out.Active = true
		out.Generation = pub.snap.Generation
		out.RefreshedAt = pub.snap.RefreshedAt
		out.ModelCount = len(pub.snap.Models)
		out.BackendModelCounts = cloneIntMap(pub.backendModelCounts)
		// Publication-tied discoveries are the default coherent pair. After a
		// retain-last-good refresh failure, live discoveries describe the failed
		// attempt and are preferred for operator Diagnostics (not BoundView).
		if out.LastRefreshErrorCategory != RefreshFailureNone && len(liveDisc) > 0 {
			out.BackendDiscoveries = liveDisc
		} else {
			out.BackendDiscoveries = slices.Clone(pub.discoveries)
		}
	} else {
		out.BackendDiscoveries = liveDisc
	}
	if out.BackendDiscoveries == nil {
		out.BackendDiscoveries = []BackendDiscovery{}
	}
	return out
}

func (r *Runtime) Discoveries() []BackendDiscovery {
	if r == nil {
		return []BackendDiscovery{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.discoveries)
	if out == nil {
		return []BackendDiscovery{}
	}
	return out
}

func (r *Runtime) ActiveRegistry() *Registry {
	if r == nil {
		return nil
	}
	pub := r.published.Load()
	if pub == nil {
		return nil
	}
	return pub.reg
}

func (r *Runtime) LastRefreshFailure() RefreshFailureCategory {
	if r == nil {
		return RefreshFailureNone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastFail
}

func (r *Runtime) LastCacheFailure() RefreshFailureCategory {
	if r == nil {
		return RefreshFailureNone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cacheFail
}

func (r *Runtime) refresh(ctx context.Context) error {
	built, err := Build(ctx, r.cfg.Inventories, r.cfg.Log)
	if err != nil {
		return refreshFailureError{category: RefreshFailureFetch, err: err}
	}

	merged, fresh, transientFailed, failedAny := r.mergeWithRetained(built)
	merged = enrichMergedModels(merged, r.cfg.Inventories)
	if len(merged) == 0 {
		if allDiscoveriesEmpty(built) {
			// Authoritative empty: every backend reported empty inventory.
			// Publish the empty registry instead of resurrecting stale rows.
			r.publishEmpty(built.Discoveries)
			r.setOK()
			r.setCacheOK()
			return nil
		}
		if prev := r.published.Load(); prev != nil && prev.reg != nil && prev.reg.Len() > 0 {
			// Retain last-good registry. Publish discovery status for the failed
			// attempt, then re-sync allowlists to the retained snapshot.
			r.setDiscoveries(built.Discoveries)
			r.syncAllowlistsUnion(prev.snap.Models)
			return refreshFailureError{category: RefreshFailureFetch, err: ErrNoUsableInventory}
		}
		// Cold with nothing usable: publish the empty registry so startup can
		// boot degraded (Start falls back to cache when available, and swallows
		// the error whenever a usable registry is active).
		r.publishEmpty(built.Discoveries)
		if failedAny {
			return refreshFailureError{category: RefreshFailureFetch, err: ErrNoUsableInventory}
		}
		r.setOK()
		r.setCacheOK()
		return nil
	}

	registeredPrefixes, err := validateInventoryPrefixes(r.cfg.Inventories)
	if err != nil {
		return refreshFailureError{category: RefreshFailureFetch, err: err}
	}
	reg, err := newRegistryFromBackendModels(merged, registeredPrefixes)
	if err != nil {
		return refreshFailureError{category: RefreshFailureFetch, err: err}
	}
	if fresh == 0 {
		// Total transient failure: every advertised row is retained from the
		// previous snapshot. Record the failure via the returned error (the
		// RunRefresh wrapper applies it); Start boots degraded when a usable
		// registry is active.
		r.publish(reg, Snapshot{
			Generation:  strconv.FormatInt(r.cfg.Now().UnixNano(), 10),
			RefreshedAt: r.cfg.Now().UTC(),
			Models:      slices.Clone(reg.all),
		}, built.Discoveries)
		return refreshFailureError{category: RefreshFailureFetch, err: ErrNoUsableInventory}
	}

	// One clone for cache Save + publish ownership; Registry.all stays immutable.
	modelsCopy := slices.Clone(reg.all)
	if modelsCopy == nil {
		modelsCopy = []BackendModel{}
	}
	snap := Snapshot{
		Generation:  strconv.FormatInt(r.cfg.Now().UnixNano(), 10),
		RefreshedAt: r.cfg.Now().UTC(),
		Models:      modelsCopy,
	}
	if r.cfg.Cache != nil && !transientFailed {
		// Persist only fully fresh snapshots: rows retained from a failed
		// backend must not overwrite the cache as if freshly observed.
		if err := r.cfg.Cache.Save(ctx, snap); err != nil {
			r.publish(reg, snap, built.Discoveries)
			r.setOK()
			r.setCacheFailure(RefreshFailureCache)
			return nil
		}
	}
	r.publish(reg, snap, built.Discoveries)
	r.setOK()
	r.setCacheOK()
	return nil
}

// allDiscoveriesEmpty reports whether every backend discovery is an
// authoritative empty inventory (no failures). Only then may an empty merge
// replace a previously published registry.
func allDiscoveriesEmpty(built BuildResult) bool {
	if len(built.Discoveries) == 0 {
		return false
	}
	for _, d := range built.Discoveries {
		if d.Status != modelinventory.DiscoveryStatusEmpty {
			return false
		}
	}
	return true
}

// mergeWithRetained combines fresh Build rows with previously published rows
// for backends whose load failed transiently (unavailable/timeout). Backends
// with successful, empty, or permanently failed (e.g. invalid inventory)
// discoveries contribute only their fresh rows — possibly none — so removals
// and permanent failures propagate instead of being resurrected.
// It returns the merged rows, the count of fresh rows, whether any backend
// failed transiently, and whether any backend discovery failed at all.
func (r *Runtime) mergeWithRetained(built BuildResult) (merged []BackendModel, fresh int, transientFailed, failedAny bool) {
	freshByBackend := make(map[string][]BackendModel, len(r.cfg.Inventories))
	if built.Registry != nil {
		for _, row := range built.Registry.all {
			id := strings.TrimSpace(row.BackendID)
			freshByBackend[id] = append(freshByBackend[id], row)
		}
	}
	var prevByBackend map[string][]BackendModel
	if prev := r.published.Load(); prev != nil {
		prevByBackend = make(map[string][]BackendModel, len(r.cfg.Inventories))
		for _, row := range prev.snap.Models {
			id := strings.TrimSpace(row.BackendID)
			prevByBackend[id] = append(prevByBackend[id], row)
		}
	}
	byDiscovery := make(map[string]BackendDiscovery, len(built.Discoveries))
	for _, d := range built.Discoveries {
		byDiscovery[strings.TrimSpace(d.BackendID)] = d
	}
	merged = []BackendModel{}
	for _, inv := range r.cfg.Inventories {
		id := strings.TrimSpace(inv.BackendID)
		disc, ok := byDiscovery[id]
		if !ok {
			continue
		}
		if isTransientDiscovery(disc) {
			transientFailed = true
			failedAny = true
			merged = append(merged, prevByBackend[id]...)
			continue
		}
		if disc.Status == modelinventory.DiscoveryStatusUnavailable {
			failedAny = true
		}
		rows := freshByBackend[id]
		fresh += len(rows)
		merged = append(merged, rows...)
	}
	return merged, fresh, transientFailed, failedAny
}

// enrichMergedModels restores per-backend enrichment (route prefix,
// capability source) on merged rows. Fresh Build rows already carry it;
// rows retained from a previous snapshot may predate enrichment (e.g. cache
// hydration drops it), so enrichment is applied idempotently to all rows.
func enrichMergedModels(merged []BackendModel, inventories []BackendInventory) []BackendModel {
	if len(merged) == 0 {
		return merged
	}
	byID := make(map[string]BackendInventory, len(inventories))
	for _, inv := range inventories {
		if id := strings.TrimSpace(inv.BackendID); id != "" {
			byID[id] = inv
		}
	}
	for i, row := range merged {
		if inv, ok := byID[strings.TrimSpace(row.BackendID)]; ok {
			merged[i] = enrichBackendModel(row, inv)
		}
	}
	return merged
}

// isTransientDiscovery reports whether a failed discovery may succeed on
// retry (unreachable backend or timeout). Empty, invalid-inventory, and other
// permanent failures are authoritative and must not resurrect prior rows.
func isTransientDiscovery(disc BackendDiscovery) bool {
	if disc.Status != modelinventory.DiscoveryStatusUnavailable {
		return false
	}
	return disc.ErrorCode == modelinventory.ErrorCodeUnavailable ||
		disc.ErrorCode == modelinventory.ErrorCodeTimeout
}

// publishEmpty publishes an empty snapshot without touching the cache. Used
// for authoritative-empty and cold-unusable refreshes.
func (r *Runtime) publishEmpty(discoveries []BackendDiscovery) {
	r.publish(&Registry{byCanonical: map[string][]BackendModel{}, all: []BackendModel{}}, Snapshot{
		Generation:  strconv.FormatInt(r.cfg.Now().UnixNano(), 10),
		RefreshedAt: r.cfg.Now().UTC(),
		Models:      []BackendModel{},
	}, discoveries)
}

func (r *Runtime) publishSnapshot(snap Snapshot, discoveries []BackendDiscovery) error {
	if err := r.validateSnapshotBackends(snap); err != nil {
		return err
	}
	registeredPrefixes, err := validateInventoryPrefixes(r.cfg.Inventories)
	if err != nil {
		return err
	}
	reg, err := newRegistryFromBackendModels(snap.Models, registeredPrefixes)
	if err != nil {
		return err
	}
	r.publish(reg, snap, discoveries)
	return nil
}

func (r *Runtime) validateSnapshotBackends(snap Snapshot) error {
	allowed := make(map[string]struct{}, len(r.cfg.Inventories))
	for _, inv := range r.cfg.Inventories {
		if id := strings.TrimSpace(inv.BackendID); id != "" {
			allowed[id] = struct{}{}
		}
	}
	for i, model := range snap.Models {
		backendID := strings.TrimSpace(model.BackendID)
		if _, ok := allowed[backendID]; !ok {
			return fmt.Errorf("%w: cached model[%d] references unconfigured backend %q", ErrInvalidModel, i, backendID)
		}
	}
	return nil
}

// publish applies provider allowlist unions before advertising the new
// publication so Open acceptance never lags advertisement, and never revokes
// previously accepted natives within this Runtime generation.
//
// Order is intentional:
//  1. prepare published object (registry + discoveries + models JSON)
//  2. syncAllowlistsUnion — provider gates receive union(A∪new) first
//  3. published Store — /v1/models and Lookup advertise the new generation
func (r *Runtime) publish(reg *Registry, snap Snapshot, discoveries []BackendDiscovery) {
	fp := fingerprintModels(snap.Models)
	prev := r.published.Load()
	contentChanged := prev == nil || prev.fingerprint != fp
	generation := snap.Generation
	if !contentChanged && prev != nil {
		generation = prev.snap.Generation
	}
	// Take ownership of snap.Models when refresh already cloned; still clone for
	// cache-hydration paths that may share the cache's backing slice.
	models := snap.Models
	if models == nil {
		models = []BackendModel{}
	} else {
		models = slices.Clone(snap.Models)
	}
	cp := Snapshot{
		Generation:  generation,
		RefreshedAt: snap.RefreshedAt,
		Models:      models,
	}
	modelsJSON, err := MarshalOpenAIModelsListJSON(models)
	if err != nil {
		// Defensive: BuildOpenAIModelsList only uses strings; marshal should not fail.
		modelsJSON = []byte(`{"object":"list","data":[]}`)
	}
	counts := make(map[string]int, len(r.cfg.Inventories))
	for _, row := range models {
		counts[row.BackendID]++
	}
	discCopy := slices.Clone(discoveries)
	if discCopy == nil {
		discCopy = []BackendDiscovery{}
	}
	next := &published{
		reg:                reg,
		snap:               cp,
		modelsJSON:         modelsJSON,
		fingerprint:        fp,
		backendModelCounts: counts,
		discoveries:        discCopy,
	}
	// Provider acceptance before advertisement (and always on first publish).
	if contentChanged || prev == nil {
		r.syncAllowlistsUnion(cp.Models)
	}
	r.published.Store(next)
	r.setDiscoveries(discCopy)
}

// syncAllowlistsUnion merges models into the generation-lifetime accepted union
// and pushes the full union to each AcceptedInventory provider. Previously
// accepted native IDs are never removed during a refresh in this Runtime.
func (r *Runtime) syncAllowlistsUnion(models []BackendModel) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.acceptedUnion == nil {
		r.acceptedUnion = make(map[string]map[string]modelinventory.Model, len(r.cfg.Inventories))
	}
	for _, m := range models {
		backendID := strings.TrimSpace(m.BackendID)
		nativeID := strings.TrimSpace(m.NativeID)
		if backendID == "" || nativeID == "" {
			continue
		}
		byNative := r.acceptedUnion[backendID]
		if byNative == nil {
			byNative = make(map[string]modelinventory.Model)
			r.acceptedUnion[backendID] = byNative
		}
		byNative[nativeID] = modelinventory.Model{
			CanonicalID: m.CanonicalID,
			NativeID:    m.NativeID,
			DisplayName: m.DisplayName,
		}
	}
	// Snapshot unions under lock, then release before provider callbacks.
	unionByBackend := make(map[string][]modelinventory.Model, len(r.cfg.Inventories))
	for _, inv := range r.cfg.Inventories {
		id := strings.TrimSpace(inv.BackendID)
		byNative := r.acceptedUnion[id]
		rows := make([]modelinventory.Model, 0, len(byNative))
		for _, m := range byNative {
			rows = append(rows, m)
		}
		slices.SortFunc(rows, func(a, b modelinventory.Model) int {
			if c := strings.Compare(a.CanonicalID, b.CanonicalID); c != 0 {
				return c
			}
			return strings.Compare(a.NativeID, b.NativeID)
		})
		unionByBackend[id] = rows
	}
	r.mu.Unlock()

	for _, inv := range r.cfg.Inventories {
		a, ok := inv.Provider.(modelinventory.AcceptedInventory)
		if !ok {
			continue
		}
		id := strings.TrimSpace(inv.BackendID)
		a.AcceptInventory(unionByBackend[id])
	}
}

func (r *Runtime) setFailure(cat RefreshFailureCategory) {
	r.mu.Lock()
	r.lastFail = cat
	r.mu.Unlock()
}

func (r *Runtime) setOK() {
	r.setFailure(RefreshFailureNone)
}

func (r *Runtime) setCacheFailure(cat RefreshFailureCategory) {
	r.mu.Lock()
	r.cacheFail = cat
	r.mu.Unlock()
}

func (r *Runtime) setCacheOK() {
	r.setCacheFailure(RefreshFailureNone)
}

func (r *Runtime) setDiscoveries(discoveries []BackendDiscovery) {
	r.mu.Lock()
	r.discoveries = slices.Clone(discoveries)
	r.mu.Unlock()
}

func discoveriesFromCache(inventories []BackendInventory, snap Snapshot) []BackendDiscovery {
	counts := make(map[string]int, len(inventories))
	sources := make(map[string]modelinventory.Source, len(inventories))
	for _, row := range snap.Models {
		id := strings.TrimSpace(row.BackendID)
		counts[id]++
		if _, ok := sources[id]; !ok {
			sources[id] = row.Source
		}
	}
	out := make([]BackendDiscovery, 0, len(inventories))
	for _, inv := range inventories {
		id := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		count := counts[id]
		status := modelinventory.DiscoveryStatusCached
		errorCode := modelinventory.ErrorCodeNone
		if count == 0 {
			// Configured backend present in inventories but absent from the
			// cached snapshot: report empty, not cached-with-zero-rows.
			status = modelinventory.DiscoveryStatusEmpty
			errorCode = modelinventory.ErrorCodeEmpty
		}
		out = append(out, newBackendDiscovery(id, kind, modelinventory.Discovery{
			Status:     status,
			Source:     sources[id],
			ModelCount: count,
			ErrorCode:  errorCode,
		}))
	}
	return out
}

func fingerprintModels(models []BackendModel) uint64 {
	type row struct {
		backendID, canonical, native, kind string
	}
	rows := make([]row, 0, len(models))
	for _, m := range models {
		rows = append(rows, row{
			backendID: strings.TrimSpace(m.BackendID),
			canonical: strings.TrimSpace(m.CanonicalID),
			native:    strings.TrimSpace(m.NativeID),
			kind:      strings.TrimSpace(m.Kind),
		})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if c := strings.Compare(a.backendID, b.backendID); c != 0 {
			return c
		}
		if c := strings.Compare(a.canonical, b.canonical); c != 0 {
			return c
		}
		if c := strings.Compare(a.native, b.native); c != 0 {
			return c
		}
		return strings.Compare(a.kind, b.kind)
	})
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(len(rows)))
	_, _ = h.Write(buf[:])
	for _, r := range rows {
		_, _ = h.Write([]byte(r.backendID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.canonical))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.native))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.kind))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func cloneIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	maps.Copy(out, in)
	return out
}

type refreshFailureError struct {
	category RefreshFailureCategory
	err      error
}

func (e refreshFailureError) Error() string {
	if e.err == nil {
		return "modelregistry: refresh failure"
	}
	return e.err.Error()
}

func (e refreshFailureError) Unwrap() error {
	return e.err
}
