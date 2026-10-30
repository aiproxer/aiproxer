package archtest

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Source-level credential-acquisition gate (requirement 5.2).
//
// The YAML/filesystem negative fixtures prove validators reject secret-bearing
// configuration, but they cannot prove a connector implementation does not
// acquire credentials directly via os.Getenv/os.ReadFile. This gate scans
// first-party connector non-test Go source for direct credential acquisition
// and fails deterministically on violations, with explicit OAuth/workload
// exceptions and a real negative Go fixture.

// credentialEnvTokens are normalized (lowercase, separators removed)
// substrings that mark an environment name as credential-bearing. Short
// tokens like "pat" are handled separately to avoid matching "path".
var credentialEnvTokens = []string{
	"apikey", "apitoken", "accesstoken", "authtoken", "bearer",
	"privatekey", "clientsecret", "secret", "credential", "token",
	"authjson",
}

// credentialFileTokens mark a file path as credential-bearing. They are a
// supplementary signal: the primary filesystem policy below is restrictive
// (deny by default with a reviewed allowlist), so an innocuous-looking
// credential filename such as /run/api_key still fails closed.
var credentialFileTokens = []string{
	"auth.json", ".env", "credential", "secret", "token",
	"privatekey", "authjson", "dotenv", "api_key", "apikey", "key.json",
}

// credentialFileReadAllowlist names the non-excepted connector files
// permitted to acquire filesystem paths, with the reviewed justification
// for each. Every other connector file fails closed on any os file
// acquisition (ReadFile/Open/OpenFile/WriteFile): static-key connectors
// must receive credentials via host Configure secrets, and a new file read
// requires an explicit allowlist entry with justification. Allowlisted
// files still undergo credential-token analysis, so a future credential
// read added to one of them is still caught.
var credentialFileReadAllowlist = map[string]string{
	// Agent-runtime host introspection and process metadata.
	"connectors/claudeagentacp/internal/product/adapter.go": "agent runtime kernel/binary metadata and pid files",
	// Connector-owned lifecycle state (no credentials): the bypass runtime
	// supervisor writes and reads its own shutdown marker, and inspects
	// /proc for identity-bound descendant reaping on retirement.
	"connectors/claudeagentacp/internal/product/authenv.go":          "connector-owned shutdown marker drain acknowledgement",
	"connectors/claudeagentacp/internal/product/supervisor_linux.go": "connector-owned shutdown marker and /proc process-tree supervision",
	"connector-support/acp/process_identity_unix.go":                 "agent process /proc identity",
	"connector-support/acp/workspace.go":                             "agent workspace I/O",
	// Local CLIs and catalog/agent plumbing (no static credentials).
	"connectors/codex/cmd/fake-codex-cli/main.go":                     "fake CLI pid files",
	"connectors/codex/internal/catalog/fallback.go":                   "catalog fallback data",
	"connectors/codex/internal/codex/durable_sync_unix.go":            "sync directory listing",
	"connectors/agycliacp/internal/product/wrapperinstall/install.go": "wrapper binary install",
}

// credentialSourceException reports whether rel is an explicitly classified
// OAuth-user, workload-identity, or reviewed managed-OAuth path whose
// provider-managed auth stores are not static API-key config (requirements
// 2.4, 4.5). Literal provider secrets in AIProxer YAML remain prohibited for
// every path; this exception covers only connector-side OAuth token files
// and workload chains, never static bearer-key pooling. Exceptions are an
// explicit directory/file allowlist: a new connector cannot except itself by
// mentioning OAuth in its path.
func credentialSourceException(rel string) bool {
	rel = SlashPath(rel)
	// Shared OAuth token store.
	if strings.HasPrefix(rel, "connector-support/oauthcred/") {
		return true
	}
	// OAuth-user connectors (personal/subscription credentials).
	for _, prefix := range []string{
		"connectors/gitlabduo/", "connectors/nousportal/", "connectors/qwenoauth/",
		"connectors/xaioauth/", "connectors/minimexoauth/",
		"connectors/openaichatgptplan/",
	} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	// Workload/service-account connectors (non-bearer signing chains).
	for _, prefix := range []string{
		"connectors/vertex/", "connectors/sagemaker/", "connectors/oci/",
		"connectors/sapaicore/",
	} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	// Reviewed managed-OAuth file helpers: the codex subscription flow keeps
	// its OAuth lifecycle in these files. Each entry is a deliberate,
	// reviewable exception; sibling codex files stay gated.
	return slices.Contains([]string{
		"connectors/codex/internal/codex/managed_oauth.go",
		"connectors/codex/internal/codex/managed_oauth_files.go",
		"connectors/codex/internal/codex/managed_oauth_quota.go",
		"connectors/codex/internal/codex/managed_oauth_store.go",
		"connectors/codex/internal/codex/oauth.go",
	}, rel)
}

func normalizeCredentialToken(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, "/", "")
	return s
}

// containsPAT reports a standalone PAT token without matching "path",
// "patch", or "pattern": _pat, pat_, or exact pat (case-insensitive).
func containsPAT(s string) bool {
	lower := strings.ToLower(s)
	if lower == "pat" {
		return true
	}
	if strings.Contains(lower, "_pat") || strings.Contains(lower, "pat_") {
		return true
	}
	// Hyphenated variants (e.g. personal-access-token is covered by token,
	// but bare pat- prefix still marks a PAT root).
	if strings.Contains(lower, "-pat") || strings.Contains(lower, "pat-") {
		return true
	}
	return false
}

func isCredentialEnvName(name string) bool {
	if containsPAT(name) {
		return true
	}
	normalized := normalizeCredentialToken(name)
	for _, tok := range credentialEnvTokens {
		if strings.Contains(normalized, tok) {
			return true
		}
	}
	return false
}

func isCredentialFilePath(path string) bool {
	lower := strings.ToLower(path)
	for _, tok := range credentialFileTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return containsPAT(path)
}

// osLocalNames returns the local package names bound to the "os" import in
// f, resolving explicit aliases (import o "os"). The second result reports a
// dot-import (import . "os"), whose Getenv/ReadFile calls have no package
// qualifier. An aliased import must not escape the gate.
func osLocalNames(f *ast.File) (names map[string]bool, dotImport bool) {
	names = map[string]bool{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "os" {
			continue
		}
		if imp.Name == nil {
			names["os"] = true
			continue
		}
		switch imp.Name.Name {
		case "_":
			// Blank import: no calls possible.
		case ".":
			dotImport = true
		default:
			names[imp.Name.Name] = true
		}
	}
	return names, dotImport
}

// stringConstants collects file-scoped string constants: const/var string
// declarations and simple assignments of string literals (name := "literal").
// A later reassignment keeps every observed literal so a variable that ever
// holds credential material resolves as credential-bearing (fail closed).
func stringConstants(f *ast.File) map[string][]string {
	out := map[string][]string{}
	add := func(name, value string) {
		if name == "" || name == "_" {
			return
		}
		out[name] = append(out[name], value)
	}
	literalValue := func(e ast.Expr) (string, bool) { return "", false }
	literalValue = func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", false
			}
			return strings.Trim(v.Value, `"'`), true
		case *ast.BinaryExpr:
			if v.Op != token.ADD {
				return "", false
			}
			left, ok := literalValue(v.X)
			if !ok {
				return "", false
			}
			right, ok := literalValue(v.Y)
			if !ok {
				return "", false
			}
			return left + right, true
		}
		return "", false
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gen.Tok != token.CONST && gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					break
				}
				if lit, ok := literalValue(vs.Values[i]); ok {
					add(name.Name, lit)
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if lit, ok := literalValue(assign.Rhs[0]); ok {
			add(lhs.Name, lit)
		}
		return true
	})
	return out
}

// credentialArgCandidates returns every credential-relevant string for one
// os-call argument: identifier names in the expression subtree, string
// literals in the subtree (catching assembled paths such as
// filepath.Join(dir, "auth.json")), and constant-resolved values for
// identifiers. Checking values instead of only names closes the
// name := "FUTURE_CONNECTOR_API_KEY"; os.Getenv(name) bypass.
func credentialArgCandidates(arg ast.Expr, consts map[string][]string) []string {
	var out []string
	ast.Inspect(arg, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				out = append(out, strings.Trim(v.Value, `"'`))
			}
		case *ast.Ident:
			if v.Name == "" || v.Name == "_" {
				return true
			}
			out = append(out, v.Name)
			out = append(out, consts[v.Name]...)
		}
		return true
	})
	return out
}

// credentialAcquisitionViolations reports direct credential acquisition in one
// connector source file: os.Getenv/LookupEnv with a credential-bearing env
// name or value, os.ReadFile/Open/OpenFile/WriteFile with a
// credential-bearing path or value, or any filesystem acquisition outside
// the reviewed credentialFileReadAllowlist. Import aliases, file-scoped
// constants, and literals inside assembled path expressions are resolved,
// so renaming the os import or laundering the name through a variable does
// not escape the gate. Exception paths return nil.
func credentialAcquisitionViolations(rel string, file *ast.File) []string {
	if credentialSourceException(rel) {
		return nil
	}
	osNames, osDot := osLocalNames(file)
	consts := stringConstants(file)
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var method string
		isOSCall := false
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkg, ok := fun.X.(*ast.Ident)
			if !ok || !osNames[pkg.Name] {
				return true
			}
			method, isOSCall = fun.Sel.Name, true
		case *ast.Ident:
			if !osDot {
				return true
			}
			method, isOSCall = fun.Name, true
		default:
			return true
		}
		if !isOSCall {
			return true
		}
		var wantEnv, wantFile bool
		switch method {
		case "Getenv", "LookupEnv":
			wantEnv = true
		case "ReadFile", "Open", "OpenFile", "WriteFile":
			wantFile = true
		default:
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		seen := map[string]struct{}{}
		var tokenViolations []string
		for _, candidate := range credentialArgCandidates(call.Args[0], consts) {
			if candidate == "" {
				continue
			}
			if _, dup := seen[candidate]; dup {
				continue
			}
			seen[candidate] = struct{}{}
			if wantEnv && isCredentialEnvName(candidate) {
				tokenViolations = append(tokenViolations, rel+": os."+method+"("+candidate+") reads provider credential material directly; receive ordered slots via host Configure secrets")
			}
			if wantFile && isCredentialFilePath(candidate) {
				tokenViolations = append(tokenViolations, rel+": os."+method+"("+candidate+") reads a credential file directly; static credentials come only via host Configure secrets")
			}
		}
		if wantFile {
			// Restrictive filesystem policy: filename heuristics alone
			// cannot authorize a read (/run/api_key carries no token, and
			// an opaque path argument carries no name at all). Any file
			// acquisition outside the reviewed allowlist fails closed;
			// it subsumes the token reports for this call.
			if _, allowed := credentialFileReadAllowlist[rel]; !allowed {
				violations = append(violations, rel+": os."+method+" performs filesystem acquisition without a reviewed exception ("+credentialFileReadAllowlistHint()+"); static credentials come only via host Configure secrets")
				return true
			}
		}
		violations = append(violations, tokenViolations...)
		return true
	})
	return violations
}

// credentialFileReadAllowlistHint names the allowlist symbol a fix author
// must extend (with justification) to authorize a legitimate noncredential
// file read.
func credentialFileReadAllowlistHint() string {
	return "see credentialFileReadAllowlist"
}

// connectorCredentialSourceFiles lists every non-test Go source file owned by
// connector authors, including the shared connector support modules.
func connectorCredentialSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{firstPartyConnectorsDir, firstPartySupportDir} {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if base := d.Name(); base == "testdata" || base == "node_modules" || strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("connector subtree is empty; the credential-source guard would be vacuous")
	}
	return files
}

// TestConnectorSubtree_RejectsDirectCredentialAcquisition proves no
// first-party static connector implementation acquires provider credentials
// directly from the environment or filesystem. OAuth-user, workload-identity,
// and managed-OAuth paths are explicitly excepted; every other os.Getenv or
// os.ReadFile with a credential-bearing name fails here.
func TestConnectorSubtree_RejectsDirectCredentialAcquisition(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, path := range connectorCredentialSourceFiles(t, root) {
		rel := SlashPath(strings.TrimPrefix(path, root+string(filepath.Separator)))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, violation := range credentialAcquisitionViolations(rel, file) {
			t.Error(violation)
		}
	}
}
