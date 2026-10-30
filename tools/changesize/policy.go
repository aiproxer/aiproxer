package main

import (
	"bytes"
	"strings"
)

const (
	DefaultLimit      = 100
	OverrideEnv       = "AIP_ALLOW_LARGE_CHANGE"
	OverrideGitConfig = "aip.allowLargeChange"
)

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func uniqueGoPathCount(names []string) int {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasSuffix(name, ".go") {
			continue
		}
		seen[name] = struct{}{}
	}
	return len(seen)
}

func allowed(count, limit int, override bool) bool {
	return count <= limit || override
}

func splitGitNames(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		names = append(names, string(part))
	}
	return names
}
