package backendplugin

import (
	"google.golang.org/protobuf/proto"
)

// ValidateShape reports whether the server frame kind matches an exclusive payload.
func (f ServerFrame) ValidateShape() error {
	switch f.Kind {
	case ServerFrameAccepted:
		if f.Event != nil || f.CancelOutcome != nil || f.Terminal != nil || f.Accounting != nil || f.AccountingV2 != nil || f.PromptCacheObservation != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		return nil
	case ServerFrameEvent:
		if f.Event == nil {
			return ErrUnknownEventKind
		}
		if err := ValidateEventKind(f.Event.Kind); err != nil {
			return err
		}
		if f.CancelOutcome != nil || f.Terminal != nil || f.Accounting != nil || f.AccountingV2 != nil || f.PromptCacheObservation != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		return nil
	case ServerFrameDiagnostic:
		if f.Event != nil || f.CancelOutcome != nil || f.Terminal != nil || f.Accounting != nil || f.AccountingV2 != nil || f.PromptCacheObservation != nil {
			return ErrInvalidFrame
		}
		return ValidateSize(uint64(len(f.Diagnostic)), DefaultMaxDiagnosticBytes)
	case ServerFrameCancelOutcome:
		if f.CancelOutcome == nil || f.Event != nil || f.Terminal != nil || f.Accounting != nil || f.AccountingV2 != nil || f.PromptCacheObservation != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		if f.CancelOutcome.Reason == CancelReasonUnspecified {
			return ErrUnknownEnum
		}
		if f.CancelOutcome.Mode != CancelModeUnspecified {
			if err := ValidateCancelMode(f.CancelOutcome.Mode); err != nil {
				return err
			}
		}
		return nil
	case ServerFrameTerminal:
		if f.Terminal == nil || f.Terminal.Status == TerminalUnspecified {
			return ErrUnknownEnum
		}
		if f.Event != nil || f.CancelOutcome != nil || f.Accounting != nil || f.AccountingV2 != nil || f.PromptCacheObservation != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		return nil
	case ServerFrameAccountingEvidence:
		if (f.Accounting == nil && f.AccountingV2 == nil) || (f.Accounting != nil && f.AccountingV2 != nil) || f.Event != nil || f.CancelOutcome != nil || f.Terminal != nil || f.PromptCacheObservation != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		if f.AccountingV2 != nil {
			return f.AccountingV2.Validate()
		}
		return ValidateAccountingEvidence(*f.Accounting)
	case ServerFramePromptCacheObservation:
		if f.PromptCacheObservation == nil || f.Event != nil || f.CancelOutcome != nil || f.Terminal != nil || f.Accounting != nil || f.AccountingV2 != nil || f.Diagnostic != "" {
			return ErrInvalidFrame
		}
		return f.PromptCacheObservation.Validate()
	default:
		return ErrUnknownFrameKind
	}
}

// ValidateShape reports whether the client frame kind matches an exclusive payload.
func (f ClientFrame) ValidateShape() error {
	if f.Kind == ClientFrameUnspecified {
		return ErrUnknownFrameKind
	}
	switch f.Kind {
	case ClientFrameStart:
		if f.Invocation == nil || f.InstanceID == "" {
			return ErrInvalidFrame
		}
		if f.CancelReason != CancelReasonUnspecified || f.CancelDeadlineUnixMS != 0 {
			return ErrInvalidFrame
		}
		return f.Invocation.Validate()
	case ClientFrameCancel:
		if f.InstanceID == "" || f.Invocation != nil {
			return ErrInvalidFrame
		}
		if f.CancelReason == CancelReasonUnspecified {
			return ErrUnknownEnum
		}
		return nil
	case ClientFrameCloseInput:
		if f.InstanceID == "" || f.Invocation != nil {
			return ErrInvalidFrame
		}
		if f.CancelReason != CancelReasonUnspecified || f.CancelDeadlineUnixMS != 0 {
			return ErrInvalidFrame
		}
		return nil
	default:
		return ErrUnknownFrameKind
	}
}

// ValidateServerFrameBounds enforces stream-frame wire size using the default frame ceiling.
func ValidateServerFrameBounds(f ServerFrame) error {
	return ValidateServerFrameSize(f, DefaultMaxStreamFrameBytes)
}

// ValidateServerFrameSize enforces shape plus full-frame size against limit.
func ValidateServerFrameSize(f ServerFrame, limit uint64) error {
	if err := f.ValidateShape(); err != nil {
		return err
	}
	return ValidateSize(ServerFrameSizeBytes(f), limit)
}

// ServerFrameSizeBytes returns a fail-closed frame size: max(protobuf wire, conservative payload).
func ServerFrameSizeBytes(f ServerFrame) uint64 {
	cons := ServerFrameConservativeBytes(f)
	msg, err := ServerFrameToProto(f)
	if err != nil {
		return cons
	}
	psz := uint64(proto.Size(msg))
	if cons > psz {
		return cons
	}
	return psz
}

// ServerFrameConservativeBytes sums raw payloads without relying on marshal success.
func ServerFrameConservativeBytes(f ServerFrame) uint64 {
	const envelope = 64
	switch f.Kind {
	case ServerFrameEvent:
		return envelope + eventPayloadBytes(f.Event)
	case ServerFrameAccountingEvidence:
		return envelope + accountingEvidenceBytes(f.Accounting) + accountingEvidenceV2Bytes(f.AccountingV2)
	case ServerFramePromptCacheObservation:
		if f.PromptCacheObservation == nil {
			return envelope
		}
		return envelope + uint64(len(f.PromptCacheObservation.TargetID)+len(f.PromptCacheObservation.GenerationID)+len(f.PromptCacheObservation.Handle)+len(f.PromptCacheObservation.ALegID)+len(f.PromptCacheObservation.BLegID)+len(f.PromptCacheObservation.BackendInstanceID)+128)
	case ServerFrameDiagnostic:
		return envelope + uint64(len(f.Diagnostic))
	case ServerFrameCancelOutcome:
		n := uint64(envelope)
		if f.CancelOutcome != nil {
			n += uint64(len(f.CancelOutcome.Detail))
		}
		return n
	case ServerFrameTerminal:
		n := uint64(envelope)
		if f.Terminal != nil && f.Terminal.Error != nil {
			n += uint64(len(f.Terminal.Error.Code)) + uint64(len(f.Terminal.Error.Message))
		}
		return n
	default:
		return envelope
	}
}

func accountingEvidenceBytes(e *AccountingEvidence) uint64 {
	if e == nil {
		return 0
	}
	return uint64(len(e.DedupeKey) + 64*6)
}

func accountingEvidenceV2Bytes(e *AccountingEvidenceV2) uint64 {
	if e == nil {
		return 0
	}
	b, err := e.Observation.CanonicalJSON()
	if err != nil {
		return DefaultMaxEconomicObservationBytes
	}
	return uint64(len(b) + len(e.CoverageReason) + 64)
}

// ValidateClientFrameBounds enforces client-frame wire size using protobuf encoding size.
func ValidateClientFrameBounds(f ClientFrame) error {
	if err := f.ValidateShape(); err != nil {
		return err
	}
	msg, err := ClientFrameToProto(f)
	if err != nil {
		return err
	}
	return ValidateSize(uint64(proto.Size(msg)), DefaultMaxStreamFrameBytes)
}

// eventPayloadBytes returns a conservative payload size for pre-marshal oversize checks.
func eventPayloadBytes(e *CanonicalEvent) uint64 {
	if e == nil {
		return 0
	}
	n := uint64(len(e.Opaque))
	if e.Delta != nil {
		n += uint64(len(*e.Delta))
	}
	if e.Signature != nil {
		n += uint64(len(*e.Signature))
	}
	if e.Warning != nil {
		n += uint64(len(*e.Warning))
	}
	if e.ToolCallID != nil {
		n += uint64(len(*e.ToolCallID))
	}
	if e.ToolName != nil {
		n += uint64(len(*e.ToolName))
	}
	if e.ImageRef != nil {
		n += uint64(len(*e.ImageRef))
	}
	if e.FileRef != nil {
		n += uint64(len(*e.FileRef))
	}
	if e.FinishReason != nil {
		n += uint64(len(*e.FinishReason))
	}
	if e.ResponseStatus != nil {
		n += uint64(len(*e.ResponseStatus))
	}
	if e.Error != nil {
		n += uint64(len(e.Error.Code)) + uint64(len(e.Error.Message))
	}
	if e.Usage != nil && e.Usage.RawUsageJSON.State() == RawJSONValue {
		n += uint64(len(e.Usage.RawUsageJSON.Bytes()))
	}
	if e.ReasoningSummary.State() == RawJSONValue {
		n += uint64(len(e.ReasoningSummary.Bytes()))
	}
	if e.ReasoningContent.State() == RawJSONValue {
		n += uint64(len(e.ReasoningContent.Bytes()))
	}
	if e.ReasoningEncryptedContent.State() == RawJSONValue {
		n += uint64(len(e.ReasoningEncryptedContent.Bytes()))
	}
	return n
}
