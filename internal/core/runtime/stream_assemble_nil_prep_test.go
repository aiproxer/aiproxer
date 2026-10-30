package runtime

import (
	"context"
	"testing"
)

// TestAssembleStreamRejectsNilPreparedRequest is a fail-closed boundary test:
// a nil prepared request must produce an error, never a nil-pointer panic.
func TestAssembleStreamRejectsNilPreparedRequest(t *testing.T) {
	t.Parallel()
	stream, err := streamAssembler{}.assemble(context.Background(), nil, &routePlanState{}, openedAttempt{})
	if err == nil {
		t.Fatal("nil prepared request must fail, got nil error")
	}
	if stream != nil {
		t.Fatalf("nil prepared request must yield no stream, got %T", stream)
	}
}
