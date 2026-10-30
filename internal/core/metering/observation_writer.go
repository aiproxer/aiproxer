package metering

import (
	"fmt"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// The canonical neutral observation-writer enum lives in the public metering
// SDK, because the public runtime options carry it and a public contract may
// not expose an internal package path. The internal names below stay the
// canonical spelling for the runtime and the Enterprise composition.
type (
	// ObservationWriterSelection is the neutral declaration of the process's
	// single observation writer.
	ObservationWriterSelection = sdkmetering.ObservationWriterSelection
)

// ErrObservationWriterConflict identifies a rejected observation-writer
// declaration. It is the same sentinel value the public SDK exposes, so
// errors.Is matches across both spellings.
var ErrObservationWriterConflict = sdkmetering.ErrObservationWriterConflict

const (
	// ObservationWriterUnspecified declares no selected writer.
	ObservationWriterUnspecified = sdkmetering.ObservationWriterUnspecified
	// ObservationWriterExternal selects the explicitly borrowed external pair.
	ObservationWriterExternal = sdkmetering.ObservationWriterExternal
	// ObservationWriterLegacy selects the configuration-backed legacy writer.
	ObservationWriterLegacy = sdkmetering.ObservationWriterLegacy
	// ObservationWriterNone declares that the process runs no observation writer.
	ObservationWriterNone = sdkmetering.ObservationWriterNone
)

// IsNilObservationPort reports whether an observation port declaration is
// absent: a nil interface or an interface holding a typed nil pointer. A
// typed-nil port would panic on first use, so every composition boundary
// treats it as absent.
func IsNilObservationPort(port any) bool {
	return isNilLateEconomicSink(port)
}

// ObservationWriterInput is the exact declaration input of the pure writer
// selector. Every field is a declaration, never a callback: the selector
// inspects what a caller asked for and never opens, closes, starts or invokes
// anything.
type ObservationWriterInput struct {
	// ExplicitRecorder reports an explicitly supplied durable recorder.
	ExplicitRecorder bool
	// ExplicitSink reports an explicitly supplied atomic observation sink.
	ExplicitSink bool
	// LegacyEconomicWriter reports an explicitly requested legacy economic
	// observation writer.
	LegacyEconomicWriter bool
}

// SelectObservationWriter is the pure, pre-acquisition writer selector. An
// unequal explicit recorder/sink declaration is a typed conflict: a sink
// without its recorder, or a recorder without its sink, is never silently
// completed from configuration. A complete explicit pair conflicts with an
// explicitly requested legacy economic writer, because one process may run
// exactly one observation writer. The selector returns the neutral selection
// otherwise and never touches a resource.
func SelectObservationWriter(in ObservationWriterInput) (ObservationWriterSelection, error) {
	if in.ExplicitRecorder != in.ExplicitSink {
		return ObservationWriterUnspecified, fmt.Errorf("%w: an explicit observation writer is a complete pair (recorder=%t sink=%t)",
			ErrObservationWriterConflict, in.ExplicitRecorder, in.ExplicitSink)
	}
	if in.ExplicitRecorder && in.ExplicitSink {
		if in.LegacyEconomicWriter {
			return ObservationWriterUnspecified, fmt.Errorf("%w: an explicit observation pair conflicts with the legacy economic observation writer",
				ErrObservationWriterConflict)
		}
		return ObservationWriterExternal, nil
	}
	if in.LegacyEconomicWriter {
		return ObservationWriterLegacy, nil
	}
	return ObservationWriterNone, nil
}

// ValidateObservationWriterSelection validates one carried writer selection
// against the concrete declarations it has to agree with. It is the single
// place where a carried selection is checked for legality, so the public build
// and the runtime preflight classify the same declaration identically.
//
// The rules are:
//
//   - External requires a complete, non-typed-nil recorder/sink pair and
//     refuses a competing legacy economic writer.
//   - Legacy refuses an external observation sink.
//   - None and Unspecified refuse an explicit recorder/sink pair; a
//     recorder-only injection stays the valid legacy contract for both.
//   - An explicit sink without its recorder is an incomplete pair and is
//     refused by every selection.
//
// The function inspects declarations only. It never opens, closes, starts or
// invokes a writer.
func ValidateObservationWriterSelection(selection ObservationWriterSelection, in ObservationWriterInput) error {
	switch selection {
	case ObservationWriterExternal:
		if !in.ExplicitRecorder || !in.ExplicitSink {
			return fmt.Errorf("%w: the external observation writer requires a complete recorder and sink", ErrObservationWriterConflict)
		}
		if in.LegacyEconomicWriter {
			return fmt.Errorf("%w: the external observation pair conflicts with the legacy economic observation writer", ErrObservationWriterConflict)
		}
	case ObservationWriterLegacy:
		if in.ExplicitSink {
			return fmt.Errorf("%w: the legacy observation writer refuses an external observation sink", ErrObservationWriterConflict)
		}
	case ObservationWriterNone, ObservationWriterUnspecified:
		if in.ExplicitRecorder && in.ExplicitSink {
			return fmt.Errorf("%w: the %s observation writer refuses an explicit recorder and sink pair", ErrObservationWriterConflict, selection)
		}
	default:
		return fmt.Errorf("%w: unknown observation writer selection %d", ErrObservationWriterConflict, int(selection))
	}
	if in.ExplicitSink && !in.ExplicitRecorder {
		return fmt.Errorf("%w: an explicit observation sink requires its recorder", ErrObservationWriterConflict)
	}
	return nil
}

// ValidateObservationWriterDeclaration validates one carried writer selection
// against the concrete recorder and sink declarations a caller supplied. It
// rejects a typed-nil port, a sink without its recorder, and a selection that
// does not match the declared pair, so a conflicting writer never reaches an
// opener, a recovery pass, or a worker start.
//
// The check inspects declarations only: it never opens, closes, starts or
// invokes a writer.
func ValidateObservationWriterDeclaration(selection ObservationWriterSelection, recorder, sink any) error {
	if sink != nil && IsNilObservationPort(sink) {
		return fmt.Errorf("%w: the explicit observation sink is typed-nil", ErrObservationWriterConflict)
	}
	recorderPresent := recorder != nil
	recorderUsable := recorderPresent && !IsNilObservationPort(recorder)
	if recorderPresent && !recorderUsable && selection == ObservationWriterExternal {
		return fmt.Errorf("%w: the explicit metering recorder is typed-nil", ErrObservationWriterConflict)
	}
	if err := ValidateObservationWriterSelection(selection, ObservationWriterInput{
		ExplicitRecorder: recorderUsable,
		ExplicitSink:     sink != nil,
	}); err != nil {
		return err
	}
	if selection != ObservationWriterExternal {
		return nil
	}
	return ValidateObservationWriterPair(recorder, sink)
}

// ValidateObservationWriterPair proves that one recorder and one sink belong
// to the same durable journal.
//
// The proof is the journal identity both writers declare: the owner mints it
// when the journal opens, and every wrapper over that journal reports it
// unchanged. Two journals over the same durable store never share an identity,
// so a pair that combines two different journals is refused even when both
// halves are individually valid. A writer that cannot declare its journal
// cannot prove the pair and is refused as well.
//
// The check inspects declarations only: it never opens, closes, starts or
// invokes a writer.
func ValidateObservationWriterPair(recorder, sink any) error {
	recorderID, ok := observationJournalIdentity(recorder)
	if !ok {
		return fmt.Errorf("%w: the explicit observation recorder does not declare its durable journal identity", ErrObservationWriterConflict)
	}
	sinkID, ok := observationJournalIdentity(sink)
	if !ok {
		return fmt.Errorf("%w: the explicit observation sink does not declare its durable journal identity", ErrObservationWriterConflict)
	}
	if recorderID == "" || sinkID == "" {
		return fmt.Errorf("%w: the explicit observation writer pair carries an empty durable journal identity", ErrObservationWriterConflict)
	}
	if recorderID != sinkID {
		return fmt.Errorf("%w: the explicit observation recorder and sink belong to different durable journals", ErrObservationWriterConflict)
	}
	return nil
}

// observationJournalIdentity reads the declared journal identity of one
// observation writer. A typed-nil port declares no identity.
func observationJournalIdentity(port any) (sdkmetering.ObservationJournalID, bool) {
	if port == nil || IsNilObservationPort(port) {
		return "", false
	}
	identified, ok := port.(sdkmetering.ObservationJournalIdentity)
	if !ok || identified == nil {
		return "", false
	}
	return identified.ObservationJournalIdentity(), true
}
