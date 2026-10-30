package engine

import (
	"cmp"
	"crypto/sha256"
	"slices"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const defaultMinSecretBytes = 8

// CatalogInput is one named secret value for catalog construction.
// Value bytes are copied into private storage; callers should not rely on Catalog exposing them.
type CatalogInput struct {
	Name              string
	Aliases           []string
	Value             string
	KnownPublicPrefix string
	SourceCategory    sdk.SourceCategory
}

type catalogEntry struct {
	value             []byte
	knownPublicPrefix []byte
	primaryName       string
	aliases           []string
	sourceCategory    sdk.SourceCategory
}

// Catalog is an immutable, opaque set of secret entries. No method returns secret values.
type Catalog struct {
	entries []catalogEntry
}

// BuildCatalog copies qualifying inputs into a frozen catalog.
// Values shorter than minSecretBytes are dropped (default 8 when minSecretBytes <= 0).
// Identical values are deduplicated: primary ref is the lexicographically first name;
// remaining names become sorted aliases.
func BuildCatalog(raw []CatalogInput, minSecretBytes int) (*Catalog, error) {
	if minSecretBytes <= 0 {
		minSecretBytes = defaultMinSecretBytes
	}

	type accum struct {
		value             []byte
		knownPublicPrefix []byte
		names             map[string]struct{}
		nameCategory      map[string]sdk.SourceCategory
	}
	byHash := make(map[[32]byte][]*accum, len(raw))

	for i := range raw {
		in := &raw[i]
		if len(in.Value) < minSecretBytes {
			continue
		}
		name := in.Name
		if name == "" {
			continue
		}

		val := make([]byte, len(in.Value))
		copy(val, in.Value)
		sum := sha256.Sum256(val)
		var a *accum
		for _, existing := range byHash[sum] {
			if slices.Equal(existing.value, val) {
				a = existing
				break
			}
		}
		if a == nil {
			var prefix []byte
			if in.KnownPublicPrefix != "" {
				prefix = append([]byte(nil), in.KnownPublicPrefix...)
			}
			a = &accum{
				value:             val,
				knownPublicPrefix: prefix,
				names:             make(map[string]struct{}, 1+len(in.Aliases)),
				nameCategory:      make(map[string]sdk.SourceCategory, 1+len(in.Aliases)),
			}
			byHash[sum] = append(byHash[sum], a)
		} else {
			clearBytes(val)
		}

		a.names[name] = struct{}{}
		if _, exists := a.nameCategory[name]; !exists {
			a.nameCategory[name] = in.SourceCategory
		}
		for _, alias := range in.Aliases {
			if alias == "" {
				continue
			}
			a.names[alias] = struct{}{}
			if _, exists := a.nameCategory[alias]; !exists {
				a.nameCategory[alias] = in.SourceCategory
			}
		}
	}

	entries := make([]catalogEntry, 0, len(byHash))
	for _, group := range byHash {
		for _, a := range group {
			names := make([]string, 0, len(a.names))
			for n := range a.names {
				names = append(names, n)
			}
			slices.Sort(names)
			primary := names[0]
			var aliases []string
			if len(names) > 1 {
				aliases = append([]string(nil), names[1:]...)
			}
			cat := a.nameCategory[primary]
			if cat == "" {
				cat = sdk.SourceCategoryUnknown
			}
			entries = append(entries, catalogEntry{
				value:             a.value,
				knownPublicPrefix: a.knownPublicPrefix,
				primaryName:       primary,
				aliases:           aliases,
				sourceCategory:    cat,
			})
		}
	}

	slices.SortFunc(entries, func(a, b catalogEntry) int {
		if c := cmp.Compare(len(b.value), len(a.value)); c != 0 {
			return c
		}
		return cmp.Compare(a.primaryName, b.primaryName)
	})

	return &Catalog{entries: entries}, nil
}

// EntryCount returns the number of distinct secret values in the catalog.
func (c *Catalog) EntryCount() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// SourceCategories returns sorted unique source-category labels (safe inventory metadata).
func (c *Catalog) SourceCategories() []string {
	if c == nil || len(c.entries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(c.entries))
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		cat := string(e.sourceCategory)
		if cat == "" {
			cat = string(sdk.SourceCategoryUnknown)
		}
		if _, ok := seen[cat]; ok {
			continue
		}
		seen[cat] = struct{}{}
		out = append(out, cat)
	}
	slices.Sort(out)
	return out
}

func clearBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
