package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalModuleFile_NormalizesCRLF(t *testing.T) {
	t.Parallel()
	lf := []byte("module example.com/m\n\ngo 1.22\n")
	crlf := []byte("module example.com/m\r\n\r\ngo 1.22\r\n")
	crOnly := []byte("module example.com/m\r\rgo 1.22\r")

	if !bytes.Equal(canonicalModuleFile(lf), canonicalModuleFile(crlf)) {
		t.Fatal("CRLF and LF forms must compare equal after canonicalization")
	}
	if !bytes.Equal(canonicalModuleFile(lf), canonicalModuleFile(crOnly)) {
		t.Fatal("CR-only and LF forms must compare equal after canonicalization")
	}
	if bytes.Contains(canonicalModuleFile(crlf), []byte{'\r'}) {
		t.Fatal("canonical form must not retain CR")
	}
}

func TestModuleFilesEqual_CRLFOnlyPasses(t *testing.T) {
	t.Parallel()
	modLF := []byte("module example.com/m\n\ngo 1.22\n")
	modCRLF := []byte("module example.com/m\r\n\r\ngo 1.22\r\n")
	sumLF := []byte("example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0/go.mod h1:def=\n")
	sumCRLF := []byte("example.com/dep v1.0.0 h1:abc=\r\nexample.com/dep v1.0.0/go.mod h1:def=\r\n")

	if err := moduleFilesDrift("go.mod", modCRLF, modLF); err != nil {
		t.Fatalf("CRLF-only go.mod must not count as drift: %v", err)
	}
	if err := moduleFilesDrift("go.sum", sumCRLF, sumLF); err != nil {
		t.Fatalf("CRLF-only go.sum must not count as drift: %v", err)
	}
}

func TestModuleFilesEqual_OneTokenFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current []byte
		tidied  []byte
	}{
		{
			name:    "go.mod require version drift",
			current: []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n"),
			tidied:  []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.1\n"),
		},
		{
			name:    "go.sum checksum token drift",
			current: []byte("example.com/dep v1.0.0 h1:AAAA\n"),
			tidied:  []byte("example.com/dep v1.0.0 h1:BBBB\n"),
		},
		{
			name:    "go.sum line order drift",
			current: []byte("a v1 h1:x\nb v1 h1:y\n"),
			tidied:  []byte("b v1 h1:y\na v1 h1:x\n"),
		},
		{
			name:    "go.mod content with CRLF still detects version drift",
			current: []byte("module example.com/m\r\n\r\ngo 1.22\r\n\r\nrequire example.com/dep v1.0.0\r\n"),
			tidied:  []byte("module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.1\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := moduleFilesDrift("module", tt.current, tt.tidied); err == nil {
				t.Fatal("expected semantic drift to fail")
			}
		})
	}
}

func TestTidyDiffOnlyLineEndings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		diff string
		want bool
	}{
		{
			name: "crlf rewrite of go.sum lines",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"-example.com/dep v1.0.0/go.mod h1:def=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n" +
				"+example.com/dep v1.0.0/go.mod h1:def=\n",
			want: true,
		},
		{
			name: "crlf rewrite with unified-diff context lines",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,4 +1,4 @@\n" +
				" example.com/keep v1.0.0 h1:keep=\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"-example.com/dep v1.0.0/go.mod h1:def=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n" +
				"+example.com/dep v1.0.0/go.mod h1:def=\n" +
				" example.com/tail v1.0.0 h1:tail=\n",
			want: true,
		},
		{
			name: "mixed unchanged and crlf-rewritten hunks across files",
			diff: "" +
				"diff current/go.mod tidy/go.mod\n" +
				"--- current/go.mod\n" +
				"+++ tidy/go.mod\n" +
				"@@ -1,5 +1,5 @@\n" +
				" module example.com/m\n" +
				" \n" +
				"-go 1.22\r\n" +
				"+go 1.22\n" +
				" \n" +
				" require example.com/dep v1.0.0\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -2,3 +2,3 @@\n" +
				" example.com/other v1.0.0 h1:zzz=\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n" +
				" example.com/other v1.0.0/go.mod h1:yyy=\n",
			want: true,
		},
		{
			name: "checksum token change",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:AAAA\n" +
				"+example.com/dep v1.0.0 h1:BBBB\n",
			want: false,
		},
		{
			name: "checksum change surrounded by context still fails",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,3 +1,3 @@\n" +
				" example.com/keep v1.0.0 h1:keep=\n" +
				"-example.com/dep v1.0.0 h1:AAAA\n" +
				"+example.com/dep v1.0.0 h1:BBBB\n" +
				" example.com/tail v1.0.0 h1:tail=\n",
			want: false,
		},
		{
			name: "dependency addition surrounded by context still fails",
			diff: "" +
				"diff current/go.mod tidy/go.mod\n" +
				"--- current/go.mod\n" +
				"+++ tidy/go.mod\n" +
				"@@ -3,3 +3,4 @@\n" +
				" go 1.22\n" +
				" \n" +
				" require example.com/dep v1.0.0\n" +
				"+require example.com/new v1.2.3\n",
			want: false,
		},
		{
			name: "dependency deletion surrounded by context still fails",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,3 +1,2 @@\n" +
				" example.com/keep v1.0.0 h1:keep=\n" +
				"-example.com/drop v1.0.0 h1:drop=\n" +
				" example.com/tail v1.0.0 h1:tail=\n",
			want: false,
		},
		{
			name: "context line outside recognized diff is rejected",
			diff: "" +
				" leading diagnostic\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "space-prefixed diagnostic after diff before hunk is rejected",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				" leading diagnostic before hunk\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "security error is not eol-only",
			diff: "verifying example.com/dep@v1.0.0: checksum mismatch\nSECURITY ERROR\n",
			want: false,
		},
		{
			name: "order drift",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-a v1 h1:x\n" +
				"-b v1 h1:y\n" +
				"+b v1 h1:y\n" +
				"+a v1 h1:x\n",
			want: false,
		},
		{
			name: "windows download prelude then eol-only crlf rewrite",
			diff: "" +
				"go: downloading golang.org/x/sys v0.22.0\n" +
				"go: downloading github.com/example/mod v1.2.3\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1,2 +1,2 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"-example.com/dep v1.0.0/go.mod h1:def=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n" +
				"+example.com/dep v1.0.0/go.mod h1:def=\n",
			want: true,
		},
		{
			name: "malformed download prelude rejected",
			diff: "" +
				"go: downloading only-one-field\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "download with extra fields rejected",
			diff: "" +
				"go: downloading example.com/mod v1.0.0 extra\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "checksum mismatch prelude rejected",
			diff: "" +
				"verifying example.com/dep@v1.0.0: checksum mismatch\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "security error prelude rejected",
			diff: "" +
				"SECURITY ERROR\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "error prelude rejected",
			diff: "" +
				"go: errors parsing go.mod\n" +
				"diff current/go.sum tidy/go.sum\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
		{
			name: "download progress after diff header rejected",
			diff: "" +
				"diff current/go.sum tidy/go.sum\n" +
				"go: downloading example.com/mod v1.0.0\n" +
				"--- current/go.sum\n" +
				"+++ tidy/go.sum\n" +
				"@@ -1 +1 @@\n" +
				"-example.com/dep v1.0.0 h1:abc=\r\n" +
				"+example.com/dep v1.0.0 h1:abc=\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tidyDiffOnlyLineEndings([]byte(tt.diff))
			if got != tt.want {
				t.Fatalf("tidyDiffOnlyLineEndings()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestCheckModuleTidy_CRLFCheckoutPasses(t *testing.T) {
	t.Parallel()
	modRoot := writeTinyModule(t, withCRLFModuleFiles(true))
	if _, err := checkModuleTidy(modRoot); err != nil {
		t.Fatalf("tidy check must pass for CRLF-only module files: %v", err)
	}
	// Source worktree must remain untouched (still CRLF).
	modBytes, err := os.ReadFile(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(modBytes, []byte("\r\n")) {
		t.Fatal("checkModuleTidy must not rewrite source go.mod line endings")
	}
}

func TestCheckModuleTidy_ChecksumDriftFails(t *testing.T) {
	t.Parallel()
	modRoot := writeTinyModule(t, withCRLFModuleFiles(false))
	sumPath := filepath.Join(modRoot, "go.sum")
	sum, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	// Inject an unused sum line tidy will drop — semantic drift, no network.
	corrupted := append(append([]byte{}, sum...), []byte("example.com/nonexistent v0.0.0 h1:DEADBEEFdeadbeefdeadbeefdeadbeefdeadbeef=\n")...)
	if err := os.WriteFile(sumPath, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkModuleTidy(modRoot); err == nil {
		t.Fatal("expected checksum/module drift to fail tidy check")
	}
}

func TestCheckModuleTidy_GoModRequireDriftFails(t *testing.T) {
	t.Parallel()
	modRoot := writeTinyModule(t, withCRLFModuleFiles(false))
	modPath := filepath.Join(modRoot, "go.mod")
	// Empty require block is dropped by tidy — semantic go.mod drift, no network.
	drifted := []byte("module example.com/releasegates/tidycase\n\ngo 1.22\n\nrequire (\n)\n")
	if err := os.WriteFile(modPath, drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkModuleTidy(modRoot); err == nil {
		t.Fatal("expected go.mod require drift to fail tidy check")
	}
}

type tinyModuleOpts struct {
	crlf bool
}

func withCRLFModuleFiles(crlf bool) tinyModuleOpts {
	return tinyModuleOpts{crlf: crlf}
}

func writeTinyModule(t *testing.T, opts tinyModuleOpts) string {
	t.Helper()
	dir := t.TempDir()
	mod := "module example.com/releasegates/tidycase\n\ngo 1.22\n"
	sum := ""
	src := "package tidycase\n\nfunc Hello() string { return \"hi\" }\n"
	if opts.crlf {
		mod = strings.ReplaceAll(mod, "\n", "\r\n")
		sum = strings.ReplaceAll(sum, "\n", "\r\n")
		src = strings.ReplaceAll(src, "\n", "\r\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
