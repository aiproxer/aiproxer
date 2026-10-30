package main

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// mandatoryStandardPlugins lists plugins that must appear in loaded configuration for the
// reference distribution composition root (IDs must align with config/config.yaml).
func mandatoryStandardPlugins() []aipsdk.Requirement {
	return aipsdk.StandardDistributionRequirements()
}

// stockEnterpriseCapabilities declares the Enterprise-only configuration
// capabilities the stock command composes in-tree. It composes none: billing
// and usage limits are provided only by an injected Enterprise composition, so
// their configuration keys are refused by validation instead of being silently
// ignored.
func stockEnterpriseCapabilities() config.CapabilitySet {
	return nil
}
