package testkit

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// AssertEventCount asserts the number of canonical events in a non-streaming collection.
func AssertEventCount(t *testing.T, events []aipapi.Event, want int) {
	t.Helper()
	if len(events) != want {
		t.Fatalf("event count: want %d, got %d", want, len(events))
	}
}
