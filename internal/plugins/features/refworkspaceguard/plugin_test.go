package refworkspaceguard_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refworkspaceguard"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func withFixtureCtx() context.Context {
	return execctx.WithViews(context.Background(), testkit.ExecCtxViewsFixture())
}

func TestStaticResolver_exposesView(t *testing.T) {
	t.Parallel()
	r := refworkspaceguard.NewStaticResolver(refworkspaceguard.Config{ProjectRoot: "/p", Markers: []string{"a"}})
	v, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.ProjectRoot != "/p" || len(v.Markers) != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestCatalogAndUnlock_twoRequests(t *testing.T) {
	t.Parallel()
	ctx := withFixtureCtx()
	st := testkit.NewFakeStateStoreAt(func() time.Time { return time.Unix(0, 0) })
	cat := refworkspaceguard.NewCatalogFilter(refworkspaceguard.Config{})
	rtx := refworkspaceguard.NewSessionUnlockTransform(refworkspaceguard.Config{})

	svcR := request.Services{State: st, Aux: nil}
	svcC := toolcatalog.Services{State: st, Aux: nil}
	call1 := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "x"}},
		}},
		Tools: []aipapi.ToolDef{
			{Name: refworkspaceguard.GatedToolName},
			{Name: "ok"},
		},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	if err := extensions.RunToolCatalogFilterStage(ctx, nil, nil, []toolcatalog.Filter{cat}, &call1, toolcatalog.CatalogMeta{}, svcC); err != nil {
		t.Fatal(err)
	}
	if len(call1.Tools) != 1 || call1.Tools[0].Name != "ok" {
		t.Fatalf("r1 tools %#v", call1.Tools)
	}
	if err := extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{rtx}, &call1, request.RequestMeta{}, svcR); err != nil {
		t.Fatal(err)
	}

	call2 := aipapi.Call{
		Messages: call1.Messages,
		Tools: []aipapi.ToolDef{
			{Name: refworkspaceguard.GatedToolName},
			{Name: "ok"},
		},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	if err := extensions.RunToolCatalogFilterStage(ctx, nil, nil, []toolcatalog.Filter{cat}, &call2, toolcatalog.CatalogMeta{}, svcC); err != nil {
		t.Fatal(err)
	}
	if len(call2.Tools) != 2 {
		t.Fatalf("r2 tools %#v", call2.Tools)
	}
}

func TestHeatReactor_respectsLabel(t *testing.T) {
	t.Parallel()
	g := refworkspaceguard.NewHeatReactor(refworkspaceguard.Config{})
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: refworkspaceguard.HeatToolPrefix + "x", ToolCallID: "c1"}
	dec, out, err := g.HandleToolEvent(context.Background(), te, sdk.ToolMeta{Workspace: workspace.WorkspaceView{
		Labels: map[string]string{refworkspaceguard.LabelDenyHeat: "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolSwallow {
		t.Fatalf("dec %v out %#v", dec, out)
	}
}

func TestHeatReactor_noLabelPasses(t *testing.T) {
	t.Parallel()
	g := refworkspaceguard.NewHeatReactor(refworkspaceguard.Config{})
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: refworkspaceguard.HeatToolPrefix + "x", ToolCallID: "c1"}
	dec, out, err := g.HandleToolEvent(context.Background(), te, sdk.ToolMeta{Workspace: workspace.WorkspaceView{Labels: nil}})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolPass {
		t.Fatalf("dec %v", dec)
	}
	_ = out
}
