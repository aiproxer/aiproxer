package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// HandlerComposer builds a complete standard http.Handler from one focused,
// lifecycle-free StandardHTTPInput projection without binding a listener or
// owning process services. cfg/log are explicit parameters (not carried on the
// input) because prepareStandardHandler needs the frozen candidate config and
// process logger for middleware composition outside the mount groups.
// Implemented by stdhttp to avoid an import cycle with this package (design
// Generation Compiler; task 3.4–3.5).
type HandlerComposer func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error)

// FrozenRoutingView is an immutable routing projection for a generation.
type FrozenRoutingView struct {
	DefaultRoute  string
	RoutePrefixes []string
}
