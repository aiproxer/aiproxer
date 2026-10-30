package backendplugin

import backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"

// ServerFrameFromProto converts plugin-to-host frames.
func ServerFrameFromProto(p *backendpluginv1.ExecuteServerFrame) (ServerFrame, error) {
	if p == nil {
		return ServerFrame{}, ErrInvalidFrame
	}
	kind, err := serverFrameKindFromProto(p.GetKind())
	if err != nil {
		return ServerFrame{}, err
	}
	ev, err := CanonicalEventFromProto(p.GetEvent())
	if err != nil {
		return ServerFrame{}, err
	}
	co, err := CancelOutcomeFromProto(p.GetCancelOutcome())
	if err != nil {
		return ServerFrame{}, err
	}
	term, err := TerminalFromProto(p.GetTerminal())
	if err != nil {
		return ServerFrame{}, err
	}
	accounting, err := accountingEvidenceFromProto(p.GetAccountingEvidence())
	if err != nil {
		return ServerFrame{}, err
	}
	accountingV2, err := AccountingEvidenceV2FromProtoPtr(p.GetAccountingEvidenceV2())
	if err != nil {
		return ServerFrame{}, err
	}
	promptCacheObservation, err := promptCacheObservationFromProto(p.GetPromptCacheObservation())
	if err != nil {
		return ServerFrame{}, err
	}
	frame := ServerFrame{
		Kind:                   kind,
		Sequence:               p.GetSequence(),
		Event:                  ev,
		Diagnostic:             p.GetDiagnostic(),
		CancelOutcome:          co,
		Terminal:               term,
		Accounting:             accounting,
		AccountingV2:           accountingV2,
		PromptCacheObservation: promptCacheObservation,
	}
	if err := frame.ValidateShape(); err != nil {
		return ServerFrame{}, err
	}
	return frame, nil
}

// ServerFrameToProto encodes plugin-to-host frames.
func ServerFrameToProto(f ServerFrame) (*backendpluginv1.ExecuteServerFrame, error) {
	if err := f.ValidateShape(); err != nil {
		return nil, err
	}
	kind, err := serverFrameKindToProto(f.Kind)
	if err != nil {
		return nil, err
	}
	ev, err := CanonicalEventToProto(f.Event)
	if err != nil {
		return nil, err
	}
	co, err := CancelOutcomeToProto(f.CancelOutcome)
	if err != nil {
		return nil, err
	}
	term, err := TerminalToProto(f.Terminal)
	if err != nil {
		return nil, err
	}
	accounting, err := accountingEvidenceToProto(f.Accounting)
	if err != nil {
		return nil, err
	}
	accountingV2, err := AccountingEvidenceV2ToProto(f.AccountingV2)
	if err != nil {
		return nil, err
	}
	promptCacheObservation, err := promptCacheObservationToProto(f.PromptCacheObservation)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.ExecuteServerFrame{
		Kind:                   kind,
		Sequence:               f.Sequence,
		Event:                  ev,
		Diagnostic:             f.Diagnostic,
		CancelOutcome:          co,
		Terminal:               term,
		AccountingEvidence:     accounting,
		AccountingEvidenceV2:   accountingV2,
		PromptCacheObservation: promptCacheObservation,
	}, nil
}

func AccountingEvidenceV2FromProtoPtr(p *backendpluginv1.AccountingEvidenceV2) (*AccountingEvidenceV2, error) {
	if p == nil {
		return nil, nil
	}
	evidence, err := AccountingEvidenceV2FromProto(p)
	if err != nil {
		return nil, err
	}
	return &evidence, nil
}
