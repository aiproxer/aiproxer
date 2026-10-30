package security_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// Consolidated adversarial proofs for threat-model controls (task 9.3).

func TestAdversarial_UnauthorizedPeerNoConfigureOrSecrets(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 4242}
	ch := &processhost.TestChannel{PeerPID: 9999}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "adv-unauth",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "adv-unauth"},
		Model:      processhost.ProcessModelPerInstance,
		Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("must-not-deliver")}},
		DialAndConfigure: func(_ context.Context, _ net.Conn, _ processhost.PeerIdentity, _ uint64, secrets backendplugin.SecretBundle, _ []byte) error {
			configured.Store(true)
			if len(secrets.Values) > 0 {
				t.Error("secrets must not reach DialAndConfigure")
			}
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonPeerRejected) {
		t.Fatalf("want peer_rejected, got %v", err)
	}
	if configured.Load() {
		t.Fatal("configure must not run")
	}
}

func TestAdversarial_StaleGenerationNoConfigure(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 5151}
	ch := &processhost.TestChannel{StaleGen: true}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "adv-stale",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "adv-stale"},
		Model:      processhost.ProcessModelPerInstance,
		Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"k": []byte("v")}},
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonStaleGeneration) {
		t.Fatalf("want stale_generation, got %v", err)
	}
	if configured.Load() {
		t.Fatal("configure must not run")
	}
}

func TestAdversarial_CookiePlaintextRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  processhost.CookiePlaintextChannel{},
	})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "adv-cookie",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "adv-cookie"},
		Model:      processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonCookieAuthRejected) && !errors.Is(err, processhost.ReasonUnsupportedChannel) {
		t.Fatalf("want cookie/unsupported, got %v", err)
	}
	if configured.Load() || launcher.Launches.Load() != 0 {
		t.Fatalf("configured=%v launches=%d", configured.Load(), launcher.Launches.Load())
	}
}

func TestAdversarial_ForbiddenEnvBootstrapRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
		AllowEnv: []string{"PLUGIN_CLIENT_CERT", "AIP_BOOTSTRAP_KEY"},
	})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "adv-env",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "adv-env"},
		Model:      processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonEnvBootstrapRejected) {
		t.Fatalf("want env_bootstrap_rejected, got %v", err)
	}
}

func TestAdversarial_MinimalEnvNoParentInheritance(t *testing.T) {
	// Cannot t.Parallel with t.Setenv.
	t.Setenv("AIP_PARENT_SHOULD_NOT_INHERIT", "leak")
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "adv-minenv",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "adv-minenv"},
		Model:      processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range launcher.LastSpec.Env {
		if strings.Contains(e, "AIP_PARENT_SHOULD_NOT_INHERIT") || strings.Contains(e, "leak") {
			t.Fatalf("parent env leaked into child: %q", e)
		}
	}
	if launcher.LastSpec.Env == nil {
		t.Fatal("env must be non-nil empty allowlist result")
	}
}

func TestAdversarial_DigestMismatchAndPathEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	exeName := "bin/plugin"
	if runtime.GOOS == "windows" {
		exeName = "bin/plugin.exe"
	}
	exePath := filepath.Join(root, filepath.FromSlash(exeName))
	if err := os.MkdirAll(filepath.Dir(exePath), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := adversarialNativeMagic(runtime.GOOS)
	if err := os.WriteFile(exePath, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	m := sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "com.aiproxer.adv", Version: "0.0.1", BuildID: "b1",
		Executable: exeName, SHA256: digest, ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 0,
		Platforms: []sdkmanifest.Platform{{OS: runtime.GOOS, Arch: runtime.GOARCH}},
		Exports: []sdkmanifest.Export{{
			Kind: "adv", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
	bad := m
	bad.SHA256 = strings.Repeat("ab", 32)
	if res := trust.Verify(root, bad, trust.VerifyOptions{StagingDir: t.TempDir()}); res.Reason != trust.ReasonDigestMismatch {
		t.Fatalf("digest: %v", res.Reason)
	}
	esc := m
	esc.Executable = "../escape"
	if res := trust.Verify(root, esc, trust.VerifyOptions{StagingDir: t.TempDir()}); res.Reason != trust.ReasonPathEscape && res.Reason != trust.ReasonOpenFailed {
		t.Fatalf("escape: %v", res.Reason)
	}
}

func TestAdversarial_DevelopmentModeForbiddenMultiUser(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, DevelopmentMode: true, Paths: []string{"/tmp/plugins"},
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "development_mode") {
		t.Fatalf("want development_mode rejection, got %v", err)
	}
}

func adversarialNativeMagic(goos string) []byte {
	switch goos {
	case "windows":
		return []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	case "linux":
		return []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}
	case "darwin":
		return []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}
	default:
		return []byte{0}
	}
}

func TestAdversarial_DarwinChannelSourceFailsClosed(t *testing.T) {
	t.Parallel()
	// Source-level guard so Windows/Linux agents still prove Darwin fail-closed policy.
	root := filepath.Join("..", "processhost", "channel_darwin.go")
	b, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, "ReasonUnsupportedChannel") {
		t.Fatal("darwin channel must fail closed with ReasonUnsupportedChannel")
	}
	if strings.Contains(body, "Listen(") && strings.Contains(body, "tcp") && strings.Contains(strings.ToLower(body), "plaintext") {
		t.Fatal("darwin must not open plaintext TCP fallback in source")
	}
}

func TestAdversarial_UnknownEventKindContract(t *testing.T) {
	t.Parallel()
	if err := backendplugin.ValidateEventKind(backendplugin.EventKind("evil_kind")); !errors.Is(err, backendplugin.ErrUnknownEventKind) {
		t.Fatalf("%v", err)
	}
	err := (backendplugin.ServerFrame{
		Kind:  backendplugin.ServerFrameEvent,
		Event: &backendplugin.CanonicalEvent{Kind: "evil_kind"},
	}).ValidateShape()
	if !errors.Is(err, backendplugin.ErrUnknownEventKind) {
		t.Fatalf("%v", err)
	}
}
