package speccheck_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

var requiredVocabulary = []string{
	"kernel invariant",
	"generic extension mechanism",
	"concrete optional feature policy",
	"feature-specific infrastructure/composition",
	"mixed/needs split",
}

type inventoryRow struct {
	Responsibility      string
	CurrentOwner        string
	ProductionConsumers string
	Classification      string
	WhyRetained         string
	FullClosureAction   string
}

func checkPreOssCoreSlimming(t *testing.T, root string) {
	t.Helper()
	checkResidualOwnershipInventory(t, root)
}

func checkResidualOwnershipInventory(t *testing.T, root string) {
	t.Helper()
	path, content, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatalf("residual ownership inventory missing: %v", err)
	}
	t.Logf("validating residual ownership inventory at: %s", path)

	if err := validateResidualOwnershipInventoryContent(content); err != nil {
		t.Fatalf("residual ownership inventory contract violation in %s: %v", path, err)
	}
	checkInventoryProvenance(t, root, content)
}

func checkInventoryProvenance(t *testing.T, root, content string) {
	t.Helper()
	t.Run("provenance", func(t *testing.T) {
		if opensource.IsOpenSourceTree(root) {
			t.Skip("public export has fresh history: private commit objects and ancestry are unavailable")
		}
		if err := validateSHAIdentity(root, content); err != nil {
			t.Fatalf("residual ownership inventory provenance: %v", err)
		}
	})
}

// The git helpers below accept either the real repository root or a throwaway
// fixture repository, so they clear git's repository-location variables instead
// of relying on ambient state. Git exports GIT_DIR and friends to every hook it
// runs, and an inherited GIT_DIR makes a fixture `git init`/`git commit` operate
// inside the real repository, destroying its index and refs.
func resolveGitSHA(root, rev string) (string, error) {
	cmd := exec.Command("git", "-C", root, "rev-parse", rev)
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func resolveGitBaselineSHA(root string) (string, error) {
	// Baseline identity must equal local main SHA (git rev-parse main).
	// Accept origin/main only when local main ref is missing.
	if sha, err := resolveGitSHA(root, "main"); err == nil && len(sha) == 40 {
		return sha, nil
	}
	if sha, err := resolveGitSHA(root, "origin/main"); err == nil && len(sha) == 40 {
		return sha, nil
	}
	return "", fmt.Errorf("could not resolve git baseline: neither main nor origin/main found in %s", root)
}

// isShallowRepository reports whether root is a depth-truncated checkout
// whose missing objects make ancestry verdicts unreliable. It never fails
// closed by itself: an undeterminable state returns false and callers keep
// their existing verdicts.
func isShallowRepository(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository")
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func checkGitCommitObject(root, sha string) error {
	cmd := exec.Command("git", "-C", root, "cat-file", "-e", sha+"^{commit}")
	cmd.Env = gitscope.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Implementation SHA %q is not an existing commit object: %w", sha, err)
	}
	return nil
}

func checkGitIsAncestor(root, ancestor, descendant string) error {
	cmd := exec.Command("git", "-C", root, "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Env = gitscope.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Implementation SHA %q is not an ancestor of git %s: %w", ancestor, descendant, err)
	}
	return nil
}

// gitOutputAt runs a git query rooted at root and returns its stdout. The
// caller supplies the argument list that follows `-C root`.
func gitOutputAt(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = gitscope.Environ()
	return cmd.Output()
}

func validateSHAIdentity(root, content string) error {
	metaSection := extractSection(content, "## Baseline & Implementation Metadata")
	if metaSection == "" {
		metaSection = content
	}

	implSHA, err := parseMetadataField(metaSection, "Implementation SHA")
	if err != nil {
		return fmt.Errorf("parse Implementation SHA: %w", err)
	}
	baseSHA, err := parseMetadataField(metaSection, "Merged-Main Baseline SHA")
	if err != nil {
		return fmt.Errorf("parse Merged-Main Baseline SHA: %w", err)
	}

	headSHA, err := resolveGitSHA(root, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve git HEAD: %w", err)
	}

	var identityErrs []string
	if err := checkGitCommitObject(root, implSHA); err != nil {
		identityErrs = append(identityErrs, fmt.Sprintf("Implementation SHA %q is not an existing commit object", implSHA))
	} else if err := checkGitIsAncestor(root, implSHA, "HEAD"); err != nil {
		identityErrs = append(identityErrs, fmt.Sprintf("Implementation SHA %q is not an ancestor of git HEAD %q", implSHA, headSHA))
	}

	// Baseline must be an existing mainline commit and an ancestor of HEAD.
	// Equality with the current main tip is NOT required: pinning equality
	// would invalidate the artifact on every future main commit (treadmill),
	// while ancestry keeps the recorded base verifiable forever.
	if err := checkGitCommitObject(root, baseSHA); err != nil {
		identityErrs = append(identityErrs, fmt.Sprintf("Merged-Main Baseline SHA %q is not an existing commit object (baseline must name a mainline commit)", baseSHA))
	} else if err := checkGitIsAncestor(root, baseSHA, "HEAD"); err != nil {
		identityErrs = append(identityErrs, fmt.Sprintf("Merged-Main Baseline SHA %q is not an ancestor of git HEAD %q (baseline is not a mainline base)", baseSHA, headSHA))
	}

	// A depth-truncated checkout reports genuinely present commits as missing
	// or non-ancestral. The verdict above stays a failure (fail closed); this
	// only names the likely cause and its remedy so the next reader does not
	// misdiagnose truncated history as rewritten provenance.
	if len(identityErrs) > 0 && isShallowRepository(root) {
		identityErrs = append(identityErrs, "repository checkout is shallow (git rev-parse --is-shallow-repository=true): ancestry and object-existence verdicts are unreliable without complete history; run `git fetch --unshallow` and re-run")
	}

	if len(identityErrs) > 0 {
		return errors.New(strings.Join(identityErrs, "; "))
	}
	return nil
}

// archivedPathSegments matches a path segment that starts with the former
// abbreviation, assembled so this file holds no retired spelling.
var archivedPathSegments = regexp.MustCompile("(^|[/`( |])" + "l" + "ip")

func loadResidualOwnershipInventory(root string) (string, string, error) {
	activePath := filepath.Join(root, ".kiro", "specs", "pre-oss-core-slimming", "residual-ownership-inventory.md")
	archivedPath := filepath.Join(root, ".kiro", "specs", "archive", "pre-oss-core-slimming", "residual-ownership-inventory.md")

	if data, err := os.ReadFile(activePath); err == nil {
		return activePath, string(data), nil
	}
	if data, err := os.ReadFile(archivedPath); err == nil {
		// The archive keeps the names from before the AIProxer rename.
		return archivedPath, archivedPathSegments.ReplaceAllString(string(data), "${1}aip"), nil
	}

	return "", "", fmt.Errorf("residual-ownership-inventory.md not found in active (%s) or archived (%s) location", activePath, archivedPath)
}

func extractSectionAndRest(content, heading string) (string, string, bool) {
	lines := strings.Split(content, "\n")
	targetHeading := strings.TrimSpace(heading)
	if !strings.HasPrefix(targetHeading, "#") {
		targetHeading = "## " + targetHeading
	}

	start := -1
	end := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start == -1 {
			if strings.EqualFold(trimmed, targetHeading) {
				start = i
			}
		} else {
			if strings.HasPrefix(trimmed, "## ") {
				end = i
				break
			}
		}
	}
	if start == -1 {
		return "", content, false
	}
	if end == -1 {
		end = len(lines)
	}

	sectionLines := lines[start+1 : end]
	var restLines []string
	restLines = append(restLines, lines[:start]...)
	restLines = append(restLines, lines[end:]...)

	return strings.Join(sectionLines, "\n"), strings.Join(restLines, "\n"), true
}

func extractSection(content, heading string) string {
	section, _, found := extractSectionAndRest(content, heading)
	if !found {
		return ""
	}
	return section
}

func parseMetadataField(content, fieldName string) (string, error) {
	escapedName := regexp.QuoteMeta(fieldName)
	fieldRegex := regexp.MustCompile(`(?m)^\s*-\s*\*\*` + escapedName + `\*\*:(.*)$`)
	matches := fieldRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("missing or invalid format for %s", fieldName)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("duplicate metadata field %s", fieldName)
	}
	raw := matches[0][1]
	raw = strings.TrimRight(raw, "\r")
	rest := strings.TrimSpace(raw)
	if rest == "" {
		return "", fmt.Errorf("empty metadata field %s", fieldName)
	}

	var val string
	if strings.HasPrefix(rest, "`") {
		if !strings.HasSuffix(rest, "`") || strings.Count(rest, "`") != 2 {
			return "", fmt.Errorf("trailing garbage or unclosed backtick in %s: %q", fieldName, rest)
		}
		val = strings.Trim(rest, "`")
	} else {
		if strings.ContainsAny(rest, " \t`") {
			return "", fmt.Errorf("trailing garbage in %s: %q", fieldName, rest)
		}
		val = rest
	}
	return val, nil
}

var knownMetadataFields = []string{
	"Specification",
	"Inventory Date",
	"Implementation SHA",
	"Merged-Main Baseline SHA",
	"Target Full-Closure SDD",
}

func checkMetadataFieldsOutsideSection(rest string) []string {
	var errs []string
	seen := make(map[string]bool)

	// 1. Check for any known metadata field occurring outside the section
	for _, field := range knownMetadataFields {
		escaped := regexp.QuoteMeta(field)
		regex := regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+\.)\s*\*\*` + escaped + `\*\*:(.*)$`)
		if regex.MatchString(rest) {
			seen[field] = true
			errs = append(errs, fmt.Sprintf("metadata field %q found outside ## Baseline & Implementation Metadata section", field))
		}
	}

	// 2. Check for any bullet item matching "- **FieldName**:" outside the section
	generalFieldRegex := regexp.MustCompile(`(?m)^\s*-\s*\*\*([^*:]+)\*\*:(.*)$`)
	matches := generalFieldRegex.FindAllStringSubmatch(rest, -1)
	for _, m := range matches {
		fieldName := strings.TrimSpace(m[1])
		if !seen[fieldName] {
			seen[fieldName] = true
			errs = append(errs, fmt.Sprintf("metadata field %q found outside ## Baseline & Implementation Metadata section", fieldName))
		}
	}

	return errs
}

func validateMetadata(content string) []string {
	var errs []string

	metaSection, rest, found := extractSectionAndRest(content, "## Baseline & Implementation Metadata")
	if !found {
		return []string{"missing ## Baseline & Implementation Metadata section"}
	}

	// Fail if fields are defined outside Baseline & Implementation Metadata section
	errs = append(errs, checkMetadataFieldsOutsideSection(rest)...)

	// Validate Inventory Date: full-line match, YYYY-MM-DD format, valid calendar date
	dateVal, err := parseMetadataField(metaSection, "Inventory Date")
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(dateVal) {
			errs = append(errs, fmt.Sprintf("invalid format for Inventory Date (expected YYYY-MM-DD): %q", dateVal))
		} else if _, err := time.Parse("2006-01-02", dateVal); err != nil {
			errs = append(errs, fmt.Sprintf("invalid Inventory Date %q: %v", dateVal, err))
		}
	}

	// Validate Implementation SHA: full-line match, 40-character hex string
	shaVal, err := parseMetadataField(metaSection, "Implementation SHA")
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		if len(shaVal) != 40 || !regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(shaVal) {
			errs = append(errs, fmt.Sprintf("invalid format for Implementation SHA (expected 40-char hex SHA): %q", shaVal))
		} else if _, err := hex.DecodeString(shaVal); err != nil {
			errs = append(errs, fmt.Sprintf("invalid hex in Implementation SHA %q: %v", shaVal, err))
		}
	}

	// Validate Merged-Main Baseline SHA: full-line match, 40-character hex string
	baseShaVal, err := parseMetadataField(metaSection, "Merged-Main Baseline SHA")
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		if len(baseShaVal) != 40 || !regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(baseShaVal) {
			errs = append(errs, fmt.Sprintf("invalid format for Merged-Main Baseline SHA (expected 40-char hex SHA): %q", baseShaVal))
		} else if _, err := hex.DecodeString(baseShaVal); err != nil {
			errs = append(errs, fmt.Sprintf("invalid hex in Merged-Main Baseline SHA %q: %v", baseShaVal, err))
		}
	}

	return errs
}

func validateVocabularySection(content string) []string {
	var errs []string
	vocabSection := extractSection(content, "## Classification Vocabulary")
	if vocabSection == "" {
		return []string{"missing ## Classification Vocabulary section"}
	}

	lines := strings.Split(vocabSection, "\n")
	listMarkerRegex := regexp.MustCompile(`^\s*(?:\d+\.|\*|-)\s+`)
	itemRegex := regexp.MustCompile("^\\s*(?:\\d+\\.|\\*|-)\\s+\\*\\*`?([^`*:]+?)`?\\*\\*\\s*:\\s*(.*)$")

	type parsedVocabItem struct {
		term string
		line string
	}
	var parsedItems []parsedVocabItem

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !listMarkerRegex.MatchString(trimmed) {
			// Non-list line (introductory prose, paragraph, etc.)
			continue
		}

		m := itemRegex.FindStringSubmatch(trimmed)
		if len(m) < 3 {
			errs = append(errs, fmt.Sprintf("malformed vocabulary list item in ## Classification Vocabulary section: %q", line))
			continue
		}
		term := strings.TrimSpace(m[1])
		desc := strings.TrimSpace(m[2])
		if desc == "" {
			errs = append(errs, fmt.Sprintf("vocabulary entry %q in ## Classification Vocabulary section has empty description", term))
		}
		parsedItems = append(parsedItems, parsedVocabItem{term: term, line: line})
	}

	if len(parsedItems) == 0 && len(errs) == 0 {
		return []string{"no vocabulary list items found in ## Classification Vocabulary section"}
	}

	// 1. Detect duplicates
	seenCounts := make(map[string]int)
	for _, item := range parsedItems {
		seenCounts[item.term]++
	}
	for term, count := range seenCounts {
		if count > 1 {
			errs = append(errs, fmt.Sprintf("duplicate vocabulary entry %q in ## Classification Vocabulary section (appears %d times)", term, count))
		}
	}

	// 2. Detect unknown / extra entries
	reqVocabMap := make(map[string]bool, len(requiredVocabulary))
	for _, v := range requiredVocabulary {
		reqVocabMap[v] = true
	}
	for _, item := range parsedItems {
		if !reqVocabMap[item.term] {
			errs = append(errs, fmt.Sprintf("unknown or extra vocabulary entry %q in ## Classification Vocabulary section (allowed: %s)", item.term, strings.Join(requiredVocabulary, ", ")))
		}
	}

	// 3. Detect missing required vocabulary entries
	for _, v := range requiredVocabulary {
		if seenCounts[v] == 0 {
			errs = append(errs, fmt.Sprintf("classification vocabulary section missing definition list item for %q", v))
		}
	}

	// 4. Enforce exact 5-entry vocabulary count
	if len(parsedItems) != len(requiredVocabulary) {
		errs = append(errs, fmt.Sprintf("classification vocabulary section must have exactly %d vocabulary entries, got %d", len(requiredVocabulary), len(parsedItems)))
	}

	return errs
}

func parseInventoryTable(content string) ([]inventoryRow, []string) {
	var errs []string
	section := extractSection(content, "## Residual Ownership Inventory Table")
	if section == "" {
		return nil, []string{"missing ## Residual Ownership Inventory Table section"}
	}

	var rows []inventoryRow
	seenHeader := false
	seenSeparator := false

	for line := range strings.SplitSeq(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, "|") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			errs = append(errs, fmt.Sprintf("malformed inventory table row (missing leading or trailing pipe): %q", line))
			continue
		}

		inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|")
		rawCells := strings.Split(inner, "|")
		if len(rawCells) != 6 {
			errs = append(errs, fmt.Sprintf("inventory table row has %d cells, expected exactly 6: %q", len(rawCells), line))
			continue
		}

		cells := make([]string, 6)
		for i := range rawCells {
			cells[i] = strings.TrimSpace(rawCells[i])
		}

		// Check if separator row
		isSeparator := true
		for _, c := range cells {
			if strings.Trim(c, "- :") != "" {
				isSeparator = false
				break
			}
		}
		if isSeparator {
			seenSeparator = true
			continue
		}

		// Check if header row
		if !seenHeader {
			expectedColumns := []string{
				"Responsibility",
				"Current owner/package",
				"Production consumers",
				"Classification",
				"Why retained/deferred",
				"Full-closure action",
			}
			for i, exp := range expectedColumns {
				if !strings.EqualFold(cells[i], exp) {
					errs = append(errs, fmt.Sprintf("inventory table column %d: got %q, want %q", i+1, cells[i], exp))
				}
			}
			seenHeader = true
			continue
		}

		if !seenSeparator {
			errs = append(errs, fmt.Sprintf("inventory table row encountered before separator row: %q", line))
			continue
		}

		// Data row: verify no cell is empty
		for i, c := range cells {
			if c == "" {
				errs = append(errs, fmt.Sprintf("inventory table row has empty cell at column %d: %q", i+1, line))
			}
		}

		rows = append(rows, inventoryRow{
			Responsibility:      cells[0],
			CurrentOwner:        cells[1],
			ProductionConsumers: cells[2],
			Classification:      cells[3],
			WhyRetained:         cells[4],
			FullClosureAction:   cells[5],
		})
	}

	if !seenHeader {
		errs = append(errs, "inventory table missing header row")
	}
	if !seenSeparator {
		errs = append(errs, "inventory table missing separator row")
	}
	if len(rows) == 0 {
		errs = append(errs, "inventory table has no data rows")
	}

	return rows, errs
}

func parseSummaryCounts(content string) (map[string]int, int, []string) {
	var errs []string
	section := extractSection(content, "## Summary Counts by Classification")
	if section == "" {
		section = extractSection(content, "## Summary Counts")
	}
	if section == "" {
		return nil, 0, []string{"missing ## Summary Counts by Classification heading"}
	}

	summaryCounts := make(map[string]int)
	totalFindings := -1
	seenHeader := false

	reqMap := make(map[string]bool, len(requiredVocabulary))
	for _, v := range requiredVocabulary {
		reqMap[v] = true
	}

	for line := range strings.SplitSeq(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, "|") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			errs = append(errs, fmt.Sprintf("malformed summary table row: %q", line))
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|")
		rawCells := strings.Split(inner, "|")
		if len(rawCells) != 2 {
			errs = append(errs, fmt.Sprintf("summary table row has %d cells, expected exactly 2: %q", len(rawCells), line))
			continue
		}
		col1 := strings.TrimSpace(rawCells[0])
		col2 := strings.TrimSpace(rawCells[1])

		// Skip separator row
		if strings.Trim(col1, "- :") == "" && strings.Trim(col2, "- :") == "" {
			continue
		}
		// Check header row
		if strings.EqualFold(col1, "Classification") && strings.EqualFold(col2, "Count") {
			seenHeader = true
			continue
		}

		cleanKey := strings.Trim(strings.Trim(col1, "*"), "`")
		cleanKey = strings.TrimSpace(cleanKey)
		cleanVal := strings.Trim(strings.Trim(col2, "*"), "`")
		cleanVal = strings.TrimSpace(cleanVal)

		count, err := strconv.Atoi(cleanVal)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid count %q in summary table row %q", col2, line))
			continue
		}

		if strings.EqualFold(cleanKey, "Total Findings") || strings.EqualFold(cleanKey, "Total") {
			if totalFindings != -1 {
				errs = append(errs, fmt.Sprintf("duplicate total row in summary table: %q", line))
			}
			totalFindings = count
		} else if !reqMap[cleanKey] {
			errs = append(errs, fmt.Sprintf("unknown classification in summary table: %q (allowed: %s, plus Total Findings)", cleanKey, strings.Join(requiredVocabulary, ", ")))
		} else if _, exists := summaryCounts[cleanKey]; exists {
			errs = append(errs, fmt.Sprintf("duplicate classification in summary table: %q", cleanKey))
		} else {
			summaryCounts[cleanKey] = count
		}
	}

	if !seenHeader {
		errs = append(errs, "summary table missing header row")
	}
	if totalFindings == -1 {
		errs = append(errs, "summary table missing total findings row")
	}

	return summaryCounts, totalFindings, errs
}

func validateResidualOwnershipInventoryContent(content string) error {
	var errs []string

	requiredHeadings := []string{
		"Baseline & Implementation Metadata",
		"Classification Vocabulary",
		"Residual Ownership Inventory Table",
		"Summary Counts by Classification",
		"Durable Handoff & Governance Statement",
	}
	for _, heading := range requiredHeadings {
		if !strings.Contains(content, heading) {
			errs = append(errs, fmt.Sprintf("missing required heading %q", heading))
		}
	}

	// 1. Metadata validation (SHA and date format)
	errs = append(errs, validateMetadata(content)...)

	// 2. Vocabulary definition validation (5 entries in Classification Vocabulary section)
	errs = append(errs, validateVocabularySection(content)...)

	// 3. Parse inventory table into rows with exactly 6 cells
	rows, tableErrs := parseInventoryTable(content)
	errs = append(errs, tableErrs...)

	// 4. Validate classification values on parsed rows
	for _, row := range rows {
		cleanClass := strings.Trim(strings.TrimSpace(row.Classification), "`")
		if !slices.Contains(requiredVocabulary, cleanClass) {
			errs = append(errs, fmt.Sprintf("row %q has invalid classification %q (must be one of: %s)", row.Responsibility, row.Classification, strings.Join(requiredVocabulary, ", ")))
		}
	}

	// 5. Validate mandatory responsibilities against parsed rows (not substrings)
	mandatoryRules := []struct {
		desc  string
		match func(row inventoryRow) bool
	}{
		{
			desc: "compaction-continuity coordination",
			match: func(r inventoryRow) bool {
				return strings.Contains(r.CurrentOwner, "internal/core/compactioncontinuity") ||
					strings.Contains(strings.ToLower(r.Responsibility), "compaction-continuity coordination")
			},
		},
		{
			desc: "conversation-view generic projection vs optional steering policy",
			match: func(r inventoryRow) bool {
				return strings.Contains(r.CurrentOwner, "internal/core/conversationview") ||
					strings.Contains(strings.ToLower(r.Responsibility), "conversation-view generic projection")
			},
		},
		{
			desc: "interleaved-thinking/state",
			match: func(r inventoryRow) bool {
				return strings.Contains(r.CurrentOwner, "internal/core/interleavedthinking") ||
					strings.Contains(r.CurrentOwner, "internal/core/interleavedstate") ||
					strings.Contains(strings.ToLower(r.Responsibility), "interleaved-thinking")
			},
		},
		{
			desc: "terminal-decision policy",
			match: func(r inventoryRow) bool {
				return strings.Contains(r.CurrentOwner, "internal/core/terminaldecisionpolicy") ||
					strings.Contains(strings.ToLower(r.Responsibility), "terminal-decision policy")
			},
		},
		{
			desc: "feature-specific public pkg/aipruntime host options/adapters",
			match: func(r inventoryRow) bool {
				return strings.Contains(r.CurrentOwner, "pkg/aipruntime") &&
					(strings.Contains(r.Responsibility, "host options") || strings.Contains(r.Responsibility, "ReasoningCompression"))
			},
		},
		{
			desc: "dedicated compaction-continuity compose adapter (compactioncompose)",
			match: func(r inventoryRow) bool {
				return strings.Trim(r.CurrentOwner, "`") == "internal/infra/compactioncompose" ||
					strings.Contains(strings.ToLower(r.Responsibility), "compaction-continuity compose adapter")
			},
		},
		{
			desc: "dedicated reasoning-preservation compose adapter (reasoningcompose)",
			match: func(r inventoryRow) bool {
				return strings.Trim(r.CurrentOwner, "`") == "internal/infra/reasoningcompose" ||
					strings.Contains(strings.ToLower(r.Responsibility), "reasoning-preservation compose adapter")
			},
		},
		{
			desc: "dedicated secret-guard compose adapter (secretguardcompose)",
			match: func(r inventoryRow) bool {
				return strings.Trim(r.CurrentOwner, "`") == "internal/infra/secretguardcompose" ||
					strings.Contains(strings.ToLower(r.Responsibility), "secret-guard compose adapter")
			},
		},
		{
			desc: "optional UX keep-warm policy/scheduling in core (keepwarm)",
			match: func(r inventoryRow) bool {
				return strings.Trim(r.CurrentOwner, "`") == "internal/core/keepwarm" ||
					strings.Contains(strings.ToLower(r.Responsibility), "keep-warm policy and scheduling")
			},
		},
	}

	for _, rule := range mandatoryRules {
		if !slices.ContainsFunc(rows, rule.match) {
			errs = append(errs, fmt.Sprintf("missing mandatory row requirement: %s", rule.desc))
		}
	}

	// 6. Validate summary counts vs parsed rows
	summaryCounts, totalFindings, summaryErrs := parseSummaryCounts(content)
	errs = append(errs, summaryErrs...)

	if summaryCounts != nil {
		actualCounts := make(map[string]int)
		for _, v := range requiredVocabulary {
			actualCounts[v] = 0
		}
		for _, r := range rows {
			cleanClass := strings.Trim(strings.TrimSpace(r.Classification), "`")
			actualCounts[cleanClass]++
		}

		for _, v := range requiredVocabulary {
			count, ok := summaryCounts[v]
			if !ok {
				errs = append(errs, fmt.Sprintf("summary table missing entry for classification %q", v))
			} else if count != actualCounts[v] {
				errs = append(errs, fmt.Sprintf("summary count mismatch for %q: summary table has %d, parsed rows have %d", v, count, actualCounts[v]))
			}
		}

		if totalFindings != -1 && totalFindings != len(rows) {
			errs = append(errs, fmt.Sprintf("summary total mismatch: summary table has %d, parsed rows count is %d", totalFindings, len(rows)))
		}
	}

	// 7. Transient history governance statement
	lowerContent := strings.ToLower(content)
	if !strings.Contains(lowerContent, "no deferred finding") || !strings.Contains(lowerContent, "transient history") {
		errs = append(errs, "missing statement affirming that no deferred finding exists only in transient history")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func removeInventoryRowByResponsibility(content, prefix string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && strings.Contains(line, prefix) {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func TestResidualOwnershipInventoryContract_Live(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	checkResidualOwnershipInventory(t, root)
}

func TestResidualOwnershipInventoryContract_AcceptsArchivedLocation(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	archivedDir := filepath.Join(tmp, ".kiro", "specs", "archive", "pre-oss-core-slimming")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	root := repoRoot(t)
	realActive := filepath.Join(root, ".kiro", "specs", "archive", "pre-oss-core-slimming", "residual-ownership-inventory.md")
	data, err := os.ReadFile(realActive)
	if err != nil {
		t.Fatalf("read real archived inventory: %v", err)
	}

	archivedFile := filepath.Join(archivedDir, "residual-ownership-inventory.md")
	if err := os.WriteFile(archivedFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := loadResidualOwnershipInventory(tmp)
	if err != nil {
		t.Fatalf("loadResidualOwnershipInventory failed for archived location: %v", err)
	}
	if path != archivedFile {
		t.Errorf("got path %s, want %s", path, archivedFile)
	}
	if err := validateResidualOwnershipInventoryContent(content); err != nil {
		t.Errorf("validation failed for archived copy: %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsAbsent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	_, _, err := loadResidualOwnershipInventory(tmp)
	if err == nil {
		t.Fatal("expected error when residual-ownership-inventory.md is absent, got nil")
	}
}

func TestResidualOwnershipInventoryContract_RejectsMissingHeadings(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(validContent, "## Classification Vocabulary", "## Other Stuff", 1)
	if err := validateResidualOwnershipInventoryContent(tampered); err == nil || !strings.Contains(err.Error(), "Classification Vocabulary") {
		t.Fatalf("expected missing heading error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsLookalikeHeading(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("SuffixedLookalikeRejected", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, "## Baseline & Implementation Metadata", "## Baseline & Implementation Metadata Spoofed", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "Baseline & Implementation Metadata") {
			t.Fatalf("expected error rejecting suffixed lookalike heading, got %v", err)
		}
	})

	t.Run("PrefixedLookalikeRejected", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, "## Baseline & Implementation Metadata", "Spoofed ## Baseline & Implementation Metadata", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "Baseline & Implementation Metadata") {
			t.Fatalf("expected error rejecting prefixed lookalike heading, got %v", err)
		}
	})

	t.Run("ExtractSectionAndRestRejectsLookalikes", func(t *testing.T) {
		t.Parallel()
		lookalikes := []string{
			"## Baseline & Implementation Metadata Spoofed",
			"## Baseline & Implementation Metadata Extra",
			"## Spoofed ## Baseline & Implementation Metadata",
			"## Baseline & Implementation Metadata: Extra",
		}
		for _, lookalike := range lookalikes {
			content := lookalike + "\n- **Inventory Date**: `2026-09-03`\n"
			if _, _, found := extractSectionAndRest(content, "## Baseline & Implementation Metadata"); found {
				t.Fatalf("extractSectionAndRest should not match lookalike heading %q", lookalike)
			}
		}
	})

	t.Run("ExtractSectionAndRestMatchesExactCaseInsensitiveAndTrimmed", func(t *testing.T) {
		t.Parallel()
		validHeadings := []string{
			"## baseline & implementation metadata",
			"## BASELINE & IMPLEMENTATION METADATA",
			"   ## Baseline & Implementation Metadata   ",
			"   ## baseline & implementation metadata   ",
		}
		for _, heading := range validHeadings {
			content := heading + "\n- **Inventory Date**: `2026-09-03`\n"
			section, _, found := extractSectionAndRest(content, "## Baseline & Implementation Metadata")
			if !found {
				t.Fatalf("extractSectionAndRest expected to find heading %q", heading)
			}
			if !strings.Contains(section, "Inventory Date") {
				t.Fatalf("extractSectionAndRest did not extract section content for %q", heading)
			}
		}
	})
}

func TestResidualOwnershipInventoryContract_RejectsMissingColumns(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(validContent, "Why retained/deferred", "Other Column", 1)
	if err := validateResidualOwnershipInventoryContent(tampered); err == nil || !strings.Contains(err.Error(), "Why retained/deferred") {
		t.Fatalf("expected missing column error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsInvalidTableCellCount(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	// Remove a column cell from one row, reducing it from 6 to 5 cells
	tampered := strings.Replace(validContent, "| `mixed/needs split` |", "", 1)
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil {
		t.Fatal("expected error when table row cell count is not 6, got nil")
	}
	if !strings.Contains(err.Error(), "cells, expected exactly 6") {
		t.Fatalf("expected cell count error, got %v", err)
	}
}

// metadataFieldLine renders the exact artifact line for a metadata field so
// tamper tests target one field even when Implementation and Baseline SHAs
// are identical (both pinned to merged main).
// metadataFieldLine renders the exact artifact line for a metadata field so
// tamper tests target one field even when Implementation and Baseline SHAs
// are identical (both pinned to merged main).
func metadataFieldLine(field, sha string) string {
	return "- **" + field + "**: `" + sha + "`"
}

// mustLiveInventorySHAs derives the Implementation and Baseline SHAs recorded in
// the live artifact so tamper tests track inventory updates instead of pinning
// stale literal SHAs.
func mustLiveInventorySHAs(t *testing.T, content string) (implSHA, baseSHA string) {
	t.Helper()
	meta := extractSection(content, "## Baseline & Implementation Metadata")
	if meta == "" {
		meta = content
	}
	var err error
	if implSHA, err = parseMetadataField(meta, "Implementation SHA"); err != nil {
		t.Fatalf("parse live Implementation SHA: %v", err)
	}
	if baseSHA, err = parseMetadataField(meta, "Merged-Main Baseline SHA"); err != nil {
		t.Fatalf("parse live Merged-Main Baseline SHA: %v", err)
	}
	return implSHA, baseSHA
}

func TestResidualOwnershipInventoryContract_RejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	implSHA, baseSHA := mustLiveInventorySHAs(t, validContent)

	t.Run("InvalidDate", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, "2026-09-03", "2026-99-99", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "Inventory Date") {
			t.Fatalf("expected invalid date error, got %v", err)
		}
	})

	t.Run("InvalidImplementationSHA", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, metadataFieldLine("Implementation SHA", implSHA), metadataFieldLine("Implementation SHA", "not-a-valid-40-char-hex-sha"), 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "Implementation SHA") {
			t.Fatalf("expected invalid Implementation SHA error, got %v", err)
		}
	})

	t.Run("InvalidBaselineSHA", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, metadataFieldLine("Merged-Main Baseline SHA", baseSHA), metadataFieldLine("Merged-Main Baseline SHA", "shortsha"), 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "Merged-Main Baseline SHA") {
			t.Fatalf("expected invalid Merged-Main Baseline SHA error, got %v", err)
		}
	})

	t.Run("TrailingGarbageDate", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, "2026-09-03`", "2026-09-03` trailing-garbage-bypass", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || (!strings.Contains(err.Error(), "Inventory Date") && !strings.Contains(err.Error(), "trailing garbage")) {
			t.Fatalf("expected trailing garbage error for Inventory Date, got %v", err)
		}
	})

	t.Run("TrailingGarbageImplementationSHA", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, metadataFieldLine("Implementation SHA", implSHA), metadataFieldLine("Implementation SHA", implSHA)+" -- bypass attempt", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || (!strings.Contains(err.Error(), "Implementation SHA") && !strings.Contains(err.Error(), "trailing garbage")) {
			t.Fatalf("expected trailing garbage error for Implementation SHA, got %v", err)
		}
	})

	t.Run("TrailingGarbageBaselineSHA", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(validContent, metadataFieldLine("Merged-Main Baseline SHA", baseSHA), metadataFieldLine("Merged-Main Baseline SHA", baseSHA)+" trailing bypass", 1)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || (!strings.Contains(err.Error(), "Merged-Main Baseline SHA") && !strings.Contains(err.Error(), "trailing garbage")) {
			t.Fatalf("expected trailing garbage error for Merged-Main Baseline SHA, got %v", err)
		}
	})
}

func TestResidualOwnershipInventoryContract_RejectsMissingVocabEntry(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(validContent, "feature-specific infrastructure/composition", "other-custom-term", 1)
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil || !strings.Contains(err.Error(), "feature-specific infrastructure/composition") {
		t.Fatalf("expected missing vocabulary error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsInvalidClassification(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	// Change classification of first row in table to unknown classification
	tampered := strings.Replace(validContent, "| `mixed/needs split` |", "| `unknown-classification` |", 1)
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil || !strings.Contains(err.Error(), "invalid classification") {
		t.Fatalf("expected invalid classification error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsSummaryCountMismatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(validContent, "| `concrete optional feature policy` | 3 |", "| `concrete optional feature policy` | 99 |", 1)
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil || !strings.Contains(err.Error(), "summary count mismatch") {
		t.Fatalf("expected summary count mismatch error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsMissingCompactionComposeRow(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := removeInventoryRowByResponsibility(validContent, "Dedicated compaction-continuity compose adapter")
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil {
		t.Fatal("expected error when compaction-compose row is removed, got nil")
	}
	if !strings.Contains(err.Error(), "compaction-continuity compose adapter") && !strings.Contains(err.Error(), "compactioncompose") {
		t.Fatalf("expected error mentioning compaction-continuity compose adapter, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsMissingKeepWarmRow(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := removeInventoryRowByResponsibility(validContent, "Keep-warm policy and scheduling")
	err = validateResidualOwnershipInventoryContent(tampered)
	if err == nil {
		t.Fatal("expected error when keep-warm row is removed, got nil")
	}
	if !strings.Contains(err.Error(), "keep-warm") && !strings.Contains(err.Error(), "keepwarm") {
		t.Fatalf("expected error mentioning keep-warm, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsMissingTransientHistoryStatement(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.ReplaceAll(validContent, "transient history", "deleted conversation")
	if err := validateResidualOwnershipInventoryContent(tampered); err == nil || !strings.Contains(err.Error(), "transient history") {
		t.Fatalf("expected missing transient history statement error, got %v", err)
	}
}

func TestResidualOwnershipInventoryContract_RejectsVocabularyViolations(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("DuplicateEntry", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"5. **`mixed/needs split`**:",
			"5. **`mixed/needs split`**: Subsystems currently combining kernel invariants.\n6. **`kernel invariant`**: Duplicate entry for kernel invariant.",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected duplicate vocabulary error, got %v", err)
		}
	})

	t.Run("ExtraEntry", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"5. **`mixed/needs split`**:",
			"5. **`mixed/needs split`**: Subsystems currently combining kernel invariants.\n6. **`speculative extra invariant`**: Extra unapproved vocabulary entry.",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || (!strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "extra") && !strings.Contains(err.Error(), "exactly 5")) {
			t.Fatalf("expected extra/unknown vocabulary error, got %v", err)
		}
	})

	t.Run("ProseOnlyOccurrence", func(t *testing.T) {
		t.Parallel()
		// Remove item 5 list definition, but mention "mixed/needs split" in introductory prose
		tampered := strings.Replace(
			validContent,
			"is classified according to the following vocabulary:",
			"is classified according to the following vocabulary (including mixed/needs split in prose):",
			1,
		)
		item5Prefix := "5. **`mixed/needs split`**:"
		lines := strings.Split(tampered, "\n")
		var filteredLines []string
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), item5Prefix) {
				continue
			}
			filteredLines = append(filteredLines, l)
		}
		tampered = strings.Join(filteredLines, "\n")

		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil {
			t.Fatal("expected error when vocabulary entry is only mentioned in prose, got nil")
		}
		if !strings.Contains(err.Error(), "mixed/needs split") && !strings.Contains(err.Error(), "exactly 5") {
			t.Fatalf("expected error mentioning missing vocabulary list item or count, got %v", err)
		}
	})

	t.Run("MalformedListItem", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"1. **`kernel invariant`**:",
			"1. kernel invariant without bold or colon delimiter",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil {
			t.Fatal("expected error when vocabulary list item is malformed, got nil")
		}
	})
}

func TestResidualOwnershipInventoryContract_RejectsSummaryViolations(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("UnknownClassification", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"| **Total Findings** |",
			"| `unknown-classification` | 1 |\n| **Total Findings** |",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("expected unknown classification error in summary table, got %v", err)
		}
	})

	t.Run("DuplicateClassification", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"| `kernel invariant` | 0 |",
			"| `kernel invariant` | 0 |\n| `kernel invariant` | 0 |",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected duplicate classification error in summary table, got %v", err)
		}
	})

	t.Run("DuplicateTotal", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"| **Total Findings** | **10** |",
			"| **Total Findings** | **10** |\n| **Total Findings** | **10** |",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected duplicate total error in summary table, got %v", err)
		}
	})
}

func TestResidualOwnershipInventoryContract_ValidatesSHAIdentity(t *testing.T) {
	t.Parallel()
	_, validContent, err := loadResidualOwnershipInventory(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	implSHA, baseSHA := mustLiveInventorySHAs(t, validContent)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=speccheck-test", "-c", "user.email=speccheck@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture"},
		{"-c", "user.name=speccheck-test", "-c", "user.email=speccheck@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture head"},
	} {
		if out, err := gitOutputAt(root, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	fixtureSHA, err := resolveGitSHA(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	validContent = strings.Replace(validContent, metadataFieldLine("Implementation SHA", implSHA), metadataFieldLine("Implementation SHA", fixtureSHA), 1)
	validContent = strings.Replace(validContent, metadataFieldLine("Merged-Main Baseline SHA", baseSHA), metadataFieldLine("Merged-Main Baseline SHA", fixtureSHA), 1)
	implSHA, baseSHA = fixtureSHA, fixtureSHA

	t.Run("PublicExportKeepsFormatChecksWithoutPrivateHistory", func(t *testing.T) {
		t.Parallel()
		publicRoot := t.TempDir()
		if err := validateResidualOwnershipInventoryContent(validContent); err != nil {
			t.Fatalf("public export should not require private commit objects: %v", err)
		}
		checkInventoryProvenance(t, publicRoot, validContent)
		tampered := strings.Replace(validContent, metadataFieldLine("Implementation SHA", implSHA), metadataFieldLine("Implementation SHA", "not-a-sha"), 1)
		if err := validateResidualOwnershipInventoryContent(tampered); err == nil || !strings.Contains(err.Error(), "40-char hex") {
			t.Fatalf("public export must retain SHA format validation: %v", err)
		}
	})

	t.Run("ValidIdentity", func(t *testing.T) {
		t.Parallel()
		if err := validateSHAIdentity(root, validContent); err != nil {
			t.Fatalf("expected valid SHA identity to pass, got: %v", err)
		}
	})

	t.Run("ImplementationSHAMismatch", func(t *testing.T) {
		t.Parallel()
		// Implementation SHA must be an existing commit object AND ancestor of HEAD.
		// A commit created on a disconnected branch/tree is not an ancestor of HEAD.
		treeOutput, err := gitOutputAt(root, "rev-parse", "HEAD^{tree}")
		if err != nil {
			t.Fatalf("rev-parse tree: %v", err)
		}
		// Hermetic committer identity: commit-tree must not depend on ambient
		// git user.name/user.email (bare CI workers have none configured).
		commitOutput, err := gitOutputAt(root,
			"-c", "user.name=speccheck-test",
			"-c", "user.email=speccheck@example.invalid",
			"-c", "commit.gpgsign=false",
			"commit-tree", "-m", "test-not-ancestor", strings.TrimSpace(string(treeOutput)))
		if err != nil {
			t.Fatalf("commit-tree: %v", err)
		}
		notAncestorSHA := strings.TrimSpace(string(commitOutput))

		tampered := strings.Replace(
			validContent,
			metadataFieldLine("Implementation SHA", implSHA),
			metadataFieldLine("Implementation SHA", notAncestorSHA),
			1,
		)
		err = validateSHAIdentity(root, tampered)
		if err == nil || (!strings.Contains(err.Error(), "ancestor") && !strings.Contains(err.Error(), "HEAD")) {
			t.Fatalf("expected ancestor/HEAD error for implementation SHA, got %v", err)
		}
	})

	t.Run("ImplementationSHANotCommitObject", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			metadataFieldLine("Implementation SHA", implSHA),
			metadataFieldLine("Implementation SHA", "0000000000000000000000000000000000000000"),
			1,
		)
		err := validateSHAIdentity(root, tampered)
		if err == nil || (!strings.Contains(err.Error(), "commit object") && !strings.Contains(err.Error(), "does not exist")) {
			t.Fatalf("expected commit object error for implementation SHA, got %v", err)
		}
	})

	t.Run("ImplementationSHAAncestorOfHEADPasses", func(t *testing.T) {
		t.Parallel()
		// Older ancestor commit (e.g. baseline or parent of HEAD) must be accepted,
		// confirming no self-invalidation when commits advance.
		parentOutput, err := gitOutputAt(root, "rev-parse", "HEAD~1")
		if err == nil {
			parentSHA := strings.TrimSpace(string(parentOutput))
			if len(parentSHA) == 40 {
				tampered := strings.Replace(
					validContent,
					metadataFieldLine("Implementation SHA", implSHA),
					metadataFieldLine("Implementation SHA", parentSHA),
					1,
				)
				if err := validateSHAIdentity(root, tampered); err != nil {
					t.Fatalf("expected ancestor commit %s to pass as implementation SHA: %v", parentSHA, err)
				}
			}
		}
	})

	t.Run("BaselineSHAMismatch", func(t *testing.T) {
		t.Parallel()
		// Replace baseline SHA with non-baseline SHA
		tampered := strings.Replace(
			validContent,
			metadataFieldLine("Merged-Main Baseline SHA", baseSHA),
			metadataFieldLine("Merged-Main Baseline SHA", "0000000000000000000000000000000000000000"),
			1,
		)
		err := validateSHAIdentity(root, tampered)
		if err == nil || (!strings.Contains(err.Error(), "baseline") && !strings.Contains(err.Error(), "main")) {
			t.Fatalf("expected baseline mismatch error, got %v", err)
		}
	})
}

func TestResidualOwnershipInventoryContract_RejectsMetadataOutsideSection(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	implSHA, baseSHA := mustLiveInventorySHAs(t, validContent)

	t.Run("FieldAfterMetadataSection", func(t *testing.T) {
		t.Parallel()
		tampered := validContent + "\n- **Implementation SHA**: `" + implSHA + "`\n"
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "outside ## Baseline & Implementation Metadata") {
			t.Fatalf("expected metadata outside section error, got %v", err)
		}
	})

	t.Run("FieldBeforeMetadataSection", func(t *testing.T) {
		t.Parallel()
		tampered := "- **Inventory Date**: `2026-09-03`\n" + validContent
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "outside ## Baseline & Implementation Metadata") {
			t.Fatalf("expected metadata outside section error, got %v", err)
		}
	})

	t.Run("FieldInVocabularySection", func(t *testing.T) {
		t.Parallel()
		tampered := strings.Replace(
			validContent,
			"## Classification Vocabulary\n",
			"## Classification Vocabulary\n\n- **Merged-Main Baseline SHA**: `"+baseSHA+"`\n",
			1,
		)
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "outside ## Baseline & Implementation Metadata") {
			t.Fatalf("expected metadata outside section error, got %v", err)
		}
	})

	t.Run("FieldMovedEntirelyOutside", func(t *testing.T) {
		t.Parallel()
		// Remove Inventory Date from metadata section and place it at end of document
		tampered := strings.Replace(validContent, "- **Inventory Date**: `2026-09-03`\n", "", 1)
		tampered = tampered + "\n- **Inventory Date**: `2026-09-03`\n"
		err := validateResidualOwnershipInventoryContent(tampered)
		if err == nil || !strings.Contains(err.Error(), "outside ## Baseline & Implementation Metadata") {
			t.Fatalf("expected metadata outside section error, got %v", err)
		}
	})
}

func TestResidualOwnershipInventoryContract_RejectsDivergedBaseline(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "enterprise"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = gitscope.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-q")
	runGit("config", "user.email", "speccheck@example.invalid")
	runGit("config", "user.name", "speccheck test")
	runGit("config", "commit.gpgsign", "false")

	// Commit A: local main
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("commit A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "commit A")
	commitA := runGit("rev-parse", "HEAD")

	runGit("branch", "-M", "main")

	// Commit B: diverged commit on origin/main
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("commit B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "commit B")
	commitB := runGit("rev-parse", "HEAD")

	// Set origin/main to commit B, reset local main to commit A
	runGit("update-ref", "refs/remotes/origin/main", commitB)
	runGit("checkout", "-q", "-B", "main", commitA)

	mainSHA := runGit("rev-parse", "main")
	originMainSHA := runGit("rev-parse", "origin/main")
	if mainSHA != commitA || originMainSHA != commitB {
		t.Fatalf("git setup failed: main=%s (want %s), origin/main=%s (want %s)", mainSHA, commitA, originMainSHA, commitB)
	}

	// 1. resolveGitBaselineSHA must return local main SHA (commit A), NOT origin/main (commit B)
	baselineSHA, err := resolveGitBaselineSHA(repo)
	if err != nil {
		t.Fatalf("resolveGitBaselineSHA: %v", err)
	}
	if baselineSHA != commitA {
		t.Fatalf("resolveGitBaselineSHA returned %s, expected local main %s (not diverged origin/main %s)", baselineSHA, commitA, commitB)
	}

	root := repoRoot(t)
	_, validContent, err := loadResidualOwnershipInventory(root)
	if err != nil {
		t.Fatal(err)
	}

	// 2. If metadata specifies origin/main SHA (commit B) when main and origin/main diverge, validateSHAIdentity must reject it
	liveImplSHA, liveBaseSHA := mustLiveInventorySHAs(t, validContent)
	tampered := strings.Replace(validContent, metadataFieldLine("Merged-Main Baseline SHA", liveBaseSHA), metadataFieldLine("Merged-Main Baseline SHA", commitB), 1)
	tampered = strings.Replace(tampered, metadataFieldLine("Implementation SHA", liveImplSHA), metadataFieldLine("Implementation SHA", commitA), 1)

	err = validateSHAIdentity(repo, tampered)
	if err == nil {
		t.Fatal("expected validateSHAIdentity to reject origin/main baseline when diverged from local main")
	}
	if !strings.Contains(err.Error(), "baseline") && !strings.Contains(err.Error(), "main") {
		t.Fatalf("expected baseline mismatch error, got: %v", err)
	}

	// 3. When metadata specifies local main SHA (commit A), validateSHAIdentity accepts it
	correctContent := strings.Replace(validContent, metadataFieldLine("Merged-Main Baseline SHA", liveBaseSHA), metadataFieldLine("Merged-Main Baseline SHA", commitA), 1)
	correctContent = strings.Replace(correctContent, metadataFieldLine("Implementation SHA", liveImplSHA), metadataFieldLine("Implementation SHA", commitA), 1)
	if err := validateSHAIdentity(repo, correctContent); err != nil {
		t.Fatalf("expected validateSHAIdentity to accept local main baseline: %v", err)
	}

	// 4. Accept origin/main only when local main ref is missing
	runGit("update-ref", "-d", "refs/heads/main")
	fallbackBaseline, err := resolveGitBaselineSHA(repo)
	if err != nil {
		t.Fatalf("expected origin/main fallback when local main missing: %v", err)
	}
	if fallbackBaseline != commitB {
		t.Fatalf("expected fallback to origin/main %s, got %s", commitB, fallbackBaseline)
	}
}

func TestResidualOwnershipInventoryContract_ShallowCheckoutDiagnosis(t *testing.T) {
	t.Parallel()
	_, validContent, err := loadResidualOwnershipInventory(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	liveImplSHA, liveBaseSHA := mustLiveInventorySHAs(t, validContent)
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "enterprise"), 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(args ...string) string {
		t.Helper()
		base := []string{"-C", src, "-c", "user.email=speccheck@example.invalid", "-c", "user.name=speccheck test", "-c", "commit.gpgsign=false"}
		cmd := exec.Command("git", append(base, args...)...)
		cmd.Env = gitscope.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mk("init", "-q")
	mk("commit", "-q", "--allow-empty", "-m", "commit A")
	commitA := mk("rev-parse", "HEAD")
	mk("commit", "-q", "--allow-empty", "-m", "commit B")
	commitB := mk("rev-parse", "HEAD")
	shallow := filepath.Join(t.TempDir(), "shallow")
	mk("clone", "-q", "--depth", "1", "file://"+src, shallow)
	if err := os.Mkdir(filepath.Join(shallow, "enterprise"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(validContent, metadataFieldLine("Implementation SHA", liveImplSHA), metadataFieldLine("Implementation SHA", commitA), 1)
	content = strings.Replace(content, metadataFieldLine("Merged-Main Baseline SHA", liveBaseSHA), metadataFieldLine("Merged-Main Baseline SHA", commitB), 1)
	if err := validateSHAIdentity(src, content); err != nil {
		t.Fatalf("expected fixture SHAs to validate on full history: %v", err)
	}
	// Truncated history stays a failure, but names its cause and remedy.
	err = validateSHAIdentity(shallow, content)
	if err == nil || !strings.Contains(err.Error(), "shallow") || !strings.Contains(err.Error(), "fetch --unshallow") {
		t.Fatalf("expected fail-closed shallow diagnosis, got: %v", err)
	}
	tampered := strings.Replace(content, metadataFieldLine("Merged-Main Baseline SHA", commitB), metadataFieldLine("Merged-Main Baseline SHA", "0000000000000000000000000000000000000000"), 1)
	err = validateSHAIdentity(src, tampered)
	if err == nil || strings.Contains(err.Error(), "shallow") {
		t.Fatalf("expected full-history failure without shallow hint, got: %v", err)
	}
}
