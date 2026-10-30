// Package modelregistry provides core-owned lookup over backend-exposed models.
package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"golang.org/x/sync/errgroup"
)

var (
	ErrNilContext             = errors.New("modelregistry: nil context")
	ErrMissingProvider        = errors.New("modelregistry: missing inventory provider")
	ErrMissingBackendPrefix   = errors.New("modelregistry: missing backend prefix")
	ErrDuplicateBackendPrefix = errors.New("modelregistry: duplicate backend prefix")
	ErrInvalidModel           = errors.New("modelregistry: invalid model")
	ErrInvalidCanonicalID     = errors.New("modelregistry: invalid canonical model id")
	ErrNoUsableInventory      = errors.New("modelregistry: no usable inventory")
	// ErrConflictingMapping is returned when two rows claim the same
	// (backendID, canonicalID) with different NativeID values.
	ErrConflictingMapping = errors.New("modelregistry: conflicting exact model mapping")
)

// inventoryFetchConcurrency caps parallel LoadModels during Build.
const inventoryFetchConcurrency = 4

type BackendInventory struct {
	BackendID       string
	Kind            string
	BackendPrefixes []string
	Provider        modelinventory.Provider
	FetchTimeout    time.Duration
}

type BackendModel struct {
	CanonicalID      string
	NativeID         string
	DisplayName      string
	BackendID        string
	Kind             string
	Source           modelinventory.Source
	LoadedAt         time.Time
	Prefix           string
	CapabilitySource string
	// IsDefault marks the model the provider designated as the instance's
	// default. It is the provider's choice: only the provider knows the
	// model's role.
	IsDefault bool
}

type Snapshot struct {
	Generation  string
	RefreshedAt time.Time
	Models      []BackendModel
}

// BackendDiscovery is per-backend inventory discovery state for diagnostics.
type BackendDiscovery struct {
	BackendID  string
	Kind       string
	Status     modelinventory.DiscoveryStatus
	Source     modelinventory.Source
	ModelCount int
	ErrorCode  string
}

func newBackendDiscovery(backendID, kind string, disc modelinventory.Discovery) BackendDiscovery {
	return BackendDiscovery{
		BackendID:  backendID,
		Kind:       kind,
		Status:     disc.Status,
		Source:     disc.Source,
		ModelCount: disc.ModelCount,
		ErrorCode:  disc.ErrorCode,
	}
}

// BuildResult is the fail-soft aggregation outcome for configured backend inventories.
type BuildResult struct {
	Registry    *Registry
	Discoveries []BackendDiscovery
}

type Registry struct {
	byCanonical map[string][]BackendModel
	all         []BackendModel // immutable after Build / newRegistry*
}

func warnInventory(ctx context.Context, log *slog.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.WarnContext(ctx, msg, args...)
}

type inventoryLoadResult struct {
	disc BackendDiscovery
	rows []BackendModel
}

func Build(ctx context.Context, inventories []BackendInventory, log *slog.Logger) (BuildResult, error) {
	if ctx == nil {
		return BuildResult{}, ErrNilContext
	}
	registeredPrefixes, err := validateInventoryPrefixes(inventories)
	if err != nil {
		return BuildResult{}, err
	}

	// Validate inventory metadata serially before parallel fetch.
	for i, inv := range inventories {
		backendID := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		if backendID == "" || kind == "" {
			return BuildResult{}, fmt.Errorf("%w at inventory[%d]", ErrInvalidModel, i)
		}
		if inv.Provider == nil {
			return BuildResult{}, fmt.Errorf("%w for backend %q", ErrMissingProvider, backendID)
		}
	}

	results := make([]inventoryLoadResult, len(inventories))
	g, gctx := errgroup.WithContext(ctx)
	limit := inventoryFetchConcurrency
	if n := runtime.GOMAXPROCS(0); n > 0 && n < limit {
		limit = n
	}
	if len(inventories) < limit {
		limit = len(inventories)
	}
	if limit < 1 {
		limit = 1
	}
	g.SetLimit(limit)

	var mu sync.Mutex
	for i, inv := range inventories {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			backendID := strings.TrimSpace(inv.BackendID)
			kind := strings.TrimSpace(inv.Kind)
			loadCtx := gctx
			var cancel context.CancelFunc
			if inv.FetchTimeout > 0 {
				loadCtx, cancel = context.WithTimeout(gctx, inv.FetchTimeout)
			}
			snap, err := inv.Provider.LoadModels(loadCtx)
			if cancel != nil {
				cancel()
			}
			if err != nil {
				// Parent cancel/deadline aborts aggregation; per-backend timeouts stay fail-soft.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if gctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
					// Another worker hit parent cancel; surface it.
					if errors.Is(gctx.Err(), context.Canceled) || errors.Is(gctx.Err(), context.DeadlineExceeded) {
						return gctx.Err()
					}
				}
				disc := modelinventory.DiscoveryFromLoadError(err)
				mu.Lock()
				warnInventory(
					ctx, log, "modelregistry: inventory load failed",
					"backend_id", backendID,
					"kind", kind,
					"error_code", disc.ErrorCode,
					"error", err,
				)
				mu.Unlock()
				results[i] = inventoryLoadResult{disc: newBackendDiscovery(backendID, kind, disc)}
				return nil
			}
			disc := modelinventory.DiscoveryFromSnapshot(snap)
			if disc.Status == modelinventory.DiscoveryStatusEmpty {
				results[i] = inventoryLoadResult{disc: newBackendDiscovery(backendID, kind, disc)}
				return nil
			}
			backendRows := make([]BackendModel, 0, len(snap.Models))
			invalid := false
			for _, m := range snap.Models {
				canonical := strings.TrimSpace(m.CanonicalID)
				native := strings.TrimSpace(m.NativeID)
				if canonical == "" || native == "" || !validCanonicalID(canonical) || canonicalUsesRegisteredPrefixQualifier(canonical, registeredPrefixes) {
					invalid = true
					break
				}
				backendRows = append(backendRows, BackendModel{
					CanonicalID: canonical,
					NativeID:    native,
					DisplayName: strings.TrimSpace(m.DisplayName),
					BackendID:   backendID,
					Kind:        kind,
					Source:      snap.Source,
					LoadedAt:    snap.LoadedAt,
					IsDefault:   m.IsDefault,
				})
			}
			if invalid {
				mu.Lock()
				warnInventory(
					ctx, log, "modelregistry: invalid inventory omitted",
					"backend_id", backendID,
					"kind", kind,
					"error_code", modelinventory.ErrorCodeInvalidInventory,
				)
				mu.Unlock()
				results[i] = inventoryLoadResult{disc: newBackendDiscovery(backendID, kind, modelinventory.Discovery{
					Status:    modelinventory.DiscoveryStatusUnavailable,
					Source:    snap.Source,
					ErrorCode: modelinventory.ErrorCodeInvalidInventory,
				})}
				return nil
			}
			results[i] = inventoryLoadResult{
				disc: newBackendDiscovery(backendID, kind, disc),
				rows: enrichBackendModels(backendRows, inv),
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return BuildResult{}, err
	}

	// Aggregate in config order.
	all := []BackendModel{}
	discoveries := make([]BackendDiscovery, 0, len(inventories))
	for _, res := range results {
		discoveries = append(discoveries, res.disc)
		all = append(all, res.rows...)
	}
	if len(all) == 0 {
		return BuildResult{
			Registry: &Registry{
				byCanonical: map[string][]BackendModel{},
				all:         []BackendModel{},
			},
			Discoveries: discoveries,
		}, nil
	}
	reg, err := newRegistryFromValidatedBackendModels(all)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Registry: reg, Discoveries: discoveries}, nil
}

func (r *Registry) Lookup(canonicalID string) ([]BackendModel, bool) {
	if r == nil {
		return nil, false
	}
	models, ok := r.byCanonical[strings.TrimSpace(canonicalID)]
	if !ok || len(models) == 0 {
		return nil, false
	}
	return slices.Clone(models), true
}

func (r *Registry) All() []BackendModel {
	if r == nil || len(r.all) == 0 {
		return []BackendModel{}
	}
	return slices.Clone(r.all)
}

// Len returns the number of published backend model rows without cloning.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.all)
}

// DefaultModel returns the model a backend instance designates as its
// default, and whether it designates one at all.
//
// The provider owns that choice, because only the provider knows the model's
// role: a model the provider marked as the default wins. Without such a
// marker, a single published model is unambiguous and is the default. Several
// unmarked models have no reliable preference order, so the instance
// designates no default and the caller keeps the absent default route
// instead of naming an arbitrary model.
//
// A provider that marks more than one model designates no default: the
// choice is ambiguous, and an ambiguous default must not become a route.
//
// The policy does not claim capability compatibility. The inventory carries no
// capability data, so the proxy cannot tell whether the chosen model can serve
// the operation a frontend sends: a sole model that cannot serve it fails at
// the backend, which reports the failure, instead of the proxy refusing a
// request it could have attempted.
func (r *Registry) DefaultModel(instanceID string) (BackendModel, bool) {
	if r == nil {
		return BackendModel{}, false
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return BackendModel{}, false
	}
	var marked *BackendModel
	markedCount := 0
	published := 0
	var only *BackendModel
	for i := range r.all {
		row := r.all[i]
		if strings.TrimSpace(row.BackendID) != instanceID {
			continue
		}
		published++
		if only == nil {
			only = &r.all[i]
		}
		if !row.IsDefault {
			continue
		}
		markedCount++
		if marked == nil {
			marked = &r.all[i]
		}
	}
	switch {
	case markedCount == 1:
		return *marked, true
	case markedCount > 1:
		return BackendModel{}, false
	case published == 1 && only != nil:
		return *only, true
	default:
		return BackendModel{}, false
	}
}

func validCanonicalID(id string) bool {
	left, right, ok := strings.Cut(id, "/")
	if !ok {
		return false
	}
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && !strings.Contains(right, "/")
}

type prefixOwner struct {
	backendID string
	kind      string
}

func validateInventoryPrefixes(inventories []BackendInventory) (map[string]struct{}, error) {
	owners := make(map[string]prefixOwner)
	registered := make(map[string]struct{})
	for i, inv := range inventories {
		backendID := strings.TrimSpace(inv.BackendID)
		kind := strings.TrimSpace(inv.Kind)
		valid := normalizeBackendPrefixes(inv.BackendPrefixes)
		if len(valid) == 0 {
			if backendID == "" {
				backendID = fmt.Sprintf("inventory[%d]", i)
			}
			return nil, fmt.Errorf("%w for backend %q at inventory[%d]", ErrMissingBackendPrefix, backendID, i)
		}
		for _, prefix := range valid {
			if prev, ok := owners[prefix]; ok {
				if prev.kind != kind {
					return nil, fmt.Errorf("%w %q claimed by backend %q (kind %q) and backend %q (kind %q)", ErrDuplicateBackendPrefix, prefix, prev.backendID, prev.kind, backendID, kind)
				}
				continue
			}
			owners[prefix] = prefixOwner{backendID: backendID, kind: kind}
			registered[prefix] = struct{}{}
		}
	}
	return registered, nil
}

func normalizeBackendPrefixes(prefixes []string) []string {
	out := []string{}
	seen := make(map[string]struct{}, len(prefixes))
	for _, raw := range prefixes {
		prefix := strings.TrimSpace(raw)
		if prefix == "" || strings.Contains(prefix, "/") || strings.Contains(prefix, ":") {
			continue
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	return out
}

func canonicalUsesRegisteredPrefixQualifier(canonical string, registeredPrefixes map[string]struct{}) bool {
	prefix, rest, ok := strings.Cut(canonical, ":")
	if !ok {
		return false
	}
	prefix = strings.TrimSpace(prefix)
	rest = strings.TrimSpace(rest)
	if prefix == "" || rest == "" {
		return false
	}
	_, registered := registeredPrefixes[prefix]
	return registered
}

type exactMappingKey struct {
	backendID   string
	canonicalID string
}

// dedupeExactMappings rejects conflicting (backend, canonical)→native rows and
// deterministically drops identical duplicates.
func dedupeExactMappings(models []BackendModel) ([]BackendModel, error) {
	seen := make(map[exactMappingKey]string, len(models))
	index := make(map[exactMappingKey]int, len(models))
	out := make([]BackendModel, 0, len(models))
	for i, row := range models {
		key := exactMappingKey{
			backendID:   strings.TrimSpace(row.BackendID),
			canonicalID: strings.TrimSpace(row.CanonicalID),
		}
		native := strings.TrimSpace(row.NativeID)
		if prev, ok := seen[key]; ok {
			if prev != native {
				return nil, fmt.Errorf("%w: backend %q canonical %q natives %q vs %q at model[%d]",
					ErrConflictingMapping, key.backendID, key.canonicalID, prev, native, i)
			}
			// The duplicate carries the same mapping, so the default designation
			// is merged into the retained row: the enumeration order must not
			// decide which of two identical rows keeps the designation.
			if row.IsDefault {
				if at, ok := index[key]; ok {
					out[at].IsDefault = true
				}
			}
			continue // identical duplicate
		}
		seen[key] = native
		index[key] = len(out)
		out = append(out, row)
	}
	return out, nil
}

// newRegistryFromValidatedBackendModels builds a registry from rows already
// trimmed and validated by Build (fail-soft path). Rejects conflicting exact mappings.
func newRegistryFromValidatedBackendModels(models []BackendModel) (*Registry, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("%w: no models", ErrInvalidModel)
	}
	models, err := dedupeExactMappings(models)
	if err != nil {
		return nil, err
	}
	byCanonical := make(map[string][]BackendModel, len(models))
	all := make([]BackendModel, len(models))
	copy(all, models)
	for _, row := range all {
		byCanonical[row.CanonicalID] = append(byCanonical[row.CanonicalID], row)
	}
	return &Registry{byCanonical: byCanonical, all: all}, nil
}

func newRegistryFromBackendModels(models []BackendModel, registeredPrefixes map[string]struct{}) (*Registry, error) {
	byCanonical := make(map[string][]BackendModel)
	all := make([]BackendModel, 0, len(models))
	for i, m := range models {
		canonical := strings.TrimSpace(m.CanonicalID)
		native := strings.TrimSpace(m.NativeID)
		backendID := strings.TrimSpace(m.BackendID)
		kind := strings.TrimSpace(m.Kind)
		if canonical == "" || native == "" || backendID == "" {
			return nil, fmt.Errorf("%w at model[%d]", ErrInvalidModel, i)
		}
		if !validCanonicalID(canonical) {
			return nil, fmt.Errorf("%w %q at model[%d]", ErrInvalidCanonicalID, canonical, i)
		}
		if canonicalUsesRegisteredPrefixQualifier(canonical, registeredPrefixes) {
			return nil, fmt.Errorf("%w %q at model[%d]", ErrInvalidCanonicalID, canonical, i)
		}
		row := BackendModel{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: strings.TrimSpace(m.DisplayName),
			BackendID:   backendID,
			Kind:        kind,
			Source:      m.Source,
			LoadedAt:    m.LoadedAt,
			// Prefix and CapabilitySource are enrichment carried from Build or
			// merge-time enrichment; preserved so rebuilt registries (refresh
			// merges, cache hydration) keep route-prefix attribution.
			Prefix:           strings.TrimSpace(m.Prefix),
			CapabilitySource: strings.TrimSpace(m.CapabilitySource),
			// The provider's default designation is preserved the same way, so
			// a rebuilt registry keeps the instance's designated default model.
			IsDefault: m.IsDefault,
		}
		all = append(all, row)
	}
	all, err := dedupeExactMappings(all)
	if err != nil {
		return nil, err
	}
	for _, row := range all {
		byCanonical[row.CanonicalID] = append(byCanonical[row.CanonicalID], row)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%w: no models", ErrInvalidModel)
	}
	return &Registry{
		byCanonical: byCanonical,
		all:         all,
	}, nil
}
