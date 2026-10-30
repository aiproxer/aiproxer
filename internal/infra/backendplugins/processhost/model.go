package processhost

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ProcessModel is the declared ownership model for supervised plugin processes.
type ProcessModel string

const (
	ProcessModelPerInstance    ProcessModel = "per_instance"
	ProcessModelSharedArtifact ProcessModel = "shared_artifact"
)

// SharingOptions gates shared-process reuse.
type SharingOptions struct {
	IsolationDeclared   bool
	ConcurrencyDeclared bool
}

func ProcessModelFromSharing(s backendplugin.ProcessSharing, opt SharingOptions) (ProcessModel, error) {
	switch s {
	case backendplugin.ProcessSharingPerInstance:
		return ProcessModelPerInstance, nil
	case backendplugin.ProcessSharingSharedArtifact:
		if !opt.IsolationDeclared || !opt.ConcurrencyDeclared {
			return "", fmt.Errorf("%w: shared artifact requires isolation and concurrency declaration", ReasonProcessModelViolation)
		}
		return ProcessModelSharedArtifact, nil
	default:
		return "", fmt.Errorf("%w: process_sharing unspecified", ReasonProcessModelViolation)
	}
}

// OwnershipKey uniquely identifies the supervised process slot.
func OwnershipKey(model ProcessModel, artifactDigest, instanceID string) string {
	switch model {
	case ProcessModelPerInstance:
		return "pi:" + artifactDigest + ":" + instanceID
	case ProcessModelSharedArtifact:
		return "sa:" + artifactDigest
	default:
		return "reject:" + artifactDigest + ":" + instanceID
	}
}
