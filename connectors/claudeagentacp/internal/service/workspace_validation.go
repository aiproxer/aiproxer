package service

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Canonical workspace_path admission for the Claude connector only. Shared
// ACP/HTTP/core contracts are unchanged; other hint keys keep their existing
// behavior and are not validated here.
const (
	workspacePathExtensionKey = "workspace_path"
	// maxWorkspacePathDecodedBytes bounds the decoded JSON string value,
	// matching the shared HTTP hint carrier contract (4096).
	maxWorkspacePathDecodedBytes = 4096
	// maxWorkspacePathRawBytes bounds the raw JSON encoding before parsing so
	// a huge value cannot force a large allocation. A 4096-byte value with
	// every byte escaped as \uXXXX is 6*4096+2 quotes; 24600 covers that
	// worst case plus margin without rejecting valid escaped values.
	maxWorkspacePathRawBytes = 24600
)

func invalidWorkspacePath() error {
	return &aipapi.RejectError{Reason: "claudeagentacp: invalid workspace_path"}
}

// validateWorkspacePathExtension rejects a present-but-malformed canonical
// workspace_path extension with a static capability error. Absent, empty, or
// blank-string values are inert: the configured default owns resolution.
// It performs syntax checks only and never touches the filesystem, logs, or
// the call; usability remains with WorkspacePolicy.
func validateWorkspacePathExtension(call *aipapi.Call) error {
	if call == nil || len(call.Extensions) == 0 {
		return nil
	}
	raw, ok := call.Extensions[workspacePathExtensionKey]
	if !ok {
		return nil
	}
	if len(raw) > maxWorkspacePathRawBytes {
		return invalidWorkspacePath()
	}
	if !utf8.Valid(raw) {
		return invalidWorkspacePath()
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' {
		return invalidWorkspacePath()
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return invalidWorkspacePath()
	}
	if len(value) > maxWorkspacePathDecodedBytes {
		return invalidWorkspacePath()
	}
	if !utf8.ValidString(value) {
		return invalidWorkspacePath()
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return invalidWorkspacePath()
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if !filepath.IsAbs(strings.TrimSpace(value)) {
		return invalidWorkspacePath()
	}
	return nil
}
