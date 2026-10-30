//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aiproxer/aiproxer/internal/featureplanegen"
)

func main() {
	checkFlag := flag.Bool("check", false, "verify that generated output matches disk without modifying files")
	manifestPathFlag := flag.String("manifest", "", "path to plane_manifest.go (default: auto-detected)")
	outPathFlag := flag.String("out", "", "path to plane_generated.go (default: auto-detected)")
	eligibilityOutFlag := flag.String("eligibility-out", "", "path to plane_catalog_generated.go (default: auto-detected when -out is not explicitly given)")
	flag.Parse()

	repoRoot := findRepoRoot()
	defaultManifestPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_manifest.go")
	defaultPlaneOutPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go")
	defaultEligibilityOutPath := filepath.Join(repoRoot, "internal", "core", "largebody", "plane_catalog_generated.go")
	manifestPath := *manifestPathFlag
	if manifestPath == "" {
		manifestPath = defaultManifestPath
	}
	outGiven := *outPathFlag != ""
	eligibilityGiven := *eligibilityOutFlag != ""
	planeOutPath := *outPathFlag
	if planeOutPath == "" {
		planeOutPath = filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go")
	}
	// Default invocation covers both artifacts. An explicit legacy -out
	// remains SDK-only unless -eligibility-out is also given, so existing
	// scratch fixtures cannot overwrite the repository core artifact.
	// Custom two-output probes pass explicit destinations for both outputs.
	eligibilityOutPath := *eligibilityOutFlag
	wantEligibility := false
	if !outGiven && !eligibilityGiven {
		wantEligibility = true
		eligibilityOutPath = filepath.Join(repoRoot, "internal", "core", "largebody", "plane_catalog_generated.go")
	} else if eligibilityGiven {
		wantEligibility = true
		if eligibilityOutPath == "" {
			eligibilityOutPath = filepath.Join(repoRoot, "internal", "core", "largebody", "plane_catalog_generated.go")
		}
	}

	absCleanPath := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(p)
	}
	// sameDestinationPath reports whether two destinations resolve to the same
	// file. When both paths exist, symlinks are resolved before comparing so
	// alias spellings of one file are caught; otherwise absolute clean paths
	// are compared.
	sameDestinationPath := func(a, b string) bool {
		ca, cb := absCleanPath(a), absCleanPath(b)
		if ea, errA := filepath.EvalSymlinks(ca); errA == nil {
			if eb, errB := filepath.EvalSymlinks(cb); errB == nil {
				return ea == eb
			}
		}
		return ca == cb
	}
	// The two artifacts carry different content: installing or checking them
	// against one destination would silently overwrite one rendering with the
	// other while reporting success.
	if wantEligibility && sameDestinationPath(planeOutPath, eligibilityOutPath) {
		fmt.Fprintf(os.Stderr, "error: -out and -eligibility-out resolve to the same file %q: use distinct destinations\n", absCleanPath(planeOutPath))
		os.Exit(1)
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}

	// Validate and render every selected output before installing any file,
	// so invalid or over-capacity input installs neither artifact.
	formattedPlanes, err := featureplanegen.GenerateFeaturePlanesCode(manifestBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating feature planes code: %v\n", err)
		os.Exit(1)
	}

	var formattedEligibility []byte
	if wantEligibility {
		formattedEligibility, err = featureplanegen.GenerateWireEligibilityCode(manifestBytes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error generating wire eligibility code: %v\n", err)
			os.Exit(1)
		}
	}

	if *checkFlag {
		checkFileMatches(planeOutPath, formattedPlanes)
		if wantEligibility {
			checkFileMatches(eligibilityOutPath, formattedEligibility)
		}
		fmt.Println("generated feature planes file is up to date.")
		return
	}

	// Install mode only: a custom manifest with an implicit repository-default
	// destination would overwrite a tracked artifact with fixture-derived
	// bytes. Require explicit destinations alongside custom manifests; an
	// explicit repository path stays allowed as the user's explicit choice.
	if absCleanPath(manifestPath) != absCleanPath(defaultManifestPath) {
		if !outGiven && sameDestinationPath(planeOutPath, defaultPlaneOutPath) {
			fmt.Fprintf(os.Stderr, "error: custom -manifest requires explicit -out: refusing to overwrite repository default %q\n", absCleanPath(planeOutPath))
			os.Exit(1)
		}
		if wantEligibility && !eligibilityGiven && sameDestinationPath(eligibilityOutPath, defaultEligibilityOutPath) {
			fmt.Fprintf(os.Stderr, "error: custom -manifest requires explicit -eligibility-out: refusing to overwrite repository default %q\n", absCleanPath(eligibilityOutPath))
			os.Exit(1)
		}
	}

	if err := featureplanegen.WriteGeneratedFileAtomic(planeOutPath, formattedPlanes); err != nil {
		fmt.Fprintf(os.Stderr, "error writing generated file atomically: %v\n", err)
		os.Exit(1)
	}
	if wantEligibility {
		// Per-file atomic install with no cross-file transaction promise:
		// a failed second install returns nonzero and a later -check
		// reports the drift; rerun repairs it.
		if err := featureplanegen.WriteGeneratedFileAtomic(eligibilityOutPath, formattedEligibility); err != nil {
			fmt.Fprintf(os.Stderr, "error writing generated file atomically: %v\n", err)
			os.Exit(1)
		}
	}
	if wantEligibility {
		fmt.Printf("Generated %s and %s successfully.\n", planeOutPath, eligibilityOutPath)
		return
	}
	fmt.Printf("Generated %s successfully.\n", planeOutPath)
}

func checkFileMatches(path string, formatted []byte) {
	existing, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading existing generated file %s: %v\n", path, err)
		os.Exit(1)
	}
	normExisting := bytes.ReplaceAll(existing, []byte("\r\n"), []byte("\n"))
	normFormatted := bytes.ReplaceAll(formatted, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(normExisting, normFormatted) {
		fmt.Fprintf(os.Stderr, "ERROR: generated file %s is stale or differs from manifest\nRun 'go run ./scripts/generate-feature-planes.go' to regenerate.\n", path)
		os.Exit(1)
	}
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}
