package auxiliary_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// externalCompatClient simulates an external SDK consumer that implements only
// the historical three-method BackgroundClient. It must remain source-compatible
// after the optional BackgroundPoller capability is introduced.
type externalCompatClient struct{}

func (externalCompatClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}

func (externalCompatClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (externalCompatClient) Forget(auxiliary.JobID) {}

// Compile-time assertion: external three-method client still satisfies BackgroundClient.
var (
	_ auxiliary.BackgroundClient = externalCompatClient{}
	_ auxiliary.BackgroundClient = (*externalCompatClient)(nil)
)

func TestBackgroundClient_SourceCompatibility_ExternalThreeMethodStillSatisfies(t *testing.T) {
	t.Parallel()
	var c auxiliary.BackgroundClient = externalCompatClient{}
	// Ensure it deliberately does NOT satisfy the optional BackgroundPoller.
	if _, isPoller := any(c).(auxiliary.BackgroundPoller); isPoller {
		t.Fatalf("external three-method client must NOT satisfy BackgroundPoller; Poll must be optional")
	}
	if _, isPoller := any(externalCompatClient{}).(auxiliary.BackgroundPoller); isPoller {
		t.Fatalf("external value must NOT satisfy BackgroundPoller")
	}
}

func TestBackgroundClient_DisabledStillDoesNotRequirePoll(t *testing.T) {
	t.Parallel()
	var c auxiliary.BackgroundClient = auxiliary.DisabledBackgroundClient{}
	if _, isPoller := any(c).(auxiliary.BackgroundPoller); isPoller {
		t.Fatalf("DisabledBackgroundClient must NOT be forced to implement BackgroundPoller")
	}
	// Historical interface still has exactly three required methods; verify via assignment.
	var _ auxiliary.BackgroundClient = auxiliary.DisabledBackgroundClient{}
}
