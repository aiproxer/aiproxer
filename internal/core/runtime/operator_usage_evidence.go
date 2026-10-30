package runtime

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// emptyOperatorUsageShell is the legal unobserved operator usage event for
// incurred settle/egress when no provider usage was seen (req 1.5 / 2.9).
func emptyOperatorUsageShell() aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventUsageDelta}
}

// operatorUsageEvidence derives authority-preferring operator usage from observed
// stream events. Returns an empty Kind when no UsageDelta was present.
func operatorUsageEvidence(events []aipapi.Event) aipapi.Event {
	return authorityUsageEvent(tokenAccountingUsageEvents(events))
}

// operatorUsageOrShell returns observed evidence or the empty shell.
func operatorUsageOrShell(events []aipapi.Event) aipapi.Event {
	if ev := operatorUsageEvidence(events); ev.Kind != "" {
		return ev
	}
	return emptyOperatorUsageShell()
}
