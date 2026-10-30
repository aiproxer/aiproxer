package runtimebundle

import (
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// mergeCandidateBuildOptions overlays generation-owned FeatureLifecycles onto a
// shallow copy of process options without mutating Process.
// ReplaceCandidateSurface true replaces FeatureLifecycles even when nil/empty;
// false means a nil overlay field is "no override".
func mergeCandidateBuildOptions(process *BuildOptions, overlay *BuildOptions) *BuildOptions {
	if process == nil && overlay == nil {
		return nil
	}
	if process == nil {
		out := *overlay
		out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
		out.ReplaceCandidateSurface = false
		return &out
	}
	out := *process
	if overlay != nil {
		if overlay.ReplaceCandidateSurface {
			out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
			out.FeaturePlanes, out.CorePorts = overlay.FeaturePlanes, overlay.CorePorts
			out.HostProjections = overlay.HostProjections
		} else {
			if overlay.FeatureLifecycles != nil {
				out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
			}
			if !overlay.FeaturePlanes.IsZero() {
				out.FeaturePlanes = overlay.FeaturePlanes
			}
			out.CorePorts = out.CorePorts.Overlay(overlay.CorePorts)
		}
		if overlay.WireModel != nil {
			out.WireModel = overlay.WireModel
		}
	}
	// Always keep process factory catalog / infra / testing / production / auth.
	out.PluginRegistry = process.PluginRegistry
	out.Startup = process.Startup
	out.Infra = process.Infra
	out.Auth = process.Auth
	out.Policy = process.Policy
	out.Diagnostics = process.Diagnostics
	out.Testing = process.Testing
	out.Production = process.Production
	out.ReplaceCandidateSurface = false
	return &out
}

func prependGeneratedLifecycles(gen, overlay []aipplugin.Lifecycle) []aipplugin.Lifecycle {
	if gen == nil && overlay == nil {
		return nil
	}
	out := make([]aipplugin.Lifecycle, 0, len(gen)+len(overlay))
	out = append(out, gen...)
	out = append(out, overlay...)
	return out
}

// resolveCandidateConvReader resolves the candidate conversation reader and certifies
// stock origin only when the resolved reader is genuinely identical to the standard
// features stock reader. Forged stock origin assertions on non-stock readers are declined.
func resolveCandidateConvReader(ports featurehost.CorePorts, sf *featurehost.Runtime) (conversationprojection.Reader, bool) {
	convReader, stockConvReader := ports.ConversationViewReader, false
	if sf != nil {
		if stockReader := sf.ConversationReader(); convReader == nil {
			convReader, stockConvReader = stockReader, stockReader != nil
		} else if isSameConversationReader(convReader, stockReader) {
			stockConvReader = ports.ConversationReaderStockOrigin && stockReader != nil
		}
	}
	return convReader, stockConvReader
}

func isSameConversationReader(a, b conversationprojection.Reader) bool {
	defer func() { _ = recover() }()
	return a == b
}
