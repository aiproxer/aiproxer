package runtime

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// causeSystemPrompt is the bounded, content-free label the runtime records on
// a request when the conversation bootstrap committed an effective system or
// developer instruction change for it. The prompt-cache collapse diagnostic
// owns the full vocabulary and ignores labels it does not know. It is a fixed
// diagnostic token: it never carries content.
const causeSystemPrompt = "system-prompt"

// bootstrapStructuralCausePermitted reports whether the conversation bootstrap
// may publish a label on the canonical request. The port's only permitted
// publication is the bootstrap's own system-prompt token. Any other label,
// including a short in-bounds non-vocabulary string, is dropped rather than
// placed on the request or its usage samples. The label is advisory: it
// changes no request content, routing, stream or billing.
func bootstrapStructuralCausePermitted(cause string) bool {
	return cause == causeSystemPrompt
}

// noteStructuralCause records the bootstrap's bounded structural cause on the
// live request. The first cause on a request wins: it is the earliest change
// to the cached prefix, and every later change is downstream of it. A label
// that cannot cross the call boundary safely — empty, beyond the canonical
// bound, or outside the bootstrap's permitted vocabulary — is dropped rather
// than applied, so a composition callback can never fail a request or publish
// an unrecognized label by returning a hostile string.
func noteStructuralCause(call *aipapi.Call, cause string) {
	if call == nil {
		return
	}
	cause = strings.TrimSpace(cause)
	if cause == "" || len(cause) > aipapi.MaxStructuralCauseBytes || call.StructuralCause != "" {
		return
	}
	if !bootstrapStructuralCausePermitted(cause) {
		return
	}
	call.StructuralCause = cause
}
