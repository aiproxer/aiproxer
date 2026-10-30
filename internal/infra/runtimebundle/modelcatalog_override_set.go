package runtimebundle

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

// OverrideSetFromModelCatalog maps operator model_catalog YAML override rows to [modelcatalog.OverrideSet].
// Backend+model rows take precedence at resolve time over model-only keys per [modelcatalog.OverrideResolver].
func OverrideSetFromModelCatalog(mc config.ModelCatalogConfig) modelcatalog.OverrideSet {
	pair := make(map[string]modelcatalog.ModelFacts, len(mc.BackendModelOverrides))
	for _, o := range mc.BackendModelOverrides {
		b := strings.TrimSpace(o.Backend)
		m := strings.TrimSpace(o.Model)
		if b == "" || m == "" {
			continue
		}
		pair[b+":"+m] = modelFactsFromBackendModelOverrideEntry(o)
	}
	model := make(map[string]modelcatalog.ModelFacts, len(mc.ModelOverrides))
	for _, o := range mc.ModelOverrides {
		m := strings.TrimSpace(o.Model)
		if m == "" {
			continue
		}
		model[m] = modelFactsFromModelOverrideEntry(o)
	}
	return modelcatalog.OverrideSet{Pair: pair, Model: model}
}

func modelFactsFromModelOverrideEntry(e config.ModelCatalogModelOverrideEntry) modelcatalog.ModelFacts {
	return modelFactsFromOverrideYAML(
		e.Tools, e.StructuredOutputs, e.Reasoning, e.Vision, e.Documents,
		e.ContextLimitTokens, e.InputLimitTokens, e.OutputLimitTokens,
		modelcatalog.FactSourceModelOverride,
	)
}

func modelFactsFromBackendModelOverrideEntry(e config.ModelCatalogBackendModelOverrideEntry) modelcatalog.ModelFacts {
	return modelFactsFromOverrideYAML(
		e.Tools, e.StructuredOutputs, e.Reasoning, e.Vision, e.Documents,
		e.ContextLimitTokens, e.InputLimitTokens, e.OutputLimitTokens,
		modelcatalog.FactSourcePairOverride,
	)
}

func modelFactsFromOverrideYAML(
	tools, structuredOutputs, reasoning, vision, documents *bool,
	contextLimit, inputLimit, outputLimit *int64,
	src modelcatalog.FactSource,
) modelcatalog.ModelFacts {
	return modelcatalog.ModelFacts{
		Tools:             optionalBoolToTriState(tools),
		StructuredOutputs: optionalBoolToTriState(structuredOutputs),
		Reasoning:         optionalBoolToTriState(reasoning),
		Vision:            optionalBoolToTriState(vision),
		Documents:         optionalBoolToTriState(documents),
		ContextLimit:      optionalPositiveLimit(contextLimit),
		InputLimit:        optionalPositiveLimit(inputLimit),
		OutputLimit:       optionalPositiveLimit(outputLimit),
		Source:            src,
		MatchKind:         modelcatalog.MatchExact,
	}
}

func optionalBoolToTriState(v *bool) modelcatalog.CapabilityTriState {
	if v == nil {
		return modelcatalog.CapabilityUnknown
	}
	if *v {
		return modelcatalog.CapabilitySupported
	}
	return modelcatalog.CapabilityUnsupported
}

func optionalPositiveLimit(v *int64) modelcatalog.LimitFact {
	if v == nil || *v <= 0 {
		return modelcatalog.LimitFact{State: modelcatalog.LimitUnknown}
	}
	return modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: *v}
}
