package qa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This is intentionally independent of the JSON manifest. A mutable manifest
// must not be able to redefine the expected official checkout.
const expectedOpenResponsesOfficialManifestSHA256 = "64366dc5e30e3f1c744db4a2ef40a05076b766a7b9454948f58aa40347314762"

type officialManifest struct {
	Artifacts []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

func TestOpenResponsesOfficialVendoredManifest(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "tools", "openresponses-compliance")
	manifestPath := filepath.Join(root, "MANIFEST.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestData)
	if got := hex.EncodeToString(manifestSum[:]); got != expectedOpenResponsesOfficialManifestSHA256 {
		t.Fatalf("official manifest digest mismatch: got %s, want %s", got, expectedOpenResponsesOfficialManifestSHA256)
	}
	var manifest officialManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	t.Logf("official upstream revision=%s", "92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c")
	if len(manifest.Artifacts) == 0 {
		t.Fatal("official manifest lists no artifacts")
	}
	listed := make(map[string]bool, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == "" || artifact.SHA256 == "" {
			t.Fatalf("invalid manifest artifact: %+v", artifact)
		}
		if listed[artifact.Path] {
			t.Fatalf("duplicate official artifact %q", artifact.Path)
		}
		listed[artifact.Path] = true
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read official artifact %q: %v", artifact.Path, err)
		}
		sum := sha256.Sum256(data)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if got != artifact.SHA256 {
			t.Fatalf("official artifact %q digest mismatch: got %s, want %s", artifact.Path, got, artifact.SHA256)
		}
		t.Logf("verified official artifact %s=%s", artifact.Path, got)
	}
}
