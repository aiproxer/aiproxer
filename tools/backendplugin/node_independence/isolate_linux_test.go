//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// namespaceChildEnv makes the re-executed test copy behave as the isolated
// child instead of running the whole suite again.
const namespaceChildEnv = "AIP_NODE_MASK_TEST_CHILD"

// TestMain lets the test binary stand in for the lane binary when the privilege
// path is exercised end to end. Without this the probe flag would be parsed as an
// unknown testing flag and the run would only print usage.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case rootHelperFlag:
			code, err := rootHelperExit()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(code)
		case rootHelperProbeFlag:
			code, err := rootHelperProbeExit()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}

// TestRootHelperMasksAndDropsPrivileges is the load-bearing test for the
// isolation mechanism. It runs through the real privilege path - sudo, a
// mount-only namespace, the root helper - and then asserts that
//
//   - the Node entry points are unreachable at their absolute paths,
//   - bare-name resolution is unreachable too,
//   - the process tree is back to the unprivileged caller identity,
//   - an unrelated documented prerequisite still works.
//
// Without the last two assertions a lane could "prove" absence by destroying the
// environment it is supposed to verify, or by leaving the whole run privileged.
func TestRootHelperMasksAndDropsPrivileges(t *testing.T) {
	if os.Getenv(namespaceChildEnv) == "1" {
		maskChild(t)
		return
	}
	if !isolationSupported() {
		t.Skip("passwordless sudo plus unshare/setpriv are required for this test")
	}
	if err := requireNonRootEUID(os.Geteuid()); err != nil {
		t.Skipf("run as an unprivileged user with passwordless sudo: %v", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Stage the fixture, then enter the real helper flow with it as the target
	// list. The helper is what masks; the test only supplies candidates.
	fixture := stageMaskFixture(t)
	staged, err := stageHandoff(fixture.targets)
	if err != nil {
		t.Fatal(err)
	}
	_, argv, err := isolationArgv(self, staged, []string{rootHelperProbeFlag, fixture.dir})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), rootHelperProbeDirEnv+"="+fixture.dir)
	out, err := cmd.CombinedOutput()
	t.Logf("helper probe output:\n%s", out)
	if err != nil {
		t.Fatalf("helper probe failed: %v", err)
	}
	for _, want := range []string{"masked=2", "euid=" + strconv.Itoa(os.Geteuid()), "control-ok"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("helper probe output missing %q; got:\n%s", want, out)
		}
	}
	// The fixture must be intact afterwards: no host file is moved or rewritten.
	if _, err := os.Stat(fixture.control); err != nil {
		t.Errorf("helper damaged an unrelated file: %v", err)
	}
}

// TestRootHelperRefusesAnUntrustedMaskList is the privilege-boundary regression:
// the mask list arrives from an unprivileged process, so the helper must reject a
// target that is not a probed Node entry point rather than bind-mounting over
// whatever it is handed.
func TestRootHelperRefusesAnUntrustedMaskList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	victim := filepath.Join(root, "important.conf")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validatedMaskTargets([]string{victim}); err == nil {
		t.Error("helper accepted a mask target that is not a Node entry point")
	}
	if _, err := validatedMaskTargets([]string{"relative/node"}); err == nil {
		t.Error("helper accepted a relative mask target")
	}
	if _, err := validatedMaskTargets([]string{filepath.Join(root, "absent", "node")}); err == nil {
		t.Error("helper accepted a mask target that does not exist")
	}
	writeExecutable(t, filepath.Join(root, "node"))
	targets, err := validatedMaskTargets([]string{filepath.Join(root, "node")})
	if err != nil {
		t.Fatalf("helper rejected a legitimate entry point: %v", err)
	}
	if len(targets) != 1 {
		t.Errorf("validatedMaskTargets = %v", targets)
	}
}

// TestSudoCallerIdentityRefusesInventedIdentities covers the identity the helper
// drops to. A missing, non-numeric, negative or root SUDO_UID must all be
// refused: the helper may only ever drop back to an authenticating caller.
func TestSudoCallerIdentityRefusesInventedIdentities(t *testing.T) {
	t.Setenv(sudoUIDEnv, "")
	t.Setenv(sudoGIDEnv, "1000")
	if _, _, err := sudoCallerIdentity(); err == nil {
		t.Error("missing SUDO_UID must be refused")
	}
	t.Setenv(sudoUIDEnv, "not-a-number")
	if _, _, err := sudoCallerIdentity(); err == nil {
		t.Error("non-numeric SUDO_UID must be refused")
	}
	t.Setenv(sudoUIDEnv, "-1")
	if _, _, err := sudoCallerIdentity(); err == nil {
		t.Error("negative SUDO_UID must be refused")
	}
	t.Setenv(sudoUIDEnv, "0")
	if _, _, err := sudoCallerIdentity(); err == nil {
		t.Error("root SUDO_UID must be refused: the drop identity has to be unprivileged")
	}
	t.Setenv(sudoUIDEnv, "1000")
	uid, gid, err := sudoCallerIdentity()
	if err != nil || uid != 1000 || gid != 1000 {
		t.Errorf("authentic sudo identity not accepted: uid=%d gid=%d err=%v", uid, gid, err)
	}
}

// TestRequireNonRootEUIDRejectsAdministrativeCaller is the reason the lane needs
// a real elevation rather than a user namespace: the host refuses to start as an
// administrative user, so an EUID-0 verification surface proves nothing.
func TestRequireNonRootEUIDRejectsAdministrativeCaller(t *testing.T) {
	t.Parallel()
	if err := requireNonRootEUID(0); err == nil {
		t.Error("EUID 0 must be rejected")
	} else if !strings.Contains(err.Error(), "administrative user") {
		t.Errorf("rejection must name the host policy it protects, got %q", err)
	}
	if err := requireNonRootEUID(-1); err == nil {
		t.Error("an impossible negative EUID must be rejected")
	}
	if err := requireNonRootEUID(1000); err != nil {
		t.Errorf("an ordinary caller must be accepted, got %v", err)
	}
	// On a real host the live value must agree with the policy above.
	if os.Geteuid() == 0 {
		if err := requireNonRootEUID(os.Geteuid()); err == nil {
			t.Error("the live EUID check disagrees with the policy")
		}
	}
}

// TestMaskTargetsReportsUnmaskableTargets fails closed instead of pretending the
// toolchain is gone when the kernel refuses the bind mount.
func TestMaskTargetsReportsUnmaskableTargets(t *testing.T) {
	root := t.TempDir()
	// An absent target is not an error: there is nothing left to mask.
	if err := maskTargets(root, []string{filepath.Join(root, "not-here", "node")}); err != nil {
		t.Errorf("masking an already absent target must be a no-op, got %v", err)
	}
	// A regular file cannot be bind-mounted over a directory. The helper must
	// surface that instead of reporting the toolchain as masked.
	if err := maskTargets(root, []string{root}); err == nil {
		t.Error("expected the kernel bind failure to be reported")
	} else if !strings.Contains(err.Error(), root) {
		t.Errorf("failure must name the unmaskable target, got %q", err)
	}
}

func TestIsolationContractIsLinuxOnly(t *testing.T) {
	t.Parallel()
	if reason := isolationUnavailableReason(); reason == "" {
		t.Error("isolationUnavailableReason must explain the failure in operator terms")
	}
	if !isolationSupported() {
		t.Skip("this host has no passwordless namespace support")
	}
	// isolationSupported must actually mean passwordless elevation: a prompting
	// sudo would hang the lane rather than run it.
	if !sudoAvailable() {
		t.Error("isolationSupported returned true without passwordless sudo")
	}
	if slices.Contains(nodeToolNames, "") {
		t.Error("nodeToolNames must not contain an empty entry")
	}
}

type maskFixture struct {
	dir     string
	targets []string
	control string
}

// stageMaskFixture plants two Node entry points and one innocent neighbour.
func stageMaskFixture(t *testing.T) maskFixture {
	t.Helper()
	dir := t.TempDir()
	byName := filepath.Join(dir, "node")
	absoluteOnly := filepath.Join(dir, "toolcache", "node", "20.0.0", "bin", "node")
	control := filepath.Join(dir, "keep-me")
	for _, path := range []string{byName, absoluteOnly, control} {
		writeExecutable(t, path)
	}
	return maskFixture{dir: dir, targets: []string{byName, absoluteOnly}, control: control}
}

// maskChild is the body that runs as the unprivileged caller after the helper has
// masked and dropped privileges. It lives in the lane tool itself so the
// assertions run against the real helper output rather than a reimplementation.
func maskChild(t *testing.T) {
	t.Helper()
	dir := os.Getenv(rootHelperProbeDirEnv)
	if dir == "" {
		t.Fatal("probe directory not handed to the helper probe")
	}
	fixture := maskFixture{
		dir:     dir,
		targets: []string{filepath.Join(dir, "node"), filepath.Join(dir, "toolcache", "node", "20.0.0", "bin", "node")},
		control: filepath.Join(dir, "keep-me"),
	}
	masked := maskedPathsFromEnv()
	if len(masked) != 2 {
		t.Errorf("helper masked %v, want two targets", masked)
	}
	fmt.Println("masked=" + strconv.Itoa(len(masked)))

	env := []string{"PATH=" + dir}
	for _, target := range fixture.targets {
		control := runControl("negative", dir, env, target, "--version")
		if !control.Pass || !control.LaunchFailed {
			t.Errorf("masked absolute path %s was still startable: pass=%t launch_failed=%t note=%q",
				target, control.Pass, control.LaunchFailed, control.Note)
		}
	}
	bare := runControl("negative", dir, env, "node", "--version")
	if !bare.Pass || !bare.LaunchFailed {
		t.Errorf("bare-name node was still startable: pass=%t launch_failed=%t", bare.Pass, bare.LaunchFailed)
	}
	if control := runControl("positive", dir, env, fixture.control, "--version"); !control.Pass {
		t.Errorf("masking damaged an unrelated executable: %s", control.Note)
	}
	fmt.Println("euid=" + strconv.Itoa(os.Geteuid()))
	fmt.Println("control-ok")
	unmask(fixture.targets)
}
