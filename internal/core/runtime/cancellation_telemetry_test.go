package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewCancellationObservation_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cause        aipapi.CancelKind
		mode         aipapi.CancelMode
		phase        string
		fallback     string
		wantCause    CancellationCauseClass
		wantMode     CancellationModeClass
		wantPhase    CancellationPhase
		wantFallback CancellationFallback
	}{
		{
			name:         "all_empty_maps_to_none",
			cause:        "",
			mode:         "",
			phase:        "",
			fallback:     "",
			wantCause:    CancellationCauseNone,
			wantMode:     CancellationModeNone,
			wantPhase:    CancellationPhaseNone,
			wantFallback: CancellationFallbackNone,
		},
		{
			name:         "explicit_provider_requested_negotiated",
			cause:        aipapi.CancelExplicit,
			mode:         aipapi.CancelModeProvider,
			phase:        "requested",
			fallback:     "negotiated",
			wantCause:    CancellationCauseExplicit,
			wantMode:     CancellationModeProvider,
			wantPhase:    CancellationPhaseRequested,
			wantFallback: CancellationFallbackNegotiated,
		},
		{
			name:         "client_gone_transport_outcome_legacy",
			cause:        aipapi.CancelClientGone,
			mode:         aipapi.CancelModeTransport,
			phase:        "outcome",
			fallback:     "legacy",
			wantCause:    CancellationCauseClientGone,
			wantMode:     CancellationModeTransport,
			wantPhase:    CancellationPhaseOutcome,
			wantFallback: CancellationFallbackLegacy,
		},
		{
			name:         "context_done_close_only_forced_none",
			cause:        aipapi.CancelContextDone,
			mode:         aipapi.CancelModeCloseOnly,
			phase:        "forced",
			fallback:     "none",
			wantCause:    CancellationCauseContextDone,
			wantMode:     CancellationModeCloseOnly,
			wantPhase:    CancellationPhaseForced,
			wantFallback: CancellationFallbackNone,
		},
		{
			name:         "race_loser_none_terminal_none",
			cause:        aipapi.CancelRaceLoser,
			mode:         aipapi.CancelModeNone,
			phase:        "terminal",
			fallback:     "none",
			wantCause:    CancellationCauseRaceLoser,
			wantMode:     CancellationModeNone,
			wantPhase:    CancellationPhaseTerminal,
			wantFallback: CancellationFallbackNone,
		},
		{
			name:         "phase_explicit_none_maps_to_none",
			cause:        aipapi.CancelExplicit,
			mode:         aipapi.CancelModeNone,
			phase:        "none",
			fallback:     "none",
			wantCause:    CancellationCauseExplicit,
			wantMode:     CancellationModeNone,
			wantPhase:    CancellationPhaseNone,
			wantFallback: CancellationFallbackNone,
		},
		{
			name:         "unknown_values_map_to_other",
			cause:        aipapi.CancelKind("unexpected_cause"),
			mode:         aipapi.CancelMode("unexpected_mode"),
			phase:        "unexpected_phase",
			fallback:     "unexpected_fallback",
			wantCause:    CancellationCauseOther,
			wantMode:     CancellationModeOther,
			wantPhase:    CancellationPhaseOther,
			wantFallback: CancellationFallbackOther,
		},
		{
			name:         "individual_dimension_cause_other",
			cause:        "invalid_cause_value",
			mode:         aipapi.CancelModeTransport,
			phase:        "terminal",
			fallback:     "legacy",
			wantCause:    CancellationCauseOther,
			wantMode:     CancellationModeTransport,
			wantPhase:    CancellationPhaseTerminal,
			wantFallback: CancellationFallbackLegacy,
		},
		{
			name:         "individual_dimension_mode_other",
			cause:        aipapi.CancelExplicit,
			mode:         "invalid_mode_value",
			phase:        "terminal",
			fallback:     "negotiated",
			wantCause:    CancellationCauseExplicit,
			wantMode:     CancellationModeOther,
			wantPhase:    CancellationPhaseTerminal,
			wantFallback: CancellationFallbackNegotiated,
		},
		{
			name:         "individual_dimension_phase_other",
			cause:        aipapi.CancelExplicit,
			mode:         aipapi.CancelModeTransport,
			phase:        "invalid_phase_value",
			fallback:     "legacy",
			wantCause:    CancellationCauseExplicit,
			wantMode:     CancellationModeTransport,
			wantPhase:    CancellationPhaseOther,
			wantFallback: CancellationFallbackLegacy,
		},
		{
			name:         "individual_dimension_fallback_other",
			cause:        aipapi.CancelExplicit,
			mode:         aipapi.CancelModeTransport,
			phase:        "terminal",
			fallback:     "invalid_fallback_value",
			wantCause:    CancellationCauseExplicit,
			wantMode:     CancellationModeTransport,
			wantPhase:    CancellationPhaseTerminal,
			wantFallback: CancellationFallbackOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obs := newCancellationObservation(tt.cause, tt.mode, tt.phase, tt.fallback)
			if obs.CauseClass != tt.wantCause {
				t.Errorf("CauseClass = %q, want %q", obs.CauseClass, tt.wantCause)
			}
			if obs.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", obs.Mode, tt.wantMode)
			}
			if obs.Phase != tt.wantPhase {
				t.Errorf("Phase = %q, want %q", obs.Phase, tt.wantPhase)
			}
			if obs.Fallback != tt.wantFallback {
				t.Errorf("Fallback = %q, want %q", obs.Fallback, tt.wantFallback)
			}
		})
	}
}
