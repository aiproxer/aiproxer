// Package kirospec validates the Kiro spec tree: lifecycle placement, slice
// budgets from .kiro/steering/delivery.md, and stale references to archived
// specs. It depends only on the standard library so the pre-commit hook can
// run it through `go run ./tools/kiro/kirocheck` without a heavy Go slot.
package kirospec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

const maxStaleReferenceLineBytes = 4 * 1024 * 1024

// Validate checks the repository rooted at root and returns sorted findings.
func Validate(root string) []string {
	specsRoot := filepath.Join(root, ".kiro", "specs")
	errs := ValidateLifecycle(specsRoot)
	errs = append(errs, ValidateBudgets(specsRoot)...)
	errs = append(errs, ValidateStaleReferences(root)...)
	sort.Strings(errs)
	return errs
}

type lifecycleMetadata struct {
	Phase                  string `json:"phase"`
	ReadyForImplementation bool   `json:"ready_for_implementation"`
	Completed              bool   `json:"completed"`
}

// ValidateLifecycle checks that finished specs are archived and archived specs are finished.
func ValidateLifecycle(specsRoot string) []string {
	errs, _ := ValidateLifecycleWithOmissions(specsRoot)
	return errs
}

// ValidateLifecycleWithOmissions also reports exact files omitted by export policy.
func ValidateLifecycleWithOmissions(specsRoot string) ([]string, []string) {
	var errs []string
	var omissions []string
	active := make(map[string]struct{})

	entries, err := os.ReadDir(specsRoot)
	if err != nil {
		return []string{fmt.Sprintf("read active specs: %v", err)}, omissions
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "archive" {
			continue
		}
		dir := filepath.Join(specsRoot, entry.Name())
		omitted, missing := specOmissions(specsRoot, dir)
		omissions = append(omissions, omitted...)
		errs = append(errs, missing...)
		active[entry.Name()] = struct{}{}
		metadata, loadErr := loadLifecycle(filepath.Join(specsRoot, entry.Name(), "spec.json"))
		if loadErr != nil && !omittedFile(omitted, "spec.json") {
			errs = append(errs, fmt.Sprintf("active/%s: %v", entry.Name(), loadErr))
		}
		if loadErr == nil && (metadata.Completed || metadata.Phase == "completed" || metadata.Phase == "superseded") {
			errs = append(errs, fmt.Sprintf("active/%s: completed or superseded specs must be archived", entry.Name()))
		}
		checked, unchecked, countErr := countTaskBoxes(filepath.Join(specsRoot, entry.Name(), "tasks.md"))
		if countErr != nil && !omittedFile(omitted, "tasks.md") {
			errs = append(errs, fmt.Sprintf("active/%s: %v", entry.Name(), countErr))
		} else if checked > 0 && unchecked == 0 {
			errs = append(errs, fmt.Sprintf("active/%s: all %d tasks are checked; completed specs must be archived", entry.Name(), checked))
		}
	}

	archiveRoot := filepath.Join(specsRoot, "archive")
	archived, err := os.ReadDir(archiveRoot)
	if err != nil {
		errs = append(errs, fmt.Sprintf("read archived specs: %v", err))
		sort.Strings(errs)
		return errs, omissions
	}
	for _, entry := range archived {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(archiveRoot, name)
		omitted, missing := specOmissions(specsRoot, dir)
		omissions = append(omissions, omitted...)
		errs = append(errs, missing...)
		if _, duplicated := active[name]; duplicated {
			errs = append(errs, fmt.Sprintf("archive/%s: duplicate active spec exists", name))
		}
		metadata, loadErr := loadLifecycle(filepath.Join(dir, "spec.json"))
		if loadErr != nil {
			if !omittedFile(omitted, "spec.json") {
				errs = append(errs, fmt.Sprintf("archive/%s: %v", name, loadErr))
			}
			continue
		}
		if metadata.Phase != "completed" && metadata.Phase != "superseded" {
			errs = append(errs, fmt.Sprintf("archive/%s: phase %q must be completed or superseded", name, metadata.Phase))
		}
		if !metadata.Completed {
			errs = append(errs, fmt.Sprintf("archive/%s: completed must be true", name))
		}
		if metadata.ReadyForImplementation {
			errs = append(errs, fmt.Sprintf("archive/%s: ready_for_implementation must be false", name))
		}
		if metadata.Phase == "completed" && !omittedFile(omitted, "tasks.md") {
			errs = append(errs, uncheckedTasks(dir, name)...)
		}
	}

	sort.Strings(errs)
	sort.Strings(omissions)
	return errs, omissions
}

// Only an absent file explicitly excluded by export policy may be tolerated.
func exportOmitted(specsRoot, path string) bool {
	root, err := filepath.Abs(filepath.Dir(filepath.Dir(specsRoot)))
	if err != nil || !opensource.IsOpenSourceTree(root) {
		return false
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", root, "check-attr", "-z", "export-ignore", "--", filepath.ToSlash(rel))
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	fields := strings.Split(string(out), "\x00")
	return err == nil && len(fields) == 4 && fields[2] == "set"
}

func specOmissions(specsRoot, dir string) (omissions, errs []string) {
	for _, name := range []string{"spec.json", "tasks.md", "requirements.md"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			if exportOmitted(specsRoot, path) {
				omissions = append(omissions, path)
			} else {
				errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			}
		}
	}
	// Design is optional for early-phase specs, but an exported-out design must
	// still be reported and excluded from grandfathered shrinkage comparisons.
	if path := filepath.Join(dir, "design.md"); exportOmitted(specsRoot, path) {
		omissions = append(omissions, path)
	}
	return omissions, errs
}

func omittedFile(omissions []string, name string) bool {
	for _, path := range omissions {
		if filepath.Base(path) == name {
			return true
		}
	}
	return false
}

func loadLifecycle(path string) (lifecycleMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return lifecycleMetadata{}, err
	}
	var metadata lifecycleMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return lifecycleMetadata{}, err
	}
	return metadata, nil
}

func isUncheckedBox(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "- [ ]")
}

func isCheckedBox(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "- [x]")
}

// countTaskBoxes reports checked and unchecked task boxes in tasks.md.
// Specs with no boxes at all count as (0, 0) and never trip the
// fully-checked-active finding; unreadable files are errors.
func countTaskBoxes(path string) (checked, unchecked int, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		switch {
		case isUncheckedBox(scanner.Text()):
			unchecked++
		case isCheckedBox(scanner.Text()):
			checked++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return checked, unchecked, nil
}

func uncheckedTasks(dir, name string) []string {
	file, err := os.Open(filepath.Join(dir, "tasks.md"))
	if err != nil {
		return []string{fmt.Sprintf("archive/%s: read tasks.md: %v", name, err)}
	}
	defer func() { _ = file.Close() }()

	var errs []string
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		if isUncheckedBox(scanner.Text()) {
			errs = append(errs, fmt.Sprintf("archive/%s: unchecked task at tasks.md:%d", name, line))
		}
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("archive/%s: scan tasks.md: %v", name, err))
	}
	return errs
}

var activeReference = regexp.MustCompile(`\.kiro/specs/([a-z0-9][a-z0-9-]*)/`)

// ValidateStaleReferences reports code that reaches an archived spec through its old active path.
func ValidateStaleReferences(root string) []string {
	active := directoryNames(filepath.Join(root, ".kiro", "specs"), "archive")
	archived := directoryNames(filepath.Join(root, ".kiro", "specs", "archive"))
	allowedExtensions := map[string]bool{".go": true, ".sh": true, ".ps1": true, ".yml": true, ".yaml": true}
	var errs []string

	for _, tree := range []string{"cmd", "internal", "pkg", "scripts", "tools", ".github"} {
		base := filepath.Join(root, tree)
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, walkErr))
				return nil
			}
			if entry.IsDir() || !allowedExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, err))
				return nil
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(nil, maxStaleReferenceLineBytes)
			for line := 1; scanner.Scan(); line++ {
				for _, match := range activeReference.FindAllStringSubmatch(scanner.Text(), -1) {
					name := match[1]
					_, isActive := active[name]
					_, isArchived := archived[name]
					if !isActive && isArchived {
						rel, _ := filepath.Rel(root, path)
						errs = append(errs, fmt.Sprintf("%s:%d references archived spec %s through stale active path", filepath.ToSlash(rel), line, name))
					}
				}
			}
			if err := scanner.Err(); err != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, err))
			}
			_ = file.Close()
			return nil
		})
	}
	sort.Strings(errs)
	return errs
}

func directoryNames(path string, excluded ...string) map[string]struct{} {
	names := make(map[string]struct{})
	exclusions := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		exclusions[name] = struct{}{}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, skip := exclusions[entry.Name()]; !skip {
				names[entry.Name()] = struct{}{}
			}
		}
	}
	return names
}
