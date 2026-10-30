package policy

import "github.com/aiproxer/aiproxer/pkg/aipapi"

type RoutingAttemptOutcomeSink interface {
	OnRoutingAttemptOutcome(candidateKey string, outcome aipapi.AttemptOutcome)
}
