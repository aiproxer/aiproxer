package product

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strings"
)

// PolicySnapshot is a consent decision, not a security boundary. Assembly must
// include Fingerprint in the existing pool's processConfig and re-check before
// each turn; ClaimForTurn then replaces a changed runtime under its sole owner.
type PolicySnapshot struct {
	Fingerprint string
	Sources     []string
}

// PolicyGate observes policy only. Files is an optional filesystem rooted at /
// for hermetic tests. Native observation currently supports Linux (including
// WSL's user-writable /etc tier); other platforms fail closed pending native
// registry/MDM certification. No source is hidden or modified under honor.
type PolicyGate struct{ Files fs.FS }

func policyMode(mode string) (string, error) {
	if mode == "" {
		mode = "refuse"
	}
	if mode != "refuse" && mode != "honor" {
		return "", errors.New("claudeagentacp: admin_policy must be refuse or honor")
	}
	return mode, nil
}

// serverPolicyConsent is the pinned profile's pre-exec consent decision. Its
// auth status contract cannot exclude server authority for any supported source:
// API keys and injected tokens are eligible, and reported Pro/Max labels need
// not reflect the runtime's eligibility metadata. Local cache absence is not a
// server-policy snapshot. Honor permits that authority, not an identity bypass.
func serverPolicyConsent(mode string) error {
	mode, err := policyMode(mode)
	if err != nil {
		return err
	}
	if mode != "honor" {
		return errors.New("claudeagentacp: potential organisation/server-delivered policy; select admin_policy: honor to honor its authority")
	}
	return nil
}

func (g PolicyGate) Check(ctx context.Context, config, mode string) (PolicySnapshot, error) {
	var result PolicySnapshot
	mode, err := policyMode(mode)
	if err != nil {
		return result, err
	}
	files := g.Files
	if files == nil {
		if runtime.GOOS != "linux" {
			return result, errors.New("claudeagentacp: native administrator-policy observation is not certified on this platform")
		}
		files = os.DirFS("/")
	}
	if !path.IsAbs(config) || strings.ContainsRune(config, 0) {
		return result, errors.New("claudeagentacp: absolute effective configuration directory required")
	}
	locations := []string{"etc/claude-code/managed-settings.json", "etc/claude-code/managed-settings.d", "etc/claude-code/managed-mcp.json", path.Join(strings.TrimPrefix(path.Clean(config), "/"), "remote-settings.json")}
	h := sha256.New()
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		label := "/" + location
		if location == locations[3] {
			label = "effective configuration remote-settings.json"
		}
		_, err := fs.Stat(files, location)
		if errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(h, "%s:absent\n", location) // hash.Hash writes never fail
			continue
		}
		if err != nil {
			return result, fmt.Errorf("claudeagentacp: cannot inspect policy source %s", label)
		}
		result.Sources = append(result.Sources, label)
		if mode == "refuse" {
			return result, fmt.Errorf("claudeagentacp: administrator policy at %s; select admin_policy: honor to honor its authority", label)
		}
		count := 0
		err = fs.WalkDir(files, location, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			count++
			if count > 256 {
				return errors.New("policy too large")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "%s:%s:%d:%d\n", name, info.Mode(), info.Size(), info.ModTime().UnixNano())
			if entry.IsDir() {
				return nil
			}
			file, err := files.Open(name)
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || len(data) > 1<<20 {
				return errors.New("policy unreadable")
			}
			_, _ = fmt.Fprintf(h, "%x\n", sha256.Sum256(data))
			return nil
		})
		if err != nil {
			return result, fmt.Errorf("claudeagentacp: cannot fingerprint policy source %s", label)
		}
	}
	result.Fingerprint = fmt.Sprintf("%x", h.Sum(nil))
	return result, nil
}
