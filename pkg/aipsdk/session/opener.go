package session

import (
	"context"
	"maps"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// OpenInput is the read-only context passed to [Opener.Open] before submit-time shaping (design §3).
type OpenInput struct {
	TraceID   string
	Principal execview.PrincipalView
	Session   SessionView
}

// OpenResult carries optional session metadata upserts from one opener invocation.
type OpenResult struct {
	SessionLabelUpserts map[string]string
}

// Merge combines label upserts from other into r (other wins on key collision).
func (r OpenResult) Merge(other OpenResult) OpenResult {
	out := OpenResult{}
	if len(r.SessionLabelUpserts) > 0 {
		out.SessionLabelUpserts = maps.Clone(r.SessionLabelUpserts)
	}
	if len(other.SessionLabelUpserts) > 0 {
		if out.SessionLabelUpserts == nil {
			out.SessionLabelUpserts = map[string]string{}
		}
		maps.Copy(out.SessionLabelUpserts, other.SessionLabelUpserts)
	}
	return out
}

// Opener runs during the session_open stage after transport identity and before submit hooks (R5, R12).
type Opener interface {
	ID() string
	Open(ctx context.Context, in OpenInput) (OpenResult, error)
}
