package openresponses

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type ManifestArtifact struct {
	Role         string `json:"role"`
	UpstreamPath string `json:"upstream_path"`
	LocalPath    string `json:"local_path"`
	SHA256       string `json:"sha256"`
}

type Manifest struct {
	Profile struct {
		Family             string `json:"family"`
		Version            string `json:"version"`
		SourceCommit       string `json:"source_commit"`
		SourceRepository   string `json:"source_repository"`
		SchemaDigest       string `json:"schema_digest"`
		ComplianceDigest   string `json:"compliance_digest"`
		License            string `json:"license"`
		LicenseAttribution string `json:"license_attribution"`
	} `json:"profile"`
	Deviations []string           `json:"deviations"`
	Artifacts  []ManifestArtifact `json:"artifacts"`
}

// ValidateManifest parses a manifest and validates every listed artifact against baseDir.
func ValidateManifest(manifestData []byte, baseDir string) error {
	var m Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return fmt.Errorf("invalid manifest JSON: %w", err)
	}

	desc := GetProfileDescriptor()
	if m.Profile.Family != desc.Family {
		return fmt.Errorf("manifest family %q != descriptor %q", m.Profile.Family, desc.Family)
	}
	if m.Profile.Version != desc.Version {
		return fmt.Errorf("manifest version %q != descriptor %q", m.Profile.Version, desc.Version)
	}
	if m.Profile.SourceCommit != desc.SourceCommit {
		return fmt.Errorf("manifest source_commit %q != descriptor %q", m.Profile.SourceCommit, desc.SourceCommit)
	}
	if m.Profile.SourceRepository != desc.SourceRepository {
		return fmt.Errorf("manifest source_repository %q != descriptor %q", m.Profile.SourceRepository, desc.SourceRepository)
	}
	if m.Profile.SchemaDigest != desc.SchemaDigest {
		return fmt.Errorf("manifest schema_digest %q != descriptor %q", m.Profile.SchemaDigest, desc.SchemaDigest)
	}
	if m.Profile.ComplianceDigest != desc.ComplianceDigest {
		return fmt.Errorf("manifest compliance_digest %q != descriptor %q", m.Profile.ComplianceDigest, desc.ComplianceDigest)
	}
	if m.Profile.License != desc.License {
		return fmt.Errorf("manifest license %q != descriptor %q", m.Profile.License, desc.License)
	}
	if m.Profile.LicenseAttribution != desc.LicenseAttribution {
		return fmt.Errorf("manifest license_attribution %q != descriptor %q", m.Profile.LicenseAttribution, desc.LicenseAttribution)
	}
	if !slices.Equal(m.Deviations, desc.Deviations) {
		return fmt.Errorf("manifest deviations != descriptor deviations")
	}

	if len(m.Artifacts) == 0 {
		return fmt.Errorf("manifest contains no artifacts")
	}

	seenRoles := make(map[string]bool)
	seenPaths := make(map[string]bool)
	listedFiles := make(map[string]bool)

	var schemaDigestFound, complianceDigestFound, licenseDigestFound bool

	for _, art := range m.Artifacts {
		if art.Role == "" {
			return fmt.Errorf("artifact has empty role")
		}
		if seenRoles[art.Role] {
			return fmt.Errorf("duplicate artifact role: %s", art.Role)
		}
		seenRoles[art.Role] = true

		if art.LocalPath == "" {
			return fmt.Errorf("artifact %s has empty local_path", art.Role)
		}
		cleanLocal := filepath.Clean(art.LocalPath)
		if seenPaths[cleanLocal] {
			return fmt.Errorf("duplicate artifact local path: %s", cleanLocal)
		}
		seenPaths[cleanLocal] = true

		fullPath := filepath.Join(baseDir, art.LocalPath)
		listedFiles[filepath.Clean(fullPath)] = true

		content, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("failed to read artifact %s at %s: %w", art.Role, fullPath, err)
		}

		actualDigest := computeSHA256(content)
		if actualDigest != art.SHA256 {
			return fmt.Errorf("artifact %s digest mismatch: want %s, got %s", art.Role, art.SHA256, actualDigest)
		}

		if art.Role == "schema" {
			schemaDigestFound = true
			if actualDigest != desc.SchemaDigest {
				return fmt.Errorf("schema artifact digest %s != descriptor schema digest %s", actualDigest, desc.SchemaDigest)
			}
		}
		if art.Role == "compliance_tests" {
			complianceDigestFound = true
			if actualDigest != desc.ComplianceDigest {
				return fmt.Errorf("compliance artifact digest %s != descriptor compliance digest %s", actualDigest, desc.ComplianceDigest)
			}
		}
		if art.Role == "license" {
			licenseDigestFound = true
			if actualDigest != ExpectedLicenseDigest {
				return fmt.Errorf("license artifact digest %s != ExpectedLicenseDigest %s", actualDigest, ExpectedLicenseDigest)
			}
			if !strings.Contains(string(content), "Apache License") || !strings.Contains(string(content), "Version 2.0") {
				return fmt.Errorf("LICENSE artifact content does not contain valid Apache-2.0 license header")
			}
		}
	}

	if !schemaDigestFound {
		return fmt.Errorf("manifest missing required 'schema' role artifact")
	}
	if !complianceDigestFound {
		return fmt.Errorf("manifest missing required 'compliance_tests' role artifact")
	}
	if !licenseDigestFound {
		return fmt.Errorf("manifest missing required 'license' role artifact")
	}

	// Reject unlisted / dangling files in baseDir
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		cleanRel := filepath.Clean(rel)
		if cleanRel == "official_2026-04-24_manifest.json" || cleanRel == "README.md" {
			return nil
		}
		if !listedFiles[filepath.Clean(path)] {
			return fmt.Errorf("unlisted vendored file found in testdata: %s", rel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("testdata directory walk failed: %w", err)
	}

	return nil
}

func TestProfileDescriptor(t *testing.T) {
	t.Parallel()
	desc := GetProfileDescriptor()

	if desc.Family != ProtocolFamily {
		t.Fatalf("expected family %q, got %q", ProtocolFamily, desc.Family)
	}
	if desc.Version != ProfileVersion {
		t.Fatalf("expected version %q, got %q", ProfileVersion, desc.Version)
	}
	if desc.SourceCommit != SourceCommit {
		t.Fatalf("expected source commit %q, got %q", SourceCommit, desc.SourceCommit)
	}
	if desc.SourceRepository != SourceRepository {
		t.Fatalf("expected source repository %q, got %q", SourceRepository, desc.SourceRepository)
	}
	if desc.SchemaDigest != ExpectedSchemaDigest {
		t.Fatalf("expected schema digest %q, got %q", ExpectedSchemaDigest, desc.SchemaDigest)
	}
	if desc.ComplianceDigest != ExpectedComplianceDigest {
		t.Fatalf("expected compliance digest %q, got %q", ExpectedComplianceDigest, desc.ComplianceDigest)
	}
	if desc.License != LicenseName {
		t.Fatalf("expected license %q, got %q", LicenseName, desc.License)
	}
	if desc.LicenseAttribution != LicenseAttribution {
		t.Fatalf("expected license attribution %q, got %q", LicenseAttribution, desc.LicenseAttribution)
	}
	if len(desc.Deviations) == 0 {
		t.Fatal("expected non-empty documented deviations list")
	}
}

func TestProfileImmutability(t *testing.T) {
	t.Parallel()
	d1 := GetProfileDescriptor()
	if len(d1.Deviations) > 0 {
		d1.Deviations[0] = "mutated_deviation"
	}

	d2 := GetProfileDescriptor()
	if len(d2.Deviations) > 0 && d2.Deviations[0] == "mutated_deviation" {
		t.Fatal("GetProfileDescriptor returned mutable slice for Deviations")
	}
}

func TestManifestAndVendoredArtifacts(t *testing.T) {
	t.Parallel()
	manifestPath := filepath.Join("testdata", "official_2026-04-24_manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read manifest: %v", err)
	}

	if err := ValidateManifest(data, "testdata"); err != nil {
		t.Fatalf("ValidateManifest failed on official testdata: %v", err)
	}
}

func TestManifestValidatorNegativeCases(t *testing.T) {
	t.Parallel()

	manifestPath := filepath.Join("testdata", "official_2026-04-24_manifest.json")
	validData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read valid manifest: %v", err)
	}

	t.Run("malformed json", func(t *testing.T) {
		if err := ValidateManifest([]byte("{invalid json"), "testdata"); err == nil {
			t.Error("expected error for malformed JSON, got nil")
		}
	})

	t.Run("tampered schema digest", func(t *testing.T) {
		var m Manifest
		_ = json.Unmarshal(validData, &m)
		m.Profile.SchemaDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		tamperedBytes, _ := json.Marshal(m)
		if err := ValidateManifest(tamperedBytes, "testdata"); err == nil {
			t.Error("expected error for tampered schema digest, got nil")
		}
	})

	t.Run("duplicate artifact role", func(t *testing.T) {
		var m Manifest
		_ = json.Unmarshal(validData, &m)
		m.Artifacts = append(m.Artifacts, m.Artifacts[0])
		tamperedBytes, _ := json.Marshal(m)
		if err := ValidateManifest(tamperedBytes, "testdata"); err == nil {
			t.Error("expected error for duplicate artifact role, got nil")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		var m Manifest
		_ = json.Unmarshal(validData, &m)
		m.Artifacts = append(m.Artifacts, ManifestArtifact{
			Role:         "nonexistent",
			UpstreamPath: "missing.json",
			LocalPath:    "missing.json",
			SHA256:       "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		})
		tamperedBytes, _ := json.Marshal(m)
		if err := ValidateManifest(tamperedBytes, "testdata"); err == nil {
			t.Error("expected error for missing artifact file, got nil")
		}
	})

	t.Run("tampered artifact content", func(t *testing.T) {
		// Test validation against a temp directory where one artifact has modified content
		tempDir := t.TempDir()
		var m Manifest
		_ = json.Unmarshal(validData, &m)

		for _, art := range m.Artifacts {
			srcPath := filepath.Join("testdata", art.LocalPath)
			content, _ := os.ReadFile(srcPath)
			destPath := filepath.Join(tempDir, art.LocalPath)
			_ = os.MkdirAll(filepath.Dir(destPath), 0o755)
			if art.Role == "official_example_param" {
				content = []byte(`{"tampered": true}`)
			}
			_ = os.WriteFile(destPath, content, 0o644)
		}

		if err := ValidateManifest(validData, tempDir); err == nil {
			t.Error("expected error for tampered artifact content, got nil")
		}
	})

	t.Run("unlisted extra file", func(t *testing.T) {
		tempDir := t.TempDir()
		var m Manifest
		_ = json.Unmarshal(validData, &m)

		for _, art := range m.Artifacts {
			srcPath := filepath.Join("testdata", art.LocalPath)
			content, _ := os.ReadFile(srcPath)
			destPath := filepath.Join(tempDir, art.LocalPath)
			_ = os.MkdirAll(filepath.Dir(destPath), 0o755)
			_ = os.WriteFile(destPath, content, 0o644)
		}
		// Write an unlisted file
		_ = os.WriteFile(filepath.Join(tempDir, "unlisted_extra.json"), []byte("{}"), 0o644)

		if err := ValidateManifest(validData, tempDir); err == nil {
			t.Error("expected error for unlisted file in testdata, got nil")
		}
	})
}

func TestLicenseAttribution(t *testing.T) {
	t.Parallel()
	desc := GetProfileDescriptor()

	if desc.License != "Apache-2.0" {
		t.Fatalf("expected license Apache-2.0, got %q", desc.License)
	}
	if !strings.Contains(desc.LicenseAttribution, "Apache License, Version 2.0") {
		t.Fatalf("expected license attribution to mention Apache License, Version 2.0, got %q", desc.LicenseAttribution)
	}
}
