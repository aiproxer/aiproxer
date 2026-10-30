package completion_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
)

func TestOutcomeValidate(t *testing.T) {
	t.Parallel()
	if err := completion.PassOriginalOutcome().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.ReplayOriginalOutcome().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.RejectOutcome(errors.New("no")).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (completion.Outcome{Kind: completion.OutcomePassOriginal, Events: []aipapi.Event{{}}}).Validate(); err == nil {
		t.Fatal("expected err")
	}
	if err := (completion.Outcome{Kind: completion.OutcomeReject}).Validate(); err == nil {
		t.Fatal("expected err")
	}
}

func TestLastEventKind(t *testing.T) {
	t.Parallel()
	if completion.LastEventKind(nil) != "" {
		t.Fatal("empty")
	}
	if completion.LastEventKind([]aipapi.Event{{Kind: aipapi.EventTextDelta}}) != aipapi.EventTextDelta {
		t.Fatal("delta")
	}
}
