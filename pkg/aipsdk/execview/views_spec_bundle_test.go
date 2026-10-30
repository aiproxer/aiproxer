package execview_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func TestPrincipalView_nilSlicesAndClaimsRoundTripConstruct(t *testing.T) {
	t.Parallel()
	v := execview.PrincipalView{
		ID: "id", DisplayName: "n",
		Roles:  nil,
		Claims: nil,
	}
	if v.ID != "id" || v.DisplayName != "n" {
		t.Fatalf("unexpected fields %+v", v)
	}
	if v.Roles != nil || v.Claims != nil {
		t.Fatalf("nil preserved: roles=%#v claims=%#v", v.Roles, v.Claims)
	}
}

func TestAttemptView_zeroValueIsUsable(t *testing.T) {
	t.Parallel()
	var v execview.AttemptView
	if v.AttemptSeq != 0 {
		t.Fatalf("AttemptSeq=%d", v.AttemptSeq)
	}
}
