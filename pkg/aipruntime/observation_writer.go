package aipruntime

import (
	"fmt"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ObservationWriterSelection is the neutral declaration of the process's single
// observation writer. It names no writer implementation and no storage: it only
// states which writer the process may run, so an explicitly borrowed external
// pair and the configuration-backed legacy writer can never both be active.
type ObservationWriterSelection = sdkmetering.ObservationWriterSelection

const (
	// ObservationWriterUnspecified declares no selected writer. It stays valid
	// only for callers that carry no explicit recorder/sink pair.
	ObservationWriterUnspecified = sdkmetering.ObservationWriterUnspecified
	// ObservationWriterExternal selects the explicitly borrowed external pair as
	// the process's only observation writer.
	ObservationWriterExternal = sdkmetering.ObservationWriterExternal
	// ObservationWriterLegacy selects the configuration-backed legacy writer.
	ObservationWriterLegacy = sdkmetering.ObservationWriterLegacy
	// ObservationWriterNone declares that the process runs no observation writer.
	ObservationWriterNone = sdkmetering.ObservationWriterNone
)

// ErrObservationWriterConflict identifies a rejected observation-writer
// declaration: two writers for one process, or an incomplete explicit pair.
var ErrObservationWriterConflict = sdkmetering.ErrObservationWriterConflict

// validateObservationWriterDeclaration refuses an incomplete or mismatched
// observation-writer declaration before the runtime acquires anything.
func validateObservationWriterDeclaration(opts Options) error {
	if err := coremetering.ValidateObservationWriterDeclaration(opts.MeteringObservationWriterSelection, opts.MeteringRecorder, opts.MeteringObservationSink); err != nil {
		return fmt.Errorf("aipruntime: %w", err)
	}
	return nil
}
