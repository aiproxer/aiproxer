package modelcatalog

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
)

// SnapshotSource fetches a remote catalog snapshot. Implementations live outside this package (infra).
type SnapshotSource interface {
	Fetch(ctx context.Context) (Snapshot, error)
}

// SnapshotCache loads and persists validated snapshots locally. Implementations live outside this package.
type SnapshotCache interface {
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, snapshot Snapshot) error
}

// Snapshot is an immutable, validated catalog view for one refresh generation.
type Snapshot struct {
	Generation  string
	FetchedAt   time.Time
	ContentHash string
	Index       *SnapshotIndex
	// WirePayload holds the original catalog JSON object bytes used to build this snapshot for disk cache round-trips.
	WirePayload []byte
}

// SnapshotRef is a lightweight handle to the snapshot generation used for a routing decision.
type SnapshotRef struct {
	Generation string
}

// ActiveSnapshotProvider supplies the current immutable catalog index for each [CatalogResolverImpl.Resolve] call.
// Implementations typically read an atomically published snapshot ([CatalogRuntime]) so refresh updates
// affect subsequent routing decisions without per-resolve I/O.
type ActiveSnapshotProvider interface {
	ActiveIndex() (*SnapshotIndex, SnapshotRef)
}

// StaticActiveSnapshotProvider returns a fixed index/ref (tests and static catalogs).
type StaticActiveSnapshotProvider struct {
	Index *SnapshotIndex
	Ref   SnapshotRef
}

// ActiveIndex implements [ActiveSnapshotProvider].
func (s StaticActiveSnapshotProvider) ActiveIndex() (*SnapshotIndex, SnapshotRef) {
	return s.Index, s.Ref
}

// SnapshotIndex is a read-only view of catalog model ids to facts for deterministic matching.
type SnapshotIndex struct {
	byCatalogModelID map[string]ModelFacts
	// normToIDs maps NormalizeStripOneProviderPrefix(catalogId) -> sorted catalog ids sharing that suffix.
	normToIDs map[string][]string
	// suffixToIDs maps dotted/dashed suffix lookup keys -> sorted catalog ids sharing that suffix.
	suffixToIDs map[string][]string
}

// NewSnapshotIndex returns an index backed by a defensive copy of catalog entries.
func NewSnapshotIndex(catalog map[string]ModelFacts) *SnapshotIndex {
	m := make(map[string]ModelFacts, len(catalog))
	maps.Copy(m, catalog)
	return &SnapshotIndex{
		byCatalogModelID: m,
		normToIDs:        buildNormToIDs(m),
		suffixToIDs:      buildSuffixToIDs(m),
	}
}

// FactsByCatalogModelID returns catalog-derived facts for an exact catalog model id key.
func (s *SnapshotIndex) FactsByCatalogModelID(catalogModelID string) (ModelFacts, bool) {
	if s == nil {
		return ModelFacts{}, false
	}
	f, ok := s.byCatalogModelID[catalogModelID]
	return f, ok
}

// CatalogIDsForSuffixLookup returns sorted catalog ids whose model suffix matches lookup keys
// (including dotted/dashed numeric variants). Nil when the index or suffix is empty.
func (s *SnapshotIndex) CatalogIDsForSuffixLookup(suffix string) []string {
	if s == nil || s.suffixToIDs == nil {
		return nil
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, key := range SuffixLookupKeys(suffix) {
		for _, id := range s.suffixToIDs[key] {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}
