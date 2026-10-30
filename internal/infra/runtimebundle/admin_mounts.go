package runtimebundle

import (
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// adminMountsForGeneration projects the protected operator mounts one
// generation serves: the caller-registered Enterprise mounts snapshotted at
// the process boundary. The open-source runtime adds no billing surface of its
// own; the Enterprise composition registers its configured billing handler as
// one of these generic mounts, and stdhttp validates every path and mounts it
// behind the existing admin authentication.
func adminMountsForGeneration(cand *candidateAssembly) []httpcontract.AdminMount {
	if cand == nil {
		return nil
	}
	if ps := cand.process.ps; ps != nil && ps.opts != nil {
		return httpcontract.CloneAdminMounts(ps.opts.Production.AdminMounts)
	}
	return nil
}
