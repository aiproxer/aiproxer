package runtimebundle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestTOCTOU_ServeGateAndBootstrapDisagreeAcrossControlledLoads proves the
// historical two-load defect is closed: BuildHost evaluates the multi-user gate
// against the same accepted snapshot used for generation/reload (req 4.2-4.3).
func TestTOCTOU_ServeGateAndBootstrapDisagreeAcrossControlledLoads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pathA := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18101", accessmode.ModeMultiUser)
	pathB := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18102", accessmode.ModeSingleUser)

	snapA := mustLoadBootstrapSnapshot(ctx, t, pathA)
	snapB := mustLoadBootstrapSnapshot(ctx, t, pathB)
	if snapA.eff.Identity.PublicFingerprint == "" || snapB.eff.Identity.PublicFingerprint == "" {
		t.Fatal("expected non-empty public fingerprints")
	}
	if snapA.eff.Identity.PublicFingerprint == snapB.eff.Identity.PublicFingerprint {
		t.Fatal("controlled snapshots A/B must be distinguishable by public fingerprint")
	}
	if snapA.active.HandleIdentity == snapB.active.HandleIdentity {
		t.Fatal("controlled snapshots A/B must be distinguishable by source handle identity")
	}
	if snapA.active.HandleIdentity.Scheme == "linux-ext4-dev-ino-lease-v1" {
		if !snapA.owner.Matches(snapA.active) || !snapB.owner.Matches(snapB.active) {
			t.Fatal("supported ext4 fixtures must retain each accepted snapshot's own source owner")
		}
	}

	var loads atomic.Int32
	load := func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		n := loads.Add(1)
		switch n {
		case 1:
			return snapA.eff, snapA.active, snapA.owner, snapA.fixed, nil
		default:
			return snapB.eff, snapB.active, snapB.owner, snapB.fixed, nil
		}
	}

	flagTrue := true
	out, err := buildHostOutcome(ctx, hostBuildInput{
		ConfigPath:              pathA,
		Mandatory:               aipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagTrue,
	}, load)
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { cleanupHost(t, out.Host) })

	gotLoads := int(loads.Load())
	wantFP := snapA.eff.Identity.PublicFingerprint
	genFP := ""
	if out.Host.manager != nil && out.Host.manager.Active() != nil {
		genFP = out.Host.manager.Active().Status().Meta.PublicFingerprint
	}
	processAddr := ""
	if out.Host.config != nil {
		processAddr = out.Host.config.Server.Address
	}
	reloadFP := ""
	if out.Host.effective != nil {
		reloadFP = out.Host.effective.Identity.PublicFingerprint
	}

	var problems []string
	if gotLoads != 1 || out.Journal.Loads != 1 {
		problems = append(problems, "effective loads="+strconv.Itoa(gotLoads)+" journal="+strconv.Itoa(out.Journal.Loads)+" want 1")
	}
	if wantFP != genFP {
		problems = append(problems, "gate fingerprint="+wantFP+" generation fingerprint="+genFP)
	}
	if wantFP != reloadFP {
		problems = append(problems, "gate fingerprint="+wantFP+" reload Effective fingerprint="+reloadFP)
	}
	if processAddr != snapA.eff.Config.Server.Address {
		problems = append(problems, "process address="+processAddr+" want snapshot A address="+snapA.eff.Config.Server.Address)
	}
	if !hostIsComplete(out) {
		problems = append(problems, "incomplete Host")
	}
	if snapA.active.HandleIdentity.Scheme == "linux-ext4-dev-ino-lease-v1" {
		if !snapA.owner.IsEmpty() {
			problems = append(problems, "accepted snapshot A owner was not transferred into Host reload state")
		}
		if snapB.owner.IsEmpty() {
			problems = append(problems, "unused snapshot B owner was incorrectly transferred")
		}
		if borrow, err := snapA.active.Borrow(); err != nil {
			problems = append(problems, "accepted snapshot A baseline is no longer borrowable: "+err.Error())
		} else {
			borrow.Release()
		}
		if borrow, err := snapB.active.Borrow(); err != nil {
			problems = append(problems, "unused snapshot B owner was disposed: "+err.Error())
		} else {
			borrow.Release()
		}
	}
	if len(problems) != 0 {
		t.Fatalf("one-snapshot HostBuild invariant failed (%d):\n- %s", len(problems), strings.Join(problems, "\n- "))
	}
}

// TestOneSnapshot_HostTransactionSharesAcceptedSnapshot is the desired HostBuilder
// contract (req 4.1-4.4).
func TestOneSnapshot_HostTransactionSharesAcceptedSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pathA := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18201", accessmode.ModeSingleUser)
	pathB := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18202", accessmode.ModeSingleUser)
	snapA := mustLoadBootstrapSnapshot(ctx, t, pathA)
	snapB := mustLoadBootstrapSnapshot(ctx, t, pathB)

	var loads atomic.Int32
	load := func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		if loads.Add(1) == 1 {
			return snapA.eff, snapA.active, snapA.owner, snapA.fixed, nil
		}
		return snapB.eff, snapB.active, snapB.owner, snapB.fixed, nil
	}

	flagFalse := false
	out, err := buildHostOutcome(ctx, hostBuildInput{
		ConfigPath:      pathA,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
		MultiUser:       &flagFalse,
	}, load)
	if err != nil {
		t.Fatalf("HostBuilder transaction must succeed with one accepted snapshot: %v", err)
	}
	t.Cleanup(func() { cleanupHost(t, out.Host) })

	wantFP := snapA.eff.Identity.PublicFingerprint
	wantAddr := snapA.eff.Config.Server.Address
	if out.Journal.Loads != 1 || loads.Load() != 1 {
		t.Fatalf("HostBuilder must read effective config exactly once; journal.loads=%d loader.loads=%d (req 4.2)", out.Journal.Loads, loads.Load())
	}
	if !hostIsComplete(out) {
		t.Fatalf("HostBuilder must return one complete Host (req 4.1, 4.5)")
	}
	if out.Host.effective == nil || out.Host.effective.Identity.PublicFingerprint != wantFP {
		t.Fatalf("reload Effective fingerprint mismatch want %q", wantFP)
	}
	if out.Host.effective.Config == nil || out.Host.effective.Config.Server.Address != wantAddr {
		t.Fatalf("process runtime config must derive from accepted snapshot")
	}
	if out.Host.manager == nil || out.Host.manager.Active() == nil {
		t.Fatal("complete Host must publish generation 1")
	}
	if out.Host.manager.Active().Status().Meta.PublicFingerprint != wantFP {
		t.Fatalf("generation 1 fingerprint must derive from accepted snapshot")
	}
	if out.Host.source == nil {
		t.Fatal("complete Host must bind ActiveSource identity")
	}
}

type bootstrapSnapshot struct {
	eff    *config.EffectiveConfig
	active *configsource.ActiveSourceVersion
	owner  *configsource.SourceOwnerSlot
	fixed  config.StreamRecoveryOverrides
}

func mustLoadBootstrapSnapshot(ctx context.Context, t *testing.T, path string) bootstrapSnapshot {
	t.Helper()
	eff, active, owner, fixed, err := LoadBootstrapEffectiveWithSource(ctx, path, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatalf("preload %s: %v", path, err)
	}
	if eff == nil || active == nil {
		t.Fatal("preload returned nil effective/active")
	}
	t.Cleanup(func() {
		if closeErr := owner.Close(context.Background()); closeErr != nil {
			t.Errorf("close bootstrap source owner: %v", closeErr)
		}
	})
	return bootstrapSnapshot{eff: eff, active: active, owner: owner, fixed: fixed}
}

func writeOneSnapshotMarkerConfig(t *testing.T, address string, mode accessmode.Mode) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(base)
	text = strings.Replace(text, `address: "127.0.0.1:18080"`, `address: "`+address+`"`, 1)
	if !strings.Contains(text, address) {
		t.Fatal("failed to rewrite server address marker")
	}
	if mode == accessmode.ModeMultiUser {
		insert := "" +
			"access:\n" +
			"  mode: multi_user\n" +
			"auth:\n" +
			"  handler: local_api_key\n" +
			"  required_level: api_key\n" +
			"  local_api_keys:\n" +
			"    - key_id: k1\n" +
			"      principal_id: p1\n" +
			"      key: \"test-key-at-least-16-chars\"\n"
		if strings.Contains(text, "\naccess:\n") {
			t.Fatal("dogfood fixture unexpectedly declares access block")
		}
		text = strings.Replace(text, "\nrouting:\n", "\n"+insert+"routing:\n", 1)
		if !strings.Contains(text, "mode: multi_user") {
			t.Fatal("failed to inject multi_user access mode")
		}
		if !strings.Contains(text, "auth_mode:") {
			text = strings.Replace(text, "server:\n", "server:\n  auth_mode: external\n", 1)
		}
	}
	path := filepath.Join(t.TempDir(), "one-snapshot.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return MaterializeExampleConfigForTest(t, path)
}

func cleanupHost(t *testing.T, host *Host) {
	t.Helper()
	if host == nil {
		return
	}
	_ = host.Close(context.Background())
}
