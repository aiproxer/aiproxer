package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreOwnershipManifestCoversEveryTopLevelPackage is the package admission
// gate (Task 11.1, Req 12.3/13.2): every final top-level internal/core/*
// package holding production code must have exactly one ownership manifest
// entry. A new top-level core package without an entry fails this test until
// it receives explicit architecture review.
func TestCoreOwnershipManifestCoversEveryTopLevelPackage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	missing := missingCoreOwnershipEntries(filepath.Join(root, "internal", "core"), coreOwnershipByPackage())
	if len(missing) > 0 {
		t.Fatalf("top-level core packages without ownership manifest entry (%d):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestCoreOwnershipManifestEntriesValid locks the manifest shape: known
// categories only, concise reasons, generic mechanisms carry independent
// consumer evidence, and no stale entries for removed packages.
func TestCoreOwnershipManifestEntriesValid(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	if problems := validateCoreOwnershipEntries(root, CoreOwnershipManifest); len(problems) > 0 {
		t.Fatalf("core ownership manifest invalid (%d):\n%s", len(problems), strings.Join(problems, "\n"))
	}
}

// TestCoreOwnershipAdmission_RecursiveSubtreeScan proves admission walks the
// full subtree: a package whose production code lives only in a nested
// directory (e.g. internal/core/newpolicy/app/policy.go with no .go files at
// the top level) still requires a manifest entry.
func TestCoreOwnershipAdmission_RecursiveSubtreeScan(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "internal", "core", "newpolicy", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "policy.go"), []byte("package app\n\nfunc Enforce() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := missingCoreOwnershipEntries(filepath.Join(root, "internal", "core"), map[string]CoreOwnershipEntry{})
	if len(missing) != 1 || missing[0] != "internal/core/newpolicy" {
		t.Fatalf("nested-only production package must require a manifest entry, got missing=%v", missing)
	}
}

// TestCoreOwnershipManifest_RejectsStaleEntries proves validation fails
// closed: a manifest entry whose top-level directory holds zero production
// Go anywhere beneath is a violation until removed or re-justified.
func TestCoreOwnershipManifest_RejectsStaleEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	staleDir := filepath.Join(root, "internal", "core", "stalepkg")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "doc_test.go"), []byte("package stalepkg\n\nfunc Example() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := []CoreOwnershipEntry{
		{Package: "stalepkg", Category: CoreOwnershipKernelInvariant, Reason: "Synthetic stale fixture."},
	}
	problems := validateCoreOwnershipEntries(root, manifest)
	found := false
	for _, p := range problems {
		if strings.Contains(p, "stalepkg") {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale manifest entry must be rejected, got problems=%v", problems)
	}
}
