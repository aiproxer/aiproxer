package sessionwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// WorkspaceExtensionKey is the single canonical extension carrying the
// explicit HTTP workspace hint. Backend resolvers already recognize it, so
// no new canonical field or ABI is introduced.
const WorkspaceExtensionKey = "workspace_path"

// maxWorkspaceHintBytes bounds the raw selected header value (Requirements 1.3).
const maxWorkspaceHintBytes = 4096

// existingWorkspaceExtensionKeys are the neutral canonical workspace hint
// keys recognized downstream. When any of them carries an effective
// (non-blank JSON string) hint, the HTTP carrier must not overwrite it.
// Connector-specific key names are deliberately not referenced here: they
// already take precedence downstream, so adding workspace_path alongside
// them cannot change resolution.
var existingWorkspaceExtensionKeys = []string{"project_dir", "workspace_path", "cwd", "project"}

// ErrInvalidWorkspaceHint is returned for a present-but-malformed workspace
// header value. It is static: it never echoes the supplied value.
var ErrInvalidWorkspaceHint = errors.New("sessionwire: invalid workspace hint")

// ApplyWorkspaceHint projects the validated workspace header value to
// Call.Extensions["workspace_path"] through one shared parser. The hint is
// consumed where a canonical Call is materialized (ordinary decode and
// bounded-path canonical fallback); wire-executed turns carry no extensions,
// so for non-canonical consumers the hint is inert rather than transported.
//
//   - Header selection follows the existing first-nonempty alias order
//     ([aipsdk.FirstHeader] convention); absent or whitespace-only input is
//     inert and returns without allocation or mutation.
//   - Raw selected input is validated before trimming: at most 4096 bytes,
//     valid UTF-8, no control characters. The trimmed value must use
//     absolute-path syntax: POSIX rooted paths, rooted drive paths and
//     valid UNC paths. Drive-relative, single-backslash root-relative,
//     relative and malformed UNC roots are rejected. This is syntax only:
//     no filesystem I/O, cleanup or resolution occurs here.
//   - Malformed nonempty input returns [ErrInvalidWorkspaceHint] with no
//     path echo. Existing canonical workspace hints are never overwritten,
//     and session, route, authentication, provider, MCP and policy fields
//     are left unchanged.
func ApplyWorkspaceHint(call *aipapi.Call, headers http.Header, aliases aipsdk.HTTPHeaders) error {
	if call == nil || headers == nil {
		return nil
	}
	raw := firstWorkspaceHeader(headers, aliases.OrDefault().Workspace)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if len(raw) > maxWorkspaceHintBytes {
		return ErrInvalidWorkspaceHint
	}
	if !utf8.ValidString(raw) {
		return ErrInvalidWorkspaceHint
	}
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return ErrInvalidWorkspaceHint
	}
	path := strings.TrimSpace(raw)
	if !isAbsoluteWorkspacePath(path) {
		return ErrInvalidWorkspaceHint
	}
	if hasEffectiveWorkspaceHint(call.Extensions) {
		return nil
	}
	encoded, err := json.Marshal(path)
	if err != nil {
		return fmt.Errorf("sessionwire: marshal workspace hint: %w", err)
	}
	if call.Extensions == nil {
		call.Extensions = make(map[string]json.RawMessage, 1)
	}
	call.Extensions[WorkspaceExtensionKey] = encoded
	return nil
}

// firstWorkspaceHeader returns the raw value of the first alias with a
// non-blank value, mirroring [aipsdk.FirstHeader] selection order while
// preserving raw bytes for the size/control checks.
func firstWorkspaceHeader(h http.Header, names []string) string {
	if h == nil {
		return ""
	}
	for _, name := range names {
		if raw := h.Get(name); strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return ""
}

// hasEffectiveWorkspaceHint reports whether ext already carries a usable
// workspace hint under any neutral canonical key, using the same
// non-blank-string acceptance the backend resolver applies.
func hasEffectiveWorkspaceHint(ext map[string]json.RawMessage) bool {
	for _, key := range existingWorkspaceExtensionKeys {
		raw, ok := ext[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// isAbsoluteWorkspacePath accepts POSIX rooted paths, rooted drive paths
// (letter, colon, slash) and valid UNC paths (server and share present).
func isAbsoluteWorkspacePath(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return true
	}
	if isRootedDrivePath(p) {
		return true
	}
	return isValidUNCPath(p)
}

func isRootedDrivePath(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	if p[1] != ':' {
		return false
	}
	return p[2] == '/' || p[2] == '\\'
}

func isValidUNCPath(p string) bool {
	if !strings.HasPrefix(p, `\\`) {
		return false
	}
	rest := p[2:]
	if rest == "" || rest[0] == '\\' {
		return false
	}
	_, share, ok := strings.Cut(rest, `\`)
	if !ok || share == "" || share[0] == '\\' {
		return false
	}
	return true
}
