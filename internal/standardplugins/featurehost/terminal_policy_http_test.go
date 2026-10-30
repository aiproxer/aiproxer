package featurehost

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type policyTestTerminalDecisionProvider struct {
	id string
}

func (p *policyTestTerminalDecisionProvider) ID() string { return p.id }
func (p *policyTestTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

func TestTerminalDecisionPolicyHTTPProjection_Snapshots(t *testing.T) {
	t.Parallel()

	provA := &policyTestTerminalDecisionProvider{id: "provider-a"}
	provB := &policyTestTerminalDecisionProvider{id: "provider-b"}

	// G1 snapshot has provider A
	set1 := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(set1, aipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA)))
	snapG1 := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: set1.Freeze(),
	})

	// G2 snapshot has provider B
	set2 := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(set2, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provB)))
	snapG2 := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: set2.Freeze(),
	})

	// G3 snapshot has no provider
	snapG3 := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{})

	// Capture all three returned TerminalDecisionPolicyInput values before any assertion
	projG1 := TerminalDecisionPolicyHTTPProjection(nil, snapG1, aipsdk.HTTPHeaders{}, 65536, nil)
	projG2 := TerminalDecisionPolicyHTTPProjection(nil, snapG2, aipsdk.HTTPHeaders{}, 65536, nil)
	projG3 := TerminalDecisionPolicyHTTPProjection(nil, snapG3, aipsdk.HTTPHeaders{}, 65536, nil)

	ctx := context.Background()

	// G1 assertions
	known1, avail1, err1 := projG1.FeatureStatus(ctx, "terminal-decision")
	require.NoError(t, err1)
	assert.True(t, known1)
	assert.True(t, avail1)
	assert.True(t, projG1.GenerationDefault("terminal-decision"))

	known1Unk, avail1Unk, err1Unk := projG1.FeatureStatus(ctx, "unknown-feature")
	require.NoError(t, err1Unk)
	assert.False(t, known1Unk)
	assert.True(t, avail1Unk, "existing contract preserves available=generationProviderPresent for unknown feature")
	assert.False(t, projG1.GenerationDefault("unknown-feature"))

	// G2 assertions
	known2, avail2, err2 := projG2.FeatureStatus(ctx, "terminal-decision")
	require.NoError(t, err2)
	assert.True(t, known2)
	assert.True(t, avail2)
	assert.True(t, projG2.GenerationDefault("terminal-decision"))

	known2Unk, avail2Unk, err2Unk := projG2.FeatureStatus(ctx, "unknown-feature")
	require.NoError(t, err2Unk)
	assert.False(t, known2Unk)
	assert.True(t, avail2Unk)
	assert.False(t, projG2.GenerationDefault("unknown-feature"))

	// G3 assertions
	known3, avail3, err3 := projG3.FeatureStatus(ctx, "terminal-decision")
	require.NoError(t, err3)
	assert.True(t, known3)
	assert.False(t, avail3)
	assert.False(t, projG3.GenerationDefault("terminal-decision"))

	known3Unk, avail3Unk, err3Unk := projG3.FeatureStatus(ctx, "unknown-feature")
	require.NoError(t, err3Unk)
	assert.False(t, known3Unk)
	assert.False(t, avail3Unk)
	assert.False(t, projG3.GenerationDefault("unknown-feature"))

	// After creating G2 and G3 projections, re-call G1 callbacks and prove they retain G1 values
	known1Post, avail1Post, err1Post := projG1.FeatureStatus(ctx, "terminal-decision")
	require.NoError(t, err1Post)
	assert.True(t, known1Post)
	assert.True(t, avail1Post)
	assert.True(t, projG1.GenerationDefault("terminal-decision"))

	known1UnkPost, avail1UnkPost, err1UnkPost := projG1.FeatureStatus(ctx, "unknown-feature")
	require.NoError(t, err1UnkPost)
	assert.False(t, known1UnkPost)
	assert.True(t, avail1UnkPost)
	assert.False(t, projG1.GenerationDefault("unknown-feature"))
}
