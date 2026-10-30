package completion

import (
	"context"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// Gate performs whole-completion control after bounded buffering (R8, design §6).
type Gate interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, meta Meta, buf Buffered, svc Services) (Outcome, error)
}
