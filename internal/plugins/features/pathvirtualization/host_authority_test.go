package pathvirtualization_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// modulePath is the repository module prefix the mapper must not depend on.
const modulePath = "github.com/aiproxer/aiproxer"

// forbiddenHostImports lists packages flavor parsing must never depend on.
// Host separators and host path semantics would make a foreign client path
// unparseable, and os/runtime would let host-dependent or filesystem behavior
// leak into a pure lexical classifier.
var forbiddenHostImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// TestFlavorParsingHasNoHostAuthority parses the package's own non-test sources
// and proves the classifier holds no host OS, host separator, or filesystem
// authority. This is the structural half of the host-independence rule; the
// recognition table in flavor_test.go is the behavioral half.
func TestFlavorParsingHasNoHostAuthority(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		checked++
		for _, spec := range file.Imports {
			imported, importErr := strconv.Unquote(spec.Path.Value)
			if importErr != nil {
				t.Fatalf("import path in %s is not a string literal: %v", name, importErr)
			}
			for _, forbidden := range forbiddenHostImports {
				if imported == forbidden {
					t.Errorf("%s imports %q; flavor parsing must stay host-independent", name, imported)
				}
			}
			if strings.HasPrefix(imported, modulePath) {
				t.Errorf("%s imports repo package %q; the lexical mapper must stand alone", name, imported)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the host-authority guard proved nothing")
	}
}

// TestFlavorRecognitionIsHostIndependent pins one fixed path vector per flavor
// plus the rejection classes to host-independent constants. The test never
// consults the running host, so its expectations stay the same on every
// operating system and a host-dependent implementation cannot pass it.
func TestFlavorRecognitionIsHostIndependent(t *testing.T) {
	t.Parallel()

	vector := []struct {
		name     string
		path     string
		wantFlav pathvirtualization.PathFlavor
		wantSkip pathvirtualization.SkipReason
	}{
		{name: "posix", path: `/home/dev/project`, wantFlav: pathvirtualization.FlavorPOSIX},
		{name: "drive_backslash", path: `C:\Users\dev\project`, wantFlav: pathvirtualization.FlavorWindowsDrive},
		{name: "drive_forward_slash", path: `C:/Users/dev/project`, wantFlav: pathvirtualization.FlavorWindowsDrive},
		{name: "unc", path: `\\build01\dev\project`, wantFlav: pathvirtualization.FlavorWindowsUNC},
		{name: "extended_drive", path: `\\?\C:\Users\dev\project`, wantFlav: pathvirtualization.FlavorWindowsExtendedDrive},
		{name: "extended_unc", path: `\\?\UNC\build01\dev\project`, wantFlav: pathvirtualization.FlavorWindowsExtendedUNC},
		{name: "device", path: `\\.\PIPE\aip`, wantSkip: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "relative", path: `relative\path`, wantSkip: pathvirtualization.SkipReasonRelativeRoot},
	}
	for _, tc := range vector {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.ClassifyPath(tc.path)
			if reason != tc.wantSkip {
				t.Fatalf("ClassifyPath(%q) reason = %q, want %q", tc.path, reason, tc.wantSkip)
			}
			if tc.wantSkip == pathvirtualization.SkipReasonNone && got.Flavor != tc.wantFlav {
				t.Fatalf("ClassifyPath(%q) flavor = %v, want %v", tc.path, got.Flavor, tc.wantFlav)
			}
		})
	}
}
