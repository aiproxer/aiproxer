package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// errAccountFileNotObject rejects a valid-JSON document that is not an object
// (for example "null"), which decodes into a nil map that cannot be merged.
var errAccountFileNotObject = errors.New("account file is not a JSON object")

func (s *accountStore) persistQuotaHeaders(acct managedAccount, headers map[string]string) error {
	if len(headers) == 0 {
		return nil
	}
	if err := writeQuotaHeadersToFile(acct.FilePath, headers); err != nil {
		return err
	}
	planType := strings.TrimSpace(headers["x-codex-plan-type"])
	if planType == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.meta {
		if s.meta[i].FilePath == acct.FilePath {
			s.meta[i].PlanType = planType
			return nil
		}
	}
	return nil
}

// writeQuotaHeadersToFile merges quota headers into the managed account file
// through a crash-safe replace: the rewritten credentials are published by a
// rename whose bytes were flushed first and whose directory entry is flushed
// after. The account file holds tokens, so a partial write must never become
// visible.
func writeQuotaHeadersToFile(filePath string, headers map[string]string) error {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read account file %q: %w", filePath, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("decode account file %q: %w", filePath, err)
	}
	if root == nil {
		return fmt.Errorf("decode account file %q: %w", filePath, errAccountFileNotObject)
	}
	qh, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("marshal quota headers for %q: %w", filePath, err)
	}
	root["quota_headers"] = qh
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal account file %q: %w", filePath, err)
	}
	out = append(out, '\n')
	return atomicReplaceFile(filePath, out, defaultAtomicReplaceOps())
}

func codexQuotaHeaders(h map[string][]string) map[string]string {
	out := make(map[string]string)
	for k, vals := range h {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-codex-") || len(vals) == 0 {
			continue
		}
		if v := strings.TrimSpace(vals[0]); v != "" {
			out[lk] = v
		}
	}
	return out
}
