package backendplugin

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// StreamValidator enforces accepted-before-sequence, monotonic sequences, and one terminal.
type StreamValidator struct {
	isAccepted bool
	nextSeq    uint64
	terminated bool
}

// Push validates one plugin-to-host frame against stream rules.
func (v *StreamValidator) Push(frame ServerFrame) error {
	if v.terminated {
		if frame.Kind == ServerFrameTerminal {
			return ErrMultipleTerminals
		}
		return ErrEventAfterTerminal
	}
	if err := frame.ValidateShape(); err != nil {
		return err
	}
	if err := ValidateSize(ServerFrameSizeBytes(frame), DefaultMaxStreamFrameBytes); err != nil {
		return err
	}
	switch frame.Kind {
	case ServerFrameAccepted:
		if v.isAccepted {
			return ErrInvalidFrame
		}
		if frame.Sequence != 0 {
			return ErrSequenceGap
		}
		v.isAccepted = true
		v.nextSeq = 1
		return nil
	case ServerFrameEvent, ServerFrameDiagnostic, ServerFrameCancelOutcome, ServerFrameTerminal, ServerFrameAccountingEvidence, ServerFramePromptCacheObservation:
		if !v.isAccepted {
			return ErrAcceptedRequired
		}
		if frame.Sequence != v.nextSeq {
			return ErrSequenceGap
		}
		v.nextSeq++
		if frame.Kind == ServerFrameTerminal {
			v.terminated = true
		}
		return nil
	default:
		return ErrUnknownFrameKind
	}
}

// OutputCommittedEvent reports whether an event kind commits output for failover.
func OutputCommittedEvent(kind EventKind) bool {
	return aipapi.OutputCommitted(aipapi.Event{Kind: kind})
}
