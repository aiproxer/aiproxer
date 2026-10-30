package geoip

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

// update downloads, validates, durably selects, and publishes one DB-IP Lite
// version. The response body is always closed exactly once.
func (u *ManagedUpdater) update(ctx context.Context) (UpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, coregeoip.DatabaseOperationTimeout)
	defer cancel()
	outcome := "failed"
	defer func() {
		if u.config.Observe != nil {
			u.config.Observe(outcome)
		}
		if u.config.Logger != nil {
			u.config.Logger.Info("geoip database update", "result", outcome)
		}
	}()
	if err := os.MkdirAll(u.config.Directory, 0o755); err != nil {
		return "", fmt.Errorf("geoip: create managed directory: %w", err)
	}
	now := u.config.Now().UTC()
	url := fmt.Sprintf("https://download.db-ip.com/free/%s-%04d-%02d.mmdb.gz", u.config.Edition, now.Year(), now.Month())
	req, err := http.NewRequestWithContext(operationCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("geoip: build managed request: %w", err)
	}
	// DB-IP publishes a monthly immutable URL. When a valid active version has
	// a filesystem timestamp, use it as an upstream change hint so unchanged
	// responses can be completed without a download; checksum comparison below
	// remains the local correctness/change-detection fallback.
	if active, manifestErr := readManifest(u.config.Directory); manifestErr == nil && manifestMatchesEdition(active, u.config.Edition) {
		if info, statErr := os.Stat(filepath.Join(u.config.Directory, active.Path)); statErr == nil {
			req.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
		}
	}
	resp, err := u.config.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("geoip: managed download: %w", err)
	}
	if resp.Body == nil {
		return "", fmt.Errorf("geoip: managed response has nil body")
	}
	bodyClosed := false
	defer func() {
		if !bodyClosed {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode == http.StatusNotModified {
		outcome = string(UpdateUnchanged)
		return UpdateUnchanged, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("geoip: managed download returned HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(u.config.Directory, ".download-*")
	if err != nil {
		return "", fmt.Errorf("geoip: create candidate: %w", err)
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: decode managed gzip: %w", err)
	}
	written, copyErr := io.Copy(tmp, io.LimitReader(gz, coregeoip.MaxDatabaseDownloadBytes+1))
	gzErr := gz.Close()
	if copyErr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: write managed candidate: %w", copyErr)
	}
	if gzErr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: close managed gzip: %w", gzErr)
	}
	if err := resp.Body.Close(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: close managed response: %w", err)
	}
	bodyClosed = true
	if written > coregeoip.MaxDatabaseDownloadBytes {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: managed database exceeds %d bytes", coregeoip.MaxDatabaseDownloadBytes)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("geoip: sync managed candidate: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("geoip: close managed candidate: %w", err)
	}

	checksum, err := hashFile(tmpName)
	if err != nil {
		return "", fmt.Errorf("geoip: hash managed candidate: %w", err)
	}
	finalName := filepath.Join(u.config.Directory, fmt.Sprintf("%s.%s.mmdb", u.config.Edition, checksum))
	if old, err := readManifest(u.config.Directory); err == nil && old.Checksum == checksum && old.Edition == u.config.Edition &&
		verifyFileChecksum(filepath.Join(u.config.Directory, old.Path), old.Checksum) == nil {
		outcome = string(UpdateUnchanged)
		return UpdateUnchanged, nil
	}
	// Verify the closed temporary file before it becomes a retained version.
	// On Windows this is also what allows the verification handle to be closed
	// before the atomic rename of the candidate.
	verified, err := openMMDB(tmpName)
	if err != nil {
		return "", fmt.Errorf("geoip: verify managed candidate: %w", err)
	}
	if err := verified.Close(); err != nil {
		return "", fmt.Errorf("geoip: close verified managed candidate: %w", err)
	}
	if err := os.Rename(tmpName, finalName); err != nil {
		return "", fmt.Errorf("geoip: publish managed version: %w", err)
	}
	removeTemp = false
	if err := syncDirectory(u.config.Directory); err != nil {
		return "", fmt.Errorf("geoip: sync managed version directory: %w", err)
	}
	candidate, err := openMMDB(finalName)
	if err != nil {
		return "", fmt.Errorf("geoip: reopen managed candidate: %w", err)
	}
	m := manifest{Version: checksum, Edition: u.config.Edition, Checksum: checksum, Path: filepath.Base(finalName)}
	if err := u.service.PublishVersionWithCommit(operationCtx, candidate, checksum, func() error {
		return writeManifest(u.config.Directory, m)
	}); err != nil {
		return "", fmt.Errorf("geoip: activate managed candidate: %w", err)
	}
	// Reader publication has closed the retired reader before cleanup. Keep the
	// active version plus one prior verified LKG for deterministic restart
	// recovery; cleanup failures do not invalidate the active reader.
	_ = cleanupVersions(u.config.Directory, u.config.Edition, filepath.Base(finalName))
	outcome = string(UpdateUpdated)
	return UpdateUpdated, nil
}

func cleanupVersions(directory, edition, active string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	type versionFile struct {
		name string
		mod  time.Time
	}
	versions := make([]versionFile, 0, len(entries))
	prefix := edition + "."
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == active || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".mmdb") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		versions = append(versions, versionFile{name: name, mod: info.ModTime()})
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].mod.Equal(versions[j].mod) {
			return versions[i].name < versions[j].name
		}
		return versions[i].mod.After(versions[j].mod)
	})
	for i := 1; i < len(versions); i++ {
		if err := os.Remove(filepath.Join(directory, versions[i].name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Stale temporary candidates are never authoritative and may otherwise
	// accumulate after a crash or a bounded download failure.
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasPrefix(entry.Name(), ".download-") && !strings.HasPrefix(entry.Name(), ".active-")) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
