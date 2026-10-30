package economics

import (
	"context"
	"errors"
	"testing"
)

var errPhase9Repair3Rater = errors.New("phase9 repair3 rater sentinel")

type phase9Repair3Rater struct{}

func (phase9Repair3Rater) Rate(context.Context, RatingInput) (Valuation, error) {
	return Valuation{}, errPhase9Repair3Rater
}

func TestPhase9Repair3_PublicRaterContractRemainsProviderNeutral(t *testing.T) {
	t.Parallel()
	var rater Rater = phase9Repair3Rater{}
	_, err := rater.Rate(context.Background(), RatingInput{})
	if !errors.Is(err, errPhase9Repair3Rater) {
		t.Fatalf("public Rater call error=%v, want provider-neutral contract dispatch", err)
	}
}
