package kirospec

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// DeliveryBudget holds the PR rows of the Budgets table in
// .kiro/steering/delivery.md. tools/changesize reports against it, and
// ValidatePolicyDocs keeps the table in step with it.
type DeliveryBudget struct {
	Files           int // changed files per PR
	ProductionLines int // added non-test Go lines per PR
	TestRatio       int // added test lines per added production line
}

// PRBudget is the advisory per-PR budget.
var PRBudget = DeliveryBudget{Files: 40, ProductionLines: 1500, TestRatio: 5}

// MaxChangedGoFiles is the hard change-size gate stated in AGENTS.md; commit
// hooks, CI and internal/qa enforce it.
const MaxChangedGoFiles = 100

// policyDocuments are the agent-instruction documents outside
// .kiro/steering/ whose references and budgets ValidatePolicyDocs checks.
var policyDocuments = []string{
	"AGENTS.md",
	".kiro/AGENTS.md",
	"docs/agent-workflow.md",
	"docs/agent-handoffs.md",
	"docs/agent-closeout.md",
	"docs/development-iteration.md",
}

// forbiddenSkillRoots are named in the instructions as places a skill must not
// be copied to; they are expected to be absent and are not references.
var forbiddenSkillRoots = map[string]bool{
	".codex/skills": true, ".cursor/skills": true, ".kiro/skills": true, ".opencode/skills": true, ".pi/skills": true,
}

var (
	backtickSpan   = regexp.MustCompile("`([^`\n]+)`")
	markdownLink   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	fencedMake     = regexp.MustCompile(`^\s*(?:\$\s+)?make\s+([A-Za-z0-9][A-Za-z0-9_.-]*)`)
	makeRule       = regexp.MustCompile(`^([A-Za-z0-9_.-][A-Za-z0-9_. -]*):([^=]|$)`)
	symbolRef      = regexp.MustCompile(`^(.*/[a-z0-9_]+)\.([A-Z][A-Za-z0-9_]*)$`)
	skillName      = regexp.MustCompile(`^(golang|kiro|aip)-[a-z0-9]+(-[a-z0-9]+)*$`)
	historyFooter  = regexp.MustCompile(`(?i)^\s*[_*]*(last[ -]?)?(updated|reason|changelog)[_*]*\s*:|^#{1,6}\s*(changelog|change log|revision history|history)\s*$`)
	goVersion      = regexp.MustCompile(`\bGo 1\.\d+|\bgo1\.\d+`)
	componentCount = regexp.MustCompile(`(?i)\b\d+\s+(registered\s+|persistence\s+|production\s+)*(components|persistence families)\b`)
	goFileGate     = regexp.MustCompile("(\\d+)(?: `\\*\\.go` files|-Go-file)")
	specRow        = regexp.MustCompile("(?m)^\\| Spec \\|.*?at most (\\d+) requirement areas and (\\d+) acceptance criteria; `design\\.md` at most (\\d+) lines; at most (\\d+) leaf tasks")
	prRow          = regexp.MustCompile(`(?m)^\| PR \|.*?~?([\d,]+) lines of non-test Go and ~?(\d+) changed files`)
	testsRow       = regexp.MustCompile(`(?m)^\| Tests in a feature PR \|.*?~?(\d+(?:\.\d+)?)x`)
	headingLine    = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	explicitAnchor = regexp.MustCompile(`<a\b[^>]*\b(?:name|id)\s*=\s*"([^"]+)"`)
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
	imageText      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkText       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	emphasisStar   = regexp.MustCompile(`\*+`)
	emphasisTilde  = regexp.MustCompile(`~~?`)
	emphasisUnder  = regexp.MustCompile(`(^|[\s(])_([^_\s][^_]*)_($|[\s).,;:!?])`)
)

// pathSuffixes are file formats that make an inline span a path even when its
// first component is absent: a misspelt path, not prose.
var pathSuffixes = []string{".go", ".md", ".sh", ".ps1", ".yml", ".yaml", ".json"}

// ValidatePolicyDocs checks the delivery budget table and selected hygiene
// and references in the root and Kiro AGENTS.md, steering and agent docs.
// Inline paths are checked when their first component exists or they name a
// file format in pathSuffixes; Markdown links are checked for a destination
// file and, when the destination is Markdown, for its heading anchor.
func ValidatePolicyDocs(root string) []string {
	steering, err := filepath.Glob(filepath.Join(root, ".kiro", "steering", "*.md"))
	if err != nil || len(steering) == 0 {
		return []string{fmt.Sprintf("no steering documents under %s", filepath.Join(root, ".kiro", "steering"))}
	}
	targets, err := makeTargets(filepath.Join(root, "Makefile"))
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string
	docs := make([]string, 0, len(policyDocuments)+len(steering))
	for _, rel := range policyDocuments {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, statErr := os.Stat(path); statErr != nil {
			if !exportOmitted(filepath.Join(root, ".kiro", "specs"), path) {
				errs = append(errs, fmt.Sprintf("%s: %v", rel, statErr))
			}
			continue
		}
		docs = append(docs, path)
	}
	docs = append(docs, steering...)
	anchors := anchorCache{}
	for _, path := range docs {
		errs = append(errs, validatePolicyDoc(root, path, targets, anchors)...)
	}
	errs = append(errs, validateDeliveryTable(root)...)
	sort.Strings(errs)
	return errs
}

func validatePolicyDoc(root, path string, targets map[string]bool, anchors anchorCache) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}
	rel := relativeSlash(root, path)
	steering := strings.HasPrefix(rel, ".kiro/steering/")
	instructions := steering || filepath.Base(rel) == "AGENTS.md"
	var errs []string
	report := func(line int, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("%s:%d: %s", rel, line, fmt.Sprintf(format, args...)))
	}
	fenced := false
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			if m := fencedMake.FindStringSubmatch(line); m != nil && !targets[m[1]] {
				report(n, "make target %q is not defined in the Makefile", m[1])
			}
			continue
		}
		if instructions && historyFooter.MatchString(line) {
			report(n, "history/changelog footer; git tracks history")
		}
		if steering && goVersion.MatchString(line) {
			report(n, "copied Go version; go.mod pins the toolchain")
		}
		if steering && componentCount.MatchString(line) {
			report(n, "copied component count; derive it from its executable inventory")
		}
		if rel == "AGENTS.md" && strings.Contains(line, "Change-size gate") {
			if m := goFileGate.FindStringSubmatch(line); m != nil && m[1] != strconv.Itoa(MaxChangedGoFiles) {
				report(n, "change-size gate %s differs from MaxChangedGoFiles %d", m[1], MaxChangedGoFiles)
			}
		}
		for _, m := range backtickSpan.FindAllStringSubmatch(line, -1) {
			if msg := checkSpan(root, filepath.Dir(path), m[1], targets); msg != "" {
				report(n, "%s", msg)
			}
		}
		for _, m := range markdownLink.FindAllStringSubmatch(line, -1) {
			if msg := checkLink(root, path, m[1], anchors); msg != "" {
				report(n, "%s", msg)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %v", rel, err))
	}
	return errs
}

func validateDeliveryTable(root string) []string {
	path := filepath.Join(root, ".kiro", "steering", "delivery.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}
	text := string(data)
	var errs []string
	compare := func(row string, got []string, want ...int) {
		if got == nil {
			errs = append(errs, fmt.Sprintf(".kiro/steering/delivery.md: Budgets table %q row not found in its checked form", row))
			return
		}
		for i, w := range want {
			if strings.ReplaceAll(got[i+1], ",", "") != strconv.Itoa(w) {
				errs = append(errs, fmt.Sprintf(".kiro/steering/delivery.md: Budgets table %q row states %s, the enforced value is %d", row, got[i+1], w))
			}
		}
	}
	compare("Spec", specRow.FindStringSubmatch(text), SliceBudget.Areas, SliceBudget.Criteria, SliceBudget.Design, SliceBudget.Tasks)
	compare("PR", prRow.FindStringSubmatch(text), PRBudget.ProductionLines, PRBudget.Files)
	compare("Tests in a feature PR", testsRow.FindStringSubmatch(text), PRBudget.TestRatio)
	return errs
}

// checkSpan validates one inline-code span: `make <target>`, a repository
// skill name, or a repository path (optionally ending in .Symbol).
func checkSpan(root, docDir, span string, targets map[string]bool) string {
	if rest, ok := strings.CutPrefix(span, "make "); ok {
		target := strings.Fields(rest)
		if len(target) > 0 && !strings.ContainsAny(target[0], "$<[") && !targets[target[0]] {
			return fmt.Sprintf("make target %q is not defined in the Makefile", target[0])
		}
		return ""
	}
	if skillName.MatchString(span) {
		if _, err := os.Stat(filepath.Join(root, ".agents", "skills", span, "SKILL.md")); err != nil {
			return fmt.Sprintf("skill %q is not in .agents/skills/", span)
		}
		return ""
	}
	return checkPath(root, docDir, span)
}

func checkPath(root, docDir, span string) string {
	ref := strings.TrimSuffix(strings.TrimSuffix(span, "..."), "()")
	if !strings.Contains(ref, "/") || strings.ContainsAny(ref, " \t*<>{}$=:?[") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "-") {
		return ""
	}
	var symbol string
	if m := symbolRef.FindStringSubmatch(ref); m != nil {
		ref, symbol = m[1], m[2]
	}
	ref = strings.TrimSuffix(ref, "/")
	if forbiddenSkillRoots[ref] {
		return ""
	}
	first, _, _ := strings.Cut(ref, "/")
	recognized, missingSymbol := false, false
	for _, base := range []string{root, docDir} {
		if _, err := os.Stat(filepath.Join(base, first)); err != nil {
			continue
		}
		recognized = true
		target := filepath.Join(base, filepath.FromSlash(ref))
		if _, err := os.Stat(target); err != nil {
			if exportOmitted(filepath.Join(root, ".kiro", "specs"), target) {
				return ""
			}
			continue
		}
		if symbol != "" && !declaresSymbol(target, symbol) {
			missingSymbol = true
			continue
		}
		return ""
	}
	if missingSymbol {
		return fmt.Sprintf("%s does not declare %s", ref, symbol)
	}
	if recognized || namesKnownFile(ref) {
		return fmt.Sprintf("path %q does not exist", span)
	}
	return ""
}

// namesKnownFile reports whether ref ends in a repository file format.
func namesKnownFile(ref string) bool {
	for _, suffix := range pathSuffixes {
		if strings.HasSuffix(ref, suffix) {
			return true
		}
	}
	return false
}

func checkLink(root, docPath, target string, anchors anchorCache) string {
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return ""
	}
	if fragment, sameDocument := strings.CutPrefix(target, "#"); sameDocument {
		return checkAnchor(docPath, "", fragment, anchors)
	}
	link, fragment, _ := strings.Cut(target, "#")
	path := filepath.Join(filepath.Dir(docPath), filepath.FromSlash(link))
	if _, err := os.Stat(path); err != nil {
		if exportOmitted(filepath.Join(root, ".kiro", "specs"), path) {
			return ""
		}
		return fmt.Sprintf("link target %q does not exist", link)
	}
	if fragment == "" || !strings.HasSuffix(link, ".md") {
		return ""
	}
	return checkAnchor(path, link, fragment, anchors)
}

func checkAnchor(path, link, fragment string, anchors anchorCache) string {
	if fragment == "" {
		return ""
	}
	headings, ok := anchors.get(path)
	if !ok || headings[fragment] {
		return ""
	}
	if link == "" {
		return fmt.Sprintf("anchor %q is not a heading in this document", "#"+fragment)
	}
	return fmt.Sprintf("anchor %q is not a heading in %q", "#"+fragment, link)
}

// anchorCache maps a document to its anchors; a nil entry means unreadable.
type anchorCache map[string]map[string]bool

func (c anchorCache) get(path string) (map[string]bool, bool) {
	if set, ok := c[path]; ok {
		return set, set != nil
	}
	set, err := markdownAnchors(path)
	if err != nil {
		c[path] = nil
		return nil, false
	}
	c[path] = set
	return set, true
}

// markdownAnchors collects GitHub-style slugs of ATX headings and explicit
// <a name|id> anchors, ignoring fenced code blocks.
func markdownAnchors(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	anchors := make(map[string]bool)
	counts := make(map[string]int)
	used := make(map[string]int)
	fenced := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range explicitAnchor.FindAllStringSubmatch(line, -1) {
			anchors[m[1]] = true
		}
		m := headingLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		anchors[uniqueSlug(headingText(m[1]), counts, used)] = true
	}
	return anchors, nil
}

// headingText approximates a heading's rendered text for slugging.
func headingText(text string) string {
	text = htmlTag.ReplaceAllString(text, "")
	text = imageText.ReplaceAllString(text, "$1")
	text = linkText.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "`", "")
	text = emphasisStar.ReplaceAllString(text, "")
	text = emphasisTilde.ReplaceAllString(text, "")
	text = emphasisUnder.ReplaceAllString(text, "$1$2$3")
	return text
}

// uniqueSlug returns the GitHub slug for a heading and suffixes duplicates.
func uniqueSlug(text string, counts, used map[string]int) string {
	base := githubSlug(text)
	slug := base
	for used[slug] != 0 {
		counts[base]++
		slug = fmt.Sprintf("%s-%d", base, counts[base])
	}
	used[slug] = 1
	return slug
}

// githubSlug reproduces GitHub's anchor slug: lowercase, each space becomes a
// hyphen, and punctuation and symbols are dropped except - and _.
func githubSlug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func declaresSymbol(dir, symbol string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == symbol {
					return true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.Name == symbol {
							return true
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.Name == symbol {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

func makeTargets(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Makefile: %w", err)
	}
	targets := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		m := makeRule.FindStringSubmatch(line)
		if m == nil || strings.HasPrefix(m[1], ".") {
			continue
		}
		for _, name := range strings.Fields(m[1]) {
			targets[name] = true
		}
	}
	return targets, nil
}

func relativeSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
