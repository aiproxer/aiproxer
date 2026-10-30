package compactioncontinuity

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// CauseCompaction is the bounded, content-free label the feature records on a
// request when a continuity projection reached it. The prompt-cache collapse
// diagnostic owns the label vocabulary and ignores a label it does not know.
// It is a fixed diagnostic token: it never carries content.
const CauseCompaction = "compaction"

// recordStructuralCause marks the request with the bounded cause of the
// committed continuity projection. The first structural cause on a request
// wins: it is the earliest change to the cached prefix, and every later change
// is downstream of it.
func recordStructuralCause(call *aipapi.Call) {
	if call == nil || call.StructuralCause != "" {
		return
	}
	call.StructuralCause = CauseCompaction
}
