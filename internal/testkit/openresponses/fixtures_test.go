package openresponses_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/openresponses"
)

func TestFixtureDescriptor_Validation(t *testing.T) {
	t.Parallel()

	desc, err := testkit.NewOfficialFixtureDescriptor("fix-01", "basic_request", "official", "testdata/official/basic.json")
	if err != nil {
		t.Fatalf("unexpected error creating official fixture descriptor: %v", err)
	}

	if err := desc.Validate(); err != nil {
		t.Fatalf("valid official fixture descriptor failed validation: %v", err)
	}

	profile := openresponses.GetProfileDescriptor()
	if desc.SchemaDigest != profile.SchemaDigest {
		t.Fatalf("schema digest %q does not match profile schema digest %q", desc.SchemaDigest, profile.SchemaDigest)
	}
	if desc.ComplianceDigest != profile.ComplianceDigest {
		t.Fatalf("compliance digest %q does not match profile compliance digest %q", desc.ComplianceDigest, profile.ComplianceDigest)
	}
	if desc.SourceCommit != profile.SourceCommit {
		t.Fatalf("source commit %q does not match profile source commit %q", desc.SourceCommit, profile.SourceCommit)
	}

	// Test invalid schema digest
	invalidDesc := desc
	invalidDesc.SchemaDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := invalidDesc.Validate(); err == nil {
		t.Fatal("expected error on tampered schema digest, got nil")
	}

	// Test empty fixture ID
	_, err = testkit.NewOfficialFixtureDescriptor("", "name", "category", "path")
	if err == nil {
		t.Fatal("expected error creating descriptor with empty ID, got nil")
	}
}

func TestFixture_Immutability(t *testing.T) {
	t.Parallel()

	desc, err := testkit.NewOfficialFixtureDescriptor("fix-02", "streaming_response", "official", "testdata/official/stream.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutating returned struct doesn't corrupt Task 1.1 profile
	desc.SchemaDigest = "corrupted"
	profile := openresponses.GetProfileDescriptor()
	if profile.SchemaDigest == "corrupted" {
		t.Fatal("profile schema digest mutated!")
	}
}

func TestBinaryFixtureDescriptor_Validation(t *testing.T) {
	t.Parallel()

	bin := testkit.BinaryFixtureDescriptor{
		ID:        "bin-image-01",
		MediaType: "image/png",
		SizeBytes: 1024,
		Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		RelPath:   "testdata/fixtures/sample.png",
	}

	if err := bin.Validate(); err != nil {
		t.Fatalf("valid binary fixture rejected: %v", err)
	}

	// Invalid digest prefix
	invalidBin := bin
	invalidBin.Digest = "md5:123456"
	if err := invalidBin.Validate(); err == nil {
		t.Fatal("expected error for non-sha256 digest, got nil")
	}

	// Non-positive size
	invalidBin2 := bin
	invalidBin2.SizeBytes = 0
	if err := invalidBin2.Validate(); err == nil {
		t.Fatal("expected error for size 0, got nil")
	}
}
