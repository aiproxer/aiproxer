package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestIsThinDelegate verifies AST inspection for strictly thin delegates.
func TestIsThinDelegate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		src  string
		want bool
	}{
		{
			name: "aipfeature.Get call",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) []int { return aipfeature.Get(s.frozen, p) }",
			want: true,
		},
		{
			name: "feature.Get call",
			src:  "package p\nimport \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) []int { return feature.Get(s.frozen, p) }",
			want: true,
		},
		{
			name: "aipfeature.FrozenIdentity call",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) string { return aipfeature.FrozenIdentity(s.frozen, p) }",
			want: true,
		},
		{
			name: "bare Get call in feature package",
			path: "pkg/aipsdk/feature/test.go",
			src:  "package feature\nfunc F(s *S) []int { return Get(s.frozen, p) }",
			want: true,
		},
		{
			name: "bare Get call in non-feature package rejected",
			src:  "package p\nfunc F(s *S) []int { return Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "foreign directory package feature bare Get rejected",
			path: "internal/custom/test.go",
			src:  "package feature\nfunc F(s *S) []int { return Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "evil import suffix spoofing rejected",
			src:  "package p\nimport \"evil.example/pkg/aipsdk/feature\"\nfunc F(s *S) []int { return feature.Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "evil import aliased as aipfeature rejected",
			src:  "package p\nimport aipfeature \"evil.example/pkg/aipsdk/feature\"\nfunc F(s *S) []int { return aipfeature.Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "other package Get call rejected",
			src:  "package p\nimport \"other/pkg\"\nfunc F(s *S) []int { return pkg.Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "other package aliased as feature rejected",
			src:  "package p\nimport feature \"other/pkg\"\nfunc F(s *S) []int { return feature.Get(s.frozen, p) }",
			want: false,
		},
		{
			name: "extra if branch on function without receiver rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) []int {\n\tif s == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with receiver == nil passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with nil == receiver passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif nil == s { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with parenthesized nil return passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return (nil) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with deeply parenthesized nil return passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return (((nil))) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with parenthesized nil in condition passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == (nil) { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with parenthesized receiver in condition passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif (s) == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with parenthesized condition passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif (s == nil) { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with parenthesized condition nil == s passes",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif (nil == s) { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: true,
		},
		{
			name: "nil-safe method delegate with zero int literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() int {\n\tif s == nil { return 0 }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with zero float literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() float64 {\n\tif s == nil { return 0.0 }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with zero string literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() string {\n\tif s == nil { return \"\" }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with zero char literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() rune {\n\tif s == nil { return '\\x00' }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with zero bool literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() bool {\n\tif s == nil { return false }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with empty struct composite literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() T {\n\tif s == nil { return T{} }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with empty slice composite literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return []int{} }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with empty map composite literal rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() map[string]int {\n\tif s == nil { return map[string]int{} }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with named identifier alias rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nvar nilAlias *S = nil\nfunc (s *S) F() *S {\n\tif s == nil { return nilAlias }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with function call rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return getFallback() }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized zero int rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() int {\n\tif s == nil { return (0) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized zero bool rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() bool {\n\tif s == nil { return (false) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized zero string rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() string {\n\tif s == nil { return (\"\") }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized empty struct rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() T {\n\tif s == nil { return (T{}) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized named identifier alias rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nvar nilAlias *S = nil\nfunc (s *S) F() *S {\n\tif s == nil { return (nilAlias) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "nil-safe method delegate with parenthesized function call rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return (getFallback()) }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with != nil rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s != nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with non-receiver nil check rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif other == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with field nil check rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s.frozen == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with compound condition rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil && len(s.frozen) > 0 { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with if-init statement rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif x := 1; s == nil { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with if-else branch rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return nil } else { return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with mutation in guard body rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { s = new(S); return nil }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with function call in guard return rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return getFallback() }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with non-zero literal in guard return rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() int {\n\tif s == nil { return 123 }\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "adversarial method delegate with extra statement before final return rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc (s *S) F() []int {\n\tif s == nil { return nil }\n\tvar extra = 1\n\t_ = extra\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "extra statement rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) []int {\n\tvar x int = 1\n\t_ = x\n\treturn aipfeature.Get(s.frozen, p)\n}",
			want: false,
		},
		{
			name: "non-return body rejected",
			src:  "package p\nfunc F(s *S) {\n\tfor {}\n}",
			want: false,
		},
		{
			name: "multiple return values rejected",
			src:  "package p\nimport aipfeature \"github.com/aiproxer/aiproxer/pkg/aipsdk/feature\"\nfunc F(s *S) ([]int, error) {\n\treturn aipfeature.Get(s.frozen, p), nil\n}",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filePath := tc.path
			if filePath == "" {
				filePath = "test.go"
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, filePath, tc.src, 0)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			var fnDecl *ast.FuncDecl
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fnDecl = fd
					break
				}
			}
			if fnDecl == nil {
				t.Fatalf("no function declaration found in %s", tc.src)
			}
			got := IsThinDelegate(filePath, fnDecl, f)
			if got != tc.want {
				t.Errorf("IsThinDelegate() = %v, want %v", got, tc.want)
			}
		})
	}
}
