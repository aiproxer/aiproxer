package reasoningpreservation_test

import (
	"strings"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/stretchr/testify/require"
)

func TestCompressorBilling_WorkloadIdentity(t *testing.T) {
	t.Parallel()
	identity, err := usageevidence.WorkloadIdentityFromAuxiliaryRole(usageevidence.WorkloadRoleReasoningPreservationCompressor)
	require.NoError(t, err)
	require.Equal(t, usageevidence.WorkloadClassAuxiliary, identity.Class)
	require.Equal(t, usageevidence.WorkloadRole(usageevidence.WorkloadRoleReasoningPreservationCompressor), identity.Role)
	require.NoError(t, identity.Validate())

	// metering projects same identity from auxiliary fact
	fact := metering.Fact{Lifecycle: metering.LifecycleAuxiliaryRequest, Scope: scope.PrincipalScopeView{Origin: scope.OriginInternal}}
	projected, err := coremetering.ProjectWorkloadIdentity(fact, string(usageevidence.WorkloadRoleReasoningPreservationCompressor))
	require.NoError(t, err)
	require.Equal(t, identity, projected)

	// control-plane alternative: internal origin also projects auxiliary
	fact2 := metering.Fact{Lifecycle: metering.LifecycleBackendAttempt, Scope: scope.PrincipalScopeView{Origin: scope.OriginInternal}}
	projected2, err := coremetering.ProjectWorkloadIdentity(fact2, string(usageevidence.WorkloadRoleReasoningPreservationCompressor))
	require.NoError(t, err)
	require.Equal(t, usageevidence.WorkloadClassAuxiliary, projected2.Class)
}

func TestCompressorBilling_PromptExcludesControlPlane(t *testing.T) {
	t.Parallel()
	trace := "trace-ctrl-123"
	aleg := "aleg-ctrl-456"
	bleg := "bleg-ctrl-789"
	branch := "branch-ctrl-xyz"
	principalID := "principal-ctrl-999"
	req, err := reasoningpreservation.BuildCompressorAuxRequest(reasoningpreservation.CompressorAuxRequestParams{
		Route:               "compressor-route",
		ParentTraceID:       trace,
		ParentALegID:        aleg,
		ParentBLegID:        bleg,
		ParentBranchBinding: branch,
		Segments:            []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "sanitized reasoning text"}},
		MaxOutputTokens:     100,
	})
	require.NoError(t, err)
	require.Equal(t, "reasoning_preservation_compressor", req.Role)
	require.Equal(t, "private", req.Visibility)
	var modelBlob strings.Builder
	for _, m := range req.Call.Messages {
		for _, p := range m.Parts {
			modelBlob.WriteString(p.Text)
		}
	}
	// sanitized text and schema must be present
	require.Contains(t, modelBlob.String(), "sanitized reasoning text")
	require.Contains(t, modelBlob.String(), reasoningpreservation.CompressorSystemPrompt)
	require.Contains(t, modelBlob.String(), reasoningpreservation.CompressorOutputSchema)
	// control-plane lineage must NOT leak into model prompt
	for _, leak := range []string{trace, aleg, bleg, branch, principalID, "reasoning_preservation_compressor", "private"} {
		require.NotContains(t, modelBlob.String(), leak, "leak %q", leak)
	}
	// envelope retains lineage for billing/routing
	require.Equal(t, trace, req.ParentTraceID)
	require.Equal(t, aleg, req.ParentALegID)
	require.Equal(t, bleg, req.ParentBLegID)
	require.Equal(t, branch, req.ParentBranchBinding)
}

func TestCompressorBilling_RawOversizeStillBillable_AtFeatureBoundary(t *testing.T) {
	t.Parallel()
	// Feature-level raw extractor must reject oversize before decode, but executor billing is independent.
	var c aipapi.Collected
	c.Text.WriteString(strings.Repeat("a", 11) + `{"schema_version":1,"segments":[{"index":0,"text":"ok"}]}`)
	c.FinishReceived = true
	_, err := reasoningpreservation.ExtractBoundedRaw(c, 10)
	require.Error(t, err)
	require.ErrorIs(t, err, reasoningpreservation.ErrRawOversize)

	// Billing identity for compressor remains valid regardless of raw outcome
	identity, err := usageevidence.WorkloadIdentityFromAuxiliaryRole(usageevidence.WorkloadRoleReasoningPreservationCompressor)
	require.NoError(t, err)
	require.True(t, identity.IsAuxiliary())
}
