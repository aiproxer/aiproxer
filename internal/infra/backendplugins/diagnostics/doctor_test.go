package diagnostics_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/diagnostics"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDoctor_OnlySelectedConfiguredInstances(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 9101}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "doctor-aa"}

	rep, err := diagnostics.Doctor(context.Background(), diagnostics.DoctorInput{
		InstanceIDs: []string{"cfg-1"},
		Targets: map[string]diagnostics.DoctorTarget{
			"cfg-1": {InstanceID: "cfg-1", Kind: "synthetic", Artifact: art},
			"cfg-2": {InstanceID: "cfg-2", Kind: "other", Artifact: art},
		},
		Host: h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d want 1 (only selected)", launcher.Launches.Load())
	}
	if len(rep.Results) != 1 || rep.Results[0].InstanceID != "cfg-1" || rep.Results[0].State != catalog.StateActive {
		t.Fatalf("%+v", rep.Results)
	}
}

func TestDoctor_MissingPluginNoLaunch(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 9102}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	rep, err := diagnostics.Doctor(context.Background(), diagnostics.DoctorInput{
		InstanceIDs: []string{"missing-1"},
		Targets:     map[string]diagnostics.DoctorTarget{},
		Host:        h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Launches.Load() != 0 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
	if len(rep.Results) != 1 || rep.Results[0].Reason != string(catalog.ReasonEnabledMissing) {
		t.Fatalf("%+v", rep.Results)
	}
	if strings.Contains(rep.Results[0].Guidance, "api_key") || strings.Contains(rep.Results[0].Guidance, `\`) {
		t.Fatalf("unsafe guidance: %q", rep.Results[0].Guidance)
	}
}

func TestDoctor_SecureChannel_NoCredentialsAfterPeerFailure(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 9103}
	ch := &processhost.TestChannel{PeerPID: 9999} // unauthorized peer
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	var configures atomic.Int64
	sec := backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("super-secret-value")}}

	rep, err := diagnostics.Doctor(context.Background(), diagnostics.DoctorInput{
		InstanceIDs: []string{"cfg-peer"},
		Targets: map[string]diagnostics.DoctorTarget{
			"cfg-peer": {
				InstanceID: "cfg-peer", Kind: "synthetic",
				Artifact: &trust.VerifiedArtifact{DigestHex: "doctor-bb"},
				Secrets:  sec,
			},
		},
		Host: h,
		DialAndConfigure: func(_ context.Context, _ net.Conn, _ processhost.PeerIdentity, _ uint64, secrets backendplugin.SecretBundle, _ []byte) error {
			configures.Add(1)
			if len(secrets.Values) > 0 {
				t.Fatal("credentials delivered despite peer failure")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if configures.Load() != 0 {
		t.Fatalf("DialAndConfigure called %d times after peer failure", configures.Load())
	}
	if len(rep.Results) != 1 || rep.Results[0].State != catalog.StateFailed {
		t.Fatalf("%+v", rep.Results)
	}
	if !strings.Contains(rep.Results[0].Guidance, "credentials were not delivered") {
		t.Fatalf("guidance=%q", rep.Results[0].Guidance)
	}
	if strings.Contains(rep.Results[0].Guidance, "super-secret-value") {
		t.Fatal("secret leaked in guidance")
	}
}

func TestDoctor_NeverAllDiscovered(t *testing.T) {
	t.Parallel()
	_, err := diagnostics.Doctor(context.Background(), diagnostics.DoctorInput{
		InstanceIDs: nil,
		Host:        processhost.NewHost(processhost.Config{Launcher: &processhost.TestLauncher{}, Channel: &processhost.TestChannel{}}),
	})
	if err == nil || !strings.Contains(err.Error(), "instance id") {
		t.Fatalf("got %v", err)
	}
}
