package engine

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/proxycredentials"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type inventoryEntry struct {
	name     string
	value    string
	category sdk.SourceCategory
}

// collectSingleUserInventory builds the name-preserving env inventory for single-user mode.
// Snapshot is taken once; IncludeEnv may Lookup names absent from the snapshot.
// ExcludeEnv wins over all includes (exact name).
func collectSingleUserInventory(env Environment, opts SingleUserOptions) []inventoryEntry {
	if env == nil {
		return nil
	}

	snap := env.Snapshot()
	byName := parseEnviron(snap)

	selected := make(map[string]inventoryEntry, len(byName))

	for name, value := range byName {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := proxycredentials.MatchProxyCredentialName(name); ok {
			selected[name] = inventoryEntry{
				name:     name,
				value:    value,
				category: sdk.SourceCategoryProxyEnv,
			}
		}
	}

	if opts.IncludePopularEnv {
		for name, value := range byName {
			if strings.TrimSpace(value) == "" {
				continue
			}
			if !isPopularSecretEnvName(name) {
				continue
			}
			if _, exists := selected[name]; exists {
				continue
			}
			selected[name] = inventoryEntry{
				name:     name,
				value:    value,
				category: sdk.SourceCategoryPopularEnv,
			}
		}
	}

	for _, name := range opts.IncludeEnv {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := selected[name]; exists {
			continue
		}
		value, ok := byName[name]
		if !ok {
			value, ok = env.Lookup(name)
		}
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		selected[name] = inventoryEntry{
			name:     name,
			value:    value,
			category: sdk.SourceCategoryOperatorEnv,
		}
	}

	for _, name := range opts.ExcludeEnv {
		delete(selected, name)
	}

	out := make([]inventoryEntry, 0, len(selected))
	for _, e := range selected {
		out = append(out, e)
	}
	return out
}

func parseEnviron(pairs []string) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			continue
		}
		out[name] = value
	}
	return out
}

func catalogInputsFromInventory(in []inventoryEntry) []CatalogInput {
	out := make([]CatalogInput, 0, len(in))
	for _, e := range in {
		out = append(out, CatalogInput{
			Name:              e.name,
			Value:             e.value,
			KnownPublicPrefix: detectKnownPublicPrefix(e.value),
			SourceCategory:    e.category,
		})
	}
	return out
}
