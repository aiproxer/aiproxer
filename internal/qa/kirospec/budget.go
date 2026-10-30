package kirospec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Budget is the slice size of one spec, counted from its Markdown files.
type Budget struct {
	Areas    int // "## Requirement" headings in requirements.md
	Criteria int // top-level numbered items ("1. ") in requirements.md
	Design   int // design.md lines
	Tasks    int // leaf checkboxes in tasks.md
}

// SliceBudget mirrors the Budgets table in .kiro/steering/delivery.md.
var SliceBudget = Budget{Areas: 5, Criteria: 25, Design: 300, Tasks: 12}

// grandfathered holds active specs that predate the budget check. Their
// measured size is a ceiling that may only shrink; lower it when a spec is
// trimmed and delete the entry when the spec fits SliceBudget or is archived.
// New specs are never added here without explicit owner approval: split the
// slice instead. Only the owner may approve adding an entry or raising a ceiling.
var grandfathered = map[string]Budget{
	"billing-financial-safety-contracts":         {Areas: 24, Criteria: 156, Design: 687, Tasks: 113},
	"high-concurrency-performance-hardening":     {Areas: 19, Criteria: 184, Design: 909, Tasks: 52},
	"openai-chatgpt-plan-go-connector-migration": {Areas: 14, Criteria: 129, Design: 511, Tasks: 39},
}

var (
	areaHeading   = regexp.MustCompile(`^#{2,3} +Requirement\b`)
	criterion     = regexp.MustCompile(`^[0-9]+\. `)
	deferred      = regexp.MustCompile(`(?i)^#+ .*deferred`)
	taskCheckbox  = regexp.MustCompile(`^(\s*)- \[[ xX]\]`)
	budgetFields  = []string{"requirement areas", "acceptance criteria", "design.md lines", "leaf tasks"}
	budgetPointer = "see .kiro/steering/delivery.md; split the slice and move the rest to Deferred"
)

// ValidateBudgets checks every active spec against SliceBudget (or its
// grandfathered ceiling) and requires a Deferred section in requirements.md.
func ValidateBudgets(specsRoot string) []string {
	errs, _ := ValidateBudgetsWithOmissions(specsRoot)
	return errs
}

// ValidateBudgetsWithOmissions measures surviving files and reports export omissions separately.
func ValidateBudgetsWithOmissions(specsRoot string) ([]string, []string) {
	entries, err := os.ReadDir(specsRoot)
	if err != nil {
		return []string{fmt.Sprintf("read active specs: %v", err)}, nil
	}
	var errs []string
	var omissions []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "archive" {
			continue
		}
		name := entry.Name()
		omitted, missing := specOmissions(specsRoot, filepath.Join(specsRoot, name))
		omissions = append(omissions, omitted...)
		errs = append(errs, missing...)
		got, hasDeferred, err := Measure(filepath.Join(specsRoot, name))
		if err != nil {
			errs = append(errs, fmt.Sprintf("active/%s: %v", name, err))
			continue
		}
		limit, old := grandfathered[name]
		if !old {
			limit = SliceBudget
			if !hasDeferred && !omittedFile(omitted, "requirements.md") {
				errs = append(errs, fmt.Sprintf("active/%s: requirements.md needs a Deferred section (%s)", name, budgetPointer))
			}
		}
		gotValues := []int{got.Areas, got.Criteria, got.Design, got.Tasks}
		limitValues := []int{limit.Areas, limit.Criteria, limit.Design, limit.Tasks}
		for i, field := range budgetFields {
			if gotValues[i] > limitValues[i] {
				errs = append(errs, fmt.Sprintf("active/%s: %d %s exceeds budget %d (%s)", name, gotValues[i], field, limitValues[i], budgetPointer))
			}
		}
	}
	for name := range grandfathered {
		if _, err := os.Stat(filepath.Join(specsRoot, name)); err != nil {
			omitted, missing := specOmissions(specsRoot, filepath.Join(specsRoot, name))
			if os.IsNotExist(err) && len(missing) == 0 {
				omissions = append(omissions, omitted...)
			} else {
				errs = append(errs, fmt.Sprintf("kirospec: grandfathered spec %s is no longer active; delete its entry", name))
			}
		}
	}
	sort.Strings(errs)
	sort.Strings(omissions)
	return errs, omissions
}

// Measure counts one spec directory. Missing optional files count as zero.
func Measure(dir string) (Budget, bool, error) {
	var b Budget
	requirements, err := readLines(filepath.Join(dir, "requirements.md"))
	if err != nil {
		return b, false, err
	}
	hasDeferred := false
	for _, line := range requirements {
		switch {
		case areaHeading.MatchString(line):
			b.Areas++
		case criterion.MatchString(line):
			b.Criteria++
		case deferred.MatchString(line):
			hasDeferred = true
		}
	}
	design, err := readLines(filepath.Join(dir, "design.md"))
	if err != nil {
		return b, false, err
	}
	b.Design = len(design)
	tasks, err := readLines(filepath.Join(dir, "tasks.md"))
	if err != nil {
		return b, false, err
	}
	b.Tasks = leafTasks(tasks)
	return b, hasDeferred, nil
}

// leafTasks counts checkboxes with no subtask beneath them. A checkbox is a
// parent when the next checkbox is indented deeper or numbered as its child
// ("- [ ] 2." followed by "- [ ] 2.1"); parents group work and are not counted.
func leafTasks(lines []string) int {
	type task struct {
		indent int
		label  string
	}
	var tasks []task
	for _, line := range lines {
		m := taskCheckbox.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		label, _, _ := strings.Cut(strings.TrimSpace(line[len(m[0]):]), " ")
		tasks = append(tasks, task{
			indent: len(strings.ReplaceAll(m[1], "\t", "    ")),
			label:  strings.TrimSuffix(label, "."),
		})
	}
	leaves := 0
	for i, t := range tasks {
		if i+1 < len(tasks) {
			next := tasks[i+1]
			if next.indent > t.indent || (t.label != "" && strings.HasPrefix(next.label, t.label+".")) {
				continue
			}
		}
		leaves++
	}
	return leaves
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}
