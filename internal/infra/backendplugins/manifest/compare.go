package manifest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// CompareDescriptorToManifest validates that a runtime Describe descriptor
// agrees with its packaged manifest on export identity and credential
// posture. It rejects missing and extra exports and per-kind disagreements on
// credential_mode, access_scope and process_sharing; a static factory on
// either side requires a manifest credential contract on the manifest side.
// Execution class is manifest-authoritative at host install (the wire
// descriptor carries none) and is bound into ValidatedExport.ExecProfile by
// CollectInstallableExports, so it is not compared here.
//
// Errors name only export kinds and posture values: descriptors and manifests
// hold no secret values, and this function never logs any.
func CompareDescriptorToManifest(m sdkmanifest.Manifest, d backendplugin.PluginDescriptor) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("%w: invalid descriptor: %v", ErrDescriptorManifestMismatch, err)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("%w: invalid manifest: %v", ErrDescriptorManifestMismatch, err)
	}
	manifestByKind := make(map[string]sdkmanifest.Export, len(m.Exports))
	for _, e := range m.Exports {
		manifestByKind[e.Kind] = e
	}
	seen := make(map[string]struct{}, len(d.Factories))
	for _, f := range d.Factories {
		kind := strings.TrimSpace(f.Kind)
		if _, dup := seen[kind]; dup {
			return fmt.Errorf("%w: duplicate descriptor factory %q", ErrDescriptorManifestMismatch, kind)
		}
		seen[kind] = struct{}{}
		exp, ok := manifestByKind[kind]
		if !ok {
			return fmt.Errorf("%w: descriptor exports %q with no manifest export", ErrDescriptorManifestMismatch, kind)
		}
		if f.CredentialMode != exp.CredentialMode {
			return fmt.Errorf("%w: kind %q credential_mode descriptor %q manifest %q",
				ErrDescriptorManifestMismatch, kind, f.CredentialMode, exp.CredentialMode)
		}
		if f.AccessScope != exp.AccessScope {
			return fmt.Errorf("%w: kind %q access_scope descriptor %q manifest %q",
				ErrDescriptorManifestMismatch, kind, f.AccessScope, exp.AccessScope)
		}
		if f.ProcessSharing != exp.ProcessSharing {
			return fmt.Errorf("%w: kind %q process_sharing descriptor %q manifest %q",
				ErrDescriptorManifestMismatch, kind, f.ProcessSharing, exp.ProcessSharing)
		}
		if f.CredentialMode == backendplugin.CredentialModeStatic && exp.CredentialContract == nil {
			return fmt.Errorf("%w: kind %q static descriptor without manifest credential contract",
				ErrDescriptorManifestMismatch, kind)
		}
	}
	if len(seen) != len(manifestByKind) {
		manifestKinds := make([]string, 0, len(manifestByKind))
		for kind := range manifestByKind {
			if _, ok := seen[kind]; !ok {
				manifestKinds = append(manifestKinds, kind)
			}
		}
		sort.Strings(manifestKinds)
		return fmt.Errorf("%w: manifest exports missing from descriptor: %s",
			ErrDescriptorManifestMismatch, strings.Join(manifestKinds, ", "))
	}
	return nil
}
