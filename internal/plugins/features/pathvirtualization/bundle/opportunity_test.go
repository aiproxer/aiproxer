package bundle_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestBundle_PassOpportunitiesDoNotClaimCandidateSavings(t *testing.T) {
	for _, mode := range []string{"audit", "rewrite"} {
		t.Run(mode, func(t *testing.T) {
			resolved := resolve(t, "enabled: true\nmode: "+mode+"\n")
			tel, b, err := bundle.FeatureBundleWithTelemetry(resolved)
			if err != nil {
				t.Fatal(err)
			}
			call := pathCall()
			meta := request.AttemptMeta{Workspace: workspace.WorkspaceView{ProjectRoot: fixtureRoot}}
			if _, err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0].HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
				t.Fatal(err)
			}
			// Late shaping introduces a second occurrence absent from the early pass.
			late := pathCall().Items[0]
			late.ID, late.ToolCall.CallID = "late_item", "late_call"
			call.Items = append(call.Items, late)
			if err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0].HandleRequestParts(pinnedWorkspace(fixtureRoot), call, hooks.PartMeta{}); err != nil {
				t.Fatal(err)
			}
			snapshot := tel.Snapshot()
			early, _ := savingsPassRow(snapshot, outbound.PassAttempt.String())
			lateRow, _ := savingsPassRow(snapshot, outbound.PassRequestPart.String())
			wantLate, wantTotal := int64(16), int64(24)
			wantArgs := fixtureArgs
			if mode == "rewrite" {
				wantLate, wantTotal = 8, 16
				wantArgs = strings.Replace(fixtureArgs, fixtureTarget, fixtureVirtual, 1)
			}
			if early.BytesSaved != 8 || lateRow.BytesSaved != wantLate {
				t.Errorf("per-pass opportunities = %d/%d, want 8/%d", early.BytesSaved, lateRow.BytesSaved, wantLate)
			}
			for _, item := range call.Items {
				if string(item.ToolCall.Arguments) != wantArgs {
					t.Errorf("arguments = %s, want %s", item.ToolCall.Arguments, wantArgs)
				}
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var published struct {
				Total    map[string]int64 `json:"total"`
				Outbound struct {
					Virtualized map[string]any `json:"virtualized"`
				} `json:"outbound"`
			}
			if err := json.Unmarshal(raw, &published); err != nil {
				t.Fatal(err)
			}
			if got := published.Total["pass_observed_opportunity_bytes"]; got != wantTotal {
				t.Errorf("pass opportunity sum = %d, want %d", got, wantTotal)
			}
			if _, exists := published.Total["bytes_saved"]; exists {
				t.Error("total mislabels duplicate pass observations as candidate savings")
			}
			if _, exists := published.Outbound.Virtualized["bytes_saved"]; exists {
				t.Error("outbound aggregate mislabels pass observations as candidate savings")
			}
		})
	}
}

func TestBundle_OptionalTelemetryDoesNotChangeDecisions(t *testing.T) {
	resolved := resolve(t, "enabled: true\nmode: rewrite\n")
	stock, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatal(err)
	}
	tel, observed, err := bundle.FeatureBundleWithTelemetry(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []aipfeature.FeatureBundle{stock, observed} {
		call := pathCall()
		meta := request.AttemptMeta{Workspace: workspace.WorkspaceView{ProjectRoot: fixtureRoot}}
		if _, err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0].HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
			t.Fatal(err)
		}
		if err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0].HandleRequestParts(pinnedWorkspace(fixtureRoot), call, hooks.PartMeta{}); err != nil {
			t.Fatal(err)
		}
		res, err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0].Finalize(t.Context(), toolcall.CompletedCall{ToolName: fixtureTool, ArgsJSON: call.Items[0].ToolCall.Arguments}, call.Tools[0], call.Tools, toolcall.Meta{Workspace: meta.Workspace})
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != toolcall.ActionRewrite || string(res.ArgsJSON) != fixtureArgs {
			t.Errorf("inbound result=%+v, want restored arguments", res)
		}
	}
	if snapshot := tel.Snapshot(); snapshot.Outbound.Reports != 2 || snapshot.Inbound.Reports != 1 {
		t.Errorf("opt-in reader missed stage reports: %+v", snapshot)
	}
}
