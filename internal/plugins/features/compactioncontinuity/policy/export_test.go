package policy

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// WithSecureTurn is a test-only helper for the trusted secure-session policy seam.
func WithSecureTurn(ctx context.Context, transcriptEnabled bool) context.Context {
	return session.WithSecureTurnPolicy(ctx, session.SecureTurnPolicyView{TranscriptEnabled: transcriptEnabled})
}
