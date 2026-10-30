package modelcatalog

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
