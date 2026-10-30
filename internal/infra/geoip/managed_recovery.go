package geoip

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OpenManaged recovers a strictly validated active manifest or the newest
// retained verified version. It never performs network acquisition.
func OpenManaged(directory, edition string) (*Service, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("geoip: managed directory is required")
	}
	if edition == "" {
		edition = dbIPEdition
	}
	if edition != dbIPEdition {
		return nil, fmt.Errorf("geoip: unsupported managed edition %q", edition)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("geoip: create managed directory: %w", err)
	}
	if m, err := readManifest(directory); err == nil && manifestMatchesEdition(m, edition) {
		path := filepath.Join(directory, m.Path)
		if checksumErr := verifyFileChecksum(path, m.Checksum); checksumErr == nil {
			if reader, openErr := openMMDB(path); openErr == nil {
				service := New(reader)
				service.version = m.Version
				if info, infoErr := os.Stat(path); infoErr == nil {
					service.updatedAt = info.ModTime()
				}
				return service, nil
			}
		}
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("geoip: scan managed versions: %w", err)
	}
	type candidate struct {
		name string
		mod  time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !isVersionFilename(name, edition) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		candidates = append(candidates, candidate{name: name, mod: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mod.Equal(candidates[j].mod) {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].mod.After(candidates[j].mod)
	})
	for _, item := range candidates {
		checksum := versionChecksum(item.name, edition)
		path := filepath.Join(directory, item.name)
		if verifyFileChecksum(path, checksum) != nil {
			continue
		}
		reader, openErr := openMMDB(path)
		if openErr != nil {
			continue
		}
		m := manifest{Version: checksum, Edition: edition, Checksum: checksum, Path: item.name}
		if err := writeManifest(directory, m); err != nil {
			_ = reader.Close()
			return nil, fmt.Errorf("geoip: repair managed manifest: %w", err)
		}
		service := New(reader)
		service.version = m.Version
		service.updatedAt = item.mod
		return service, nil
	}
	return New(nil), nil
}
