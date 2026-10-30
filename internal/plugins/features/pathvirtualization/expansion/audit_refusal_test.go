package expansion_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestExpansionFinalizer_AuditNeverPublishesRefusal(t *testing.T) {
	fixture := newExpansionFixture(t)
	for _, tc := range []struct {
		name, root, args string
		reason           expansion.Reason
	}{
		{"stale", expansionOtherRoot, fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath), expansion.ReasonWorkspaceMismatch},
		{"malformed_alias", expansionProjectRoot, `{"file_path":"/.__aip_v1__/w_short/a"}`, expansion.ReasonMalformedReservedAlias},
		{"unusable_root", "relative", fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath), expansion.ReasonWorkspaceMismatch},
		{"malformed_json", expansionProjectRoot, `{"file_path":"` + fixture.aliasPath, expansion.ReasonArgsUnparseable},
		{"output_limit", expansionProjectRoot, fmt.Sprintf(`{"file_path":%q,"padding":%q}`, fixture.aliasPath, strings.Repeat("x", aipapi.MaxEventDeltaBytes-len(fixture.aliasPath)-35)), expansion.ReasonExpandedTooLarge},
	} {
		for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				var report expansion.Report
				fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"), mode, expansion.Policy{}, expansion.WithReporter(func(r expansion.Report) { report = r }))
				if err != nil {
					t.Fatal(err)
				}
				call := expansionCall(expansionToolName, tc.args)
				before := bytes.Clone(call.ArgsJSON)
				got, err := fin.Finalize(t.Context(), call, aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.root))
				if err != nil {
					t.Fatal(err)
				}
				wantAction := toolcall.ActionReject
				if mode == rewrite.ModeAudit {
					wantAction = toolcall.ActionPass
				}
				if got.Action != wantAction || got.ArgsJSON != nil || !bytes.Equal(call.ArgsJSON, before) {
					t.Errorf("action=%v want %v; arguments must remain unpublished and unchanged", got.Action, wantAction)
				}
				if report.Reason != tc.reason {
					t.Errorf("reported reason=%v want %v", report.Reason, tc.reason)
				}
			})
		}
	}
}
