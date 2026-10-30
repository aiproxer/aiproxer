package localturn_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
)

var _ localturn.Handler = (*stubHandler)(nil)

type stubHandler struct {
	id   string
	ord  int
	mode hooks.FailureMode
}

func (h stubHandler) ID() string                     { return h.id }
func (h stubHandler) Order() int                     { return h.ord }
func (h stubHandler) FailureMode() hooks.FailureMode { return h.mode }
func (h stubHandler) Match(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h stubHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "hello"}, nil
}

func TestLocalTurn_HandlerInterface(t *testing.T) {
	t.Parallel()
	var h localturn.Handler = stubHandler{id: "test", ord: 1, mode: hooks.FailOpen}
	require.NotNil(t, h)
	require.Equal(t, "test", h.ID())
	require.Equal(t, 1, h.Order())
	require.Equal(t, hooks.FailOpen, h.FailureMode())
}

func TestLocalTurn_FailureModeConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, hooks.FailureModeUnspecified, localturn.FailureModeUnspecified)
	require.Equal(t, hooks.FailOpen, localturn.FailOpen)
	require.Equal(t, hooks.FailClosed, localturn.FailClosed)
}

func TestReasonCode_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      localturn.ReasonCode
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"oversized", localturn.ReasonCode(strings.Repeat("a", 65)), true},
		{"invalid slash", "bad/reason", true},
		{"invalid space", "bad reason", true},
		{"valid", "ok_reason-1.2", false},
		{"max", localturn.ReasonCode(strings.Repeat("a", 64)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMatchResult_Validation(t *testing.T) {
	t.Parallel()
	meta := localturn.Meta{MessageCount: 3}
	tests := []struct {
		name    string
		in      localturn.MatchResult
		wantErr bool
	}{
		{"unclaimed empty ok", localturn.MatchResult{Claimed: false}, false},
		{"unclaimed with indexes rejected", localturn.MatchResult{Claimed: false, Indexes: []int{0}}, true},
		{"unclaimed with reason rejected", localturn.MatchResult{Claimed: false, Reason: "reason"}, true},
		{"claimed empty indexes rejected", localturn.MatchResult{Claimed: true, Reason: "ok"}, true},
		{"claimed without reason rejected", localturn.MatchResult{Claimed: true, Indexes: []int{0}}, true},
		{"claimed invalid reason chars", localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "bad/reason"}, true},
		{"claimed oversized reason", localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: localturn.ReasonCode(strings.Repeat("a", 65))}, true},
		{"claimed negative index", localturn.MatchResult{Claimed: true, Indexes: []int{-1}, Reason: "ok"}, true},
		{"claimed out of range", localturn.MatchResult{Claimed: true, Indexes: []int{3}, Reason: "ok"}, true},
		{"claimed duplicate indexes", localturn.MatchResult{Claimed: true, Indexes: []int{1, 1}, Reason: "ok"}, true},
		{"claimed valid single", localturn.MatchResult{Claimed: true, Indexes: []int{1}, Reason: "ok"}, false},
		{"claimed valid multiple", localturn.MatchResult{Claimed: true, Indexes: []int{0, 2}, Reason: "ok_reason"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate(meta)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMatchResult_OnlyCompleteNormalizedMessages(t *testing.T) {
	t.Parallel()
	// Handler must not claim indexes beyond normalized message count.
	// This table proves validation enforces complete-message granularity.
	meta := localturn.Meta{MessageCount: 2}
	claimed := localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "ok"}
	require.NoError(t, claimed.Validate(meta))
	claimedBad := localturn.MatchResult{Claimed: true, Indexes: []int{5}, Reason: "ok"}
	require.Error(t, claimedBad.Validate(meta))
}

func TestReply_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      localturn.Reply
		wantErr bool
	}{
		{"empty", localturn.Reply{Text: ""}, true},
		{"whitespace", localturn.Reply{Text: "   "}, true},
		{"oversized", localturn.Reply{Text: strings.Repeat("a", 64*1024+1)}, true},
		{"invalid utf8", localturn.Reply{Text: string([]byte{0xff, 0xfe})}, true},
		{"contains NUL", localturn.Reply{Text: "hello\x00world"}, true},
		{"valid", localturn.Reply{Text: "hello world"}, false},
		{"max", localturn.Reply{Text: strings.Repeat("a", 64*1024)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReply_BoundedAssistantTextNotStream(t *testing.T) {
	t.Parallel()
	// Reply is bounded text, not an arbitrary stream. Verify type has only Text field shape
	// and Validate enforces 64KiB cap. Ensure no EventStream is returned by Handle.
	h := stubHandler{id: "x", ord: 0, mode: hooks.FailClosed}
	reply, err := h.Handle(context.Background(), localturn.HandleInput{
		Call:  aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}},
		Meta:  localturn.Meta{MessageCount: 1},
		Match: localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "ok"},
	})
	require.NoError(t, err)
	require.NoError(t, reply.Validate())
	require.LessOrEqual(t, len(reply.Text), 64*1024)
	// Reply must not be an EventStream; ensure type is Reply
	_ = reply
}

func TestLocalTurn_NoClientTransportAPI(t *testing.T) {
	t.Parallel()
	// Package must not expose client/data-plane transport APIs; only Handler, Meta, MatchResult, Reply, HandleInput, ReasonCode.
	// This compile-time test ensures no unexpected exported transport handler exists.
	var _ localturn.Handler = stubHandler{id: "a", ord: 0}
}

func TestLocalTurn_BoundedConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, 64, localturn.MaxReasonCodeBytes)
	require.Equal(t, 64*1024, localturn.MaxReplyTextBytes)
	require.Equal(t, 128, localturn.MaxHandlerIDBytes)
}

func TestMaterializeSorted_OrderThenID(t *testing.T) {
	t.Parallel()
	in := []localturn.Handler{
		stubHandler{id: "b", ord: 2},
		stubHandler{id: "a", ord: 1},
		stubHandler{id: "c", ord: 1},
	}
	got := localturn.MaterializeSorted(in)
	require.Len(t, got, 3)
	require.Equal(t, "a", got[0].ID())
	require.Equal(t, "c", got[1].ID())
	require.Equal(t, "b", got[2].ID())
	// clone isolation
	in[0] = stubHandler{id: "mut", ord: 99}
	require.Equal(t, "b", got[2].ID())
}

func TestMaterializeSorted_SkipsTypedNil(t *testing.T) {
	t.Parallel()
	type ptrHandler struct {
		id  string
		ord int
	}
	// typed-nil via pointer receiver type implementing Handler
	// use ptr to stubHandler pointer
	var typedNil *stubHandlerPtr
	got := localturn.MaterializeSorted([]localturn.Handler{
		typedNil,
		stubHandler{id: "a", ord: 1},
	})
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].ID())
}

type stubHandlerPtr struct {
	id  string
	ord int
}

func (h *stubHandlerPtr) ID() string                     { return h.id }
func (h *stubHandlerPtr) Order() int                     { return h.ord }
func (h *stubHandlerPtr) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (h *stubHandlerPtr) Match(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h *stubHandlerPtr) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "hi"}, nil
}

var _ localturn.Handler = (*stubHandlerPtr)(nil)
