package extensions_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

const (
	stageControlProviderID  = "generic.stage.control"
	stageControlToolName    = "proxy_control"
	stageControlInstruction = "Call proxy_control only when the assigned proxy-local control action is complete."
	stageControlSchema      = `{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`
)

// stageControlProvider is the generic control provider for the stage seam tests.
// It counts spec reads and can be armed to panic, so a test can prove the stage
// resolves a spec exactly once per candidate behind the safety boundary.
type stageControlProvider struct {
	id         string
	spec       controltool.Spec
	specPanic  bool
	specCalls  int
	handleCall int
}

func (p *stageControlProvider) ID() string { return p.id }

func (p *stageControlProvider) Spec() controltool.Spec {
	p.specCalls++
	if p.specPanic {
		panic("control spec unavailable")
	}
	return p.spec
}

func (p *stageControlProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handleCall++
	return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "control_handler_reached"}, nil
}

func stageControlSpec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        stageControlToolName,
			Description: "Call this only when the assigned proxy-local control action is complete.",
			Parameters:  json.RawMessage(stageControlSchema),
		},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: stageControlInstruction},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

func stageControlRequest(provider controltool.Provider) extensions.ControlToolProjectionRequest {
	call := aipapi.Call{
		ID: "stage-control",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("do the work")},
		}},
	}
	return extensions.ControlToolProjectionRequest{
		ProviderID: stageControlProviderID,
		Provider:   provider,
		Call:       call,
		Caps:       aipapi.NewBackendCaps(aipapi.CapabilityTools),
	}
}

func stageCountControlTools(call aipapi.Call) int {
	count := 0
	for _, tool := range call.Tools {
		if tool.Name == stageControlToolName {
			count++
		}
	}
	return count
}

func stageCountControlText(call aipapi.Call) int {
	count := 0
	for _, message := range call.Instructions {
		for _, part := range message.Parts {
			if part.Text == stageControlInstruction {
				count++
			}
		}
	}
	return count
}

// TestProjectControlTool_activeCandidateProjectsOnceAndNeverHandles pins the
// successful generic path: the spec is read exactly once per candidate, the
// client tool choice is untouched, the appended contract is the provider's byte
// identity, and the control handler is never reachable from the request path.
func TestProjectControlTool_activeCandidateProjectsOnceAndNeverHandles(t *testing.T) {
	t.Parallel()

	provider := &stageControlProvider{id: stageControlProviderID, spec: stageControlSpec()}
	in := stageControlRequest(controltool.Provider(provider))
	clientChoice := in.Call.ToolChoice

	projected, activation, err := extensions.ProjectControlTool(in)
	require.NoError(t, err, "an eligible generic control spec must project")
	require.NoError(t, projected.Validate(), "the projected call must stay canonically valid")
	assert.True(t, activation.Active(), "an eligible candidate must activate the control contract")
	assert.Empty(t, activation.Reason(), "an active projection has no inactive reason")
	assert.Equal(t, stageControlProviderID, activation.ProviderID, "the pinned identity must be the caller's frozen identity")
	assert.Same(t, provider, activation.Provider, "the activation must pin the caller's provider instance")
	assert.Equal(t, 1, provider.specCalls, "the spec must be resolved exactly once per candidate")
	assert.Zero(t, provider.handleCall, "the request path must never invoke the control handler")

	assert.Equal(t, 1, stageCountControlTools(projected), "exactly one control tool may be projected")
	assert.Equal(t, 1, stageCountControlText(projected), "exactly one control instruction may be projected")
	assert.Equal(t, clientChoice, projected.ToolChoice, "the client tool choice must never be rewritten")
	assert.Empty(t, in.Call.Tools, "the input call must not be mutated")
	assert.Equal(t, controltool.DefaultMaxArgsBytes, activation.Projection.MaxArgsBytes(),
		"the approved args budget must come from the provider spec")
}

// TestProjectControlTool_ineligibleCandidateIsInactiveNotAnError pins ordinary
// ineligibility: the bounded reason is reported, the call is returned exactly as
// supplied, and an optional control feature never becomes a candidate rejection.
func TestProjectControlTool_ineligibleCandidateIsInactiveNotAnError(t *testing.T) {
	t.Parallel()

	provider := &stageControlProvider{id: stageControlProviderID, spec: stageControlSpec()}
	in := stageControlRequest(controltool.Provider(provider))
	in.Caps = aipapi.NewBackendCaps(aipapi.CapabilityStreaming)

	projected, activation, err := extensions.ProjectControlTool(in)
	require.NoError(t, err, "an ineligible control candidate is not an error")
	assert.False(t, activation.Active())
	assert.Equal(t, controltool.ReasonBackendToolsUnsupported, activation.Reason())
	assert.Equal(t, in.Call, projected, "an inactive projection must return the call unchanged")
}

// TestProjectControlTool_failsClosedOnUnusableProvider pins the stage's
// fail-closed inputs: no provider, an unnamed provider, an invalid spec, and a
// provider that panics while producing its spec. Every failure returns the call
// unchanged and a bounded, content-free error, and none of them reaches the
// control handler.
func TestProjectControlTool_failsClosedOnUnusableProvider(t *testing.T) {
	t.Parallel()

	t.Run("absent_provider", func(t *testing.T) {
		t.Parallel()

		in := stageControlRequest(nil)
		projected, activation, err := extensions.ProjectControlTool(in)
		require.ErrorIs(t, err, controltool.ErrInvalidProvider)
		assert.Equal(t, in.Call, projected)
		assert.False(t, activation.Active())
	})

	t.Run("unnamed_provider", func(t *testing.T) {
		t.Parallel()

		in := stageControlRequest(controltool.Provider(&stageControlProvider{id: stageControlProviderID, spec: stageControlSpec()}))
		in.ProviderID = ""
		_, activation, err := extensions.ProjectControlTool(in)
		require.ErrorIs(t, err, controltool.ErrInvalidProvider, "an unnamed owner must never project a contract")
		assert.False(t, activation.Active())
	})

	t.Run("invalid_spec", func(t *testing.T) {
		t.Parallel()

		provider := &stageControlProvider{id: stageControlProviderID, spec: controltool.Spec{}}
		in := stageControlRequest(controltool.Provider(provider))
		projected, activation, err := extensions.ProjectControlTool(in)
		require.ErrorIs(t, err, controltool.ErrInvalidSpec, "a generation spec defect is not per-request best effort")
		assert.Equal(t, in.Call, projected)
		assert.False(t, activation.Active())
		assert.Zero(t, provider.handleCall, "a broken spec must never reach the control handler")
	})

	t.Run("spec_panic_is_isolated", func(t *testing.T) {
		t.Parallel()

		provider := &stageControlProvider{id: stageControlProviderID, spec: stageControlSpec(), specPanic: true}
		in := stageControlRequest(controltool.Provider(provider))
		projected, activation, err := extensions.ProjectControlTool(in)
		require.Error(t, err, "a provider spec panic must fail the candidate closed")
		assert.Contains(t, err.Error(), "boundary=extension_execution", "the spec call must run behind the extension boundary")
		assert.NotContains(t, err.Error(), "control spec unavailable", "a provider panic payload must never reach the error text")
		assert.Equal(t, in.Call, projected)
		assert.False(t, activation.Active())
		assert.Equal(t, stageControlProviderID, activation.ProviderID, "a failed projection must still report the pinned identity")
	})
}
