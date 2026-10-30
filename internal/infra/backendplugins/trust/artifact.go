package trust

import (
	"io"
	"os"

	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// BindingStrategy identifies how verified bytes will be preserved for later launch.
type BindingStrategy string

const (
	// BindingDescriptor holds an opened file descriptor (Linux-oriented).
	BindingDescriptor BindingStrategy = "descriptor"
	// BindingProtectedStaging copies into a private digest-addressed store.
	BindingProtectedStaging BindingStrategy = "protected_staging"
	// BindingUnsupported means the host cannot preserve verified identity.
	BindingUnsupported BindingStrategy = "unsupported"
)

// VerifiedArtifact is the digest-bound launch preparation result. It never
// starts a process; Phase 3 consumes it for exact launch.
type VerifiedArtifact struct {
	Manifest   sdkmanifest.Manifest
	DigestHex  string
	Strategy   BindingStrategy
	StagedPath string // set for protected staging; empty for descriptor binding
	file       *os.File
}

// LaunchSource is an alias documenting Phase 3 consumption of VerifiedArtifact.
type LaunchSource = VerifiedArtifact

// OpenFile returns the held descriptor when Strategy is BindingDescriptor.
func (v *VerifiedArtifact) OpenFile() *os.File { return v.file }

// Close releases held resources (descriptor or staged file handle).
func (v *VerifiedArtifact) Close() error {
	if v == nil || v.file == nil {
		return nil
	}
	err := v.file.Close()
	v.file = nil
	return err
}

// Reader returns a reader over verified bytes when a file handle is held.
func (v *VerifiedArtifact) Reader() (io.Reader, error) {
	if v == nil || v.file == nil {
		return nil, os.ErrInvalid
	}
	if _, err := v.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return v.file, nil
}
