package providerprofiles

import (
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Binding names the already-certified adapter family. It contains no factory
// or lifecycle function, so one family binding serves every profile.
type Binding struct {
	Family      Family
	FactoryKind string
}

func FamilyBinding(f Family) (Binding, error) {
	switch f {
	case FamilyOpenAIChat:
		return Binding{Family: f, FactoryKind: "custom-openai-legacy-compatible"}, nil
	case FamilyOpenAIResponses:
		return Binding{Family: f, FactoryKind: "custom-openai-responses-compatible"}, nil
	case FamilyAnthropic:
		return Binding{Family: f, FactoryKind: "custom-anthropic-compatible"}, nil
	case FamilyOpenResponses:
		return Binding{Family: f, FactoryKind: "custom-openresponses-compatible"}, nil
	case FamilySystemOne:
		return Binding{Family: f, FactoryKind: "custom-systemone-compatible"}, nil
	default:
		return Binding{}, fmt.Errorf("unknown provider family %q", f)
	}
}

// CompiledProfile is the immutable composition input produced by validation.
// Static profile values are copied so catalog callers cannot mutate a compiled
// generation through their original slices.
type CompiledProfile struct {
	Profile      Profile
	Binding      Binding
	Capabilities aipapi.BackendCaps
	Dialects     aipapi.DialectSupport
}

func CompileProfile(p Profile) (CompiledProfile, error) {
	p = cloneProfile(p)
	compiled, err := Compile(p)
	if err != nil {
		return CompiledProfile{}, err
	}
	binding, err := FamilyBinding(p.Family)
	if err != nil {
		return CompiledProfile{}, err
	}
	return CompiledProfile{Profile: p, Binding: binding, Capabilities: compiled.Capabilities, Dialects: compiled.Dialects}, nil
}

func cloneProfile(p Profile) Profile {
	p.Headers = slices.Clone(p.Headers)
	p.Quirks = slices.Clone(p.Quirks)
	p.Models.Static = slices.Clone(p.Models.Static)
	p.Capabilities.Enable = slices.Clone(p.Capabilities.Enable)
	p.Capabilities.Disable = slices.Clone(p.Capabilities.Disable)
	p.Dialects.Item = slices.Clone(p.Dialects.Item)
	p.Dialects.Reasoning = slices.Clone(p.Dialects.Reasoning)
	p.Dialects.Compaction = slices.Clone(p.Dialects.Compaction)
	p.Dialects.Extensions = slices.Clone(p.Dialects.Extensions)
	return p
}

func (c *Catalog) CompileAll() ([]CompiledProfile, error) {
	if c == nil {
		return nil, fmt.Errorf("nil profile catalog")
	}
	out := make([]CompiledProfile, 0, len(c.profiles))
	for _, p := range c.profiles {
		compiled, err := CompileProfile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, compiled)
	}
	return out, nil
}
