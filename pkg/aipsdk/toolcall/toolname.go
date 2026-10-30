package toolcall

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// NormalizeToolName reports the canonical spelling used for tool identity
// when an exact catalog name is absent.
//
// It lowercases ASCII letters and drops ASCII whitespace, underscores, and
// hyphens. It does not inspect arguments, schemas, providers, or request
// state. Two spellings with the same normalized form are the same tool-call
// identity for catalog matching; two spellings with different forms are not.
//
// This is the shared spelling equivalence behind tool-call repair's unique
// normalized-name lookup. It lives here, rather than in that feature, because
// mandatory buffering must anticipate the same identity before the arguments
// are complete: a call named differently from its catalog entry can still
// become that entry through repair, and buffering decided on the exact
// spelling alone would release the call before repair ever sees it.
func NormalizeToolName(name string) string {
	if name == "" {
		return ""
	}
	out := make([]byte, 0, len(name))
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' || c == '_' || c == '-':
			continue
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// CanonicalToolIdentity resolves a model-emitted tool name to the catalog
// entry it names, if exactly one does.
//
// An exact catalog entry always wins. When no exact entry exists, a unique
// normalized entry wins: exactly one catalog tool whose normalized spelling
// equals the normalized emitted spelling. Ambiguity is not resolved: when
// zero or two catalog tools match, the original name and zero ToolDef are
// returned, so no caller can mistake one tool for another.
//
// The supplied tool definition is trusted only when it already names the
// emitted tool. That mirrors repair's fast path: an exact resolved definition
// supplied with the call is used without reindexing the catalog. Otherwise the
// catalog decides, because only it can prove uniqueness.
func CanonicalToolIdentity(catalog []aipapi.ToolDef, name string, tool aipapi.ToolDef) (string, aipapi.ToolDef) {
	if name != "" && tool.Name == name {
		return name, tool
	}
	for _, entry := range catalog {
		if entry.Name == name {
			return entry.Name, entry
		}
	}
	normalized := NormalizeToolName(name)
	if normalized == "" {
		return name, aipapi.ToolDef{}
	}
	var match aipapi.ToolDef
	found := false
	for _, entry := range catalog {
		if NormalizeToolName(entry.Name) != normalized {
			continue
		}
		if found {
			return name, aipapi.ToolDef{}
		}
		match = entry
		found = true
	}
	if !found {
		return name, aipapi.ToolDef{}
	}
	return match.Name, match
}
