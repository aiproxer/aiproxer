package extensions

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// LegalPipelineStageNames returns the core-owned ordered list of legal extension stages (R2).
// Each call allocates a fresh slice; see feature.LegalPipelineStageIDs.
func LegalPipelineStageNames() []string {
	return feature.LegalPipelineStageIDs()
}

// LegalStageDescriptors returns the SDK canonical stage descriptor table (read-only).
func LegalStageDescriptors() []feature.StageDescriptor {
	return feature.LegalStageDescriptors()
}

// StageDescriptorByID returns the descriptor for a legal stage id.
func StageDescriptorByID(id string) (feature.StageDescriptor, bool) {
	return feature.StageDescriptorByID(id)
}

// ValidateStageID reports whether id is a legal pipeline stage.
func ValidateStageID(id string) bool {
	return feature.ValidateStageID(id)
}
