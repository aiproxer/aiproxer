//go:build linux

package trust

import (
	"fmt"
	"os"

	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// bindVerified keeps the opened descriptor as the verified identity (Linux).
// Exact fexecve/launch is Phase 3; here we only prepare the LaunchSource.
func bindVerified(f *os.File, m sdkmanifest.Manifest, digest string, _ VerifyOptions) VerifyResult {
	if f == nil {
		return VerifyResult{Reason: ReasonStagingUnsupported, Err: fmt.Errorf("nil file")}
	}
	return VerifyResult{
		Artifact: &VerifiedArtifact{
			Manifest:  m,
			DigestHex: digest,
			Strategy:  BindingDescriptor,
			file:      f,
		},
		Reason: ReasonOK,
	}
}
