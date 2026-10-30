package runtimebundle

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestActivation_DescribeMismatchReceivesSecretsBeforeRejection pins the
// accepted activation-ordering trust boundary (task 12, requirement 5.3):
// Describe runs after authenticated Configure, so a drifted-but-authenticated
// peer receives its Configure secrets before the host rejects the descriptor
// mismatch and releases the candidate. Reordering would not strengthen the
// boundary — the peer is already the digest-verified trusted-root artifact
// running as the proxy service account (see docs/backend-plugins/operator.md
// trust posture) — so the host documents this ordering and always fails the
// activation closed with a secret-free error instead of claiming pre-delivery
// verification.
func TestActivation_DescribeMismatchReceivesSecretsBeforeRejection(t *testing.T) {
	t.Parallel()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 4242},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	var closed atomic.Int64
	var secretNames int
	rogue := backendplugin.FactoryDescriptor{
		Kind:           "undeclared-kind",
		CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope:    backendplugin.AccessScopeAny,
		ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}
	dial := func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		secretNames = len(req.Secrets.Values)
		fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return &activationDescribeSession{
			ConfiguredInstance: inst,
			descriptor:         inferenceActivationDescriptor(rogue),
			closed:             &closed,
		}, profile, nil
	}
	export := ValidatedExport{
		Kind:     "openrouter",
		Artifact: &trust.VerifiedArtifact{DigestHex: "sha256:openrouter-artifact"},
		Model:    processhost.ProcessModelPerInstance,
		Manifest: inferenceActivationManifest(t),
	}
	input := inferencePhysicalInput()
	input.InstanceID = "or-1"
	_, _, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, new(atomic.Uint64), nil)
	if err == nil {
		t.Fatal("descriptor exporting an undeclared kind must fail activation")
	}
	if secretNames == 0 {
		t.Fatal("documented ordering: the authenticated peer received Configure secrets before Describe rejection")
	}
	if closed.Load() != 1 {
		t.Fatalf("mismatched activation closed the session %d times, want exactly once", closed.Load())
	}
	if strings.Contains(err.Error(), "or-key") {
		t.Fatalf("secret-bearing activation error: %v", err)
	}
}
