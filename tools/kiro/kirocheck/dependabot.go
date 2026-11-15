package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aiproxer/aiproxer/internal/qa/kirospec"
	"gopkg.in/yaml.v3"
)

var issueURL = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/issues/\d+`)

// validateDependabotIgnores requires a tracking issue directly above each entry.
func validateDependabotIgnores(root string) []string {
	path := filepath.Join(root, ".github", "dependabot.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && kirospec.ExportOmitted(root, path) {
			return nil
		}
		return []string{fmt.Sprintf(".github/dependabot.yml: %v", err)}
	}
	var config struct {
		Updates []struct {
			Ignore yaml.Node `yaml:"ignore"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return []string{fmt.Sprintf(".github/dependabot.yml: invalid YAML: %v", err)}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var errs []string
	for _, update := range config.Updates {
		list := update.Ignore
		if list.Kind == 0 || list.Tag == "!!null" {
			continue
		}
		if list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 && len(list.Content) > 0 {
			errs = append(errs, fmt.Sprintf(".github/dependabot.yml:%d: ignore must be a block list with a tracking issue above each entry", list.Line))
			continue
		}
		for _, item := range list.Content {
			linked := false
			for i := item.Line - 2; i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "#"); i-- {
				linked = linked || issueURL.MatchString(lines[i])
			}
			if !linked {
				errs = append(errs, fmt.Sprintf(".github/dependabot.yml:%d: ignore entry needs its tracking issue URL in the comment directly above it", item.Line))
			}
		}
	}
	return errs
}
