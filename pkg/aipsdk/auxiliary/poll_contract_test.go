package auxiliary_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// fakePoller is a minimal in-memory BackgroundPoller for contract tests. It is
// intentionally separate from the process-owned scheduler and validates the
// exported capability contract in isolation.
type fakePoller struct {
	jobs map[auxiliary.JobID]auxiliary.PollResult
}

func newFakePoller() *fakePoller {
	return &fakePoller{jobs: make(map[auxiliary.JobID]auxiliary.PollResult)}
}

func (f *fakePoller) Poll(_ context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	res, ok := f.jobs[id]
	if !ok {
		return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
	}
	switch res.State {
	case auxiliary.PollPending, auxiliary.PollNotFound:
		return auxiliary.PollResult{State: res.State}, nil
	case auxiliary.PollFailed:
		return auxiliary.PollResult{State: res.State, Err: res.Err}, nil
	case auxiliary.PollCompleted:
		var cloned aipapi.Collected
		cloned.Text.WriteString(res.Collected.Text.String())
		cloned.Reasoning.WriteString(res.Collected.Reasoning.String())
		if res.Collected.ToolArgs != nil {
			cloned.ToolArgs = make(map[string]*strings.Builder, len(res.Collected.ToolArgs))
			for k, b := range res.Collected.ToolArgs {
				if b == nil {
					cloned.ToolArgs[k] = nil
					continue
				}
				nb := &strings.Builder{}
				nb.WriteString(b.String())
				cloned.ToolArgs[k] = nb
			}
		}
		if res.Collected.ToolNames != nil {
			cloned.ToolNames = make(map[string]string, len(res.Collected.ToolNames))
			maps.Copy(cloned.ToolNames, res.Collected.ToolNames)
		}
		if res.Collected.ToolCallOrder != nil {
			cloned.ToolCallOrder = append([]string(nil), res.Collected.ToolCallOrder...)
		}
		if res.Collected.Warnings != nil {
			cloned.Warnings = append([]string(nil), res.Collected.Warnings...)
		}
		cloned.InputTokens = res.Collected.InputTokens
		cloned.OutputTokens = res.Collected.OutputTokens
		return auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: cloned}, nil
	default:
		return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
	}
}

var _ auxiliary.BackgroundPoller = (*fakePoller)(nil)

func TestBackgroundPoller_Contract_FourStates(t *testing.T) {
	t.Parallel()
	fp := newFakePoller()
	pendingID := auxiliary.JobID("pending-1")
	completedID := auxiliary.JobID("completed-1")
	failedID := auxiliary.JobID("failed-1")
	notFoundID := auxiliary.JobID("not-found-1")

	var completed aipapi.Collected
	completed.Text.WriteString("hello")
	fp.jobs[pendingID] = auxiliary.PollResult{State: auxiliary.PollPending}
	fp.jobs[completedID] = auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: completed}
	fp.jobs[failedID] = auxiliary.PollResult{State: auxiliary.PollFailed, Err: assert.AnError}

	cases := []struct {
		name  string
		id    auxiliary.JobID
		state auxiliary.PollState
	}{
		{"pending", pendingID, auxiliary.PollPending},
		{"completed", completedID, auxiliary.PollCompleted},
		{"failed", failedID, auxiliary.PollFailed},
		{"not_found", notFoundID, auxiliary.PollNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := fp.Poll(context.Background(), tc.id)
			require.NoError(t, err)
			require.Equal(t, tc.state, res.State)
		})
	}
}

func TestBackgroundPoller_Contract_ErrOnlyOnFailed(t *testing.T) {
	t.Parallel()
	fp := newFakePoller()
	var completed aipapi.Collected
	completed.Text.WriteString("ok")
	fp.jobs["p"] = auxiliary.PollResult{State: auxiliary.PollPending}
	fp.jobs["c"] = auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: completed}
	fp.jobs["f"] = auxiliary.PollResult{State: auxiliary.PollFailed, Err: assert.AnError}
	fp.jobs["n"] = auxiliary.PollResult{State: auxiliary.PollNotFound}

	// pending/completed/not_found must have nil Err
	for _, id := range []auxiliary.JobID{"p", "c", "n"} {
		res, err := fp.Poll(context.Background(), id)
		require.NoError(t, err)
		assert.Nil(t, res.Err, "state %v should have nil Err", res.State)
	}
	res, err := fp.Poll(context.Background(), "f")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, res.State)
	require.Error(t, res.Err)
}

func TestBackgroundPoller_Contract_NilContentForPendingAndNotFound(t *testing.T) {
	t.Parallel()
	fp := newFakePoller()
	fp.jobs["pending"] = auxiliary.PollResult{State: auxiliary.PollPending, Collected: func() aipapi.Collected {
		var c aipapi.Collected
		c.Text.WriteString("should-not-be-returned")
		return c
	}()}
	fp.jobs["completed"] = auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: func() aipapi.Collected {
		var c aipapi.Collected
		c.Text.WriteString("real")
		return c
	}()}

	pending, err := fp.Poll(context.Background(), "pending")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, pending.State)
	assert.Equal(t, 0, pending.Collected.Text.Len(), "pending must return no content")
	assert.Nil(t, pending.Collected.ToolArgs)

	notFound, err := fp.Poll(context.Background(), "missing")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, notFound.State)
	assert.Equal(t, 0, notFound.Collected.Text.Len(), "not_found must return no content")

	completed, err := fp.Poll(context.Background(), "completed")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, completed.State)
	assert.Equal(t, "real", completed.Collected.Text.String())
}

func TestBackgroundPoller_Contract_DefensiveCopyOnCompleted(t *testing.T) {
	t.Parallel()
	fp := newFakePoller()
	var original aipapi.Collected
	original.Text.WriteString("original")
	original.ToolNames = map[string]string{"id-1": "tool-a"}
	b := &strings.Builder{}
	b.WriteString(`{"a":1}`)
	original.ToolArgs = map[string]*strings.Builder{"id-1": b}
	original.ToolCallOrder = []string{"id-1"}
	fp.jobs["job-1"] = auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: original}

	first, err := fp.Poll(context.Background(), "job-1")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, first.State)
	require.Equal(t, "original", first.Collected.Text.String())

	// Mutate returned copy without writing directly on the copied Builder
	// (which would panic due to builder copy detection). Replace the builder
	// with a fresh zero value before writing.
	first.Collected.Text = strings.Builder{}
	first.Collected.Text.WriteString("mutated")
	first.Collected.ToolNames["id-1"] = "mutated"
	if b2 := first.Collected.ToolArgs["id-1"]; b2 != nil {
		b2.WriteString("-mutated")
	}
	first.Collected.ToolCallOrder[0] = "mutated"

	second, err := fp.Poll(context.Background(), "job-1")
	require.NoError(t, err)
	assert.Equal(t, "original", second.Collected.Text.String(), "defensive copy must be isolated")
	assert.Equal(t, "tool-a", second.Collected.ToolNames["id-1"])
	assert.Equal(t, `{"a":1}`, second.Collected.ToolArgs["id-1"].String())
	assert.Equal(t, []string{"id-1"}, second.Collected.ToolCallOrder)

	// Removing defensive copy would make this fail.
}

func TestBackgroundPoller_PollStateValuesDistinct(t *testing.T) {
	t.Parallel()
	states := []auxiliary.PollState{auxiliary.PollPending, auxiliary.PollCompleted, auxiliary.PollFailed, auxiliary.PollNotFound}
	seen := make(map[auxiliary.PollState]bool)
	for _, s := range states {
		assert.False(t, seen[s], "PollState values must be distinct")
		seen[s] = true
	}
}

func TestBackgroundPoller_InterfaceIsOptional(t *testing.T) {
	t.Parallel()
	// Prove that adding Poll to BackgroundPoller does not retroactively require it on BackgroundClient.
	var client auxiliary.BackgroundClient = externalCompatClient{}
	_, isPoller := any(client).(auxiliary.BackgroundPoller)
	assert.False(t, isPoller, "historical client must not be a Poller")
}
