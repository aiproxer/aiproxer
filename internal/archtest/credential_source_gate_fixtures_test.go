package archtest

import (
	"testing"
)

// TestCredentialSourceDetector_RejectsViolatingFixture proves the detector
// fails deliberately noncompliant connector implementations instead of
// trusting the live tree by default, including laundering bypasses: a real
// Go fixture reading an API key from the environment and a credential file
// from disk is rejected, as are the constant-resolved, aliased-import,
// assembled-path, plain-filename, and opaque-path variants, while an
// ordinary binary-discovery read and a reviewed allowlist entry stay clean.
func TestCredentialSourceDetector_RejectsViolatingFixture(t *testing.T) {
	t.Parallel()
	violating := `package fixture

import "os"

func acquire() (string, []byte) {
	key := os.Getenv("FUTURE_CONNECTOR_API_KEY")
	data, _ := os.ReadFile("/etc/secrets/connector_key.json")
	return key, data
}
`
	_, file, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(violating))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", file)
	if len(got) != 2 {
		t.Fatalf("violating fixture must report env + file acquisition, got %v", got)
	}

	// Constant-laundered bypass: the names look innocent but resolve to
	// credential material. The detector resolves file-scoped constants.
	laundered := `package fixture

import "os"

func loadCredential() (string, error) {
	name := "FUTURE_CONNECTOR_API_KEY"
	key := os.Getenv(name)

	path := "/etc/secrets/provider.key"
	data, err := os.ReadFile(path)

	return key, err
}
`
	_, launderedFile, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(laundered))
	if err != nil {
		t.Fatalf("laundered fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", launderedFile); len(got) != 2 {
		t.Fatalf("constant-laundered bypass must report env + file acquisition, got %v", got)
	}

	// Aliased-import bypass: renaming the os import must not escape the gate.
	aliased := `package fixture

import o "os"

func acquire() string {
	if v, ok := o.LookupEnv("FUTURE_CONNECTOR_API_TOKEN"); ok {
		return v
	}
	return ""
}
`
	_, aliasedFile, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(aliased))
	if err != nil {
		t.Fatalf("aliased fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", aliasedFile); len(got) != 1 {
		t.Fatalf("aliased-import bypass must report env acquisition, got %v", got)
	}

	// Assembled-path bypass: the credential filename is joined dynamically
	// but a literal fragment still names the credential file.
	assembled := `package fixture

import (
	"os"
	"path/filepath"
)

func load(base string) ([]byte, error) {
	return os.ReadFile(filepath.Join(base, "auth.json"))
}
`
	_, assembledFile, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(assembled))
	if err != nil {
		t.Fatalf("assembled fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", assembledFile); len(got) != 1 {
		t.Fatalf("assembled-path bypass must report file acquisition, got %v", got)
	}

	// Plain API-key filename: /run/api_key carries no credential token,
	// yet it is an unambiguous credential-file read. The restrictive
	// filesystem policy fails it closed without token heuristics.
	plainKeyFile := `package fixture

import "os"

func loadAPIKey() ([]byte, error) {
	return os.ReadFile("/run/api_key")
}
`
	_, plainKeyFileAST, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(plainKeyFile))
	if err != nil {
		t.Fatalf("plain key-file fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", plainKeyFileAST); len(got) != 1 {
		t.Fatalf("plain /run/api_key read must fail closed, got %v", got)
	}

	// Opaque path bypass: a dynamically supplied path argument carries no
	// name or literal at all. Outside the reviewed allowlist it still
	// fails closed, since the gate cannot prove it is not a credential.
	opaquePath := `package fixture

import "os"

func load(path string) ([]byte, error) {
	return os.ReadFile(path)
}
`
	_, opaquePathAST, err := ParseGoSource("connectors/future-connector/internal/service/service.go", []byte(opaquePath))
	if err != nil {
		t.Fatalf("opaque path fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/future-connector/internal/service/service.go", opaquePathAST); len(got) != 1 {
		t.Fatalf("opaque-path file read must fail closed without an allowlist entry, got %v", got)
	}

	// Reviewed allowlist: a legitimate noncredential read inside an
	// allowlisted file stays clean.
	allowlisted := `package fixture

import "os"

func load(path string) ([]byte, error) {
	return os.ReadFile(path)
}
`
	_, allowlistedAST, err := ParseGoSource("connectors/codex/internal/catalog/fallback.go", []byte(allowlisted))
	if err != nil {
		t.Fatalf("allowlist fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/codex/internal/catalog/fallback.go", allowlistedAST); len(got) != 0 {
		t.Fatalf("allowlisted noncredential read must stay clean, got %v", got)
	}

	// Ordinary non-credential reads must stay clean, including short-token
	// traps like PATH and binary-discovery names.
	clean := `package fixture

import "os"

func binary() string {
	if env := os.Getenv("CURSOR_AGENT_BIN"); env != "" {
		return env
	}
	return os.Getenv("HOME")
}
`
	_, cleanFile, err := ParseGoSource("connectors/cursorcliacp/internal/service/service.go", []byte(clean))
	if err != nil {
		t.Fatalf("clean fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/cursorcliacp/internal/service/service.go", cleanFile); len(got) != 0 {
		t.Fatalf("detector falsely rejected ordinary env reads: %v", got)
	}

	// OAuth/workload exceptions must stay clean even with credential-like names.
	oauth := `package fixture

import "os"

func token() string {
	return os.Getenv("MINIMAX_OAUTH_TOKEN_FILE")
}
`
	_, oauthFile, err := ParseGoSource("connectors/minimexoauth/cmd/tool/main.go", []byte(oauth))
	if err != nil {
		t.Fatalf("oauth fixture parse: %v", err)
	}
	if got := credentialAcquisitionViolations("connectors/minimexoauth/cmd/tool/main.go", oauthFile); len(got) != 0 {
		t.Fatalf("detector must except OAuth paths: %v", got)
	}
}
