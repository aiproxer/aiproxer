//go:build darwin

package trust

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// bindVerified performs protected digest-addressed staging with mode 0700/0600.
// Destination handle stays open for write/sync/seek/hash and LaunchSource.
// Launch remains Phase 3. This is a source/compile staging implementation.
func bindVerified(f *os.File, m sdkmanifest.Manifest, digest string, opt VerifyOptions) VerifyResult {
	if opt.StagingDir == "" {
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingUnsupported, Err: fmt.Errorf("staging dir required")}
	}
	if err := os.MkdirAll(opt.StagingDir, 0o700); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	staged := filepath.Join(opt.StagingDir, digest)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	dst, err := os.OpenFile(staged, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	if _, err := io.Copy(dst, f); err != nil {
		_ = dst.Close()
		_ = os.Remove(staged)
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	_ = f.Close()
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		_ = os.Remove(staged)
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		_ = dst.Close()
		_ = os.Remove(staged)
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	sum, err := hashFile(dst)
	if err != nil || sum != digest {
		_ = dst.Close()
		_ = os.Remove(staged)
		return VerifyResult{Reason: ReasonSubstitution, Err: fmt.Errorf("staged digest mismatch")}
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		_ = dst.Close()
		_ = os.Remove(staged)
		return VerifyResult{Reason: ReasonStagingFailed, Err: err}
	}
	return VerifyResult{
		Artifact: &VerifiedArtifact{
			Manifest: m, DigestHex: digest, Strategy: BindingProtectedStaging,
			StagedPath: staged, file: dst,
		},
		Reason: ReasonOK,
	}
}
