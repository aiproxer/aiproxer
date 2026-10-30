package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestPkgAipapiDoesNotImportAuthSDKPackages keeps canonical LLM contracts free of transport auth
// DTOs (authentication-architecture-refactor task 10.4).
func TestPkgAipapiDoesNotImportAuthSDKPackages(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{"github.com/aiproxer/aiproxer/pkg/aipsdk/auth", "pkg/aipapi must not import pkg/aipsdk/auth"},
		{"github.com/aiproxer/aiproxer/internal/core/auth", "pkg/aipapi must not import internal/core/auth"},
	}
	assertGoListImportsExclude(t, "./pkg/aipapi", forbidden)
}

// aipsdkAuthImportClosureForbidden is matched against every ImportPath in `go list -deps` for
// ./pkg/aipsdk/auth (transitive closure, same policy as the former direct-imports-only check).
var aipsdkAuthImportClosureForbidden = []forbiddenDep{
	{Substr: "/internal/core/", ErrMsg: "pkg/aipsdk/auth must not depend on internal/core (transitively)"},
	{Substr: "/internal/plugins/", ErrMsg: "pkg/aipsdk/auth must not depend on internal/plugins (transitively)"},
	{Substr: "github.com/openai/openai-go", ErrMsg: "pkg/aipsdk/auth must not depend on OpenAI Go SDK (transitively)"},
	{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "pkg/aipsdk/auth must not depend on Anthropic SDK (transitively)"},
	{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "pkg/aipsdk/auth must not depend on AWS SDKs (transitively)"},
}

// TestPkgAipsdkAuthImportClosure_staysSDKLocal ensures public auth DTOs do not pull core, plugins,
// or provider SDKs into the stable auth package (task 10.4, complements task 1.4).
func TestPkgAipsdkAuthImportClosure_staysSDKLocal(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/aipsdk/auth"}, aipsdkAuthImportClosureForbidden)
}

func assertGoListImportsExclude(t *testing.T, goListPattern string, forbidden []struct {
	sub, msg string
},
) {
	t.Helper()
	out, err := cachedGoList(t, "-json", "-test=false", goListPattern)
	if err != nil {
		t.Fatalf("go list %s: %v", goListPattern, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			for _, r := range forbidden {
				if strings.Contains(imp, r.sub) {
					t.Fatalf("%s: forbidden import %q in %q", r.msg, imp, pkg.ImportPath)
				}
			}
		}
	}
}
