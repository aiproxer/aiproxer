package catalog

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
)

var ErrUnknownModel = errors.New("opencode: unknown model")

var legacyModelAliases = map[string]string{
	"kimi-k2.7": "kimi-k2.7-code",
}

var builtInGoModels = map[string]ModelEntry{
	"omen-alpha": {
		RawID:        "omen-alpha",
		DisplayName:  "Omen Alpha",
		AISDKPackage: "@ai-sdk/openai-compatible",
	},
}

type ResolvedModel struct {
	RawID     string
	WireModel string
	Flavor    Flavor
	Entry     ModelEntry
}

type ModelCatalog struct {
	kind          BackendKind
	prefix        string
	canonicalizer *Canonicalizer
	byCanonical   map[string]ModelEntry
	byNative      map[string]ModelEntry
	byRaw         map[string]ModelEntry
}

func NewModelCatalog(kind BackendKind, entries []ModelEntry, vendors vendor.VendorResolver) *ModelCatalog {
	c := &ModelCatalog{
		kind:          kind,
		prefix:        WirePrefix(kind),
		canonicalizer: NewCanonicalizer(vendors),
		byCanonical:   make(map[string]ModelEntry, len(entries)),
		byNative:      make(map[string]ModelEntry, len(entries)),
		byRaw:         make(map[string]ModelEntry, len(entries)),
	}
	for _, entry := range entries {
		c.addEntry(entry)
	}
	return c
}

func (c *ModelCatalog) addEntry(entry ModelEntry) {
	rawID := strings.TrimSpace(entry.RawID)
	if rawID == "" {
		return
	}
	c.byCanonical[c.canonicalizer.CanonicalID(rawID)] = entry
	c.byNative[NativeID(c.kind, rawID)] = entry
	c.byRaw[rawID] = entry
	c.byRaw[strings.ToLower(rawID)] = entry
}

func (c *ModelCatalog) Resolve(model string) (ResolvedModel, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ResolvedModel{}, fmt.Errorf("%w: empty model", ErrUnknownModel)
	}

	entry, ok := c.lookup(model)
	if !ok {
		return ResolvedModel{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	rawID := strings.TrimSpace(entry.RawID)
	wireModel := rawID
	if target, ok := legacyModelAliases[strings.ToLower(rawID)]; ok {
		wireModel = target
	}
	return ResolvedModel{
		RawID:     rawID,
		WireModel: wireModel,
		Flavor:    InferFlavor(entry),
		Entry:     entry,
	}, nil
}

func (c *ModelCatalog) lookup(model string) (ModelEntry, bool) {
	if entry, ok := c.byNative[model]; ok {
		return entry, true
	}
	if entry, ok := c.byCanonical[model]; ok {
		return entry, true
	}
	if canonical := c.canonicalizer.CanonicalID(model); canonical != model {
		if entry, ok := c.byCanonical[canonical]; ok {
			return entry, true
		}
	}
	if entry, ok := c.byRaw[model]; ok {
		return entry, true
	}
	lower := strings.ToLower(model)
	if entry, ok := c.byRaw[lower]; ok {
		return entry, true
	}
	prefix := c.prefix + "/"
	if raw, ok := strings.CutPrefix(model, prefix); ok {
		if entry, ok := c.byRaw[raw]; ok {
			return entry, true
		}
		if entry, ok := c.byRaw[strings.ToLower(raw)]; ok {
			return entry, true
		}
	}
	raw := model
	if stripped, ok := strings.CutPrefix(model, prefix); ok {
		raw = stripped
	}
	if target, ok := legacyModelAliases[strings.ToLower(raw)]; ok {
		if entry, ok := c.lookup(target); ok {
			return entry, true
		}
	}
	if c.kind == BackendGo {
		if entry, ok := builtInGoModels[strings.ToLower(raw)]; ok {
			return entry, true
		}
	}
	return ModelEntry{}, false
}

func (c *ModelCatalog) entries() []ModelEntry {
	seen := make(map[string]struct{}, len(c.byRaw))
	out := make([]ModelEntry, 0, len(c.byRaw))
	for _, entry := range c.byRaw {
		rawID := entry.RawID
		if _, ok := seen[rawID]; ok {
			continue
		}
		seen[rawID] = struct{}{}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b ModelEntry) int {
		return cmp.Compare(a.RawID, b.RawID)
	})
	return out
}
