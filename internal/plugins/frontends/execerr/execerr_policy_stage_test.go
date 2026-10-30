package execerr_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

// TestClassifyExecute_RealPreRequestDenial verifies that a denial produced by the
// real RunPreRequestStage reaches KindPolicyDenied through execerr.ClassifyExecute
// (requirement 5.1). Before the stage runner converted its legacy RejectError into a
// stable policy denied error, this classified as KindClientReject.
func TestClassifyExecute_RealPreRequestDenial(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "call",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqDenyAdapter{id: "deny", message: "no tool access"},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected denial error")
	}
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindPolicyDenied {
		t.Fatalf("kind: got %v want %v", out.Kind, execerr.KindPolicyDenied)
	}
	if out.Status != http.StatusForbidden {
		t.Fatalf("status: got %d want %d", out.Status, http.StatusForbidden)
	}
	if out.Message != "no tool access" {
		t.Fatalf("message: got %q want %q", out.Message, "no tool access")
	}
}

type preReqDenyAdapter struct {
	id      string
	message string
}

func (h preReqDenyAdapter) ID() string                        { return h.id }
func (h preReqDenyAdapter) Order() int                        { return 0 }
func (h preReqDenyAdapter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h preReqDenyAdapter) Handle(_ context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Deny(h.message), nil
}
