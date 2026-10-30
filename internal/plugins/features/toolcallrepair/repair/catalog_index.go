package repair

import (
	"bytes"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type CatalogIndex struct {
	exact      map[string]aipapi.ToolDef
	uniqueNorm map[string]aipapi.ToolDef
}

func BuildCatalogIndex(catalog []aipapi.ToolDef) *CatalogIndex {
	idx := &CatalogIndex{
		exact:      make(map[string]aipapi.ToolDef, len(catalog)),
		uniqueNorm: make(map[string]aipapi.ToolDef, len(catalog)),
	}
	normCounts := make(map[string]int, len(catalog))
	normFirst := make(map[string]aipapi.ToolDef, len(catalog))
	for _, tool := range catalog {
		owned := cloneToolDef(tool)
		idx.exact[owned.Name] = owned
		norm := NormalizeASCIIName(owned.Name)
		if norm == "" {
			continue
		}
		normCounts[norm]++
		if _, ok := normFirst[norm]; !ok {
			normFirst[norm] = owned
		}
	}
	for norm, n := range normCounts {
		if n == 1 {
			idx.uniqueNorm[norm] = normFirst[norm]
		}
	}
	return idx
}

func cloneToolDef(tool aipapi.ToolDef) aipapi.ToolDef {
	if tool.Parameters != nil {
		tool.Parameters = bytes.Clone(tool.Parameters)
	}
	return tool
}

func (idx *CatalogIndex) Exact(name string) (aipapi.ToolDef, bool) {
	if idx == nil {
		return aipapi.ToolDef{}, false
	}
	tool, ok := idx.exact[name]
	return tool, ok
}

func (idx *CatalogIndex) UniqueNormalized(name string) (aipapi.ToolDef, bool) {
	if idx == nil {
		return aipapi.ToolDef{}, false
	}
	tool, ok := idx.uniqueNorm[NormalizeASCIIName(name)]
	return tool, ok
}
