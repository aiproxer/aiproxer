package archtest

import (
	"bytes"
	"encoding/json"
	"testing"
)

const (
	meteringPath  = "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	economicsPath = "github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	authorityPath = "github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// TestDualPlaneEconomicsPublicPackageDAG locks the Phase 3 import DAG:
// authority → economics → metering (no cycles; metering stays leaf).
func TestDualPlaneEconomicsPublicPackageDAG(t *testing.T) {
	t.Parallel()

	meteringImports := directPackageImports(t, "./pkg/aipsdk/metering")
	economicsImports := directPackageImports(t, "./pkg/aipsdk/economics")
	authorityImports := directPackageImports(t, "./pkg/aipsdk/authority")

	assertImportAbsent(t, "metering", meteringImports, economicsPath)
	assertImportAbsent(t, "metering", meteringImports, authorityPath)

	assertImportPresent(t, "economics", economicsImports, meteringPath)
	assertImportAbsent(t, "economics", economicsImports, authorityPath)

	assertImportPresent(t, "authority", authorityImports, economicsPath)
	assertImportPresent(t, "authority", authorityImports, meteringPath)
}

func directPackageImports(t *testing.T, pattern string) map[string]struct{} {
	t.Helper()
	out, err := cachedGoList(t, "-json", "-test=false", pattern)
	if err != nil {
		t.Fatalf("go list %s: %v", pattern, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatalf("go list %s: empty output", pattern)
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode %s: %v", pattern, err)
	}
	set := make(map[string]struct{}, len(pkg.Imports))
	for _, imp := range pkg.Imports {
		set[imp] = struct{}{}
	}
	return set
}

func assertImportPresent(t *testing.T, pkg string, imports map[string]struct{}, want string) {
	t.Helper()
	if _, ok := imports[want]; !ok {
		t.Fatalf("%s must import %s (dual-plane economics DAG)", pkg, want)
	}
}

func assertImportAbsent(t *testing.T, pkg string, imports map[string]struct{}, forbid string) {
	t.Helper()
	if _, ok := imports[forbid]; ok {
		t.Fatalf("%s must not import %s (dual-plane economics DAG)", pkg, forbid)
	}
}
