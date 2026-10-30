package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func loadManagedAccounts(dir string, filter []string) ([]managedAccount, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: read managed oauth storage: %w", ID, err)
	}
	allowAll := accountFilterAllowsAll(filter)
	allowed := accountFilterSet(filter)
	var accounts []managedAccount
	var skipped int
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".json") {
			continue
		}
		if ent.Type()&os.ModeSymlink != 0 {
			// security: skip symlinked account files so a planted symlink cannot
			// read targets outside the managed-oauth storage directory.
			skipped++
			continue
		}
		path := filepath.Join(dir, ent.Name())
		acct, ok, err := parseManagedAccountFile(path)
		if err != nil {
			return nil, err
		}
		if !ok {
			skipped++
			continue
		}
		if !allowAll && !allowed[acct.ID] && !allowed[strings.TrimSuffix(ent.Name(), ".json")] {
			continue
		}
		accounts = append(accounts, acct)
	}
	if len(accounts) == 0 {
		if skipped > 0 {
			return nil, fmt.Errorf("%s: no usable managed oauth accounts in %q", ID, dir)
		}
		return nil, fmt.Errorf("%s: no managed oauth account files in %q", ID, dir)
	}
	slices.SortFunc(accounts, func(a, b managedAccount) int {
		return strings.Compare(a.FilePath, b.FilePath)
	})
	return accounts, nil
}

func parseManagedAccountFile(path string) (managedAccount, bool, error) {
	if err := checkTokenFilePermissions(path); err != nil {
		return managedAccount{}, false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return managedAccount{}, false, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return managedAccount{}, false, fmt.Errorf("%s: %s: invalid JSON: %w", ID, path, err)
	}
	acct := managedAccount{
		FilePath:     path,
		ID:           firstNonEmpty(jsonRawString(root, "account_id", "accountID", "id")),
		AccessToken:  jsonRawString(root, "access_token", "accessToken"),
		RefreshToken: jsonRawString(root, "refresh_token", "refreshToken"),
	}
	if tokensRaw, ok := root["tokens"]; ok {
		var tokens map[string]json.RawMessage
		if err := json.Unmarshal(tokensRaw, &tokens); err == nil {
			acct.AccessToken = firstNonEmpty(acct.AccessToken, jsonRawString(tokens, "access_token", "accessToken"))
			acct.RefreshToken = firstNonEmpty(acct.RefreshToken, jsonRawString(tokens, "refresh_token", "refreshToken"))
			acct.ID = firstNonEmpty(acct.ID, jsonRawString(tokens, "account_id", "accountID", "id"))
		}
	}
	if acct.ID == "" || acct.AccessToken == "" {
		return managedAccount{}, false, nil
	}
	if qhRaw, ok := root["quota_headers"]; ok {
		var qh map[string]string
		if err := json.Unmarshal(qhRaw, &qh); err == nil {
			acct.PlanType = strings.TrimSpace(qh["x-codex-plan-type"])
		}
	}
	return acct, true, nil
}

func accountFilterAllowsAll(filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if strings.EqualFold(strings.TrimSpace(f), "all") {
			return true
		}
	}
	return false
}

func accountFilterSet(filter []string) map[string]bool {
	out := make(map[string]bool, len(filter))
	for _, f := range filter {
		f = strings.TrimSpace(f)
		if f == "" || strings.EqualFold(f, "all") {
			continue
		}
		out[f] = true
	}
	return out
}

// checkTokenFilePermissions rejects managed-OAuth account files readable or
// writable by group or other on Unix, mirroring the Codex CLI auth.json
// guard. On Windows (ACL-based permissions, no meaningful Unix mode bits) it
// is a no-op. This guard protects the explicitly classified managed-OAuth
// storage directory only; the connector has no other credential-file reads.
func checkTokenFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil // let the caller's ReadFile produce the canonical not-exist error
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: token file %q is not a regular file", ID, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: token file %q is group/other accessible (mode %o); expected 0600", ID, path, info.Mode().Perm())
	}
	return nil
}

func jsonRawString(m map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
