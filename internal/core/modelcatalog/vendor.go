package modelcatalog

import (
	"regexp"
	"strings"
)

type VendorResolveKind uint8

const (
	VendorResolveNoMatch VendorResolveKind = iota
	VendorResolveExact
	VendorResolveCatalogSuffix
	VendorResolveVendorAlias
	VendorResolveAmbiguous
	VendorResolveKeywordFallback
)

func (k VendorResolveKind) String() string {
	switch k {
	case VendorResolveNoMatch:
		return "no_match"
	case VendorResolveExact:
		return "exact"
	case VendorResolveCatalogSuffix:
		return "catalog_suffix"
	case VendorResolveVendorAlias:
		return "vendor_alias"
	case VendorResolveAmbiguous:
		return "ambiguous"
	case VendorResolveKeywordFallback:
		return "keyword_fallback"
	default:
		return "unknown"
	}
}

type VendorResolveResult struct {
	Kind           VendorResolveKind
	InputModel     string
	CanonicalID    string
	RouteModel     string
	MatchedCatalog string
	CatalogVendor  string
	Candidates     []string
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
