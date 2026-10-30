package geoip

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

type manifest struct {
	Version  string `json:"version"`
	Edition  string `json:"edition"`
	Checksum string `json:"checksum"`
	Path     string `json:"path"`
}

func readManifest(directory string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(directory, "active.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Edition == "" || m.Checksum == "" || m.Path == "" || filepath.Base(m.Path) != m.Path || m.Version != m.Checksum {
		return manifest{}, fmt.Errorf("invalid GeoIP manifest")
	}
	if !isHexChecksum(m.Checksum) || !isVersionFilename(m.Path, m.Edition) || versionChecksum(m.Path, m.Edition) != m.Checksum {
		return manifest{}, fmt.Errorf("invalid GeoIP manifest version")
	}
	return m, nil
}

func manifestMatchesEdition(m manifest, edition string) bool {
	return m.Edition == edition && m.Version == m.Checksum && isVersionFilename(m.Path, edition) && versionChecksum(m.Path, edition) == m.Checksum
}

func isVersionFilename(name, edition string) bool {
	prefix := edition + "."
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".mmdb") && isHexChecksum(versionChecksum(name, edition))
}

func versionChecksum(name, edition string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, edition+"."), ".mmdb")
}

func isHexChecksum(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, coregeoip.MaxDatabaseDownloadBytes+1))
	if err != nil {
		return "", err
	}
	if written > coregeoip.MaxDatabaseDownloadBytes {
		return "", fmt.Errorf("database exceeds %d bytes", coregeoip.MaxDatabaseDownloadBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyFileChecksum(path, expected string) error {
	if !isHexChecksum(expected) {
		return fmt.Errorf("invalid checksum")
	}
	actual, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch")
	}
	return nil
}

func writeManifest(directory string, m manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("geoip: encode manifest: %w", err)
	}
	tmp, err := os.CreateTemp(directory, ".active-*")
	if err != nil {
		return fmt.Errorf("geoip: create manifest: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("geoip: write manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("geoip: sync manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("geoip: close manifest: %w", err)
	}
	if err := replaceFile(tmpName, filepath.Join(directory, "active.json")); err != nil {
		return fmt.Errorf("geoip: replace manifest atomically: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("geoip: sync manifest directory: %w", err)
	}
	return nil
}
