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
	if cached := newestCached(root); cached != "" {
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
	if err := os.MkdirAll(root, 0o755); err != nil {
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
	if err := os.Chmod(extracted, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(extracted, target); err != nil {
		if _, statErr := os.Stat(target); statErr != nil {
			return "", err
		}
	}
	return target, nil
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
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
