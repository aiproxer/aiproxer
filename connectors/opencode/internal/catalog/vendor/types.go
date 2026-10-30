package vendor

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

type FactSource uint8

const FactSourceCatalog FactSource = 1

type ModelFacts struct {
	Source FactSource
}

type VendorResolveKind uint8

const (
	VendorResolveNoMatch VendorResolveKind = iota
	VendorResolveExact
	VendorResolveCatalogSuffix
	VendorResolveVendorAlias
	VendorResolveAmbiguous
	VendorResolveKeywordFallback
)

type VendorResolveResult struct {
	Kind           VendorResolveKind
	InputModel     string
	CanonicalID    string
	RouteModel     string
	MatchedCatalog string
	CatalogVendor  string
	Candidates     []string
}

type VendorResolver interface {
	Resolve(model string) VendorResolveResult
}

type VendorPolicy struct {
	MapVendor            func(vendor string) string
	SuffixLookupVariants func(suffix string) []string
	KeywordFallback      func(model string) (canonical string, ok bool)
}

func (p VendorPolicy) mappedVendor(vendor string) string {
	vendor = normalizeVendorKey(vendor)
	if vendor == "" {
		return ""
	}
	if p.MapVendor != nil {
		if mapped := normalizeVendorKey(p.MapVendor(vendor)); mapped != "" {
			return mapped
		}
	}
	return vendor
}

func (p VendorPolicy) suffixLookupVariants(suffix string) []string {
	if p.SuffixLookupVariants != nil {
		return p.SuffixLookupVariants(suffix)
	}
	suffix = trimNonEmpty(suffix)
	if suffix == "" {
		return nil
	}
	return []string{suffix}
}

func (p VendorPolicy) keywordFallbackCanonical(model string) (string, bool) {
	if p.KeywordFallback == nil {
		return "", false
	}
	return p.KeywordFallback(model)
}

type ActiveSnapshotProvider interface {
	ActiveIndex() (*SnapshotIndex, SnapshotRef)
}

type SnapshotRef struct {
	Generation string
}

type StaticActiveSnapshotProvider struct {
	Index *SnapshotIndex
	Ref   SnapshotRef
}

func (s StaticActiveSnapshotProvider) ActiveIndex() (*SnapshotIndex, SnapshotRef) {
	return s.Index, s.Ref
}

type SnapshotIndex struct {
	byCatalogModelID map[string]ModelFacts
	normToIDs        map[string][]string
	suffixToIDs      map[string][]string
}

func NewSnapshotIndex(catalog map[string]ModelFacts) *SnapshotIndex {
	m := make(map[string]ModelFacts, len(catalog))
	maps.Copy(m, catalog)
	return &SnapshotIndex{
		byCatalogModelID: m,
		normToIDs:        buildNormToIDs(m),
		suffixToIDs:      buildSuffixToIDs(m),
	}
}

func (s *SnapshotIndex) FactsByCatalogModelID(catalogModelID string) (ModelFacts, bool) {
	if s == nil {
		return ModelFacts{}, false
	}
	f, ok := s.byCatalogModelID[catalogModelID]
	return f, ok
}

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

func (s *SnapshotIndex) catalogIDsForNormalized(normalized string) []string {
	if s == nil || s.normToIDs == nil {
		return nil
	}
	return s.normToIDs[normalized]
}

var betweenDigitsDash = regexp.MustCompile(`(\d)-(\d)`)

func SuffixLookupKeys(suffix string) []string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return nil
	}
	seen := map[string]struct{}{suffix: {}}
	keys := []string{suffix}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		keys = append(keys, v)
	}
	add(strings.ReplaceAll(suffix, ".", "-"))
	add(betweenDigitsDash.ReplaceAllString(suffix, "${1}.${2}"))
	return keys
}

func NormalizeStripOneProviderPrefix(s string) string {
	s = strings.TrimSpace(s)
	_, after, ok := strings.Cut(s, "/")
	if !ok {
		return s
	}
	return after
}

func normalizeVendorKey(vendor string) string {
	return strings.ToLower(strings.TrimSpace(vendor))
}

func trimNonEmpty(s string) string {
	return strings.TrimSpace(s)
}

func splitVendorModel(id string) (vendor, suffix string, ok bool) {
	id = strings.TrimSpace(id)
	before, after, found := strings.Cut(id, "/")
	if !found || before == "" || after == "" || strings.Contains(after, "/") {
		return "", "", false
	}
	return before, after, true
}

func canonicalWithSuffix(catalogID, callerSuffix string) string {
	vendor, _, ok := strings.Cut(strings.TrimSpace(catalogID), "/")
	if !ok || vendor == "" {
		return catalogID
	}
	callerSuffix = strings.TrimSpace(callerSuffix)
	if callerSuffix == "" {
		return catalogID
	}
	return vendor + "/" + callerSuffix
}

func buildNormToIDs(byID map[string]ModelFacts) map[string][]string {
	normToIDs := make(map[string][]string, len(byID))
	for id := range byID {
		n := NormalizeStripOneProviderPrefix(id)
		normToIDs[n] = append(normToIDs[n], id)
	}
	for n, list := range normToIDs {
		slices.Sort(list)
		normToIDs[n] = list
	}
	return normToIDs
}

func buildSuffixToIDs(byID map[string]ModelFacts) map[string][]string {
	suffixToIDs := make(map[string][]string, len(byID))
	for id := range byID {
		suffix := NormalizeStripOneProviderPrefix(id)
		for _, key := range SuffixLookupKeys(suffix) {
			suffixToIDs[key] = append(suffixToIDs[key], id)
		}
	}
	for key, list := range suffixToIDs {
		slices.Sort(list)
		suffixToIDs[key] = list
	}
	return suffixToIDs
}
