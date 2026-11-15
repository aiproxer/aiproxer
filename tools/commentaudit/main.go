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

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "commentaudit:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("commentaudit", flag.ContinueOnError)
	format := fs.String("format", "markdown", "output: markdown or json")
	offset := fs.Int("offset", 0, "zero-based comment-group offset")
	limit := fs.Int("limit", 100, "maximum groups (1-1000)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *offset < 0 || *limit < 1 || *limit > 1000 {
		return errors.New("offset must be nonnegative and limit must be between 1 and 1000")
	}
	if *format != "json" && *format != "markdown" {
		return fmt.Errorf("unknown format %q", *format)
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
	var all []group
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
		for _, c := range f.Comments {
			position := fset.PositionFor(c.Pos(), false)
			line := position.Line
			parts := make([]string, 0, len(c.List))
			for _, x := range c.List {
				parts = append(parts, x.Text)
			}
			text := strings.Join(parts, "\n")
			symbol := docs[c]
			if symbol == "" {
				for _, d := range f.Decls {
					if fn, ok := d.(*ast.FuncDecl); ok && fn.Pos() <= c.Pos() && c.End() <= fn.End() {
						symbol = fn.Name.Name
						break
					}
				}
			}
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", p, position.Offset, text)))
			all = append(all, group{p, line, fset.PositionFor(c.End(), false).Line, text, symbol, hex.EncodeToString(sum[:]), classify(text, docs[c])})
		}
	}
	r.Total = len(all)
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

func classify(text, doc string) []finding {
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
	if ast.IsExported(doc) {
		add("public_doc", "attached to exported declaration "+doc)
	}
	if len(result) == 0 {
		add("unknown", "no heuristic matched")
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
		if _, err := fmt.Fprintf(w, "## %s:%d\n\nSymbol: %s  \nFingerprint: `%s`\n\n", markdownText(g.Path), g.Line, markdownText(g.Symbol), g.Fingerprint); err != nil {
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
