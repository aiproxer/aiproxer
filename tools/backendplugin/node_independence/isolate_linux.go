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
	"syscall"

	"golang.org/x/sys/unix"
)

// Isolation model (spec cursor-sdk-standalone, task 5.1):
//
// The lane must run the host verification surface with Node genuinely
// unavailable *and* with a non-administrative effective UID, because the host
// itself refuses to start as root ("stdhttp: refusing to start as administrative
// user"). Those two requirements are met by one minimal, short-lived elevation:
//
//	non-root lane -> sudo unshare --mount (mount namespace only, no user
//	namespace) -> root helper masks the Node entry points -> setpriv drops back
//	to the caller identity -> the verification surface runs unprivileged inside
//	the namespace, and the namespace dies with the process.
//
// Why not a rootless user namespace: it needs no elevation but maps the caller
// to root inside the namespace, so the steps would run with EUID 0 and the host
// would refuse them. Why not a host-wide binary quarantine: it mutates shared
// state, needs a restore path that a killed step cannot honour, and leaves
// root-owned residue.
//
// The drop identity is taken only from SUDO_UID/SUDO_GID, which sudo derives from
// the real caller. It is never read from an environment variable this process
// controls, and a root helper that cannot see a non-zero sudo identity refuses to
// run rather than inventing one.
const (
	isolateTool    = "unshare"
	privilegeTool  = "sudo"
	dropTool       = "setpriv"
	mountNamespace = "--mount"
	forkNamespace  = "--fork"
	privateTree    = "--propagation"
	privateValue   = "private"

	sudoUIDEnv = "SUDO_UID"
	sudoGIDEnv = "SUDO_GID"

	maskFileName = "node-masked"
	maskFileMode = 0o000

	nonRootUID = 1
)

// validatedMaskTargets re-checks the untrusted mask list. The root helper will
// bind-mount over whatever it is handed, so every entry must be an existing
// regular file or symlink whose base name is a probed Node entry point.
func validatedMaskTargets(raw []string) ([]string, error) {
	var targets []string
	for _, candidate := range raw {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if !filepath.IsAbs(clean) {
			return nil, fmt.Errorf("mask target %q is not absolute", candidate)
		}
		if !nodeToolNameSet[filepath.Base(clean)] {
			return nil, fmt.Errorf("mask target %q is not a probed Node entry point", clean)
		}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf("mask target %q is not an existing file: %v", clean, err)
		}
		targets = append(targets, clean)
	}
	return targets, nil
}

// isolationSupported reports whether this host can build a mount namespace with
// passwordless privilege elevation.
func isolationSupported() bool {
	for _, tool := range []string{isolateTool, privilegeTool, dropTool} {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	if _, err := os.Stat("/proc/self/ns/mnt"); err != nil {
		return false
	}
	return sudoAvailable()
}

// sudoAvailable reports whether elevation can happen without an interactive
// prompt. A prompting sudo would hang a CI lane, so it counts as unavailable.
func sudoAvailable() bool {
	sudo, err := exec.LookPath(privilegeTool)
	if err != nil {
		return false
	}
	cmd := exec.Command(sudo, "-n", "true")
	return cmd.Run() == nil
}

// isolationUnavailableReason explains, in operator terms, why the lane cannot
// prove Node absence on this host.
func isolationUnavailableReason() string {
	return fmt.Sprintf(
		"Node isolation needs passwordless %s plus %s/%s/%s on PATH; "+
			"run the lane on a host where an unprivileged user may elevate without a prompt, "+
			"or in a Node-free container image",
		privilegeTool, isolateTool, privilegeTool, dropTool,
	)
}

// requireNonRootEUID rejects an administrative caller. Elevating from root would
// make the privilege step meaningless and would leave the verification surface
// running with EUID 0, which the host refuses at startup anyway.
func requireNonRootEUID(euid int) error {
	if euid == 0 {
		return fmt.Errorf(
			"node-independence must not run as root: the host refuses to start as an administrative user, " +
				"so the surface would prove nothing; run it as an unprivileged user with passwordless sudo",
		)
	}
	if euid < 0 {
		return fmt.Errorf("unexpected effective uid %d", euid)
	}
	return nil
}

// isolate re-executes the lane inside a mount namespace created by a root helper
// that immediately drops back to the calling identity. It returns only on failure.
func isolate(self string, args []string, targets []string) error {
	if err := requireNonRootEUID(os.Geteuid()); err != nil {
		return err
	}
	if !sudoAvailable() {
		return fmt.Errorf("%s", isolationUnavailableReason())
	}
	// The mask list and the caller's environment must survive sudo. sudo resets
	// the environment, so both travel as owner-only files referenced from argv;
	// the helper validates each file's owner and mode before trusting it.
	// Passing them through the environment instead would arrive empty, and an
	// empty mask list looks exactly like a clean host while a stripped
	// environment silently repoints the Go caches.
	staged, err := stageHandoff(targets)
	if err != nil {
		return err
	}
	sudo, argv, err := isolationArgv(self, staged, args)
	if err != nil {
		return err
	}
	// Replace this process so the lane reports one exit status and the namespace
	// lifetime is exactly this process tree.
	if err := syscall.Exec(sudo, argv, os.Environ()); err != nil {
		return fmt.Errorf("enter mount namespace: %w", err)
	}
	return nil
}

// isolationArgv builds the exact privilege chain, so the regression test drives
// the real thing rather than a reimplementation of it:
// sudo -> unshare --mount (no user namespace) -> root helper -> setpriv drop.
func isolationArgv(self string, staged stagedHandoff, args []string) (sudo string, argv []string, err error) {
	unshare, err := exec.LookPath(isolateTool)
	if err != nil {
		return "", nil, fmt.Errorf("locate %s: %w", isolateTool, err)
	}
	sudo, err = exec.LookPath(privilegeTool)
	if err != nil {
		return "", nil, fmt.Errorf("locate %s: %w", privilegeTool, err)
	}
	argv = []string{
		sudo, "-n", unshare, mountNamespace, forkNamespace, privateTree, privateValue,
		self, rootHelperFlag, staged.maskList, staged.environment,
	}
	return sudo, append(argv, args...), nil
}

// stagedHandoff carries the untrusted inputs across the privilege boundary.
type stagedHandoff struct {
	maskList    string
	environment string
}

// stageHandoff writes both handoff files as the unprivileged caller.
func stageHandoff(targets []string) (stagedHandoff, error) {
	dir, err := os.MkdirTemp("", "aip-node-handoff-")
	if err != nil {
		return stagedHandoff{}, fmt.Errorf("create handoff directory: %w", err)
	}
	maskList, err := writeOwnerFile(filepath.Join(dir, "targets"), strings.Join(targets, "\n"))
	if err != nil {
		return stagedHandoff{}, err
	}
	environment, err := writeOwnerFile(filepath.Join(dir, "environment"), strings.Join(os.Environ(), "\n"))
	if err != nil {
		return stagedHandoff{}, err
	}
	return stagedHandoff{maskList: maskList, environment: environment}, nil
}

func writeOwnerFile(path, content string) (string, error) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write handoff file %s: %w", path, err)
	}
	return path, nil
}

// readOwnerFile trusts a staged file only when it belongs to the sudo caller and
// no other principal can write it. Anything else is refused, because the helper
// runs as root and acts on whatever it is handed.
func readOwnerFile(path, what string, dropUID int) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%s file %s: %w", what, path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s file %s is not a regular file", what, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s file %s is accessible beyond its owner (mode %o)", what, path, info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("%s file %s ownership could not be determined", what, path)
	}
	if int(stat.Uid) != dropUID {
		return "", fmt.Errorf("%s file %s is owned by uid %d, not the sudo caller %d", what, path, stat.Uid, dropUID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s file %s: %w", what, path, err)
	}
	_ = os.Remove(path)
	return strings.TrimRight(string(raw), "\n"), nil
}

// runRootHelper masks the validated targets and drops privileges back to the sudo
// caller. It is the only code in the lane that runs as root, and it does nothing
// else: no file is moved, no command is started, and the mask is discarded when
// the namespace goes away.
// runRootHelper masks the validated targets and drops privileges back to the sudo
// caller. It is the only code in the lane that runs as root, and it does nothing
// else: no file is moved, no command is started, and the mask is discarded when
// the namespace goes away.
func runRootHelper(self string, args []string, maskDir string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("root helper must run as root, got euid %d", os.Geteuid())
	}
	dropUID, dropGID, err := sudoCallerIdentity()
	if err != nil {
		return err
	}
	listFile := ""
	envFile := ""
	if len(args) > 1 {
		listFile, envFile = args[0], args[1]
		args = args[2:]
	}
	rawList, err := readOwnerFile(listFile, "mask list", dropUID)
	if err != nil {
		return err
	}
	targets, err := validatedMaskTargets(strings.Split(rawList, "\n"))
	if err != nil {
		return err
	}
	rawEnv, err := readOwnerFile(envFile, "environment", dropUID)
	if err != nil {
		return err
	}
	if err := maskTargets(maskDir, targets); err != nil {
		return err
	}
	// Tell the resumed lane what was hidden, so the report names the masked paths
	// instead of merely asserting a clean result.
	resumed := append(strings.Split(rawEnv, "\n"), maskedEnv+"="+strings.Join(targets, ","))
	setpriv, err := exec.LookPath(dropTool)
	if err != nil {
		return fmt.Errorf("locate %s: %w", dropTool, err)
	}
	drop := []string{
		setpriv,
		"--reuid=" + strconv.Itoa(dropUID),
		"--regid=" + strconv.Itoa(dropGID),
		"--clear-groups",
		"--",
		self,
	}
	drop = append(drop, args...)
	if err := syscall.Exec(setpriv, drop, resumed); err != nil {
		return fmt.Errorf("drop privileges to uid %d: %w", dropUID, err)
	}
	return nil
}

// sudoCallerIdentity reads the identity to drop to from sudo's own variables.
// Values this process could have set are rejected outright, so a caller cannot
// choose the identity the root helper assumes.
func sudoCallerIdentity() (uid, gid int, err error) {
	uid, err = sudoIdentityValue(sudoUIDEnv)
	if err != nil {
		return 0, 0, err
	}
	gid, err = sudoIdentityValue(sudoGIDEnv)
	if err != nil {
		return 0, 0, err
	}
	if uid == 0 {
		return 0, 0, fmt.Errorf("%s is root: refusing to assume a privileged drop identity", sudoUIDEnv)
	}
	if uid < nonRootUID {
		return 0, 0, fmt.Errorf("%s=%d is not a usable unprivileged uid", sudoUIDEnv, uid)
	}
	return uid, gid, nil
}

func sudoIdentityValue(key string) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return 0, fmt.Errorf("%s is missing: the root helper must be entered through %s so the caller identity is authentic", key, privilegeTool)
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not numeric", key, raw)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s=%d is negative", key, value)
	}
	return value, nil
}

// isEntryPoint reports whether path is something a process could actually start.
// Matching on name alone would flag a shell completion file, a lint override or a
// vendored JavaScript file, and masking those would be noise dressed up as
// evidence.
func isEntryPoint(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// deviceOf and sameDevice bound the probe sweep to one filesystem. Crossing a
// mount point would let a mounted Windows or network tree dominate the sweep
// while telling us nothing about the host's own toolchain.
func deviceOf(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Dev)
}

func sameDevice(rootDevice uint64, path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if rootDevice == 0 {
		return true
	}
	return deviceOf(info) == rootDevice
}

// maskTargets makes every discovered entry point unexecutable inside the current
// namespace by bind-mounting an empty, non-executable file over it. The path keeps
// existing, so an absolute-path invocation fails with a permission error rather
// than a lookup error, and every other file in the directory is untouched.
func maskTargets(maskDir string, targets []string) error {
	mask := filepath.Join(maskDir, maskFileName)
	if err := os.WriteFile(mask, nil, maskFileMode); err != nil {
		return fmt.Errorf("create mask file: %w", err)
	}
	var failures []string
	for _, target := range targets {
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := unix.Mount(mask, target, "", unix.MS_BIND, ""); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target, err))
		}
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return fmt.Errorf("mask Node entry points: %s", strings.Join(failures, "; "))
	}
	return nil
}

// unmask detaches the masks so the masked paths can be removed again. The
// namespace teardown already discards them for the host.
func unmask(targets []string) {
	for _, target := range targets {
		_ = unix.Unmount(target, unix.MNT_DETACH)
	}
}
