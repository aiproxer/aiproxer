package toolcall

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNormalizeToolNameMatchesRepairSpellingEquivalence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "read_file", want: "readfile"},
		{in: "read-file", want: "readfile"},
		{in: "READ FILE", want: "readfile"},
		{in: "read\t_file\n", want: "readfile"},
		{in: "read.file", want: "read.file"},
		{in: "read/file", want: "read/file"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeToolName(tc.in); got != tc.want {
				t.Fatalf("NormalizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalToolIdentityPrefersExactThenUniqueNormalized(t *testing.T) {
	t.Parallel()

	catalog := []aipapi.ToolDef{
		{Name: "read_file", Parameters: []byte(`{"type":"object"}`)},
		{Name: "list_directory", Parameters: []byte(`{"type":"object"}`)},
	}
	if name, tool := CanonicalToolIdentity(catalog, "read-file", aipapi.ToolDef{}); name != "read_file" || tool.Name != "read_file" {
		t.Fatalf("unique normalized spelling must resolve to read_file, got %q/%q", name, tool.Name)
	}
	if name, tool := CanonicalToolIdentity(catalog, "read_file", aipapi.ToolDef{}); name != "read_file" || tool.Name != "read_file" {
		t.Fatalf("exact spelling must resolve to read_file, got %q/%q", name, tool.Name)
	}
	if name, tool := CanonicalToolIdentity(catalog, "unknown_tool", aipapi.ToolDef{}); name != "unknown_tool" || tool.Name != "" {
		t.Fatalf("unknown spelling must not invent a tool, got %q/%q", name, tool.Name)
	}

	ambiguous := []aipapi.ToolDef{{Name: "read_file"}, {Name: "read-file"}}
	if name, tool := CanonicalToolIdentity(ambiguous, "READ FILE", aipapi.ToolDef{}); name != "READ FILE" || tool.Name != "" {
		t.Fatalf("ambiguous normalized spelling must not choose, got %q/%q", name, tool.Name)
	}

	supplied := aipapi.ToolDef{Name: "read_file", Parameters: []byte(`{"type":"object"}`)}
	if name, tool := CanonicalToolIdentity(nil, "read_file", supplied); name != "read_file" || string(tool.Parameters) != `{"type":"object"}` {
		t.Fatalf("supplied exact definition must be trusted, got %q/%q", name, tool.Name)
	}
}
