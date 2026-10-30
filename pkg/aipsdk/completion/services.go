package completion

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// Services exposes narrow capabilities for completion gates (design §2, §6).
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}
