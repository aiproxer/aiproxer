package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestGateBufHasCommittedOutput(t *testing.T) {
	t.Parallel()
	if gateBufHasCommittedOutput(nil) {
		t.Fatal("nil buffer")
	}
	if gateBufHasCommittedOutput([]aipapi.Event{{Kind: aipapi.EventResponseStarted}}) {
		t.Fatal("lifecycle only")
	}
	if !gateBufHasCommittedOutput([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "x"}}) {
		t.Fatal("text delta commits")
	}
}
