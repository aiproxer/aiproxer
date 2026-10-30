package toolcall_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestSteerRequest_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  toolcall.SteerRequest
		want bool
	}{
		{
			name: "valid at minimum budget",
			req:  toolcall.SteerRequest{Instruction: "call an offered tool", TurnBudget: 1},
			want: true,
		},
		{
			name: "valid at maximum budget",
			req:  toolcall.SteerRequest{Instruction: "answer without a tool", TurnBudget: 3, AllowAfterReasoning: true},
			want: true,
		},
		{
			name: "instruction exactly at max bytes",
			req:  toolcall.SteerRequest{Instruction: strings.Repeat("a", 4096), TurnBudget: 1},
			want: true,
		},
		{
			name: "instruction exactly at max bytes multibyte",
			req:  toolcall.SteerRequest{Instruction: strings.Repeat("é", 2048), TurnBudget: 2},
			want: true,
		},
		{
			name: "instruction over max bytes",
			req:  toolcall.SteerRequest{Instruction: strings.Repeat("a", 4097), TurnBudget: 1},
			want: false,
		},
		{
			name: "instruction over max bytes multibyte",
			req:  toolcall.SteerRequest{Instruction: strings.Repeat("é", 3000), TurnBudget: 1},
			want: false,
		},
		{
			name: "instruction empty",
			req:  toolcall.SteerRequest{Instruction: "", TurnBudget: 1},
			want: false,
		},
		{
			name: "instruction invalid utf8",
			req:  toolcall.SteerRequest{Instruction: "bad \xff byte", TurnBudget: 1},
			want: false,
		},
		{
			name: "turn budget zero",
			req:  toolcall.SteerRequest{Instruction: "call an offered tool", TurnBudget: 0},
			want: false,
		},
		{
			name: "turn budget above maximum",
			req:  toolcall.SteerRequest{Instruction: "call an offered tool", TurnBudget: 4},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.req.Validate()
			if tc.want {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want ErrInvalidSteer")
			}
			if !errors.Is(err, toolcall.ErrInvalidSteer) {
				t.Fatalf("Validate() = %v, want an error wrapping ErrInvalidSteer", err)
			}
		})
	}
}

func TestResult_UsableSteer(t *testing.T) {
	t.Parallel()
	valid := &toolcall.SteerRequest{Instruction: "call an offered tool", TurnBudget: 2}
	invalid := &toolcall.SteerRequest{Instruction: "call an offered tool", TurnBudget: 0}
	cases := []struct {
		name string
		res  toolcall.Result
		want bool
	}{
		{
			name: "steerable action with valid request",
			res:  toolcall.Result{Action: toolcall.ActionSteer, Steer: valid},
			want: true,
		},
		{
			name: "steerable action without request",
			res:  toolcall.Result{Action: toolcall.ActionSteer},
			want: false,
		},
		{
			name: "steerable action with invalid request",
			res:  toolcall.Result{Action: toolcall.ActionSteer, Steer: invalid},
			want: false,
		},
		{
			name: "steerable action with rewritten tool name",
			res:  toolcall.Result{Action: toolcall.ActionSteer, ToolName: "invented_tool", Steer: valid},
			want: false,
		},
		{
			name: "steerable action with rewritten arguments",
			res:  toolcall.Result{Action: toolcall.ActionSteer, ArgsJSON: []byte(`{"path":"a"}`), Steer: valid},
			want: false,
		},
		{
			name: "steerable action with empty non-nil arguments",
			res:  toolcall.Result{Action: toolcall.ActionSteer, ArgsJSON: []byte{}, Steer: valid},
			want: false,
		},
		{
			// A request on any other action never yields a usable steer; core
			// owns the whole-result check (Action != ActionSteer && Steer != nil).
			name: "pass carrying a request",
			res:  toolcall.Result{Action: toolcall.ActionPass, Steer: valid},
			want: false,
		},
		{
			name: "rewrite carrying a request",
			res:  toolcall.Result{Action: toolcall.ActionRewrite, ToolName: "offered_tool", ArgsJSON: []byte(`{}`), Steer: valid},
			want: false,
		},
		{
			name: "reject carrying a request",
			res:  toolcall.Result{Action: toolcall.ActionReject, Steer: valid},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.res.UsableSteer()
			if ok != tc.want {
				t.Fatalf("UsableSteer() ok = %v, want %v", ok, tc.want)
			}
			if !tc.want {
				if got != nil {
					t.Fatalf("UsableSteer() = %v, want nil for an unusable result", got)
				}
				return
			}
			if got != tc.res.Steer {
				t.Fatalf("UsableSteer() = %p, want the result's own request %p", got, tc.res.Steer)
			}
		})
	}
}
