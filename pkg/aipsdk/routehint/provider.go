package routehint

import (
	"context"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// Provider emits advisory routing preferences (design §12).
type Provider interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Hint(ctx context.Context, in Input) (Result, error)
}
