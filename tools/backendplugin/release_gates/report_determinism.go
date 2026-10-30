package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	stableOK     = "ok"
	stableFailed = "failed"
)

var (
	reISOTime   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	reDuration  = regexp.MustCompile(`\b\d+\.\d+s\b`)
	reSHA64     = regexp.MustCompile(`(?i)\b[a-f0-9]{64}\b`)
	reWinAbs    = regexp.MustCompile(`(?i)\b[a-z]:[\\/]`)
	rePosixAbs  = regexp.MustCompile(`(^|[\s"'])/(?:tmp|var|Users|home|private|opt|usr)/`)
	reTempToken = regexp.MustCompile(`(?i)aip-(?:isolated-root|installed-smoke|release-gates)-[A-Za-z0-9._-]+`)
	reAppData   = regexp.MustCompile(`(?i)AppData|[\\/]Temp[\\/]`)
	reSecretish = regexp.MustCompile(`(?i)\b(sk-[A-Za-z0-9_-]{8,}|api[_-]?key\s*=\s*\S+|token\s*=\s*\S+|password\s*=\s*\S+|bearer\s+\S+)`)
	reCtrl      = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)
)

// normalizeSlash converts path separators to forward slashes regardless of host OS.
// filepath.ToSlash only rewrites the native separator, so Windows-style "\" must be
// replaced explicitly when sanitizing reports on Linux/macOS.
func normalizeSlash(s string) string {
	return strings.ReplaceAll(filepath.ToSlash(s), `\`, `/`)
}

func normalizeCommand(cmd string) string {
	return normalizeSlash(strings.TrimSpace(cmd))
}

func stableSuccessDetail(existing string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return stableOK
	}
	// Preserve short semantic counters from builtins (no paths/timings/hashes).
	if ensureDetailStable(existing) == nil {
		return existing
	}
	return stableOK
}

func ensureDetailStable(s string) error {
	if reDuration.MatchString(s) || reISOTime.MatchString(s) || reSHA64.MatchString(s) {
		return fmt.Errorf("non-stable token")
	}
	if reWinAbs.MatchString(s) || rePosixAbs.MatchString(s) || reTempToken.MatchString(s) || reAppData.MatchString(s) {
		return fmt.Errorf("path-like token")
	}
	if strings.Contains(s, `\`) {
		return fmt.Errorf("backslash")
	}
	return nil
}

func sanitizeFailureDetail(root, raw string) string {
	s := string(reCtrl.ReplaceAll([]byte(raw), nil))
	if root != "" {
		s = strings.ReplaceAll(s, root, "<root>")
		s = strings.ReplaceAll(s, filepath.ToSlash(root), "<root>")
		// Windows path variants
		s = strings.ReplaceAll(s, strings.ReplaceAll(root, `/`, `\`), "<root>")
		s = strings.ReplaceAll(s, strings.ReplaceAll(filepath.ToSlash(root), `/`, `\`), "<root>")
	}
	s = reTempToken.ReplaceAllString(s, "<temp>")
	s = reAppData.ReplaceAllString(s, "<temp>/")
	s = reWinAbs.ReplaceAllString(s, "<abs>/")
	s = rePosixAbs.ReplaceAllString(s, "${1}<abs>/")
	s = reDuration.ReplaceAllString(s, "<dur>")
	s = reISOTime.ReplaceAllString(s, "<time>")
	s = reSHA64.ReplaceAllString(s, "<hash>")
	s = reSecretish.ReplaceAllString(s, "<redacted>")
	s = strings.Join(strings.Fields(s), " ")
	s = truncate(s, 240)
	if s == "" {
		return stableFailed
	}
	return stableFailed + ":" + s
}

func sanitizeReport(rep *report, root string) {
	if rep == nil {
		return
	}
	for i := range rep.GateResults {
		g := &rep.GateResults[i]
		g.Command = normalizeCommand(g.Command)
		switch {
		case g.OK && (g.Status == "local_executable" || g.Status == "external_blocker"):
			g.Detail = stableSuccessDetail(g.Detail)
		case !g.OK || g.Status == "failed":
			g.Detail = sanitizeFailureDetail(root, g.Detail)
		default:
			if g.Detail != "" {
				if err := ensureDetailStable(g.Detail); err != nil {
					g.Detail = stableSuccessDetail(g.Detail)
				}
			}
		}
	}
	for i := range rep.ModuleResults {
		m := &rep.ModuleResults[i]
		m.Module = normalizeSlash(m.Module)
		if m.Error != "" {
			m.Error = sanitizeFailureDetail(root, m.Error)
		}
		for j := range m.Steps {
			m.Steps[j] = normalizeSlash(m.Steps[j])
		}
	}
	for i := range rep.Modules {
		rep.Modules[i] = normalizeSlash(rep.Modules[i])
	}
	for i := range rep.Traceability {
		row := &rep.Traceability[i]
		// Trace notes must stay catalog-stable; drop leaked stdout/detail copies.
		if row.Notes != "" && ensureDetailStable(row.Notes) != nil {
			row.Notes = ""
		}
	}
}

func ensureDeterministicReport(reportJSON []byte, root string) error {
	s := string(reportJSON)
	if strings.Contains(s, `"timestamp"`) || strings.Contains(s, `"native_host"`) {
		return fmt.Errorf("report must not include timestamp or native_host fields")
	}
	if root != "" {
		if bytesContainsFold(reportJSON, []byte(root)) {
			return fmt.Errorf("report must not embed absolute root path")
		}
		if bytesContainsFold(reportJSON, []byte(filepath.ToSlash(root))) {
			return fmt.Errorf("report must not embed absolute root path")
		}
	}
	if reISOTime.MatchString(s) {
		return fmt.Errorf("report must not include ISO timestamps")
	}
	if reDuration.MatchString(s) {
		return fmt.Errorf("report must not include durations")
	}
	if reSHA64.MatchString(s) {
		return fmt.Errorf("report must not include sha-like hashes")
	}
	if reWinAbs.MatchString(s) {
		return fmt.Errorf("report must not include absolute Windows paths")
	}
	if rePosixAbs.MatchString(s) {
		return fmt.Errorf("report must not include absolute POSIX temp/home paths")
	}
	if reTempToken.MatchString(s) {
		return fmt.Errorf("report must not include random temp directory tokens")
	}
	if reAppData.MatchString(s) {
		return fmt.Errorf("report must not include host temp paths")
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 32 || r == 127 {
			return fmt.Errorf("report must not include control characters")
		}
	}
	return nil
}
