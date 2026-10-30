package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	repair "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestRetryRecvStream_RealToolCallFinalizerSafeTailRepairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		args       string
		schema     string
		want       string
		wantReason string
	}{
		{
			name:       "terminal_comma",
			args:       `{"location":"NYC",`,
			schema:     `{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`,
			want:       `{"location":"NYC"}`,
			wantReason: toolcall.ReasonSyntaxRepaired,
		},
		{
			name:       "pending_const",
			args:       `{"mode":`,
			schema:     `{"type":"object","properties":{"mode":{"const":"safe"}},"required":["mode"],"additionalProperties":false}`,
			want:       `{"mode":"safe"}`,
			wantReason: toolcall.ReasonConstInserted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "safe-tail", ToolName: "run", MessageIndex: 3},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "safe-tail", ToolName: "run", Delta: tc.args, MessageIndex: 3},
				{Kind: aipapi.EventToolCallFinished, ToolCallID: "safe-tail", ToolName: "run", MessageIndex: 3},
				{Kind: aipapi.EventResponseFinished},
			})
			var opens atomic.Int32
			ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
				"openai": recordingBackend("openai", &opens, backendStream),
			}, extensions.SnapshotOptions{
				FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
					RequestTransforms: []request.Transform{pdNoopRtx{}},
				}),
			})
			fin := repair.NewFinalizer(repair.FinalizerPolicy{
				ID:             repair.DefaultFinalizerID,
				MaxArgsBytes:   repair.DefaultMaxArgsBytes,
				OnUnrepairable: repair.OnUnrepairablePassThrough,
				Order:          repair.DefaultFinalizerOrder,
				Schema:         repair.DefaultSchemaLimits(),
			})
			ex.SetToolCallFinalizers([]toolcall.Finalizer{fin}, repair.DefaultMaxArgsBytes)

			call := pdBaseCall("openai:gpt-4")
			call.Tools = []aipapi.ToolDef{{Name: "run", Parameters: []byte(tc.schema)}}
			call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
			stream, err := ex.Execute(principalCtx("safe-tail-"+tc.name), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := tcrCollect(t, stream)
			_ = stream.Close()
			tools := tcrToolLifecycle(got)
			if len(tools) != 3 {
				t.Fatalf("tool lifecycle len=%d want 3: %#v", len(tools), tools)
			}
			if tools[1].Delta != tc.want || tools[0].MessageIndex != 3 || tools[2].MessageIndex != 3 {
				t.Fatalf("rewritten lifecycle=%#v want args=%q index=3", tools, tc.want)
			}

			res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
				ToolCallID: "safe-tail", ToolName: "run", ArgsJSON: []byte(tc.args),
			}, call.Tools[0], call.Tools, toolcall.Meta{})
			if err != nil {
				t.Fatal(err)
			}
			if res.ReasonCode != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}
