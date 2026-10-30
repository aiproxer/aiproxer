package openresponses

import (
	"errors"
	"fmt"
	"strings"
)

// Pinned official profile digests for testkit validation (independent of production codecs).
const (
	OfficialSourceCommit     = "92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c"
	OfficialSchemaDigest     = "sha256:997c4cf16c349751502813f46ea79b2c88880b23171b69f7f2c3d4bf5b330529"
	OfficialComplianceDigest = "sha256:63b5e6595ac831ee74b8e887af76c28d69aee8e2ec7d9e99dc688eec4bccb7fb"
)

// FixtureDescriptor links official fixture metadata to Task 1.1 ProfileDescriptor without duplicating raw bytes.
type FixtureDescriptor struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Category         string `json:"category"`
	SchemaDigest     string `json:"schema_digest"`
	ComplianceDigest string `json:"compliance_digest"`
	SourceCommit     string `json:"source_commit"`
	RelPath          string `json:"rel_path"`
}

// NewOfficialFixtureDescriptor creates a FixtureDescriptor bound to the Task 1.1 Profile.
func NewOfficialFixtureDescriptor(id, name, category, relPath string) (FixtureDescriptor, error) {
	if strings.TrimSpace(id) == "" {
		return FixtureDescriptor{}, errors.New("fixture ID cannot be empty")
	}
	return FixtureDescriptor{
		ID:               id,
		Name:             name,
		Category:         category,
		SchemaDigest:     OfficialSchemaDigest,
		ComplianceDigest: OfficialComplianceDigest,
		SourceCommit:     OfficialSourceCommit,
		RelPath:          relPath,
	}, nil
}

func (f FixtureDescriptor) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return errors.New("fixture ID is required")
	}
	if f.SchemaDigest != OfficialSchemaDigest {
		return fmt.Errorf("fixture schema digest mismatch: got %q, want %q", f.SchemaDigest, OfficialSchemaDigest)
	}
	if f.ComplianceDigest != OfficialComplianceDigest {
		return fmt.Errorf("fixture compliance digest mismatch: got %q, want %q", f.ComplianceDigest, OfficialComplianceDigest)
	}
	if f.SourceCommit != OfficialSourceCommit {
		return fmt.Errorf("fixture source commit mismatch: got %q, want %q", f.SourceCommit, OfficialSourceCommit)
	}
	return nil
}

// BinaryFixtureDescriptor describes neutral binary fixture files (images, audio, etc.).
type BinaryFixtureDescriptor struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	SizeBytes int64  `json:"size_bytes"`
	Digest    string `json:"digest"`
	RelPath   string `json:"rel_path"`
}

func (b BinaryFixtureDescriptor) Validate() error {
	if strings.TrimSpace(b.ID) == "" {
		return errors.New("binary fixture ID is required")
	}
	if strings.TrimSpace(b.MediaType) == "" {
		return errors.New("binary fixture media_type is required")
	}
	if b.SizeBytes <= 0 {
		return errors.New("binary fixture size_bytes must be positive")
	}
	if !strings.HasPrefix(b.Digest, "sha256:") {
		return fmt.Errorf("binary fixture digest must start with sha256:, got %q", b.Digest)
	}
	return nil
}
