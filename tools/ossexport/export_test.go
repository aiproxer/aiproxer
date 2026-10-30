package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestExportIsDeterministicForOneRevision proves two exports of one revision
// produce byte-identical archives (requirement 4.2).
func TestExportIsDeterministicForOneRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")

	first, err := f.export(sha, filepath.Join(t.TempDir(), "first.tar"))
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	second, err := f.export(sha, filepath.Join(t.TempDir(), "second.tar"))
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("digests differ: %s != %s", first.Digest, second.Digest)
	}
	firstBytes := readFile(t, first.Archive)
	secondBytes := readFile(t, second.Archive)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("archives of one revision are not byte-identical")
	}
}

// TestExportUsesRequestedRevision proves the archive carries the requested
// revision, not the working tree and not another commit.
func TestExportUsesRequestedRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.commit("first")
	f.file("second.txt", "second revision\n")
	second := f.commit("second")
	f.file("untracked.txt", "working tree only\n")
	if err := os.WriteFile(filepath.Join(f.root, "untracked.txt"), []byte("working tree only\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	res, err := f.export(first, filepath.Join(t.TempDir(), "first.tar"))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if res.Revision != first {
		t.Fatalf("revision = %s, want %s", res.Revision, first)
	}
	names := archiveFiles(t, res.Archive)
	if contains(names, "second.txt") {
		t.Fatalf("archive of the first revision carries second.txt: %v", names)
	}
	if contains(names, "untracked.txt") {
		t.Fatalf("archive carries an untracked working-tree file: %v", names)
	}

	res, err = f.export(second, filepath.Join(t.TempDir(), "second.tar"))
	if err != nil {
		t.Fatalf("export of the second revision: %v", err)
	}
	names = archiveFiles(t, res.Archive)
	if !contains(names, "second.txt") {
		t.Fatalf("archive of the second revision misses second.txt: %v", names)
	}
	if contains(names, "untracked.txt") {
		t.Fatalf("archive carries an untracked working-tree file: %v", names)
	}
}

// TestExportHonoursExportIgnore proves the archive skips every tracked path
// the repository excludes from publication.
func TestExportHonoursExportIgnore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")

	res, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	names := archiveFiles(t, res.Archive)
	for _, excluded := range []string{"enterprise/internal/secret/secret.go", "docs/secret.md"} {
		if contains(names, excluded) {
			t.Fatalf("archive carries the excluded path %q: %v", excluded, names)
		}
	}
	for _, kept := range []string{"main.go", "main_test.go", "go.mod", "docs/readme.md", "docs/guide.md"} {
		if !contains(names, kept) {
			t.Fatalf("archive misses %q: %v", kept, names)
		}
	}
}

// TestExportRejectsEnterpriseImport proves an exported Go file that imports
// the Enterprise tree fails the export and names the offending file.
func TestExportRejectsEnterpriseImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("app/app.go", "package app\n\nimport \"example.com/fixture/enterprise/internal/secret\"\n\n// Value reads the enterprise value.\nfunc Value() int { return secret.Value() }\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if rej.violations[0].File != "app/app.go" {
		t.Fatalf("violation names %q, want app/app.go", rej.violations[0].File)
	}
	if !strings.Contains(rej.violations[0].Reason, "example.com/fixture/enterprise/internal/secret") {
		t.Fatalf("violation reason %q does not name the Enterprise import", rej.violations[0].Reason)
	}
}

// TestExportRejectsEnterpriseImportInTestFile proves test files are checked
// too: a test-only import of the Enterprise tree still fails the export.
func TestExportRejectsEnterpriseImportInTestFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("app/app_test.go", "package app\n\nimport (\n\t\"testing\"\n\n\t\"example.com/fixture/enterprise/internal/secret\"\n)\n\nfunc TestValue(t *testing.T) {\n\tif secret.Value() != 1 {\n\t\tt.Fatal(\"unexpected value\")\n\t}\n}\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 || rej.violations[0].File != "app/app_test.go" {
		t.Fatalf("violations = %v, want one naming app/app_test.go", rej.violations)
	}
}

// TestExportRejectsLinkToExcludedPath proves an exported text file that links
// to an excluded tracked path fails the export and names both files.
func TestExportRejectsLinkToExcludedPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\nSee the [secret plan](secret.md).\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violation names %q, want docs/readme.md", rej.violations[0].File)
	}
	if !strings.Contains(rej.violations[0].Reason, "docs/secret.md") {
		t.Fatalf("violation reason %q does not name the excluded target", rej.violations[0].Reason)
	}
}

// TestExportRejectsLinkToExcludedDirectory proves a link to an excluded
// directory fails the export even when no excluded file is named directly.
func TestExportRejectsLinkToExcludedDirectory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\nSee the [Enterprise tree](../enterprise/internal/secret).\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if !strings.Contains(rej.violations[0].Reason, "enterprise/internal/secret") {
		t.Fatalf("violation reason %q does not name the excluded target", rej.violations[0].Reason)
	}
}

// TestExportRejectsRawHTMLLinkToExcludedPath proves a raw HTML anchor inside a
// Markdown document that points at an excluded path fails the export.
func TestExportRejectsRawHTMLLinkToExcludedPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n<a href=secret.md>the secret plan</a>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violation names %q, want docs/readme.md", rej.violations[0].File)
	}
	if !strings.Contains(rej.violations[0].Reason, "docs/secret.md") {
		t.Fatalf("violation reason %q does not name the excluded target", rej.violations[0].Reason)
	}
}

// TestExportAcceptsUnrelatedLinks proves dangling links, remote links, and
// plain path strings are not export violations.
func TestExportAcceptsUnrelatedLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n"+
		"A [missing page](missing.md) and a [remote site](https://example.com/x) and a [mail](mailto:ops@example.com).\n")
	f.file("config/catalog.json", "{\n  \"paths\": [\"docs/secret.md\", \"enterprise/internal/secret/secret.go\"]\n}\n")
	sha := f.commit("initial")

	if _, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar")); err != nil {
		t.Fatalf("export rejected unrelated links: %v", err)
	}
}

// TestExportRejectsInjectedRevision proves the revision argument cannot reach
// Git as an option or a command.
func TestExportRejectsInjectedRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")
	marker := filepath.Join(t.TempDir(), "pwned")

	for _, rev := range []string{
		"--upload-pack=touch " + marker,
		"--exec=" + marker,
		"-o" + marker,
		"HEAD; touch " + marker,
		"HEAD\ntouch " + marker,
	} {
		_, err := f.export(rev, filepath.Join(t.TempDir(), "aiproxer.tar"))
		if err == nil {
			t.Fatalf("revision %q was accepted", rev)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatalf("revision %q reached a command", rev)
		}
	}
	if _, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar")); err != nil {
		t.Fatalf("the real revision was rejected: %v", err)
	}
}

// TestExportRejectsUnsafeArchivePaths proves the extractor refuses archive
// entries that would write outside the export tree.
func TestExportRejectsUnsafeArchivePaths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../escape.txt", "nested/../../escape.txt", "/absolute.txt"} {
		dest := t.TempDir()
		_, err := extractTar(tarWithEntry(t, name, "payload"), dest)
		if err == nil {
			t.Fatalf("archive entry %q was accepted", name)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); statErr == nil {
			t.Fatalf("archive entry %q wrote outside the export tree", name)
		}
	}
}

// TestExtractTarRejectsSymlinks proves symlink entries are refused loudly
// instead of being dropped silently.
func TestExtractTarRejectsSymlinks(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	_, err := extractTar(tarWithSymlink(t, "link.txt", "../escape.txt"), dest)
	if err == nil {
		t.Fatal("symlink entry was accepted")
	}
	if !strings.Contains(err.Error(), "link.txt") {
		t.Fatalf("error %q does not name the refused entry", err)
	}
}

// TestExportedTreeBuildsVetsAndTests proves a clean export yields a tree that
// builds, vets, and passes its own unit tests.
func TestExportedTreeBuildsVetsAndTests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")
	tree := filepath.Join(t.TempDir(), "tree")

	res, err := export(t.Context(), options{repo: f.root, rev: sha, out: filepath.Join(t.TempDir(), "aiproxer.tar"), extractDir: tree})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if res.Tree != tree {
		t.Fatalf("tree = %s, want %s", res.Tree, tree)
	}
	if _, err := os.Stat(filepath.Join(tree, "main.go")); err != nil {
		t.Fatalf("extracted tree misses main.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "enterprise")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("extracted tree carries the Enterprise tree: %v", err)
	}
	if err := runGoTool(t.Context(), tree, "build", "./..."); err != nil {
		t.Fatalf("extracted tree does not build: %v", err)
	}
	if err := runGoTool(t.Context(), tree, "vet", "./..."); err != nil {
		t.Fatalf("extracted tree does not vet: %v", err)
	}
	if err := runGoTool(t.Context(), tree, "test", "./..."); err != nil {
		t.Fatalf("extracted tree does not pass its unit tests: %v", err)
	}
}

// TestExportDoesNotOverwriteUnrelatedFiles proves the tool writes only the
// requested archive and its own staging directory.
func TestExportDoesNotOverwriteUnrelatedFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")
	outDir := t.TempDir()
	keep := filepath.Join(outDir, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}

	if _, err := f.export(sha, filepath.Join(outDir, "aiproxer.tar")); err != nil {
		t.Fatalf("export: %v", err)
	}
	content, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("read keep: %v", err)
	}
	if string(content) != "keep\n" {
		t.Fatalf("unrelated file changed: %q", content)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "aiproxer.tar") && entry.Name() != "aiproxer.tar" {
			t.Fatalf("staging file %q survived the export", entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".ossexport-tree-") {
			t.Fatalf("staging directory %q survived the export", entry.Name())
		}
	}
}

// TestExportRefusesANonEmptyExtractDirectory proves the tool never writes
// into a directory that holds unrelated files.
func TestExportRefusesANonEmptyExtractDirectory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")
	extract := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(extract, 0o755); err != nil {
		t.Fatalf("mkdir extract: %v", err)
	}
	keep := filepath.Join(extract, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if _, err := export(t.Context(), options{repo: f.root, rev: sha, out: filepath.Join(t.TempDir(), "aiproxer.tar"), extractDir: extract}); err == nil {
		t.Fatal("a non-empty extract directory was accepted")
	}
	content, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("read keep: %v", err)
	}
	if string(content) != "keep\n" {
		t.Fatalf("unrelated file changed: %q", content)
	}
}

// TestExportRejectsRevisionThatIsNotACommit proves the revision is resolved
// to a commit and never to a mutable working tree.
func TestExportRejectsRevisionThatIsNotACommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.commit("initial")
	if _, err := f.export("no-such-revision", filepath.Join(t.TempDir(), "aiproxer.tar")); err == nil {
		t.Fatal("an unknown revision was accepted")
	}
}

// TestExportRejectsDirectoryOutput proves the tool refuses to publish over a
// directory.
func TestExportRejectsDirectoryOutput(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sha := f.commit("initial")
	out := filepath.Join(t.TempDir(), "aiproxer.tar")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if _, err := f.export(sha, out); err == nil {
		t.Fatal("a directory was accepted as the archive path")
	}
}

// TestExtractTarKeepsExecutableBit proves executable files stay executable in
// the extracted tree.
func TestExtractTarKeepsExecutableBit(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("#!/bin/sh\necho ok\n")
	if err := tw.WriteHeader(&tar.Header{Name: "bin/run.sh", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if _, err := extractTar(bytes.NewReader(buf.Bytes()), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable bit lost: %v", info.Mode())
	}
}

// TestExportUsesTheRevisionModulePath proves the Enterprise import check
// anchors on the module path the revision declares, not on a hard-coded name.
func TestExportUsesTheRevisionModulePath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("go.mod", "module example.com/renamed/fixture\n\ngo 1.26\n")
	f.file("app/app.go", "package app\n\nimport \"example.com/renamed/fixture/enterprise/internal/secret\"\n\n// Value reads the enterprise value.\nfunc Value() int { return secret.Value() }\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if !strings.Contains(rej.violations[0].Reason, "example.com/renamed/fixture/enterprise") {
		t.Fatalf("violation reason %q does not name the renamed module import", rej.violations[0].Reason)
	}
}

// TestExportReportIsStable proves the disposition of a rejected export is
// deterministic and names every offending file.
func TestExportReportIsStable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("app/app.go", "package app\n\nimport \"example.com/fixture/enterprise/internal/secret\"\n\n// Value reads the enterprise value.\nfunc Value() int { return secret.Value() }\n")
	f.file("zebra/zebra.go", "package zebra\n\nimport \"example.com/fixture/enterprise/internal/secret\"\n\n// Value reads the enterprise value.\nfunc Value() int { return secret.Value() }\n")
	f.file("docs/readme.md", "# Fixture\n\nSee the [secret plan](secret.md).\n")
	sha := f.commit("initial")

	_, first := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	_, second := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if first == nil || second == nil {
		t.Fatal("expected a rejection")
	}
	if first.Error() != second.Error() {
		t.Fatalf("rejection is not deterministic:\n%s\n---\n%s", first, second)
	}
	rej := rejectionOf(t, first)
	files := make([]string, 0, len(rej.violations))
	for _, v := range rej.violations {
		files = append(files, v.File)
	}
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	if !equalStrings(files, sorted) {
		t.Fatalf("violations are not sorted: %v", files)
	}
	if len(files) != 3 {
		t.Fatalf("violations = %v, want app/app.go, docs/readme.md, zebra/zebra.go", files)
	}
}

// TestParseArgsRejectsAnIncompleteCommandLine proves the tool refuses a run
// without an archive path, an unexpected argument, and reports a help request
// as a help request.
func TestParseArgsRejectsAnIncompleteCommandLine(t *testing.T) {
	t.Parallel()
	if _, err := parseArgs(nil, io.Discard); err == nil {
		t.Fatal("a run without -out was accepted")
	}
	if _, err := parseArgs([]string{"-out", "a.tar", "extra"}, io.Discard); err == nil {
		t.Fatal("an unexpected argument was accepted")
	}
	if _, err := parseArgs([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("a help request returned %v, want flag.ErrHelp", err)
	}
	opts, err := parseArgs([]string{"-out", "a.tar"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.rev != "HEAD" {
		t.Fatalf("default revision = %q, want HEAD", opts.rev)
	}
	if opts.out != "a.tar" {
		t.Fatalf("output = %q, want a.tar", opts.out)
	}
}

// TestExportIsDeterministicAcrossAmbientConfiguration proves the archive
// bytes depend on the commit alone and not on the Git configuration of the
// machine that runs the export.
func TestExportIsDeterministicAcrossAmbientConfiguration(t *testing.T) {
	f := newFixture(t)
	sha := f.commit("initial")

	umask := writeGitConfig(t, "[tar]\n\tumask = 0777\n")
	ignored := filepath.Join(t.TempDir(), "attributes")
	if err := os.WriteFile(ignored, []byte("*.go export-ignore\n"), 0o644); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	attributes := writeGitConfig(t, "[core]\n\tattributesFile = "+ignored+"\n")

	t.Setenv("GIT_CONFIG_GLOBAL", umask)
	t.Setenv("GIT_CONFIG_SYSTEM", umask)
	first, err := f.export(sha, filepath.Join(t.TempDir(), "first.tar"))
	if err != nil {
		t.Fatalf("export under a hostile umask: %v", err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", attributes)
	t.Setenv("GIT_CONFIG_SYSTEM", attributes)
	second, err := f.export(sha, filepath.Join(t.TempDir(), "second.tar"))
	if err != nil {
		t.Fatalf("export under a hostile global attributes file: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("the archive digest follows the ambient Git configuration: %s != %s", first.Digest, second.Digest)
	}
	names := archiveFiles(t, first.Archive)
	if !contains(names, "main.go") {
		t.Fatalf("the archive lost a source file: %v", names)
	}
}

// TestExportRefusesALocalAttributesFile proves the tool refuses a clone whose
// local attributes file would change the published tree, because the export
// must be a function of the commit alone.
func TestExportRefusesALocalAttributesFile(t *testing.T) {
	f := newFixture(t)
	sha := f.commit("initial")
	info := filepath.Join(f.root, ".git", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte("*.go export-ignore\n"), 0o644); err != nil {
		t.Fatalf("write info attributes: %v", err)
	}
	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if err == nil {
		t.Fatal("an export that depends on the clone was accepted")
	}
	if !strings.Contains(err.Error(), "info/attributes") {
		t.Fatalf("error %q does not name the local attributes file", err)
	}
}

// TestExportRejectsHTMLEntityEncodedLink proves an HTML attribute that encodes
// its path is a link to that path.
func TestExportRejectsHTMLEntityEncodedLink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n<a href=\"../enterprise&#x2f;internal/secret/secret.go\">private</a>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violation names %q, want docs/readme.md", rej.violations[0].File)
	}
	if !strings.Contains(rej.violations[0].Reason, "enterprise/internal/secret/secret.go") {
		t.Fatalf("violation reason %q does not name the decoded target", rej.violations[0].Reason)
	}
}

// TestExportRejectsLinkAfterUnmatchedBacktick proves an unmatched backtick is
// literal text and does not hide the link that follows it.
func TestExportRejectsLinkAfterUnmatchedBacktick(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\nA stray ` before [the secret plan](secret.md)\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violation names %q, want docs/readme.md", rej.violations[0].File)
	}
}

// TestExportRejectsLinkInHTMLWithBacktickAttribute proves a backtick inside a
// quoted HTML attribute is not Markdown code and does not hide the link.
func TestExportRejectsLinkInHTMLWithBacktickAttribute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n<a title=\"`\" href=\"../enterprise/internal/secret/secret.go\">private</a>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 {
		t.Fatalf("violations = %v, want exactly one", rej.violations)
	}
	if !strings.Contains(rej.violations[0].Reason, "enterprise/internal/secret/secret.go") {
		t.Fatalf("violation reason %q does not name the excluded target", rej.violations[0].Reason)
	}
}

// TestArchiveEnvironmentDisablesTheSystemAttributesFile proves the archive
// subprocess carries the system-attributes switch exactly once, whatever the
// caller set.
func TestArchiveEnvironmentDisablesTheSystemAttributesFile(t *testing.T) {
	t.Setenv("GIT_ATTR_NOSYSTEM", "0")
	count := 0
	for _, entry := range archiveEnvironment() {
		if !strings.HasPrefix(entry, "GIT_ATTR_NOSYSTEM=") {
			continue
		}
		count++
		if entry != "GIT_ATTR_NOSYSTEM=1" {
			t.Fatalf("archive environment carries %q, want GIT_ATTR_NOSYSTEM=1", entry)
		}
	}
	if count != 1 {
		t.Fatalf("GIT_ATTR_NOSYSTEM appears %d times, want exactly one", count)
	}
}

// TestExportIgnoresTheSystemAttributesFile proves the system attributes file of
// the machine cannot change the published tree. The control runs in an
// isolated mount namespace with a private /etc, so the host /etc is never
// touched, and it first shows that the system attributes file is in effect
// there.
func TestExportIgnoresTheSystemAttributesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the control needs a POSIX mount namespace")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare is not available")
	}
	f := newFixture(t)
	f.file("probe.txt", "probe\n")
	sha := f.commit("initial")

	binary := filepath.Join(t.TempDir(), "export-tool")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build the export tool: %v\n%s", err, out)
	}
	fakeEtc := filepath.Join(t.TempDir(), "etc")
	if err := os.MkdirAll(fakeEtc, 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	scratch := t.TempDir()
	control := filepath.Join(scratch, "control.tar")
	toolArchive := filepath.Join(scratch, "tool.tar")
	// The private /etc holds a copy of the real one plus the system
	// attributes file, so the toolchain keeps working and the host /etc is
	// never touched.
	script := fmt.Sprintf(`set -eu
cp -R --no-preserve=ownership /etc/. %s/ 2>/dev/null || true
printf 'probe.txt export-ignore\n' > %s/gitattributes
mount --bind %s /etc
cd %s
env -u GIT_ATTR_NOSYSTEM git archive --format=tar --output=%s %s
%s -repo %s -rev %s -out %s
`, fakeEtc, fakeEtc, fakeEtc, f.root, control, sha, binary, f.root, sha, toolArchive)
	output, err := exec.Command("unshare", "-Urm", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Skipf("the isolated control did not run: %v\n%s", err, output)
	}
	if files := tarEntries(t, control); contains(files, "probe.txt") {
		t.Skip("the system attributes file had no effect in the isolated control")
	}
	if files := tarEntries(t, toolArchive); !contains(files, "probe.txt") {
		t.Fatalf("the export dropped a file that the system attributes file excludes: %v", files)
	}
}

// tarEntries returns the names of the regular files of a tar archive.
func tarEntries(t *testing.T, archive string) []string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = file.Close() }()
	reader := tar.NewReader(file)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			names = append(names, header.Name)
		}
	}
	sort.Strings(names)
	return names
}

// TestExportRejectsLinkWithSingleQuotedTitle proves an inline link with a
// single-quoted title is a link.
func TestExportRejectsLinkWithSingleQuotedTitle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n[secret](secret.md 'title')\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 || rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violations = %v, want one naming docs/readme.md", rej.violations)
	}
}

// TestExportRejectsLinkAfterEscapedBackticks proves escaped backticks are
// literal text and do not make the rest of the line code.
func TestExportRejectsLinkAfterEscapedBackticks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\nA literal \\` [secret](secret.md) \\`\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 || rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violations = %v, want one naming docs/readme.md", rej.violations)
	}
}

// TestExportRejectsLinkAfterCommentWithBacktick proves a backtick inside an
// HTML comment does not pair with a later backtick and hide the link.
func TestExportRejectsLinkAfterCommentWithBacktick(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n<!-- ` comment --> [secret](secret.md) trailing `\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 || rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violations = %v, want one naming docs/readme.md", rej.violations)
	}
}

// TestExportRejectsLinkBetweenIndentedCode proves four-space-indented
// backticks are indented code and not a fence, so the link between them is a
// link.
func TestExportRejectsLinkBetweenIndentedCode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n    ```\n[secret](secret.md)\n    ```\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	rej := rejectionOf(t, err)
	if len(rej.violations) != 1 || rej.violations[0].File != "docs/readme.md" {
		t.Fatalf("violations = %v, want one naming docs/readme.md", rej.violations)
	}
}

// TestExportAcceptsOrdinaryNonLinks proves the ordinary Markdown forms that
// carry no link are not links: a fenced code block, an inline code span, an
// HTML comment, and escaped literal backticks.
func TestExportAcceptsOrdinaryNonLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	document := "# Fixture\n\n" +
		"A fenced block:\n\n" +
		"```\n" +
		"[secret](secret.md)\n" +
		"```\n\n" +
		"An inline span: `[secret](secret.md)` stays literal.\n\n" +
		"A comment: <!-- [secret](secret.md) --> stays a comment.\n\n" +
		"Escaped backticks: \\`not code\\`.\n"
	f.file("docs/readme.md", document)
	sha := f.commit("initial")

	if _, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar")); err != nil {
		t.Fatalf("export rejected ordinary non-links: %v", err)
	}
}

// TestExportRefusesHTMLDocumentWithBaseHref proves a document that carries a
// base element with a destination is refused: the export does not resolve
// base URLs, so it names the file instead of resolving the destinations of
// such a document against the wrong directory.
func TestExportRefusesHTMLDocumentWithBaseHref(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.html", "<html><head><base href=\"../\"></head><body><a href=\"enterprise/internal/secret/secret.go\">private</a></body></html>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if err == nil {
		t.Fatal("a document with a base element was accepted")
	}
	if !strings.Contains(err.Error(), "docs/readme.html") {
		t.Fatalf("error %q does not name the file", err)
	}
	if !strings.Contains(err.Error(), "base") {
		t.Fatalf("error %q does not name the unsupported construct", err)
	}
}

// TestExportRefusesHTMLDocumentWithBaseHrefAfterTheLink proves a base element
// after the link is refused as well.
func TestExportRefusesHTMLDocumentWithBaseHrefAfterTheLink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.html", "<html><body><a href=\"enterprise/internal/secret/secret.go\">private</a><base href=\"../\"></body></html>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if err == nil {
		t.Fatal("a document with a base element was accepted")
	}
	if !strings.Contains(err.Error(), "docs/readme.html") {
		t.Fatalf("error %q does not name the file", err)
	}
}

// TestExportRefusesMarkdownDocumentWithBaseHref proves a base element in the
// raw HTML of a Markdown document is refused as well.
func TestExportRefusesMarkdownDocumentWithBaseHref(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\n<base href=\"../\">\n\n<a href=\"enterprise/internal/secret/secret.go\">private</a>\n")
	sha := f.commit("initial")

	_, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar"))
	if err == nil {
		t.Fatal("a document with a base element was accepted")
	}
	if !strings.Contains(err.Error(), "docs/readme.md") {
		t.Fatalf("error %q does not name the file", err)
	}
}

// TestExportAcceptsDocumentWithoutBaseElement proves that a base element
// without a destination, and a base that only appears in prose or in code,
// are not refused.
func TestExportAcceptsDocumentWithoutBaseElement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.file("docs/readme.md", "# Fixture\n\nA placeholder `<base>/models` stays prose.\n\n<base>\n")
	f.file("docs/readme.html", "<html><head><base target=\"_blank\"></head><body><a href=\"guide.md\">guide</a></body></html>\n")
	sha := f.commit("initial")

	if _, err := f.export(sha, filepath.Join(t.TempDir(), "aiproxer.tar")); err != nil {
		t.Fatalf("export refused a document without a base destination: %v", err)
	}
}

// writeGitConfig writes a Git configuration file and returns its path.
func writeGitConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write git config: %v", err)
	}
	return path
}

// rejectionOf asserts the error is an export rejection and returns it.
func rejectionOf(t *testing.T, err error) *rejectionError {
	t.Helper()
	if err == nil {
		t.Fatal("expected the export to be rejected")
	}
	var rej *rejectionError
	if !errors.As(err, &rej) {
		t.Fatalf("expected a rejection, got %v", err)
	}
	return rej
}

// tarWithEntry builds a one-entry tar archive.
func tarWithEntry(t *testing.T, name, content string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// tarWithSymlink builds a one-entry tar archive holding a symlink.
func tarWithSymlink(t *testing.T, name, target string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Mode: 0o777, Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// readArchiveNames returns the sorted names of the regular files in an archive.
func readArchiveNames(t *testing.T, archive string) []string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = file.Close() }()
	reader := tar.NewReader(file)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			names = append(names, header.Name)
		}
	}
	sort.Strings(names)
	return names
}

// readFile reads a whole file.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// contains reports whether the list holds the value.
func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

// equalStrings reports whether two string lists are equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
