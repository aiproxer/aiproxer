package runtime

import "github.com/aiproxer/aiproxer/pkg/aipapi"

type (
	CancellationCauseClass string // explicit | client_gone | context_done | race_loser | none | other
	CancellationModeClass  string // none | provider | transport | close_only | other
	CancellationPhase      string // requested | outcome | forced | terminal | none | other
	CancellationFallback   string // negotiated | legacy | none | other
)

const (
	CancellationCauseExplicit    CancellationCauseClass = "explicit"
	CancellationCauseClientGone  CancellationCauseClass = "client_gone"
	CancellationCauseContextDone CancellationCauseClass = "context_done"
	CancellationCauseRaceLoser   CancellationCauseClass = "race_loser"
	CancellationCauseNone        CancellationCauseClass = "none"
	CancellationCauseOther       CancellationCauseClass = "other"

	CancellationModeNone      CancellationModeClass = "none"
	CancellationModeProvider  CancellationModeClass = "provider"
	CancellationModeTransport CancellationModeClass = "transport"
	CancellationModeCloseOnly CancellationModeClass = "close_only"
	CancellationModeOther     CancellationModeClass = "other"

	CancellationPhaseRequested CancellationPhase = "requested"
	CancellationPhaseOutcome   CancellationPhase = "outcome"
	CancellationPhaseForced    CancellationPhase = "forced"
	CancellationPhaseTerminal  CancellationPhase = "terminal"
	CancellationPhaseNone      CancellationPhase = "none"
	CancellationPhaseOther     CancellationPhase = "other"

	CancellationFallbackNegotiated CancellationFallback = "negotiated"
	CancellationFallbackLegacy     CancellationFallback = "legacy"
	CancellationFallbackNone       CancellationFallback = "none"
	CancellationFallbackOther      CancellationFallback = "other"
)

type CancellationObservation struct {
	CauseClass CancellationCauseClass
	Mode       CancellationModeClass
	Phase      CancellationPhase
	Fallback   CancellationFallback
}

func newCancellationObservation(cause aipapi.CancelKind, mode aipapi.CancelMode, phase string, fallback string) CancellationObservation {
	return CancellationObservation{
		CauseClass: normalizeCancellationCauseClass(cause),
		Mode:       normalizeCancellationModeClass(mode),
		Phase:      normalizeCancellationPhase(phase),
		Fallback:   normalizeCancellationFallback(fallback),
	}
}

func normalizeCancellationCauseClass(cause aipapi.CancelKind) CancellationCauseClass {
	switch cause {
	case aipapi.CancelExplicit:
		return CancellationCauseExplicit
	case aipapi.CancelClientGone:
		return CancellationCauseClientGone
	case aipapi.CancelContextDone:
		return CancellationCauseContextDone
	case aipapi.CancelRaceLoser:
		return CancellationCauseRaceLoser
	case "":
		return CancellationCauseNone
	default:
		return CancellationCauseOther
	}
}

func normalizeCancellationModeClass(mode aipapi.CancelMode) CancellationModeClass {
	switch mode {
	case aipapi.CancelModeNone:
		return CancellationModeNone
	case aipapi.CancelModeProvider:
		return CancellationModeProvider
	case aipapi.CancelModeTransport:
		return CancellationModeTransport
	case aipapi.CancelModeCloseOnly:
		return CancellationModeCloseOnly
	case "":
		return CancellationModeNone
	default:
		return CancellationModeOther
	}
}

func normalizeCancellationPhase(phase string) CancellationPhase {
	switch phase {
	case "requested":
		return CancellationPhaseRequested
	case "outcome":
		return CancellationPhaseOutcome
	case "forced":
		return CancellationPhaseForced
	case "terminal":
		return CancellationPhaseTerminal
	case "", "none":
		return CancellationPhaseNone
	default:
		return CancellationPhaseOther
	}
}

func normalizeCancellationFallback(fallback string) CancellationFallback {
	switch fallback {
	case "negotiated":
		return CancellationFallbackNegotiated
	case "legacy":
		return CancellationFallbackLegacy
	case "", "none":
		return CancellationFallbackNone
	default:
		return CancellationFallbackOther
	}
}
