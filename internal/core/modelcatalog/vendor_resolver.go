package modelcatalog

import "strings"

type VendorResolver interface {
	Resolve(model string) VendorResolveResult
}

type DefaultVendorResolver struct {
	active          ActiveSnapshotProvider
	keywordFallback bool
	policy          VendorPolicy
}

var _ VendorResolver = (*DefaultVendorResolver)(nil)

func NewVendorResolver(active ActiveSnapshotProvider, keywordFallback bool, policy VendorPolicy) *DefaultVendorResolver {
	return &DefaultVendorResolver{
		active:          active,
		keywordFallback: keywordFallback,
		policy:          policy,
	}
}

func (r *DefaultVendorResolver) Resolve(model string) VendorResolveResult {
	input := strings.TrimSpace(model)
	if input == "" {
		return VendorResolveResult{Kind: VendorResolveNoMatch, InputModel: input}
	}

	idx := r.activeIndex()
	if idx != nil {
		if _, ok := idx.FactsByCatalogModelID(input); ok {
			vendor, _, _ := splitVendorModel(input)
			return VendorResolveResult{
				Kind:           VendorResolveExact,
				InputModel:     input,
				CanonicalID:    input,
				RouteModel:     input,
				MatchedCatalog: input,
				CatalogVendor:  vendor,
			}
		}

		callerSuffix := input
		if vendor, suffix, ok := splitVendorModel(input); ok {
			callerSuffix = suffix
			mapped := r.policy.mappedVendor(vendor)
			if mapped != "" {
				aliasID := mapped + "/" + suffix
				if _, ok := idx.FactsByCatalogModelID(aliasID); ok {
					return VendorResolveResult{
						Kind:           VendorResolveVendorAlias,
						InputModel:     input,
						CanonicalID:    canonicalWithSuffix(aliasID, suffix),
						RouteModel:     input,
						MatchedCatalog: aliasID,
						CatalogVendor:  mapped,
					}
				}
			}
		}

		lookupSuffix := NormalizeStripOneProviderPrefix(input)
		for _, variant := range r.policy.suffixLookupVariants(lookupSuffix) {
			ids := idx.CatalogIDsForSuffixLookup(variant)
			switch len(ids) {
			case 1:
				matched := ids[0]
				vendor, _, _ := splitVendorModel(matched)
				canonical := canonicalWithSuffix(matched, callerSuffix)
				return VendorResolveResult{
					Kind:           VendorResolveCatalogSuffix,
					InputModel:     input,
					CanonicalID:    canonical,
					RouteModel:     canonical,
					MatchedCatalog: matched,
					CatalogVendor:  vendor,
				}
			case 0:
				continue
			default:
				return VendorResolveResult{
					Kind:       VendorResolveAmbiguous,
					InputModel: input,
					Candidates: append([]string(nil), ids...),
				}
			}
		}
	}

	if !r.keywordFallback {
		return VendorResolveResult{Kind: VendorResolveNoMatch, InputModel: input}
	}
	if canonical, ok := r.policy.keywordFallbackCanonical(input); ok {
		vendor, _, _ := splitVendorModel(canonical)
		return VendorResolveResult{
			Kind:          VendorResolveKeywordFallback,
			InputModel:    input,
			CanonicalID:   canonical,
			RouteModel:    canonical,
			CatalogVendor: vendor,
		}
	}
	return VendorResolveResult{Kind: VendorResolveNoMatch, InputModel: input}
}

func (r *DefaultVendorResolver) activeIndex() *SnapshotIndex {
	if r == nil || r.active == nil {
		return nil
	}
	idx, _ := r.active.ActiveIndex()
	return idx
}
