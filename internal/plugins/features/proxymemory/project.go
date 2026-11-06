package proxymemory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Project is one derived project identity: an opaque key and the normalized
// label it was derived from. The label is never a filesystem handle; nothing
// in this package reads, resolves or accesses it (Requirement 2.5).
type Project struct {
	Key   string
	Label string
}

const (
	// projectKeyNamespace domain-separates project keys from any other hash
	// of the same label (design §3).
	projectKeyNamespace = "proxy-memory/v1\x00"

	// maxProjectLabelBytes bounds one raw project candidate (design §3).
	maxProjectLabelBytes = 4096

	// environmentContextPrefix opens a Codex environment context block.
	environmentContextPrefix = "<environment_context>"
)

// workspaceExtensionKeys is the canonical workspace hint priority. The order
// is the same as sessionwire.existingWorkspaceExtensionKeys and
// connector-support/acp/workspace.go.
var workspaceExtensionKeys = []string{"project_dir", "workspace_path", "cwd", "project"}

// claudeCWDPrefix is the working-directory line of Claude Code's bare-mode
// system context: "CWD: <absolute path>".
const claudeCWDPrefix = "CWD:"

// instructionPrefix is one recognized working-directory line prefix.
type instructionPrefix struct {
	prefix string
	// absolute requires absolute-path syntax before the value is accepted.
	absolute bool
}

// instructionPrefixes are the working-directory line prefixes. The first two
// are the design §3 sources; claudeCWDPrefix adds the bare-mode Claude Code
// context observed in native traffic. A CWD value must be absolute because the
// line is machine-generated for a process working directory, so prose such as
// "CWD: unknown" never names a project.
var instructionPrefixes = []instructionPrefix{
	{prefix: "Working directory:"},
	{prefix: "Primary working directory:"},
	{prefix: claudeCWDPrefix, absolute: true},
}

// DeriveProject derives the project identity of one request per design §3:
// an explicit workspace extension, then a working-directory instruction line
// (including Claude Code's bare-mode CWD line), then a Codex
// environment-context block. It performs no filesystem or environment access;
// the derived value stays an opaque label.
func DeriveProject(call *aipapi.Call) (Project, bool) {
	if call == nil {
		return Project{}, false
	}
	if label, ok := projectFromExtensions(call.Extensions); ok {
		return newProject(label), true
	}
	if label, ok := projectFromInstructions(call); ok {
		return newProject(label), true
	}
	if label, ok := projectFromEnvironmentContext(call); ok {
		return newProject(label), true
	}
	return Project{}, false
}

// newProject binds one normalized label to its opaque key.
func newProject(label string) Project {
	return Project{Key: ProjectKey(label), Label: label}
}

// projectFromExtensions scans the canonical workspace hint keys in priority
// order. Keys that are absent, empty, non-string or unusable are skipped so
// that the next candidate, and finally the next source, is tried.
func projectFromExtensions(ext map[string]json.RawMessage) (string, bool) {
	for _, key := range workspaceExtensionKeys {
		raw, ok := ext[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if label, ok := normalizeLabel(value); ok {
			return label, true
		}
	}
	return "", false
}

// projectFromInstructions scans the instruction line sources in order:
// call.Instructions, then system/developer messages, then system/developer
// message items.
func projectFromInstructions(call *aipapi.Call) (string, bool) {
	for _, message := range call.Instructions {
		if label, ok := instructionLabelFromMessage(message); ok {
			return label, true
		}
	}
	for _, message := range call.Messages {
		if !isInstructionRole(message.Role) {
			continue
		}
		if label, ok := instructionLabelFromMessage(message); ok {
			return label, true
		}
	}
	for _, item := range call.Items {
		if item.Kind != aipapi.ItemKindMessage || !isInstructionRole(item.Role) {
			continue
		}
		if label, ok := instructionLabelFromContent(item.Content); ok {
			return label, true
		}
	}
	return "", false
}

// isInstructionRole reports whether a role may supply a working-directory line.
func isInstructionRole(role aipapi.Role) bool {
	return role == aipapi.RoleSystem || role == aipapi.RoleDeveloper
}

// instructionLabelFromMessage scans one message's text parts.
func instructionLabelFromMessage(message aipapi.Message) (string, bool) {
	for _, part := range message.Parts {
		if part.Kind != aipapi.PartText {
			continue
		}
		if label, ok := instructionLabelFromText(part.Text); ok {
			return label, true
		}
	}
	return "", false
}

// instructionLabelFromContent scans one message item's text content parts.
func instructionLabelFromContent(content []aipapi.ContentPart) (string, bool) {
	for _, part := range content {
		if part.Kind != aipapi.ContentPartText {
			continue
		}
		if label, ok := instructionLabelFromText(part.Text); ok {
			return label, true
		}
	}
	return "", false
}

// instructionLabelFromText scans one text part line by line and returns the
// first usable value after a working-directory prefix.
func instructionLabelFromText(text string) (string, bool) {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, candidate := range instructionPrefixes {
			if !strings.HasPrefix(trimmed, candidate.prefix) {
				continue
			}
			value := strings.TrimSpace(trimmed[len(candidate.prefix):])
			if candidate.absolute && !isAbsolutePathSyntax(value) {
				continue
			}
			if label, ok := normalizeLabel(value); ok {
				return label, true
			}
		}
	}
	return "", false
}

// isAbsolutePathSyntax accepts POSIX rooted paths, rooted drive paths and UNC
// paths with a server and share. It is the syntax rule the explicit workspace
// header uses; it performs no filesystem access or resolution.
func isAbsolutePathSyntax(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 3 {
		c := p[0]
		if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
			return true
		}
	}
	if !strings.HasPrefix(p, `\\`) {
		return false
	}
	rest := p[2:]
	if rest == "" || rest[0] == '\\' {
		return false
	}
	_, share, ok := strings.Cut(rest, `\`)
	return ok && share != "" && share[0] != '\\'
}

// projectFromEnvironmentContext scans user-role text parts: messages first,
// then user message items. Only a part whose trimmed text starts with the
// Codex environment-context tag is considered, so rendered transcripts that
// merely embed the tag are not mistaken for a live environment block.
func projectFromEnvironmentContext(call *aipapi.Call) (string, bool) {
	for _, message := range call.Messages {
		if message.Role != aipapi.RoleUser {
			continue
		}
		for _, part := range message.Parts {
			if part.Kind != aipapi.PartText {
				continue
			}
			if label, ok := environmentContextLabel(part.Text); ok {
				return label, true
			}
		}
	}
	for _, item := range call.Items {
		if item.Kind != aipapi.ItemKindMessage || item.Role != aipapi.RoleUser {
			continue
		}
		for _, part := range item.Content {
			if part.Kind != aipapi.ContentPartText {
				continue
			}
			if label, ok := environmentContextLabel(part.Text); ok {
				return label, true
			}
		}
	}
	return "", false
}

// environmentContextLabel extracts the first <cwd>…</cwd> value inside a
// start-anchored environment context block.
func environmentContextLabel(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, environmentContextPrefix) {
		return "", false
	}
	_, rest, ok := strings.Cut(trimmed, "<cwd>")
	if !ok {
		return "", false
	}
	before, _, ok := strings.Cut(rest, "</cwd>")
	if !ok {
		return "", false
	}
	return normalizeLabel(strings.TrimSpace(before))
}

// normalizeLabel validates and normalizes one candidate label per design §3.
// It reports false when the candidate is unusable, so the caller tries the
// next candidate or source.
func normalizeLabel(raw string) (string, bool) {
	if len(raw) > maxProjectLabelBytes {
		return "", false
	}
	if !utf8.ValidString(raw) {
		return "", false
	}
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return "", false
	}
	label := NormalizePath(raw)
	if label == "" {
		return "", false
	}
	return label, true
}

// NormalizePath applies the design §3 normalization: trim, backslashes to
// slashes, collapse slash runs, and drop a trailing slash unless the whole
// value is "/".
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, `\`, `/`)
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// ProjectKey returns hex(sha256("proxy-memory/v1\x00" + label)), the opaque
// scope id for one normalized project label (design §3).
func ProjectKey(normalized string) string {
	sum := sha256.Sum256([]byte(projectKeyNamespace + normalized))
	return hex.EncodeToString(sum[:])
}
