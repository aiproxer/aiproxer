package runtimebundle

import (
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
)

// observationWriterInput is the exact declaration input of the pure
// observation-writer selector. Every field is a declaration, never a callback:
// the selector inspects what a caller asked for and never opens, closes,
// starts or invokes a writer.
type observationWriterInput = coremetering.ObservationWriterInput

// selectObservationWriter is the pure, pre-acquisition observation-writer
// selector. It is the runtime-side view of the canonical selector and is used
// only where a writer pair is being declared, never to reselect a legacy
// recorder-only runtime input.
func selectObservationWriter(in observationWriterInput) (coremetering.ObservationWriterSelection, error) {
	return coremetering.SelectObservationWriter(in)
}
