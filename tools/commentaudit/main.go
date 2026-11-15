// Command commentaudit inventories tracked Go comments without modifying files.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type finding struct {
	Category string `json:"category"`
	Evidence string `json:"evidence"`
}
type group struct {
	Path        string    `json:"path"`
	Line        int       `json:"line"`
	EndLine     int       `json:"end_line"`
	Text        string    `json:"text"`
	Symbol      string    `json:"symbol"`
	Fingerprint string    `json:"fingerprint"`
	Findings    []finding `json:"findings"`
}
type exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type report struct {
	Offset   int         `json:"offset"`
	Limit    int         `json:"limit"`
	Total    int         `json:"total"`
	Next     *int        `json:"next_offset"`
	Groups   []group     `json:"groups"`
	Excluded []exclusion `json:"excluded"`
}

type bucket int

const (
	bucketCleanup bucket = iota
	bucketJudgment
	bucketSensitive
)

var bucketTitles = [...]string{"Likely cleanup", "Needs judgment", "Protected or sensitive"}

type fileInventory struct {
	path   string
	groups []group
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "commentaudit:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("commentaudit", flag.ContinueOnError)
	format := fs.String("format", "markdown", "output: markdown or json")
	detail := fs.Bool("detail", false, "full Markdown inventory instead of the compact queue")
	offset := fs.Int("offset", 0, "zero-based comment-group offset")
	limit := fs.Int("limit", 100, "maximum groups (1-1000)")
	fileLimit := fs.Int("files", 5, "maximum files with comments (compact default 5; detail and JSON default all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if *offset < 0 || *limit < 1 || *limit > 1000 {
		return errors.New("offset must be nonnegative and limit must be between 1 and 1000")
	}
	if *fileLimit < 0 {
		return errors.New("files must be nonnegative (0 means all)")
	}
	if *format != "json" && *format != "markdown" {
		return fmt.Errorf("unknown format %q", *format)
	}
	compact := *format == "markdown" && !*detail
	if compact && (explicit["offset"] || explicit["limit"]) {
		return errors.New("compact Markdown cannot paginate comment groups; use -detail or -format=json")
	}
	scopes := fs.Args()
	for _, s := range scopes {
		if s == "" || s == "." {
			continue
		}
		if path.IsAbs(s) || strings.Contains(s, `\`) || path.Clean(s) != s || s == ".." || strings.HasPrefix(s, "../") {
			return fmt.Errorf("invalid repository-relative scope %q", s)
		}
	}
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("find git root: %w", err)
	}
	root := strings.TrimSpace(string(rootBytes))
	cmd := exec.Command("git", "ls-files", "-z", "--", "*.go")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	var paths []string
	seen := map[string]bool{}
	matched := make([]bool, len(scopes))
	for _, p := range strings.Split(string(data), "\x00") {
		if p == "" {
			continue
		}
		if seen[p] {
			return fmt.Errorf("duplicate tracked path (unmerged index): %q", p)
		}
		seen[p] = true
		for i, scope := range scopes {
			if inScope(p, []string{scope}) {
				matched[i] = true
			}
		}
		if inScope(p, scopes) {
			paths = append(paths, p)
		}
	}
	for i, ok := range matched {
		if !ok {
			return fmt.Errorf("scope %q matches no tracked Go files", scopes[i])
		}
	}
	sort.Strings(paths)
	r := report{Offset: *offset, Limit: *limit, Groups: []group{}, Excluded: []exclusion{}}
	var entries []fileInventory
	for _, p := range paths {
		if vendorPath(p) {
			r.Excluded = append(r.Excluded, exclusion{p, "vendor"})
			continue
		}
		current := root
		for _, part := range strings.Split(p, "/") {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect %q: %w", p, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in tracked path %q", p)
			}
		}
		source, err := os.ReadFile(current)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, source, parser.ParseComments|parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		if ast.IsGenerated(f) {
			r.Excluded = append(r.Excluded, exclusion{p, "generated"})
			continue
		}
		docs := map[*ast.CommentGroup]string{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Doc != nil {
					docs[n.Doc] = n.Name.Name
				}
			case *ast.GenDecl:
				if n.Doc != nil && len(n.Specs) == 1 {
					switch s := n.Specs[0].(type) {
					case *ast.TypeSpec:
						docs[n.Doc] = s.Name.Name
					case *ast.ValueSpec:
						if len(s.Names) == 1 {
							docs[n.Doc] = s.Names[0].Name
						}
					}
				}
			case *ast.TypeSpec:
				if n.Doc != nil {
					docs[n.Doc] = n.Name.Name
				}
			case *ast.ValueSpec:
				if n.Doc != nil && len(n.Names) == 1 {
					docs[n.Doc] = n.Names[0].Name
				}
			}
			return true
		})
		var groups []group
		for _, c := range f.Comments {
			position := fset.PositionFor(c.Pos(), false)
			line := position.Line
			parts := make([]string, 0, len(c.List))
			for _, x := range c.List {
				parts = append(parts, x.Text)
			}
			text := strings.Join(parts, "\n")
			symbol := docs[c]
			var enclosing *ast.FuncDecl
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Pos() <= c.Pos() && c.End() <= fn.End() {
					if symbol == "" {
						symbol = fn.Name.Name
					}
					enclosing = fn
					break
				}
			}
			inTest := enclosing != nil && strings.HasSuffix(p, "_test.go") && enclosing.Recv == nil && strings.HasPrefix(enclosing.Name.Name, "Test")
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", p, position.Offset, text)))
			groups = append(groups, group{p, line, fset.PositionFor(c.End(), false).Line, text, symbol, hex.EncodeToString(sum[:]), classify(text, docs[c], inTest)})
		}
		if len(groups) == 0 {
			continue
		}
		entries = append(entries, fileInventory{p, groups})
	}
	selected := entries
	if compact || explicit["files"] {
		if *fileLimit > 0 && *fileLimit < len(selected) {
			selected = selected[:*fileLimit]
		}
	}
	var all []group
	for _, fi := range selected {
		all = append(all, fi.groups...)
	}
	r.Total = len(all)
	if compact {
		return compactMarkdown(out, selected, len(entries), r.Total, len(r.Excluded))
	}
	start := min(*offset, len(all))
	end := min(start+*limit, len(all))
	r.Groups = append(r.Groups, all[start:end]...)
	if end < len(all) {
		r.Next = &end
	}
	if *format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	return markdown(out, r)
}

func inScope(p string, scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, s := range scopes {
		if s == "" || s == "." || p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

func vendorPath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == "vendor" {
			return true
		}
	}
	return false
}

func classify(text, doc string, inTest bool) []finding {
	var result []finding
	add := func(category, evidence string) {
		for _, f := range result {
			if f.Category == category {
				return
			}
		}
		result = append(result, finding{category, evidence})
	}
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		lower := strings.ToLower(s)
		if strings.HasPrefix(s, "/*line ") || strings.HasPrefix(s, "go:") || strings.HasPrefix(s, "+build ") || strings.HasPrefix(s, "line ") || strings.HasPrefix(s, "//go:") {
			add("directive", strings.TrimSpace(line))
		}
		if strings.Contains(lower, "nolint") || strings.Contains(lower, "lint:ignore") || strings.Contains(lower, "nosec") {
			add("lint", strings.TrimSpace(line))
		}
		if strings.Contains(lower, "todo") || strings.Contains(lower, "fixme") {
			add("todo", strings.TrimSpace(line))
		}
		if strings.Contains(lower, "warning") || strings.Contains(lower, "caution") || strings.Contains(lower, "danger") || strings.Contains(lower, "important") || strings.Contains(lower, "do not remove") || strings.Contains(lower, "workaround") || strings.Contains(lower, "hack") {
			add("warning", strings.TrimSpace(line))
		}
		if strings.Contains(lower, "copyright") || strings.Contains(lower, "spdx-license-identifier") || strings.Contains(lower, "licensed under") {
			add("license", strings.TrimSpace(line))
		}
		if len(s) >= 3 && strings.Trim(s, "-=*#/ ") == "" {
			add("banner", strings.TrimSpace(line))
		}
		if strings.Contains(lower, "rfc ") || strings.Contains(lower, "https://") || strings.Contains(lower, "http://") || strings.Contains(lower, "upstream") {
			add("external_constraint", strings.TrimSpace(line))
		}
	}
	if inTest && len(result) == 0 && isTestLabel(text) {
		add("test_label", strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "//")))
	}
	if ast.IsExported(doc) {
		add("exported_doc", "attached to exported declaration "+doc)
	}
	if len(result) == 0 {
		add("unknown", "no heuristic matched")
	}
	return result
}

// isTestLabel recognizes only short, single-line label comments. It deliberately
// ignores prose: a sentence or a longer explanation is not a cleanup candidate.
func isTestLabel(text string) bool {
	if strings.ContainsAny(text, "\r\n") {
		return false
	}
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "//"))
	if s == "" || len(s) > 40 || len(strings.Fields(s)) > 5 {
		return false
	}
	if s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	switch s[len(s)-1] {
	case '.', '?', '!', ';':
		return false
	}
	return true
}

func groupBucket(findings []finding) bucket {
	result := bucketCleanup
	for _, f := range findings {
		switch f.Category {
		case "directive", "license", "lint":
			return bucketSensitive
		case "banner", "test_label":
		default:
			result = bucketJudgment
		}
	}
	return result
}

func markdown(w io.Writer, r report) error {
	if _, err := fmt.Fprintf(w, "# Go comment inventory\n\nHeuristics are review hints, not authoritative. No files are modified.\n\nGroups: %d; offset: %d; limit: %d; next: %v\n\n", r.Total, r.Offset, r.Limit, func() any {
		if r.Next == nil {
			return "none"
		}
		return *r.Next
	}()); err != nil {
		return err
	}
	for _, g := range r.Groups {
		if _, err := fmt.Fprintf(w, "## %s:%d\n\nSymbol: %s  \nFingerprint: `%s`\n\n", markdownCode(g.Path), g.Line, markdownText(g.Symbol), g.Fingerprint); err != nil {
			return err
		}
		for _, f := range g.Findings {
			if _, err := fmt.Fprintf(w, "- **%s**: %s\n", markdownText(f.Category), markdownText(f.Evidence)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		for _, line := range strings.Split(g.Text, "\n") {
			if _, err := fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(line, "\r", `\r`)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "## Excluded files\n\n"); err != nil {
		return err
	}
	for _, e := range r.Excluded {
		if _, err := fmt.Fprintf(w, "- %s (%s)\n", markdownText(e.Path), markdownText(e.Reason)); err != nil {
			return err
		}
	}
	return nil
}

func compactMarkdown(w io.Writer, files []fileInventory, totalFiles, groups, excluded int) error {
	if _, err := fmt.Fprintf(w, "# Go comment cleanup queue\n\nHeuristics are review hints, not authoritative. No files are modified.\nRead the source before acting; compact output never quotes comment text.\n\nFiles: %d selected; %d remaining (%d with comments). Groups: %d in selected files. Excluded files: %d.\n\n", len(files), totalFiles-len(files), totalFiles, groups, excluded); err != nil {
		return err
	}
	for b, title := range bucketTitles {
		if _, err := fmt.Fprintf(w, "## %s\n\n", title); err != nil {
			return err
		}
		empty := true
		for _, fi := range files {
			var ranges []string
			hints := map[string]bool{}
			count := 0
			for _, g := range fi.groups {
				if groupBucket(g.Findings) != bucket(b) {
					continue
				}
				count++
				if g.Line == g.EndLine {
					ranges = append(ranges, fmt.Sprint(g.Line))
				} else {
					ranges = append(ranges, fmt.Sprintf("%d-%d", g.Line, g.EndLine))
				}
				for _, f := range g.Findings {
					hints[f.Category] = true
				}
			}
			if count == 0 {
				continue
			}
			empty = false
			names := make([]string, 0, len(hints))
			for name := range hints {
				names = append(names, name)
			}
			sort.Strings(names)
			label := "groups"
			if count == 1 {
				label = "group"
			}
			if _, err := fmt.Fprintf(w, "- %s: %d %s, lines %s; hints: %s\n", markdownCode(fi.path), count, label, strings.Join(ranges, ", "), strings.Join(names, ", ")); err != nil {
				return err
			}
		}
		if empty {
			if _, err := fmt.Fprint(w, "_None._\n"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// markdownCode renders untrusted text as a Markdown code span, so it stays readable and cannot form Markdown or HTML syntax.
func markdownCode(s string) string {
	s = strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s)
	delim := "`"
	for strings.Contains(s, delim) {
		delim += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		s = " " + s + " "
	}
	return delim + s + delim
}

// markdownText encodes untrusted text as entities so it cannot form Markdown or HTML syntax.
func markdownText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' {
			b.WriteRune(r)
		} else {
			fmt.Fprintf(&b, "&#%d;", r)
		}
	}
	return b.String()
}
