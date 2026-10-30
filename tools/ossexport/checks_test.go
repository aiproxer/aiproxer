package main

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestLinkTargetsFindsRepositoryLinkShapes proves the scanner recognises the
// link shapes the repository actually uses and ignores prose and code.
func TestLinkTargetsFindsRepositoryLinkShapes(t *testing.T) {
	t.Parallel()
	document := "" +
		"# Title\n" +
		"\n" +
		"A [guide](docs/guide.md) and an ![image](docs/logo.png).\n" +
		"\n" +
		"[reference]: docs/reference.md\n" +
		"\n" +
		"Inline code `[not a link](nope.md)` stays prose.\n" +
		"\n" +
		"```\n" +
		"[fenced](fenced.md)\n" +
		"```\n" +
		"\n" +
		"A [titled](docs/titled.md \"Title\") link.\n"

	got, err := linkTargets(document, linkFormatMarkdown)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	want := []string{"docs/guide.md", "docs/logo.png", "docs/reference.md", "docs/titled.md"}
	sort.Strings(got)
	if !equalStrings(got, want) {
		t.Fatalf("link targets = %v, want %v", got, want)
	}
}

// TestLinkTargetsFindsHTMLAttributes proves HTML link attributes are scanned.
func TestLinkTargetsFindsHTMLAttributes(t *testing.T) {
	t.Parallel()
	page := `<a href="docs/guide.md">guide</a><img src="docs/logo.png">`
	got, err := linkTargets(page, linkFormatHTML)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	sort.Strings(got)
	want := []string{"docs/guide.md", "docs/logo.png"}
	if !equalStrings(got, want) {
		t.Fatalf("link targets = %v, want %v", got, want)
	}
}

// TestResolveLinkTarget proves link destinations resolve to repository paths
// and that non-repository destinations are dropped.
func TestResolveLinkTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source string
		raw    string
		want   string
		ok     bool
	}{
		{name: "sibling", source: "docs/readme.md", raw: "guide.md", want: "docs/guide.md", ok: true},
		{name: "parent", source: "docs/readme.md", raw: "../README.md", want: "README.md", ok: true},
		{name: "root relative", source: "docs/readme.md", raw: "/docs/guide.md", want: "docs/guide.md", ok: true},
		{name: "fragment", source: "docs/readme.md", raw: "guide.md#section", want: "docs/guide.md", ok: true},
		{name: "query", source: "docs/readme.md", raw: "guide.md?plain=1", want: "docs/guide.md", ok: true},
		{name: "query and fragment", source: "docs/readme.md", raw: "guide.md?plain=1#section", want: "docs/guide.md", ok: true},
		{name: "percent encoding", source: "docs/readme.md", raw: "my%20guide.md", want: "docs/my guide.md", ok: true},
		{name: "angle brackets", source: "docs/readme.md", raw: "<guide.md>", want: "docs/guide.md", ok: true},
		{name: "remote", source: "docs/readme.md", raw: "https://example.com/x", ok: false},
		{name: "protocol relative", source: "docs/readme.md", raw: "//example.com/x", ok: false},
		{name: "mail", source: "docs/readme.md", raw: "mailto:ops@example.com", ok: false},
		{name: "anchor only", source: "docs/readme.md", raw: "#section", ok: false},
		{name: "escaping", source: "docs/readme.md", raw: "../../outside.md", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveLinkTarget(tc.source, tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (target %q)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("target = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestForbiddenImport proves only imports that reach the Enterprise tree are
// refused, and that the module path of the revision decides the boundary.
func TestForbiddenImport(t *testing.T) {
	t.Parallel()
	const modulePath = "example.com/fixture"
	cases := []struct {
		name       string
		imported   string
		module     string
		enterprise []string
		want       bool
	}{
		{name: "enterprise package", imported: "example.com/fixture/enterprise", module: modulePath, want: true},
		{name: "enterprise descendant", imported: "example.com/fixture/enterprise/internal/secret", module: modulePath, want: true},
		{name: "enterprise nested descendant", imported: "example.com/fixture/enterprise/internal/secret/deep", module: modulePath, want: true},
		{name: "public package", imported: "example.com/fixture/pkg/aipapi", module: modulePath, want: false},
		{name: "bare module", imported: "example.com/fixture", module: modulePath, want: false},
		{name: "enterprise fixture", imported: "example.com/fixture/testdata/enterprise_module", module: modulePath, want: false},
		{name: "third party", imported: "example.com/other/enterprise/thing", module: modulePath, want: false},
		{name: "standard library", imported: "net/http", module: modulePath, want: false},
		{name: "enterprise module", imported: "example.com/enterprise/module/pkg", module: modulePath, enterprise: []string{"example.com/enterprise/module"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := forbiddenImport(tc.imported, tc.module, tc.enterprise); got != tc.want {
				t.Fatalf("forbiddenImport(%q) = %v, want %v", tc.imported, got, tc.want)
			}
		})
	}
}

// TestExcludedTargetLookup proves only tracked-and-excluded paths are
// violations and that a directory holding excluded files is one too.
func TestExcludedTargetLookup(t *testing.T) {
	t.Parallel()
	excluded := []string{
		"docs/secret.md",
		"enterprise/internal/secret/other.go",
		"enterprise/internal/secret/secret.go",
	}
	sort.Strings(excluded)
	included := []string{"docs/guide.md", "docs/readme.md", "go.mod", "main.go"}
	sort.Strings(included)

	cases := []struct {
		target string
		want   bool
	}{
		{target: "docs/secret.md", want: true},
		{target: "enterprise/internal/secret", want: true},
		{target: "enterprise", want: true},
		{target: "docs/guide.md", want: false},
		{target: "docs", want: false},
		{target: "missing.md", want: false},
		{target: "enterprise/other/file.go", want: false},
	}
	for _, tc := range cases {
		if got := excludedTarget(excluded, included, tc.target); got != tc.want {
			t.Fatalf("excludedTarget(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

// TestLinkTargetsFindsUnquotedHTMLAttributes proves an HTML link with an
// unquoted attribute value is a link.
func TestLinkTargetsFindsUnquotedHTMLAttributes(t *testing.T) {
	t.Parallel()
	page := "<a href=../enterprise/guide.md>private</a><img src=docs/logo.png>"
	got, err := linkTargets(page, linkFormatHTML)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	sort.Strings(got)
	want := []string{"../enterprise/guide.md", "docs/logo.png"}
	if !equalStrings(got, want) {
		t.Fatalf("link targets = %v, want %v", got, want)
	}
}

// TestLinkTargetsFindsRawHTMLInMarkdown proves a raw HTML anchor inside a
// Markdown document is a link.
func TestLinkTargetsFindsRawHTMLInMarkdown(t *testing.T) {
	t.Parallel()
	document := "# Title\n\n<a href=\"../enterprise/guide.md\">private</a>\n\n<a href=../enterprise/other.md>other</a>\n"
	got, err := linkTargets(document, linkFormatMarkdown)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	sort.Strings(got)
	want := []string{"../enterprise/guide.md", "../enterprise/other.md"}
	if !equalStrings(got, want) {
		t.Fatalf("link targets = %v, want %v", got, want)
	}
}

// TestLinkTargetsIgnoresHTMLComments proves a commented-out link is not a
// link.
func TestLinkTargetsIgnoresHTMLComments(t *testing.T) {
	t.Parallel()
	page := "<!-- <a href=\"../enterprise/guide.md\">private</a> -->\n"
	got, err := linkTargets(page, linkFormatHTML)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("link targets = %v, want none", got)
	}
}

// TestLinkTargetsDecodesHTMLEntities proves an HTML attribute that encodes its
// path is read as the path it encodes.
func TestLinkTargetsDecodesHTMLEntities(t *testing.T) {
	t.Parallel()
	page := "<a href=\"../enterprise&#x2f;guide.md\">private</a>"
	got, err := linkTargets(page, linkFormatHTML)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	want := []string{"../enterprise/guide.md"}
	if !equalStrings(got, want) {
		t.Fatalf("link targets = %v, want %v", got, want)
	}
}

// TestLinkTargetsKeepsAnUnmatchedBacktick proves an unmatched backtick is
// literal text, so the link that follows it is still a link.
func TestLinkTargetsKeepsAnUnmatchedBacktick(t *testing.T) {
	t.Parallel()
	got, err := linkTargets("A stray ` before [the secret plan](secret.md)", linkFormatMarkdown)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	if !equalStrings(got, []string{"secret.md"}) {
		t.Fatalf("link targets = %v, want [secret.md]", got)
	}
}

// TestLinkTargetsKeepsRawHTMLAttributes proves the contents of a raw HTML tag
// are not Markdown code, so a backtick in an attribute value does not hide
// the link of that tag.
func TestLinkTargetsKeepsRawHTMLAttributes(t *testing.T) {
	t.Parallel()
	got, err := linkTargets("<a title=\"`\" href=\"../enterprise/guide.md\">private</a>", linkFormatMarkdown)
	if err != nil {
		t.Fatalf("linkTargets: %v", err)
	}
	if !equalStrings(got, []string{"../enterprise/guide.md"}) {
		t.Fatalf("link targets = %v, want [../enterprise/guide.md]", got)
	}
}

// TestModulePathOf proves the module path comes from the go.mod of the
// exported revision.
func TestModulePathOf(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/go.mod"
	content := "// comment\nmodule example.com/renamed/fixture\n\ngo 1.26\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	got, err := modulePathOf(path)
	if err != nil {
		t.Fatalf("modulePathOf: %v", err)
	}
	if got != "example.com/renamed/fixture" {
		t.Fatalf("module path = %q, want example.com/renamed/fixture", got)
	}
}

// TestModulePathOfRejectsMissingModule proves a tree without a module path is
// refused instead of silently skipping the import check.
func TestModulePathOfRejectsMissingModule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/go.mod"
	if err := os.WriteFile(path, []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if _, err := modulePathOf(path); err == nil {
		t.Fatal("a go.mod without a module path was accepted")
	}
}

// TestLinkFormatForFile proves only link-shaped text formats are scanned.
func TestLinkFormatForFile(t *testing.T) {
	t.Parallel()
	cases := map[string]linkFormat{
		"README.md":           linkFormatMarkdown,
		"notes.markdown":      linkFormatMarkdown,
		"index.html":          linkFormatHTML,
		"page.htm":            linkFormatHTML,
		"config/catalog.json": linkFormatNone,
		"scripts/run.sh":      linkFormatNone,
		"main.go":             linkFormatNone,
	}
	for name, want := range cases {
		if got := linkFormatForFile(name); got != want {
			t.Fatalf("linkFormatForFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestViolationsAreSorted proves the disposition lists offending files in a
// stable order.
func TestViolationsAreSorted(t *testing.T) {
	t.Parallel()
	violations := []violation{
		{File: "zebra/zebra.go", Reason: "imports Enterprise package \"x\""},
		{File: "app/app.go", Reason: "imports Enterprise package \"y\""},
		{File: "docs/readme.md", Reason: "links to excluded path \"docs/secret.md\""},
	}
	sortViolations(violations)
	want := []string{"app/app.go", "docs/readme.md", "zebra/zebra.go"}
	var got []string
	for _, v := range violations {
		got = append(got, v.File)
	}
	if !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestRejectionErrorNamesEveryFile proves the failure message lists every
// offending file with its own reason.
func TestRejectionErrorNamesEveryFile(t *testing.T) {
	t.Parallel()
	err := &rejectionError{violations: []violation{
		{File: "app/app.go", Reason: "imports Enterprise package \"example.com/fixture/enterprise/internal/secret\""},
		{File: "docs/readme.md", Reason: "links to excluded path \"docs/secret.md\""},
	}}
	message := err.Error()
	for _, want := range []string{"app/app.go", "example.com/fixture/enterprise/internal/secret", "docs/readme.md", "docs/secret.md"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q does not mention %q", message, want)
		}
	}
}
