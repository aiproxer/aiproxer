package wrapperinstall

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/matdev83/go-agy-acp-wrapper/releases/latest"
	maxDownloadBytes = 64 << 20
)

var installMu sync.Mutex

// fileSync, dirSync and renameFile are the durability primitives the managed
// install depends on: the verified executable bytes and their final mode are
// flushed before the rename publishes them, and the directory entry that names
// the published binary is flushed after. They are variables so package tests
// can prove that ordering and its failure handling; production code always
// uses the OS calls.
var (
	fileSync   = func(f *os.File) error { return f.Sync() }
	dirSync    = syncDirectory
	renameFile = os.Rename
)

// errPublishUnconfirmed marks an install whose binary reached the cache but
// whose persistence could not be confirmed. It is terminal: the offline cache
// fallback exists for a peer that could not be reached, not for hiding a
// degradation of the file this call just published. A later call re-confirms
// the directory entry instead of trusting the path, so an install that failed
// here is never acknowledged by mere presence.
var errPublishUnconfirmed = errors.New("wrapper install: published binary is not crash-durable")

// defaultInstallTimeout bounds one managed install, including body reads. The
// production call site supplies a background context and Options.Client is a
// test hook, so without this bound a stalled or hostile peer holds connector
// construction open indefinitely. It is a variable so package tests can prove
// the bound without waiting for the production value; production code always
// uses this default.
var defaultInstallTimeout = 5 * time.Minute

// installClient returns the client used when Options.Client is unset. The
// total timeout covers metadata and archive transfers alike.
func installClient() *http.Client { return &http.Client{Timeout: defaultInstallTimeout} }

type release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Options controls managed wrapper installation. URL and Client are test hooks.
type Options struct {
	CacheDir string
	URL      string
	Client   *http.Client
}

func DefaultCacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("AGY_ACP_WRAPPER_CACHE_DIR")); dir != "" {
		return dir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aiproxer", "go-agy-acp-wrapper"), nil
}

func assetSuffix() (string, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("unsupported wrapper OS %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", fmt.Errorf("unsupported wrapper architecture %s", runtime.GOARCH)
	}
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext), nil
}

func Ensure(ctx context.Context, opts Options) (string, error) {
	installMu.Lock()
	defer installMu.Unlock()
	root := strings.TrimSpace(opts.CacheDir)
	if root == "" {
		var err error
		root, err = DefaultCacheDir()
		if err != nil {
			return "", err
		}
	}
	client := opts.Client
	if client == nil {
		client = installClient()
	}
	url := opts.URL
	if url == "" {
		url = latestReleaseURL
	}
	path, err := ensureOnline(ctx, client, root, url)
	if err == nil {
		return path, nil
	}
	if errors.Is(err, errPublishUnconfirmed) {
		return "", err
	}
	if cached := newestCached(root); cached != "" {
		// The fallback still has to prove the cached name is durable: a file
		// left behind by an install whose directory flush failed must not be
		// handed to the connector just because the peer is unreachable.
		if err := confirmPublished(cached); err != nil {
			return "", err
		}
		return cached, nil
	}
	return "", err
}

func ensureOnline(ctx context.Context, client *http.Client, root, url string) (string, error) {
	var rel release
	if err := getJSON(ctx, client, url, &rel); err != nil {
		return "", err
	}
	if rel.Draft || rel.Prerelease || !validTag(rel.TagName) {
		return "", fmt.Errorf("latest wrapper release is not stable: %q", rel.TagName)
	}
	suffix, err := assetSuffix()
	if err != nil {
		return "", err
	}
	var archiveAsset, checksumAsset asset
	for _, item := range rel.Assets {
		if strings.HasSuffix(item.Name, suffix) {
			if archiveAsset.Name != "" {
				return "", fmt.Errorf("multiple wrapper assets match %s", suffix)
			}
			archiveAsset = item
		}
		if item.Name == "checksums.txt" {
			checksumAsset = item
		}
	}
	if archiveAsset.Name == "" || checksumAsset.Name == "" {
		return "", fmt.Errorf("wrapper release assets incomplete for %s", suffix)
	}
	name := "go-agy-acp-wrapper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(root, rel.TagName, name)
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		// Presence is not proof of durability: this file may be what a previous
		// install published before its directory flush failed. Repeat the
		// confirmation instead of acknowledging the path as-is.
		if err := confirmPublished(target); err != nil {
			return "", err
		}
		return target, nil
	}
	checksums, err := download(ctx, client, checksumAsset.URL)
	if err != nil {
		return "", err
	}
	archive, err := download(ctx, client, archiveAsset.URL)
	if err != nil {
		return "", err
	}
	expected, err := checksumFor(string(checksums), archiveAsset.Name)
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256(archive)
	if hex.EncodeToString(actual[:]) != expected {
		return "", fmt.Errorf("wrapper checksum mismatch for %s", archiveAsset.Name)
	}
	parent := filepath.Dir(target)
	if err := mkdirAllDurable(root); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(root, ".installing-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	extracted := filepath.Join(temp, name)
	if strings.HasSuffix(archiveAsset.Name, ".zip") {
		err = extractZip(archive, name, extracted)
	} else {
		err = extractTarGz(archive, name, extracted)
	}
	if err != nil {
		return "", err
	}
	if err := mkdirAllDurable(parent); err != nil {
		return "", err
	}
	if err := publishVerifiedBinary(extracted, target); err != nil {
		return "", err
	}
	return target, nil
}

// publishVerifiedBinary renames the already-flushed candidate over the final
// name and confirms that the published name is durable.
//
// A rename that fails because a concurrent install published the same release
// is tolerated - the peer's complete file is the same verified bytes - but its
// directory entry is still confirmed, so no caller launches a binary whose
// name a crash can take away. The candidate bytes reach this point only after
// [writeFile] flushed them, which is what makes an interrupted install unable
// to leave a truncated executable under a name later runs trust.
func publishVerifiedBinary(extracted, target string) error {
	if err := renameFile(extracted, target); err != nil {
		if _, statErr := os.Stat(target); statErr != nil {
			return fmt.Errorf("wrapper install: publish binary: %w", err)
		}
	}
	return confirmPublished(target)
}

// confirmPublished flushes the directory entry that names target and the entry
// that names that directory. It is the only acknowledgement an install may rest
// on: a rename alone can be lost by a crash, and a path that merely exists may
// be the residue of an install whose confirmation already failed. Every path
// that returns a binary - a fresh publish, an online cache hit and the offline
// fallback - runs it.
//
// The second flush covers a release directory left behind by an attempt that
// crashed (or failed) between creating it and confirming it: the directory then
// looks pre-existing, so re-flushing its parent is the only way to make its
// entry durable. Ancestors above that are assumed durable, because this process
// did not create them.
func confirmPublished(target string) error {
	dir := filepath.Dir(target)
	for _, flush := range []string{dir, filepath.Dir(dir)} {
		if err := dirSync(flush); err != nil {
			return fmt.Errorf("%w: flush directory entry for %s: %w", errPublishUnconfirmed, target, err)
		}
	}
	return nil
}

// mkdirAllDurable creates path and every missing ancestor, then flushes each
// new directory and the parent that now names it.
//
// Flushing the directory that holds the binary confirms only the binary's entry
// inside it; a crash can still lose the version directory itself, and with it
// the whole install. The same holds for every ancestor this call created, so
// the guarantee has to extend up the hierarchy the install introduced.
//
// A failure here is an ordinary error, not errPublishUnconfirmed: nothing has
// been published yet, so the offline cache may still serve an older release
// whose own entry can be confirmed.
func mkdirAllDurable(path string) error {
	missing, err := missingDirs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	// missing is deepest first; walk back up so each parent is flushed after
	// its own entry has been made durable.
	for i := len(missing) - 1; i >= 0; i-- {
		dir := missing[i]
		if err := dirSync(dir); err != nil {
			return fmt.Errorf("wrapper install: flush new directory %s: %w", dir, err)
		}
		if err := dirSync(filepath.Dir(dir)); err != nil {
			return fmt.Errorf("wrapper install: flush parent of new directory %s: %w", dir, err)
		}
	}
	return nil
}

// missingDirs returns the directories below the first existing ancestor that
// path would create, deepest first.
func missingDirs(path string) ([]string, error) {
	var missing []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("wrapper install: %s is not a directory", dir)
			}
			return missing, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, dir)
		if parent := filepath.Dir(dir); parent == dir {
			return nil, err
		}
	}
}

func validTag(tag string) bool {
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 || !strings.HasPrefix(tag, "v") {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	body, err := download(ctx, client, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wrapper download %s: HTTP %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDownloadBytes {
		return nil, errors.New("wrapper download exceeds size limit")
	}
	return body, nil
}

func checksumFor(data, name string) (string, error) {
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err == nil && len(fields[0]) == 64 {
				return strings.ToLower(fields[0]), nil
			}
		}
	}
	return "", fmt.Errorf("checksum missing for %s", name)
}

func extractZip(data []byte, name, target string) error {
	reader, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != name {
			continue
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		return writeFile(target, source)
	}
	return errors.New("wrapper executable missing from archive")
}

func extractTarGz(data []byte, name, target string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) == name && header.Typeflag == tar.TypeReg {
			return writeFile(target, tarReader)
		}
	}
	return errors.New("wrapper executable missing from archive")
}

func writeFile(path string, source io.Reader) error {
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	// The process umask may have removed the execute bits from the creation
	// mode. Setting the final mode on this descriptor keeps the bytes and the
	// metadata that make the file launchable in one flush.
	if err := target.Chmod(0o755); err != nil {
		_ = target.Close()
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	// Flush the verified bytes before the caller's rename publishes them: a
	// crash after renaming unflushed bytes leaves a truncated executable under
	// the cache's final name, and every later run trusts that name instead of
	// re-verifying the release.
	if err := fileSync(target); err != nil {
		_ = target.Close()
		return fmt.Errorf("wrapper install: flush binary: %w", err)
	}
	return target.Close()
}

func newestCached(root string) string {
	name := "go-agy-acp-wrapper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	matches, _ := filepath.Glob(filepath.Join(root, "v*", name))
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}
