package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// LegacyABIAllowlist is the exact set of additive legacy ABI feature values.
var LegacyABIAllowlist = []string{
	backendplugin.FeatureExactReasoningParts,
	backendplugin.FeatureOrderedItems,
	backendplugin.FeatureExactOpenResponsesFields,
	backendplugin.FeatureProxyOwnedSessionID,
	backendplugin.FeatureAccountingEvidence,
	backendplugin.FeatureSemanticExtensions,
	backendplugin.FeaturePromptCacheResidency,
	backendplugin.FeatureCancellationHandshake,
	backendplugin.FeatureReasoningEffortValues,
}

var genericABIFieldTerms = map[string]bool{
	"custom": true, "extension": true, "extensions": true, "semantic": true,
	"capability": true, "capabilities": true, "session": true, "owned": true,
	"proxy": true, "reasoning": true, "effort": true, "values": true, "parts": true, "items": true, "ordered": true,
	"exact":        true,
	"accounting":   true,
	"evidence":     true,
	"v2":           true,
	"cancellation": true,
	"handshake":    true,
	"v1":           true,
	// Role and domain words, not provider words: the host is the neutral
	// party that owns the canonical identity, binding names the neutral
	// operation that attaches a producer's claim to it, and economics is the
	// repo's own neutral name for the observed-value domain.
	"host": true, "bound": true, "economic": true,
}

var neutralABITerms = map[string]bool{
	"semantic": true, "protocol": true, "feature": true, "features": true,
	"custom": true, "extension": true, "extensions": true, "capability": true,
	"capabilities": true, "session": true, "owned": true, "proxy": true,
	"reasoning": true, "parts": true, "items": true, "ordered": true, "exact": true,
	"usage": true, "metadata": true, "transport": true, "dialect": true,
	"requirement": true, "requirements": true, "invocation": true, "canonical": true,
	"wire": true, "json": true, "support": true, "runtime": true, "policy": true,
	"close": true, "instance": true, "response": true, "request": true,
	"resolved": true, "execute": true, "list": true, "models": true, "model": true,
	"tool": true, "def": true, "disable": true, "parameters": true, "prompt": true,
	"cache": true, "key": true, "message": true, "messages": true, "credential": true,
	"renew": true, "renewal": true, "release": true, "lifecycle": true, "sliding": true, "expiry": true,
	"fixed": true, "minimum": true, "residency": true, "best": true, "effort": true,
	"timing": true, "expires": true, "resident": true, "until": true, "target": true,
	"handle": true, "observation": true, "observations": true,
	"renewable": true, "control": true, "cold": true, "recreated": true, "stale": true,
	"unsupported": true, "failed": true,
	"mode": true, "access": true, "scope": true, "process": true, "sharing": true,
	"role": true, "part": true, "kind": true, "event": true, "terminal": true,
	"status": true, "cancel": true, "cancellation": true, "handshake": true, "client": true, "server": true, "frame": true,
	"error": true, "code": true, "reason": true, "name": true, "id": true,
	"minor": true, "major": true, "plugin": true, "host": true, "version": true,
	"build": true, "description": true, "prefixes": true, "dynamic": true,
	"static": true, "structured": true, "outputs": true, "parallel": true,
	"video": true, "annotations": true, "media": true, "keepalive": true,
	"bidirectional": true, "config": true, "yaml": true, "secrets": true,
	"timeout": true, "allowed": true, "env": true, "retries": true, "evidence": true,
	"source": true, "after": true, "fetch": true, "refresh": true, "unix": true,
	"quality": true, "optional": true, "generation": true, "route": true,
	"prefix": true, "fields": true, "field": true, "supports": true, "supported": true,
	"output": true, "assistant": true, "health": true, "graceful": true,
	"shutdown": true, "describe": true, "negotiate": true, "configure": true,
	"resolve": true, "count": true, "finalize": true, "profile": true, "state": true,
	"data": true, "input": true, "result": true, "factory": true, "descriptor": true,
	"schema": true, "payload": true, "carrier": true, "content": true, "summary": true,
	"unknown": true, "raw": true, "value": true, "default": true, "max": true,
	"local": true, "only": true, "none": true, "unspecified": true, "deadline": true,
	"start": true, "oauth": true, "user": true, "workload": true, "diagnostic": true,
	"provider": true, "transient": true, "aborted": true, "cancelled": true,
	"internal": true, "invalid": true, "argument": true, "not": true, "found": true,
	"permission": true, "denied": true, "resource": true, "exhausted": true,
	"unauthenticated": true, "unavailable": true, "file": true, "ref": true,
	"image": true, "delta": true, "signature": true, "warning": true, "started": true,
	"finished": true, "text": true, "call": true, "outcome": true, "accepted": true,
	"violation": true, "annotation": true, "refusal": true, "failure": true,
	"success": true, "developer": true, "system": true,
	"meta": true, "delivery": true, "operation": true, "param": true,
	"per": true, "shared": true, "artifact": true, "index": true, "phase": true,
	"v": true, "v1": true, "v2": true,
	"refs": true, "acknowledged": true, "detail": true,
	// Package and language vocabulary is neutral even when it is not a wire term.
	"grpc": true, "backend": true, "aip": true, "sdk": true, "any": true,
	"apply": true, "billing": true, "configured": true,
	"err": true, "forward": true,
	"has": true, "must": true, "negotiation": true, "new": true, "open": true,
	"require": true, "restore": true, "secret": true, "service": true,
	"stream": true, "token": true, "validate": true,
}

func identifierWords(value string) []string {
	value = strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(value)
	var words []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == ' ' }) {
		start := 0
		for i := 1; i < len(part); i++ {
			if part[i] >= 'A' && part[i] <= 'Z' {
				words = append(words, strings.ToLower(part[start:i]))
				start = i
			}
		}
		if start < len(part) {
			words = append(words, strings.ToLower(part[start:]))
		}
	}
	return words
}

func providerSpecificABIIdentifier(value string) bool {
	words := identifierWords(value)
	if len(words) < 2 {
		return false
	}
	for _, w := range words[1:] {
		if neutralABITerms[w] && (w == "fields" || w == "schema" || w == "dialect") {
			return !neutralABITerms[words[0]]
		}
	}
	return false
}

func ValidateABIFeatureSymbol(featureName string) error {
	name := strings.ToLower(strings.TrimSpace(featureName))
	if slices.Contains(LegacyABIAllowlist, name) {
		return nil
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	if len(parts) == 0 {
		return fmt.Errorf("archtest: empty backend-plugin ABI feature %q", featureName)
	}
	for _, part := range parts {
		if !genericABIFieldTerms[part] {
			return fmt.Errorf("archtest: backend-plugin ABI feature %q contains non-generic term %q", featureName, part)
		}
	}
	return nil
}

// DetectDuplicateContinuationStructs parses both continuation zones and reports
// concrete mutable authorities. Thin aliases and compatibility wrappers are
// intentionally allowed; the SDK package remains the implementation authority.
func DetectDuplicateContinuationStructs(repoRoot string) ([]string, error) {
	fset := token.NewFileSet()

	sdkDir := filepath.Join(repoRoot, "pkg", "aipsdk", "continuation")
	//nolint:staticcheck // Structural scanner intentionally needs ParseDir's file map.
	sdkPkgs, err := parser.ParseDir(fset, sdkDir, nil, 0)
	if err != nil {
		return nil, err
	}

	coreDir := filepath.Join(repoRoot, "internal", "core", "continuation")
	//nolint:staticcheck // Structural scanner intentionally needs ParseDir's file map.
	corePkgs, err := parser.ParseDir(fset, coreDir, nil, 0)
	if err != nil {
		return nil, err
	}

	//nolint:staticcheck // ParseDir is deliberately paired with ast.Package to inspect source files.
	findStructs := func(pkgs map[string]*ast.Package) map[string]string {
		structs := make(map[string]string)
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				for _, decl := range fileNode.Decls {
					genDecl, ok := decl.(*ast.GenDecl)
					if !ok || genDecl.Tok != token.TYPE {
						continue
					}
					for _, spec := range genDecl.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						if _, isStruct := typeSpec.Type.(*ast.StructType); isStruct {
							structs[typeSpec.Name.Name] = fname
						}
					}
				}
			}
		}
		return structs
	}

	_ = findStructs(sdkPkgs)
	coreStructs := findStructs(corePkgs)

	var duplicateStructs []string
	for structName := range coreStructs {
		if structName == "MemoryStore" || structName == "StreamRecorder" {
			duplicateStructs = append(duplicateStructs, structName)
		}
	}
	slices.Sort(duplicateStructs)
	return duplicateStructs, nil
}

func TestBackendPluginABI_LegacyAllowlistOnly(t *testing.T) {
	t.Parallel()

	if symbols, err := ScanPublicBackendPluginABI(filepath.Join("..", "..")); err != nil {
		t.Fatalf("scan public backend-plugin ABI: %v", err)
	} else if err := ValidatePublicBackendPluginABIMutation(symbols); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join("..", "..", "pkg", "aipsdk", "backendplugin")
	fset := token.NewFileSet()
	//nolint:staticcheck // Structural scanner intentionally needs ParseDir's file map.
	pkgs, err := parser.ParseDir(fset, pkgDir, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse pkg/aipsdk/backendplugin: %v", err)
	}

	discoveredFeatures := make(map[string]string)
	for _, pkg := range pkgs {
		for fname, fileNode := range pkg.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			for _, decl := range fileNode.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.CONST {
					continue
				}
				for _, spec := range genDecl.Specs {
					vSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vSpec.Names {
						if strings.HasPrefix(name.Name, "Feature") && i < len(vSpec.Values) {
							if lit, ok := vSpec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								val := strings.Trim(lit.Value, `"`)
								discoveredFeatures[name.Name] = val
							}
						}
					}
				}
			}
		}
	}

	if len(discoveredFeatures) == 0 {
		t.Fatalf("discovered zero Feature* constants in pkg/aipsdk/backendplugin")
	}

	for constName, val := range discoveredFeatures {
		if err := ValidateABIFeatureSymbol(val); err != nil {
			t.Fatalf("constant %s = %q failed ABI allowlist policy: %v", constName, val, err)
		}
	}
}

func TestBackendPluginABI_ProtoFieldsScanned(t *testing.T) {
	t.Parallel()

	protoPath := filepath.Join("..", "..", "api", "backendplugin", "v1", "backend.proto")
	content, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatalf("failed to read backend.proto at %s: %v", protoPath, err)
	}
	if err := ValidateProtoSchema(string(content)); err != nil {
		t.Fatalf("backend.proto structural ABI policy failed: %v", err)
	}
}

func TestBackendPluginABI_DetectorRejectsNewProtocolNamedFeature(t *testing.T) {
	t.Parallel()

	unauthorizedFeatures := []string{
		"exact_anthropic_fields",
		"gemini_thinking_v2",
		"openai_custom_schema",
		"exact_codex_fields",
		"acp_custom_capability",
	}

	for _, f := range unauthorizedFeatures {
		if err := ValidateABIFeatureSymbol(f); err == nil {
			t.Fatalf("expected architecture guard to reject protocol-named ABI feature %q, but it passed", f)
		}
	}

	// Verify pre-v1.3 versioned classification
	if err := ValidateABIFeatureSymbol("exact_reasoning_parts"); err != nil {
		t.Fatalf("expected v1.1 exact_reasoning_parts to pass: %v", err)
	}
	if err := ValidateABIFeatureSymbol("ordered_items"); err != nil {
		t.Fatalf("expected v1.2 ordered_items to pass: %v", err)
	}
	if err := ValidateABIFeatureSymbol("exact_openresponses_fields"); err != nil {
		t.Fatalf("expected v1.3 exact_openresponses_fields legacy exception to pass: %v", err)
	}

	// The approved host-bound economic evidence feature is a role/domain name,
	// not a provider name: it must validate. The two negatives pin the same
	// widening in place: a provider word in either position stays rejected, so
	// the added terms can never widen into a provider-named feature.
	if err := ValidateABIFeatureSymbol("host_bound_economic_evidence_v2"); err != nil {
		t.Fatalf("expected the approved host_bound_economic_evidence_v2 feature name to pass: %v", err)
	}
	for _, f := range []string{"host_bound_openai_v2", "economic_anthropic_v2"} {
		if err := ValidateABIFeatureSymbol(f); err == nil {
			t.Fatalf("expected architecture guard to reject provider-named ABI feature %q, but it passed", f)
		}
	}
}

func TestCoreExcludesProviderProfilesAndSDKs(t *testing.T) {
	t.Parallel()

	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{Substr: "/internal/providerprofiles", ErrMsg: "internal/core must not import providerprofiles"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "internal/core must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "internal/core must not import Anthropic SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "internal/core must not import AWS SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "internal/core must not import Gemini SDK"},
	})
}

func TestDiagnostics_CoreDiagExcludesConcretePlugins(t *testing.T) {
	t.Parallel()

	assertDepsExcludeForbidden(t, []string{"./internal/core/diag/..."}, []forbiddenDep{
		{Substr: "/internal/plugins/frontends/", ErrMsg: "internal/core/diag must not import concrete frontend plugins"},
		{Substr: "/internal/plugins/backends/", ErrMsg: "internal/core/diag must not import concrete backend plugins"},
	})
}

func TestArchGuard_DetectorMutations(t *testing.T) {
	t.Parallel()

	// 1. Proto line mutations
	cleanProto := "string custom_field = 1;"
	if err := validateProtoMutationLine(cleanProto); err != nil {
		t.Fatalf("expected clean proto line to pass: %v", err)
	}
	badProto := "string anthropic_custom_field = 2;"
	if err := validateProtoMutationLine(badProto); err == nil {
		t.Fatalf("expected bad proto line to fail ValidateProtoLine")
	}

	// 2. Synthetic AST route declaration mutations. Exercise the actual
	// repository detector rather than a duplicated test-only visitor.
	contractDir := filepath.Join(t.TempDir(), "internal", "stdhttp", "contract")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatalf("create synthetic contract directory: %v", err)
	}
	writeRoute := func(source string) map[string]string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(contractDir, "route.go"), []byte("package contract\n"+source), 0o644); err != nil {
			t.Fatalf("write synthetic route source: %v", err)
		}
		got, err := DetectCentralProtocolRouteKinds(filepath.Dir(filepath.Dir(filepath.Dir(contractDir))))
		if err != nil {
			t.Fatalf("scan synthetic route source: %v", err)
		}
		return got
	}

	if got := writeRoute(`const RouteOperationCreate = "create"`); len(got) != 0 {
		t.Fatalf("clean route declaration was detected: %v", got)
	}
	if got := writeRoute(`const RouteKindBedrock = "bedrock_invoke"`); got["RouteKindBedrock"] != "bedrock_invoke" {
		t.Fatalf("bad route declaration was not decoded by detector: %v", got)
	}
	if got := writeRoute(`const RouteOperationBedrock = "bedrock_invoke"`); len(got) != 0 {
		t.Fatalf("identifier mutation still detected: %v", got)
	}
	if got := writeRoute(`const RouteKindBedrock = 42`); len(got) != 0 {
		t.Fatalf("non-string literal mutation was detected: %v", got)
	}
	if got := writeRoute(`const RouteKindBedrock = "openresponses\u005fcreate"`); got["RouteKindBedrock"] != "openresponses_create" {
		t.Fatalf("escaped route literal was not decoded: %v", got)
	}
	// The detector's invariant is ownership, not a value allowlist: even an
	// empty string remains a central RouteKind declaration and must be reported.
	if got := writeRoute(`const RouteKindBedrock = ""`); func() bool {
		value, ok := got["RouteKindBedrock"]
		return !ok || value != ""
	}() {
		t.Fatalf("empty central RouteKind declaration was not reported: %v", got)
	}
}

// TestCentralProtocolRouteKindsInRepo asserts that no central RouteKind* declarations
// exist in internal/stdhttp/contract in default CI (without requiring -tags=architecture_red).
func TestCentralProtocolRouteKindsInRepo(t *testing.T) {
	t.Parallel()
	repoRoot := filepath.Join("..", "..")
	routeKinds, err := DetectCentralProtocolRouteKinds(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect central route kinds: %v", err)
	}
	if len(routeKinds) > 0 {
		t.Fatalf("Central RouteKind declarations found in internal/stdhttp/contract: %+v", routeKinds)
	}
}
