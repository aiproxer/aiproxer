package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

type open1 struct{}

func (open1) ID() string { return "o1" }
func (open1) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"k": "v"}}, nil
}

type openErr struct{}

func (openErr) ID() string { return "err" }
func (openErr) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, context.Canceled
}

func TestRunSessionOpenStage_failOpenContinues(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "test", []session.Opener{openErr{}, open1{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	in := session.OpenInput{TraceID: "t1", Session: session.SessionView{ClientSessionHint: "s"}}
	got := extensions.RunSessionOpenStage(context.Background(), nil, nil, snap.SessionOpeners(), in)
	if got.SessionLabelUpserts["k"] != "v" {
		t.Fatalf("labels %+v", got.SessionLabelUpserts)
	}
}
