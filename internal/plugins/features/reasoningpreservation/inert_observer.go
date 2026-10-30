package reasoningpreservation

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// inertStreamObserver is a package-private no-op used when ResolveMatch is not eligible.
// It performs no event parsing, buffering, store I/O, or telemetry.
type inertStreamObserver struct{}

func (inertStreamObserver) Observe(context.Context, aipapi.Event) error { return nil }

func (inertStreamObserver) Finish(context.Context, response.StreamOutcome) error { return nil }
