package trust

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// VerifyOptions controls digest-bound preparation.
type VerifyOptions struct {
	StagingDir string
	HostOS     string
	HostArch   string
}

// VerifyResult is either a VerifiedArtifact or a bounded reason.
type VerifyResult struct {
	Artifact *VerifiedArtifact
	Reason   Reason
	Err      error
}

// Verify opens the executable under trusted root with no-follow semantics,
// verifies type/platform/digest from the held handle, and binds for later launch.
// It never falls back to a raw path when canonicalization fails and never
// reopens by path after the initial no-follow open for digest verification.
func Verify(root string, m sdkmanifest.Manifest, opt VerifyOptions) VerifyResult {
	hostOS := opt.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := opt.HostArch
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	if !platformAllowed(m, hostOS, hostArch) {
		return VerifyResult{Reason: ReasonUnsupportedPlatform, Err: fmt.Errorf("platform")}
	}
	target, reason, err := resolveUnderRoot(root, m.Executable)
	if err != nil {
		return VerifyResult{Reason: reason, Err: err}
	}
	f, err := openNoFollow(target)
	if err != nil {
		return VerifyResult{Reason: classifyOpenErr(err), Err: err}
	}
	if err := confirmOpenedUnderRoot(root, f); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: classifyOpenErr(err), Err: err}
	}
	if _, err := underRootAbs(root, f.Name()); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonPathEscape, Err: err}
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return VerifyResult{Reason: ReasonNotRegular, Err: fmt.Errorf("not regular")}
	}
	if err := rejectNonNativeName(filepath.Base(m.Executable), hostOS); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonNotExecutableType, Err: err}
	}
	if err := validateNativeMagic(f, hostOS); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonNotExecutableType, Err: err}
	}
	sum, err := hashFile(f)
	if err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonOpenFailed, Err: err}
	}
	if sum != strings.ToLower(m.SHA256) {
		_ = f.Close()
		return VerifyResult{Reason: ReasonDigestMismatch, Err: fmt.Errorf("digest")}
	}
	return bindVerified(f, m, sum, opt)
}

func classifyOpenErr(err error) Reason {
	switch {
	case errors.Is(err, ReasonSymlinkEscape):
		return ReasonSymlinkEscape
	case errors.Is(err, ReasonNotRegular):
		return ReasonNotRegular
	case errors.Is(err, ReasonStagingUnsupported):
		return ReasonStagingUnsupported
	case errors.Is(err, ReasonPathEscape):
		return ReasonPathEscape
	default:
		return ReasonOpenFailed
	}
}

func platformAllowed(m sdkmanifest.Manifest, goos, goarch string) bool {
	for _, p := range m.Platforms {
		if p.OS == goos && p.Arch == goarch {
			return true
		}
	}
	return false
}

func underRootAbs(root, candidate string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absCand, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absCand)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ReasonPathEscape
	}
	return absCand, nil
}

func rejectNonNativeName(base, goos string) error {
	lower := strings.ToLower(base)
	for _, bad := range []string{".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd", ".py", ".js", ".rb", ".pl", ".vbs", ".wsf"} {
		if strings.HasSuffix(lower, bad) {
			return fmt.Errorf("script")
		}
	}
	if slices.Contains([]string{"cmd.exe", "powershell.exe", "pwsh.exe", "bash", "sh", "python", "python3", "node"}, lower) {
		return fmt.Errorf("interpreter")
	}
	if goos == "windows" && !strings.HasSuffix(lower, ".exe") {
		return fmt.Errorf("windows exe")
	}
	return nil
}
