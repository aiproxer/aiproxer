package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	reqHeadingRe = regexp.MustCompile(`(?m)^### Requirement (\d+):\s`)
	acItemRe     = regexp.MustCompile(`(?m)^(\d+)\.\s+\S`)
)

// parseRequirementIDs reads requirements.md and returns acceptance IDs as N.M
// from each "### Requirement N" section's numbered acceptance criteria.
func parseRequirementIDs(root string) ([]string, error) {
	path := filepath.Join(root, filepath.FromSlash(".kiro/specs/archive/backend-connector-plugin-architecture/requirements.md"))
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(b)
	locs := reqHeadingRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil, fmt.Errorf("no ### Requirement N headings in %s", path)
	}
	var out []string
	seen := map[string]struct{}{}
	for i, loc := range locs {
		reqN, _ := strconv.Atoi(text[loc[2]:loc[3]])
		start := loc[1]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		section := text[start:end]
		acStart := strings.Index(section, "#### Acceptance Criteria")
		if acStart < 0 {
			return nil, fmt.Errorf("requirement %d missing Acceptance Criteria", reqN)
		}
		body := section[acStart:]
		items := acItemRe.FindAllStringSubmatch(body, -1)
		if len(items) == 0 {
			return nil, fmt.Errorf("requirement %d has zero acceptance criteria", reqN)
		}
		for _, m := range items {
			id := fmt.Sprintf("%d.%s", reqN, m[1])
			if _, ok := seen[id]; ok {
				return nil, fmt.Errorf("duplicate acceptance id %s", id)
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}
