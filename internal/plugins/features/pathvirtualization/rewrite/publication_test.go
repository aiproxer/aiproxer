package rewrite_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRewriteCall_PublicationPreservesAuditAndEnvelopeBytes(t *testing.T) {
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal(reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal(reject)
	}
	args := `{"file_path":"` + fixtureTarget + `"}`
	wrapped, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	envelope := " { \"name\" : \"read_file\", \"arguments\" : " + string(wrapped) + ",\n \"call_id\" : \"call_7f3a\", \"type\" : \"function_call\", \"id\" : \"\\u0061\" } "
	for _, tc := range []struct {
		name     string
		call     *aipapi.Call
		payload  func(*aipapi.Call) string
		rewrites bool
	}{
		{"raw_item", &aipapi.Call{Items: []aipapi.Item{toolCallItem("read_file", args)}}, func(c *aipapi.Call) string { return string(c.Items[0].ToolCall.Arguments) }, true},
		{"wrapped_item", &aipapi.Call{Items: []aipapi.Item{toolCallItem("read_file", string(wrapped))}}, func(c *aipapi.Call) string { return string(c.Items[0].ToolCall.Arguments) }, true},
		{"named_part", &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolName: "read_file", ToolCallID: "call_7f3a", Content: []byte(args)}}}}}, func(c *aipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, true},
		{"envelope", &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: []byte(envelope)}}}}}, func(c *aipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, true},
		{"object_envelope", &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: []byte(strings.Replace(envelope, string(wrapped), args, 1))}}}}}, func(c *aipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, false},
	} {
		for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				before := tc.payload(tc.call)
				out, stats, err := rewrite.NewWithMode(fixtureMapping(t), resolver, mode).RewriteCall(tc.call)
				if err != nil {
					t.Fatal(err)
				}
				want := before
				if mode == rewrite.ModeRewrite && tc.rewrites {
					want = strings.Replace(before, fixtureTarget, fixtureVPath, 1)
				}
				if got := tc.payload(out); got != want {
					t.Errorf("payload:\n got %s\nwant %s", got, want)
				}
				if tc.payload(tc.call) != before {
					t.Error("input call was mutated")
				}
				if tc.rewrites && stats.Rewritten != 1 {
					t.Errorf("detected replacements = %d, want 1", stats.Rewritten)
				}
			})
		}
	}
}
