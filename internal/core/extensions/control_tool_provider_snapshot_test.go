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
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type snapshotControlToolProvider struct {
	id      string
	handles int
}

func (p *snapshotControlToolProvider) ID() string { return p.id }

func (p *snapshotControlToolProvider) Spec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        "proxy_control",
			Description: "Call this only when the assigned proxy-local control action is complete.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"note": {
						"type": "string",
						"description": "Bounded control result."
					}
				},
				"required": ["note"],
				"additionalProperties": false
			}`),
		},
		Instruction: controltool.Instruction{
			Role: aipapi.RoleSystem,
			Text: "Call proxy_control only when the assigned work is complete.",
		},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

func (p *snapshotControlToolProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handles++
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "done", ReasonCode: "complete"}, nil
}

// snapshotCountingControlProvider records live identity reads so a test can
// prove the frozen-identity accessor never makes one, and can be armed to panic
// on any read that would break the generation pin.
type snapshotCountingControlProvider struct {
	snapshotControlToolProvider
	liveIDPanic  bool
	liveIDCalls  int
	specPanic    bool
	specCallSeen int
}

func (p *snapshotCountingControlProvider) ID() string {
	p.liveIDCalls++
	if p.liveIDPanic {
		panic("live control provider identity read on the request path")
	}
	return p.id
}

func (p *snapshotCountingControlProvider) Spec() controltool.Spec {
	p.specCallSeen++
	if p.specPanic {
		panic("control spec unavailable")
	}
	return p.snapshotControlToolProvider.Spec()
}

// TestRequestRuntimeSnapshot_ControlToolProviderIdentity pins the frozen identity
// accessor the request path uses to pin its generation and honor plugin
// suppression: it reports the cached validated identity for an occupied plane,
// reports absence for an unoccupied one, and never reads the provider's live
// identity, so it is safe to call before any spec resolution.
func TestRequestRuntimeSnapshot_ControlToolProviderIdentity(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver_reports_absent_identity", func(t *testing.T) {
		t.Parallel()

		var snap *extensions.RequestRuntimeSnapshot
		id, ok := snap.ControlToolProviderIdentity()
		assert.False(t, ok)
		assert.Empty(t, id)
	})

	t.Run("absent_plane_reports_absent_identity_without_provider_calls", func(t *testing.T) {
		t.Parallel()

		id, ok := snapshotWithControlTool(t, nil).ControlToolProviderIdentity()
		assert.False(t, ok, "an unoccupied control_tool_provider plane has no frozen identity")
		assert.Empty(t, id)
	})

	t.Run("occupied_plane_reads_cached_identity_without_live_identity_call", func(t *testing.T) {
		t.Parallel()

		provider := &snapshotCountingControlProvider{
			snapshotControlToolProvider: snapshotControlToolProvider{id: "proxy.control.example"},
		}
		snap := snapshotWithControlTool(t, controltool.Provider(provider))
		composed := provider.liveIDCalls
		require.Positive(t, composed, "composition must validate the provider identity once")
		liveCalls := provider.liveIDCalls
		specCalls := provider.specCallSeen

		// Arm the tripwire: any live identity read from here on panics.
		provider.liveIDPanic = true
		require.NotPanics(t, func() {
			id, ok := snap.ControlToolProviderIdentity()
			assert.True(t, ok, "an occupied plane must expose its frozen identity")
			assert.Equal(t, "proxy.control.example", id)
		})
		assert.Equal(t, liveCalls, provider.liveIDCalls, "frozen identity must not read the live provider identity")
		assert.Equal(t, specCalls, provider.specCallSeen, "frozen identity must not resolve the provider spec")
		assert.Zero(t, provider.handles, "frozen identity must not invoke the provider")
	})
}

func snapshotWithControlTool(t *testing.T, provider controltool.Provider) *extensions.RequestRuntimeSnapshot {
	t.Helper()

	cs := aipfeature.NewContributionSet()
	if provider != nil {
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, "plugin-1", provider))
	}
	return extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
}

// TestRequestRuntimeSnapshot_ControlToolProvider pins the snapshot accessor: a
// generation-admitted provider reads back from the immutable request snapshot,
// and a generation without one reads nil without any provider method call.
func TestRequestRuntimeSnapshot_ControlToolProvider(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver_returns_zero_value", func(t *testing.T) {
		t.Parallel()

		var snap *extensions.RequestRuntimeSnapshot
		assert.Nil(t, snap.ControlToolProvider())
	})

	t.Run("absent_provider_is_nil_without_provider_calls", func(t *testing.T) {
		t.Parallel()

		snap := snapshotWithControlTool(t, nil)
		assert.Nil(t, snap.ControlToolProvider(), "absent control-tool plane must read nil")
	})

	t.Run("admitted_provider_reads_back", func(t *testing.T) {
		t.Parallel()

		provider := &snapshotControlToolProvider{id: "proxy.control.example"}
		snap := snapshotWithControlTool(t, controltool.Provider(provider))

		got := snap.ControlToolProvider()
		require.NotNil(t, got, "occupied control_tool_provider must read back")
		assert.Equal(t, "proxy.control.example", got.ID())
		assert.Zero(t, provider.handles, "snapshot construction and access must not invoke the provider")
	})
}

// TestRequestRuntimeSnapshot_ControlToolProviderRemovalIsGenerationScoped proves
// a newly built snapshot without the provider does not disturb previously built
// occupied snapshots (requirement 10.5).
func TestRequestRuntimeSnapshot_ControlToolProviderRemovalIsGenerationScoped(t *testing.T) {
	t.Parallel()

	provider := &snapshotControlToolProvider{id: "proxy.control.example"}
	occupied := snapshotWithControlTool(t, controltool.Provider(provider))
	require.NotNil(t, occupied.ControlToolProvider())

	withdrawn := snapshotWithControlTool(t, nil)
	assert.Nil(t, withdrawn.ControlToolProvider(), "new generation must not inherit the removed provider")

	got := occupied.ControlToolProvider()
	require.NotNil(t, got, "previously admitted snapshot must remain occupied")
	assert.Equal(t, "proxy.control.example", got.ID())
	assert.Zero(t, provider.handles)
}
