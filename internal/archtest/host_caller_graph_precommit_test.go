//go:build precommit

package archtest

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestBuildHostIsSoleBindHostCaller(t *testing.T) {
	t.Parallel()
	callers, analyzed := bindHostCallersAcrossContexts(t, nil)
	sort.Strings(callers)
	if len(callers) != 1 || callers[0] != "host_build.go:buildHost" {
		t.Fatalf("bindHost caller graph must be exactly [host_build.go:buildHost], got %v", callers)
	}
	assertProductionGoInventory(t, []string{runtimebundleDir(t)}, analyzed)
}

func TestBuildHostIsSoleBindHostCaller_WindowsOverlaySecondCallerDetected(t *testing.T) {
	t.Parallel()
	dir := runtimebundleDir(t)
	overlayPath := filepath.Join(dir, "bindhost_windows_overlay.go")
	overlay := map[string][]byte{
		overlayPath: []byte(`//go:build windows

package runtimebundle

func rogueWindowsBindHostCaller() (*Host, error) {
	return bindHost("windows-overlay", bindHostInput{})
}
`),
	}
	callers, analyzed := bindHostCallersAcrossContexts(t, overlay)
	sort.Strings(callers)
	wantExtra := "bindhost_windows_overlay.go:rogueWindowsBindHostCaller"
	if !slices.Contains(callers, wantExtra) {
		t.Fatalf("windows overlay second bindHost caller must be detected; callers=%v", callers)
	}
	if !analyzed[overlayPath] && !analyzed[filepath.Clean(overlayPath)] {
		t.Fatalf("windows overlay file must count toward analyzed inventory; analyzed missing %s", overlayPath)
	}
}
