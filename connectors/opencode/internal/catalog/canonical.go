package catalog

import (
	"strings"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
)

var canonicalAliases = map[string]string{
	"kimi-k2.7": "kimi-k2.7-code",
}

type Canonicalizer struct {
	vendors vendor.VendorResolver
}

func NewCanonicalizer(vendors vendor.VendorResolver) *Canonicalizer {
	if vendors == nil {
		vendors = vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)
	}
	return &Canonicalizer{vendors: vendors}
}

func (c *Canonicalizer) CanonicalID(rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return "unknown/unknown"
	}
	model := canonicalModelName(rawID)
	if c.vendors != nil {
		if got := c.vendors.Resolve(model).CanonicalID; got != "" {
			return got
		}
	}
	return "unknown/" + rawID
}

func canonicalModelName(rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if aliased, ok := canonicalAliases[strings.ToLower(rawID)]; ok {
		return aliased
	}
	return rawID
}
