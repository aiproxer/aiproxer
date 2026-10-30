package reasoninge2e_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestCheckResponsesHistoryIDs_noRawIDLeak(t *testing.T) {
	t.Parallel()
	err := reasoninge2e.CheckResponsesHistoryIDs([]string{"rs_SECRET_GOT"}, []string{"rs_SECRET_WANT"})
	if err == nil {
		t.Fatal("expected mismatch")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET") || strings.Contains(msg, "rs_") {
		t.Fatalf("must not leak raw ids: %q", msg)
	}
}
