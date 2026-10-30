package session_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestOpenResult_Merge(t *testing.T) {
	t.Parallel()
	a := session.OpenResult{SessionLabelUpserts: map[string]string{"k1": "a", "k2": "b"}}
	b := session.OpenResult{SessionLabelUpserts: map[string]string{"k2": "c", "k3": "d"}}
	got := a.Merge(b)
	if got.SessionLabelUpserts["k1"] != "a" || got.SessionLabelUpserts["k2"] != "c" || got.SessionLabelUpserts["k3"] != "d" {
		t.Fatalf("merge: %+v", got.SessionLabelUpserts)
	}
}
