package host_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
)

// TestSession_DescribeReportsRuntimeDescriptor proves the host activation
// path can fetch the live runtime descriptor over a configured session: the
// reported factories must match what the plugin serves, so the
// manifest/descriptor comparison runs against fresh runtime metadata.
func TestSession_DescribeReportsRuntimeDescriptor(t *testing.T) {
	t.Parallel()
	plugin := &describeFake{publicFake: &publicFake{}}
	conn := startPublicFake(t, plugin)
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "describe", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	descriptor, err := sess.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(descriptor.Factories) != 1 || descriptor.Factories[0].Kind != "fake" {
		t.Fatalf("descriptor factories = %+v, want the served fake export", descriptor.Factories)
	}
}

// describeFake reuses the shared harness service but serves a convertible
// runtime descriptor (the shared fake omits credential posture enums).
type describeFake struct {
	*publicFake
}

func (f *describeFake) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorProxyOwnedSessionID,
		PluginID: "public-fake", Version: "v1",
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:               "fake",
			CredentialMode:     backendplugin.CredentialModeNone,
			AccessScope:        backendplugin.AccessScopeLocalOnly,
			ProcessSharing:     backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true},
		}},
	}, nil
}
