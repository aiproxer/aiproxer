package processhost

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
)

// Claim: only fixed, instance-private execution values reach a child; controls are exclusive.
// Source: design stock provisioning. Mutant: append arbitrary LaunchEnv unchecked.
func TestActivationEnv_DefaultEmptyAndControlsRejected(t *testing.T) {
	got, err := appendActivationEnv([]string{}, nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("default env: %v %v", got, err)
	}
	got, err = appendActivationEnv([]string{"AIP_PLUGIN_CHANNEL_FD=3"}, map[string]string{"PATH": "/bin", "HOME": "/home"})
	if err != nil || !reflect.DeepEqual(got, []string{"AIP_PLUGIN_CHANNEL_FD=3", "HOME=/home", "PATH=/bin"}) {
		t.Fatalf("env: %v %v", got, err)
	}
	for _, name := range []string{"LD_PRELOAD", "NODE_OPTIONS", "ANTHROPIC_API_KEY", "AIP_PLUGIN_CHANNEL_FD", "home"} {
		if _, err := appendActivationEnv(nil, map[string]string{name: "synthetic"}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if _, err := appendActivationEnv([]string{"HOME=/old"}, map[string]string{"HOME": "/new"}); err == nil {
		t.Fatal("replaced existing context")
	}
	if _, err := appendActivationEnv(nil, map[string]string{"HOME": "bad\x00path"}); err == nil {
		t.Fatal("accepted NUL")
	}
}

func TestActivationEnv_SharedRejectsBeforeLaunch(t *testing.T) {
	launcher := &TestLauncher{}
	h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
	defer func() { _ = h.Close() }()
	_, err := h.Activate(context.Background(), ActivateRequest{InstanceID: "shared", Artifact: &trust.VerifiedArtifact{}, Model: ProcessModelSharedArtifact, Sharing: SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true}, LaunchEnv: map[string]string{"HOME": "/home"}})
	if err != ReasonEnvBootstrapRejected {
		t.Fatalf("want shared env refusal, got %v", err)
	}
}
