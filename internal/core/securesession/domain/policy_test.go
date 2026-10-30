package domain

import "testing"

func TestPrincipalIDPresent(t *testing.T) {
	t.Parallel()
	if PrincipalIDPresent(PrincipalRef{}) {
		t.Fatalf("empty principal should be absent")
	}
	if !PrincipalIDPresent(PrincipalRef{ID: "u"}) {
		t.Fatalf("non-empty id should be present")
	}
}
