package runtime

import (
	"testing"
	"time"
)

func TestExecutor_now_usesWallClockWhenNowNil(t *testing.T) {
	t.Parallel()
	e := TestExecutor()
	before := time.Now()
	got := e.now()
	after := time.Now()
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("now() = %v, want between %v and %v", got, before, after)
	}
}
