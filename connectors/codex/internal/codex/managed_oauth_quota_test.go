package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const managedAccountBody = `{"tokens":{"access_token":"access-1","refresh_token":"refresh-1","account_id":"acct-1"},"last_refresh":"2026-01-01T00:00:00Z"}`

// seedManagedAccount writes a minimal managed-account file holding credentials.
func seedManagedAccount(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte(managedAccountBody), 0o600); err != nil {
		t.Fatalf("seed managed account: %v", err)
	}
	return path
}

func readJSONMap(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return root
}

func tempLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var leftovers []string
	for _, ent := range entries {
		if strings.HasSuffix(ent.Name(), ".tmp") {
			leftovers = append(leftovers, ent.Name())
		}
	}
	return leftovers
}

// TestWriteQuotaHeadersToFilePreservesCredentialsAndMergesHeaders proves the
// rewrite keeps the stored tokens byte-identical while adding quota headers.
func TestWriteQuotaHeadersToFilePreservesCredentialsAndMergesHeaders(t *testing.T) {
	t.Parallel()
	path := seedManagedAccount(t)
	if err := writeQuotaHeadersToFile(path, map[string]string{"x-codex-plan-type": "pro"}); err != nil {
		t.Fatalf("write quota headers: %v", err)
	}
	root := readJSONMap(t, path)
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	}
	if err := json.Unmarshal(root["tokens"], &tokens); err != nil {
		t.Fatalf("decode tokens: %v", err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" || tokens.AccountID != "acct-1" {
		t.Fatalf("credentials changed: %+v", tokens)
	}
	var headers map[string]string
	if err := json.Unmarshal(root["quota_headers"], &headers); err != nil {
		t.Fatalf("decode quota headers: %v", err)
	}
	if headers["x-codex-plan-type"] != "pro" {
		t.Fatalf("quota headers = %v, want plan type pro", headers)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten account: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("rewritten account file must end with a newline")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rewritten account: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("rewritten account mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestWriteQuotaHeadersToFilePublishesByRename proves the credential file is
// replaced instead of truncated in place: an in-place write can expose a
// partial credential record after a crash.
func TestWriteQuotaHeadersToFilePublishesByRename(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("file identity comparison is not meaningful on Windows")
	}
	path := seedManagedAccount(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if err := writeQuotaHeadersToFile(path, map[string]string{"x-codex-plan-type": "pro"}); err != nil {
		t.Fatalf("write quota headers: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("quota rewrite truncated the credential file in place; a crash mid-write could publish partial credentials")
	}
}

// TestWriteQuotaHeadersToFileRejectsInvalidAccountFile proves a corrupt source
// is rejected before anything is published.
func TestWriteQuotaHeadersToFileRejectsInvalidAccountFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed invalid account: %v", err)
	}
	err := writeQuotaHeadersToFile(path, map[string]string{"x-codex-plan-type": "pro"})
	if err == nil || !strings.Contains(err.Error(), "decode account file") {
		t.Fatalf("invalid account error = %v, want decode account file", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "{not json" {
		t.Fatalf("invalid account file changed: %q, %v", raw, readErr)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestWriteQuotaHeadersToFileRejectsNullAccountFile proves a valid-JSON null
// document is a corrupt source, not a nil-map panic: the merge fails closed and
// leaves the credential file untouched.
func TestWriteQuotaHeadersToFileRejectsNullAccountFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatalf("seed null account: %v", err)
	}
	err := writeQuotaHeadersToFile(path, map[string]string{"x-codex-plan-type": "pro"})
	if err == nil || !strings.Contains(err.Error(), "decode account file") {
		t.Fatalf("null account error = %v, want decode account file", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "null" {
		t.Fatalf("null account file changed: %q, %v", raw, readErr)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func recordingOps(t *testing.T, calls *[]string) atomicReplaceOps {
	t.Helper()
	return atomicReplaceOps{
		syncFile: func(f *os.File) error { *calls = append(*calls, "file"); return f.Sync() },
		syncDir:  func(string) error { *calls = append(*calls, "dir"); return nil },
		rename: func(oldpath, newpath string) error {
			*calls = append(*calls, "rename")
			return os.Rename(oldpath, newpath)
		},
	}
}

// TestAtomicReplaceFileSyncsDataBeforeRenameAndDirectoryAfter pins the crash
// order: bytes hit stable storage before the rename publishes them, and the
// directory entry is flushed after.
func TestAtomicReplaceFileSyncsDataBeforeRenameAndDirectoryAfter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var calls []string
	if err := atomicReplaceFile(path, []byte("new"), recordingOps(t, &calls)); err != nil {
		t.Fatalf("atomic replace: %v", err)
	}
	if got := strings.Join(calls, ","); got != "file,rename,dir" {
		t.Fatalf("durability calls %q, want %q", got, "file,rename,dir")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "new" {
		t.Fatalf("published content = %q, %v", raw, err)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestAtomicReplaceFileFailsClosedWhenFileSyncFails proves a failed data flush
// is reported and never published, so a torn write is not mistaken for a
// successful credential update.
func TestAtomicReplaceFileFailsClosedWhenFileSyncFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	renamed := false
	ops := atomicReplaceOps{
		syncFile: func(*os.File) error { return errors.New("simulated data flush failure") },
		syncDir:  func(string) error { return nil },
		rename: func(oldpath, newpath string) error {
			renamed = true
			return os.Rename(oldpath, newpath)
		},
	}
	err := atomicReplaceFile(path, []byte("new"), ops)
	if err == nil || !strings.Contains(err.Error(), "sync temp file") {
		t.Fatalf("sync failure error = %v, want sync temp file", err)
	}
	if renamed {
		t.Fatal("sync failure still published the file")
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "old" {
		t.Fatalf("original file changed after failed sync: %q, %v", raw, readErr)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestAtomicReplaceFileFailsClosedWhenRenameFails proves a failed publish
// leaves the previous credentials intact instead of truncating them in place.
func TestAtomicReplaceFileFailsClosedWhenRenameFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dirSynced := false
	ops := atomicReplaceOps{
		syncFile: func(f *os.File) error { return f.Sync() },
		syncDir:  func(string) error { dirSynced = true; return nil },
		rename:   func(string, string) error { return errors.New("simulated rename failure") },
	}
	err := atomicReplaceFile(path, []byte("new"), ops)
	if err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("rename failure error = %v, want replace", err)
	}
	if dirSynced {
		t.Fatal("directory sync ran after a failed publish")
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "old" {
		t.Fatalf("original file changed after failed rename: %q, %v", raw, readErr)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestAtomicReplaceFileReportsDirectorySyncFailureAfterPublish proves the
// directory flush runs after the rename and its failure reaches the caller
// instead of being swallowed.
func TestAtomicReplaceFileReportsDirectorySyncFailureAfterPublish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ops := atomicReplaceOps{
		syncFile: func(f *os.File) error { return f.Sync() },
		syncDir:  func(string) error { return errors.New("simulated directory flush failure") },
		rename:   os.Rename,
	}
	err := atomicReplaceFile(path, []byte("new"), ops)
	if err == nil || !strings.Contains(err.Error(), "sync directory") {
		t.Fatalf("directory sync failure error = %v, want sync directory", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "new" {
		t.Fatalf("file not published before directory flush: %q, %v", raw, readErr)
	}
}
