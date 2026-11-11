package archtest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// toolchainVersion matches a copied Go toolchain version such as go1.26.11.
var toolchainVersion = regexp.MustCompile(`go1\.[0-9]+\.[0-9]+`)

// toolchainRecommendationFiles are the places that tell a developer, a doctor
// or an acceptance run which Go toolchain to use.
var toolchainRecommendationFiles = []string{
	"scripts/cache-affinity-process-e2e.py",
	"scripts/dev-setup.sh",
	"scripts/go-dev-guard.sh",
	"docs/testing/cache-affinity-process-e2e.md",
}

// TestToolchainRecommendationFollowsGoMod proves every toolchain
// recommendation is derived from go.mod instead of carrying a copy of the
// version.
//
// The toolchain directive moves when a security-patched Go release is selected
// (GO-2026-6809 affected go1.26.9 and go1.27.2). A script or a document that
// carries its own copy of the version would keep demanding the vulnerable
// toolchain after the repository moved, so the copy is the defect.
func TestToolchainRecommendationFollowsGoMod(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if declaredToolchain(t, root) == "" {
		t.Fatal("go.mod declares no toolchain")
	}
	for _, path := range toolchainRecommendationFiles {
		raw, err := os.ReadFile(root + "/" + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		content := string(raw)
		if match := toolchainVersion.FindString(content); match != "" {
			t.Fatalf("%s carries a copied toolchain version %q; derive it from go.mod instead", path, match)
		}
		if !strings.Contains(content, "go.mod") {
			t.Fatalf("%s does not point at the repository's declared toolchain", path)
		}
	}
}

// declaredToolchain returns the toolchain the repository declares in go.mod.
func declaredToolchain(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(root + "/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "toolchain" {
			return parts[1]
		}
	}
	return ""
}
