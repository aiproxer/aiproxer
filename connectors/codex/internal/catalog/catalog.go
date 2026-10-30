package catalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// defaultReasoningEffort is the global default reasoning effort. It is a
// constant, not a model slug, so it is not part of "hardcoded models".
const defaultReasoningEffort = "medium"

// Profile is a per-model reasoning profile parsed from `codex debug models`.
type Profile struct {
	Slug                     string
	DefaultReasoningLevel    string
	SupportedReasoningLevels []string
	Visibility               string
	SupportedInAPI           bool
	ContextWindow            int
	MaxContextWindow         int
	// These fields are connector-private Codex catalog metadata. They are not
	// part of the root model inventory contract.
	AutoCompactTokenLimit int64
	CompHash              string
}

// APIAccepted reports whether the slug is selectable through the Codex
// Responses API (visible and API-supported). CLI-only and hidden entries are
// present in the catalog but not routable.
func (p Profile) APIAccepted() bool {
	return p.SupportedInAPI && p.Visibility != "hide"
}

// Supports reports whether the model accepts the given reasoning effort.
func (p Profile) Supports(effort string) bool {
	return slices.Contains(p.SupportedReasoningLevels, effort)
}

// Catalog is the parsed Codex model catalog. It is built by [Parse] and is
// safe for concurrent read access after construction.
type Catalog struct {
	profiles           map[string]Profile
	routable           []string
	effortOrder        []string
	effortDescriptions map[string]string
}

// RoutableSlugs returns API-accepted slugs in catalog priority order.
func (c *Catalog) RoutableSlugs() []string {
	if c == nil {
		return nil
	}
	return slices.Clone(c.routable)
}

// RoutableSlugsOrFallback returns cat.RoutableSlugs() when cat is non-nil,
// otherwise the shipped fallback catalog's routable slugs. Returns nil only
// when both the provided catalog and the fallback load are unavailable.
func RoutableSlugsOrFallback(cat *Catalog) []string {
	if cat != nil {
		return cat.RoutableSlugs()
	}
	if fallback, err := LoadFallback(""); err == nil {
		return fallback.RoutableSlugs()
	}
	return nil
}

// Profile returns the profile for slug (case-insensitive) and whether it exists.
func (c *Catalog) Profile(slug string) (Profile, bool) {
	if c == nil {
		return Profile{}, false
	}
	p, ok := c.profiles[strings.ToLower(strings.TrimSpace(slug))]
	return p, ok
}

// IsSupported reports whether slug is an API-accepted routable model.
func (c *Catalog) IsSupported(slug string) bool {
	p, ok := c.Profile(slug)
	return ok && p.APIAccepted()
}

// ReasoningEffortOrder returns the discovered effort hierarchy (low -> ultra),
// derived from the widest model's supported_reasoning_levels. No effort
// hierarchy is hardcoded.
func (c *Catalog) ReasoningEffortOrder() []string {
	if c == nil {
		return nil
	}
	return slices.Clone(c.effortOrder)
}

// ReasoningEffortDescription returns the verbatim CLI description for an effort.
func (c *Catalog) ReasoningEffortDescription(effort string) string {
	if c == nil || c.effortDescriptions == nil {
		return ""
	}
	return c.effortDescriptions[effort]
}

// DefaultReasoningEffort returns the global default reasoning effort ("medium").
func (c *Catalog) DefaultReasoningEffort() string {
	return defaultReasoningEffort
}

// ModelsSupporting returns routable slugs whose profile supports the effort.
func (c *Catalog) ModelsSupporting(effort string) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, slug := range c.routable {
		if p, ok := c.profiles[strings.ToLower(slug)]; ok && p.Supports(effort) {
			out = append(out, slug)
		}
	}
	return out
}

// wireLevel is a single supported_reasoning_levels entry.
type wireLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// wireModel is a single model entry in the `codex debug models` payload.
type wireModel struct {
	Slug                     string          `json:"slug"`
	DefaultReasoningLevel    string          `json:"default_reasoning_level"`
	SupportedReasoningLevels []wireLevel     `json:"supported_reasoning_levels"`
	Visibility               string          `json:"visibility"`
	SupportedInAPI           *bool           `json:"supported_in_api"`
	ContextWindow            json.RawMessage `json:"context_window"`
	MaxContextWindow         json.RawMessage `json:"max_context_window"`
	AutoCompactTokenLimit    json.RawMessage `json:"auto_compact_token_limit"`
	CompHash                 json.RawMessage `json:"comp_hash"`
}

type wireCatalog struct {
	Models []json.RawMessage `json:"models"`
}

// Parse parses raw `codex debug models` JSON into a *Catalog. Entries missing
// a slug or usable reasoning levels (or that fail to decode) are skipped. The
// reasoning-effort order is derived from the widest model's
// supported_reasoning_levels.
func Parse(raw []byte) (*Catalog, error) {
	var wire wireCatalog
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("codexcatalog: parse: %w", err)
	}
	profiles := make(map[string]Profile, len(wire.Models))
	var routable []string
	descriptions := make(map[string]string, 8)
	var widest []string
	for _, modelRaw := range wire.Models {
		var m wireModel
		if err := json.Unmarshal(modelRaw, &m); err != nil {
			continue // skip malformed model entries
		}
		slug := strings.TrimSpace(m.Slug)
		if slug == "" {
			continue
		}
		levels := make([]string, 0, len(m.SupportedReasoningLevels))
		for _, lv := range m.SupportedReasoningLevels {
			e := strings.TrimSpace(lv.Effort)
			if e == "" {
				continue
			}
			levels = append(levels, e)
			if desc := strings.TrimSpace(lv.Description); desc != "" {
				if _, ok := descriptions[e]; !ok {
					descriptions[e] = desc
				}
			}
		}
		if len(levels) == 0 {
			continue
		}
		visibility := strings.TrimSpace(m.Visibility)
		if visibility == "" {
			visibility = "list"
		}
		supportedInAPI := true
		if m.SupportedInAPI != nil {
			supportedInAPI = *m.SupportedInAPI
		}
		defaultLevel := strings.TrimSpace(m.DefaultReasoningLevel)
		if defaultLevel == "" {
			defaultLevel = defaultReasoningEffort
		}
		p := Profile{
			Slug:                     slug,
			DefaultReasoningLevel:    defaultLevel,
			SupportedReasoningLevels: levels,
			Visibility:               visibility,
			SupportedInAPI:           supportedInAPI,
			ContextWindow:            parseNonNegativeInt(m.ContextWindow),
			MaxContextWindow:         parseNonNegativeInt(m.MaxContextWindow),
			AutoCompactTokenLimit:    parseNonNegativeInt64(m.AutoCompactTokenLimit),
			CompHash:                 parseString(m.CompHash),
		}
		key := strings.ToLower(slug)
		if _, exists := profiles[key]; exists {
			continue
		}
		profiles[key] = p
		if p.APIAccepted() {
			routable = append(routable, slug)
		}
		if len(levels) > len(widest) {
			widest = levels
		}
	}
	return &Catalog{
		profiles:           profiles,
		routable:           routable,
		effortOrder:        widest,
		effortDescriptions: descriptions,
	}, nil
}

func parseNonNegativeInt(raw json.RawMessage) int {
	v := parseNonNegativeInt64(raw)
	if v > int64(^uint(0)>>1) {
		return 0
	}
	return int(v)
}

func parseNonNegativeInt64(raw json.RawMessage) int64 {
	var v int64
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v < 0 {
		return 0
	}
	return v
}

func parseString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
