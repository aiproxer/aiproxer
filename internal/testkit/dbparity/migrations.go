package dbparity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// versionedMigrationRegex matches filenames like 20260812000000_billing_baseline.go.
// It requires an exact 14-digit timestamp prefix, followed by an underscore, descriptive name, and .go extension.
var versionedMigrationRegex = regexp.MustCompile(`^(\d{14})_(.+)\.go$`)

// MigrationFile represents a discovered versioned database migration file.
type MigrationFile struct {
	ID       string `json:"id"`       // 14-digit timestamp string (e.g. "20260812000000")
	Name     string `json:"name"`     // Descriptive name after timestamp prefix (e.g. "billing_baseline")
	Filename string `json:"filename"` // Base filename (e.g. "20260812000000_billing_baseline.go")
	Path     string `json:"path"`     // Full or relative path to the migration file
}

// DiscoverMigrations discovers versioned migration files in the specified directory root.
// It extracts 14-digit timestamp IDs from filenames matching ^\d{14}_.+\.go$, excluding _test.go files.
// Discovered migrations are returned sorted chronologically by timestamp ID.
// If two eligible migration files share the same 14-digit timestamp ID, an error is returned
// naming the duplicate ID and both filenames.
func DiscoverMigrations(root string) ([]MigrationFile, error) {
	trimmedRoot := strings.TrimSpace(root)
	if trimmedRoot == "" {
		return nil, fmt.Errorf("migration root path must not be blank")
	}

	info, err := os.Stat(trimmedRoot)
	if err != nil {
		return nil, fmt.Errorf("stat migration root %s: %w", trimmedRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("migration root %s is not a directory", trimmedRoot)
	}

	entries, err := os.ReadDir(trimmedRoot)
	if err != nil {
		return nil, fmt.Errorf("read migration root %s: %w", trimmedRoot, err)
	}

	// Sort entries by filename first for deterministic deduplication and error reporting
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	seenFiles := make(map[string]string)
	var migrations []MigrationFile

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		matches := versionedMigrationRegex.FindStringSubmatch(filename)
		if matches == nil {
			continue
		}

		id := matches[1]
		name := matches[2]

		if prevFilename, exists := seenFiles[id]; exists {
			return nil, fmt.Errorf("duplicate migration ID %s found in %s and %s", id, prevFilename, filename)
		}
		seenFiles[id] = filename

		migrations = append(migrations, MigrationFile{
			ID:       id,
			Name:     name,
			Filename: filename,
			Path:     filepath.ToSlash(filepath.Join(trimmedRoot, filename)),
		})
	}

	slices.SortFunc(migrations, func(a, b MigrationFile) int {
		return strings.Compare(a.ID, b.ID)
	})

	return migrations, nil
}

// DiscoverMigrationIDs discovers and returns the sorted unique list of migration IDs in the specified root directory.
func DiscoverMigrationIDs(root string) ([]string, error) {
	files, err := DiscoverMigrations(root)
	if err != nil {
		return nil, err
	}
	return MigrationIDs(files), nil
}

// MigrationIDs extracts the slice of migration IDs from a slice of MigrationFiles.
func MigrationIDs(files []MigrationFile) []string {
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	return ids
}

// DiscoverComponentMigrations discovers all versioned migration files across all migration roots of a component.
// Discovered migrations are returned sorted chronologically by timestamp ID.
// If two migration files across roots share the same 14-digit timestamp ID, an error is returned
// naming the component, the duplicate ID, and both file paths.
func DiscoverComponentMigrations(repoRoot string, comp Component) ([]MigrationFile, error) {
	trimmedRepoRoot := strings.TrimSpace(repoRoot)
	seen := make(map[string]MigrationFile)
	var allMigrations []MigrationFile

	for _, mr := range comp.MigrationRoots {
		trimmedMR := strings.TrimSpace(mr)
		absDir := filepath.Join(trimmedRepoRoot, filepath.FromSlash(trimmedMR))
		migrations, err := DiscoverMigrations(absDir)
		if err != nil {
			return nil, fmt.Errorf("component %q: discover migrations in %s: %w", comp.ID, mr, err)
		}
		for _, m := range migrations {
			relPath := m.Path
			if trimmedRepoRoot != "" {
				if rel, err := filepath.Rel(trimmedRepoRoot, m.Path); err == nil {
					relPath = filepath.ToSlash(rel)
				}
			}
			m.Path = relPath

			if prev, exists := seen[m.ID]; exists {
				return nil, fmt.Errorf("component %q: duplicate migration ID %s found in %s and %s", comp.ID, m.ID, prev.Path, m.Path)
			}
			seen[m.ID] = m
			allMigrations = append(allMigrations, m)
		}
	}

	slices.SortFunc(allMigrations, func(a, b MigrationFile) int {
		return strings.Compare(a.ID, b.ID)
	})

	return allMigrations, nil
}

// AssertMigrationHistoryIDs verifies that every discovered migration ID is recorded in the applied migration history map.
// It returns an error naming all missing migration IDs in sorted order if any are missing.
func AssertMigrationHistoryIDs(discovered []string, recorded map[string]bool) error {
	if len(discovered) == 0 {
		return nil
	}
	var missing []string
	for _, id := range discovered {
		if !recorded[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("missing applied migration history for %d migration(s): [%s]",
			len(missing), strings.Join(missing, ", "))
	}
	return nil
}

// AssertMigrationHistory verifies that every discovered migration ID is present in the applied migration history slice.
func AssertMigrationHistory(discovered []string, applied []string) error {
	recorded := make(map[string]bool, len(applied))
	for _, id := range applied {
		recorded[id] = true
	}
	return AssertMigrationHistoryIDs(discovered, recorded)
}

// AssertMigrationFilesApplied verifies that every discovered migration file is recorded in the applied migration history map.
func AssertMigrationFilesApplied(discovered []MigrationFile, recorded map[string]bool) error {
	return AssertMigrationHistoryIDs(MigrationIDs(discovered), recorded)
}
