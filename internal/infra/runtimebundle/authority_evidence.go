package runtimebundle

import (
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/evidencesink"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// buildAuthorityEvidenceSink resolves the process evidence projection for the
// injected usage-limit port: a caller-supplied sink wins; otherwise the
// control-plane recorder and/or the operator policy observers are adapted;
// with neither, there is no sink and the host attaches nothing.
func buildAuthorityEvidenceSink(cp *controlPlaneRuntime, policyObs policydecision.Observer, opts *BuildOptions) authority.EvidenceSink {
	if opts != nil && opts.Production.EvidenceSink != nil {
		return opts.Production.EvidenceSink
	}
	var recorder *controlplane.RecorderService
	if cp != nil {
		recorder = cp.recorder
	}
	hasOperatorObservers := opts != nil && (len(opts.Policy.PolicyObservers) > 0 || len(opts.Production.PolicyObservers) > 0)
	if recorder == nil && !hasOperatorObservers {
		return nil
	}
	return evidencesink.New(recorder, policyObs)
}
