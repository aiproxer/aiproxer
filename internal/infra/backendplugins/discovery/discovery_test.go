package discovery_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
)

func TestDiscovery_ExplicitPathsNoCWDNoPATH(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeManifest(t, root, "a.backendplugin.json", manifestBody("io.a", "ka", runtime.GOOS))
	res, err := discovery.Discover(discovery.Config{
		ExplicitPaths: []string{root},
		Development:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 1 || res.Descriptors[0].Status != discovery.StatusDiscovered {
		t.Fatalf("%+v", res.Descriptors)
	}
}

func TestDiscovery_DevelopmentRequiresExplicit(t *testing.T) {
	t.Parallel()
	_, err := discovery.Discover(discovery.Config{Development: true})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDiscovery_UpstreamDefaultConstant(t *testing.T) {
	t.Parallel()
	got := discovery.UpstreamDefaultRoot()
	if filepath.Base(got) != "plugins" {
		t.Fatalf("%s", got)
	}
}

func TestDiscovery_StableOrderAndDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeManifest(t, root, "b.backendplugin.json", manifestBody("io.b", "kb", runtime.GOOS))
	writeManifest(t, root, "a.backendplugin.json", manifestBody("io.a", "ka", runtime.GOOS))
	res, err := discovery.Discover(discovery.Config{ExplicitPaths: []string{root, root}, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 2 {
		t.Fatalf("len=%d", len(res.Descriptors))
	}
	if res.Descriptors[0].SafeID > res.Descriptors[1].SafeID {
		t.Fatalf("unsorted: %s %s", res.Descriptors[0].SafeID, res.Descriptors[1].SafeID)
	}
}

func TestDiscovery_SymlinkManifestRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escaped.backendplugin.json")
	writeManifest(t, outside, "escaped.backendplugin.json", manifestBody("io.escape", "ke", runtime.GOOS))
	link := filepath.Join(root, "evil.backendplugin.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	res, err := discovery.Discover(discovery.Config{ExplicitPaths: []string{root}, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 1 {
		t.Fatalf("%+v", res.Descriptors)
	}
	if res.Descriptors[0].Status != discovery.StatusSkipped || res.Descriptors[0].Reason != "symlink_rejected" {
		t.Fatalf("%+v", res.Descriptors[0])
	}
}

func TestDiscovery_NoExecImportArch(t *testing.T) {
	t.Parallel()
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, `"os/exec"`) || strings.Contains(src, "exec.Command") || strings.Contains(src, "exec.CommandContext") {
			t.Fatalf("%s imports/uses os/exec", e.Name())
		}
	}
}

func TestDiscovery_IrregularRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dirPath := filepath.Join(root, "dir.backendplugin.json")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := discovery.Discover(discovery.Config{ExplicitPaths: []string{root}, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 1 || res.Descriptors[0].Status != discovery.StatusSkipped {
		t.Fatalf("%+v", res.Descriptors)
	}
}
