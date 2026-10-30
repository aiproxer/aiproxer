package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type explicitTestSkipFlag struct {
	name string
	set  bool
}

func (f *explicitTestSkipFlag) String() string { return f.name }

func (f *explicitTestSkipFlag) Set(name string) error {
	f.name = name
	f.set = true
	return nil
}

func validateExplicitTestSkip(set bool, name, task, scope string, full bool, base string, planOnly bool) error {
	if !set {
		return nil
	}
	if name == "" {
		return errors.New("-skip-test requires an exact top-level Test name")
	}
	if task != "test" || scope != "explicit" || full || base != "" || planOnly {
		return errors.New("-skip-test is available only for explicit -task=test runs")
	}
	if !strings.HasPrefix(name, "Test") || !quarantineName.MatchString(name) {
		return fmt.Errorf("-skip-test requires an exact top-level Test name without regexp or subtest syntax: %q", name)
	}
	return nil
}

func effectiveGOFlags(ctx context.Context, dir string, env []string) (string, error) {
	cmd := exec.Command("go", "env", "GOFLAGS")
	cmd.Dir = dir
	cmd.Env = env
	output, err := commandOutput(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("read effective GOFLAGS: %w", err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func splitGoFlags(value string) ([]string, error) {
	var fields []string
	for len(value) > 0 {
		for len(value) > 0 && isGoFlagSpace(value[0]) {
			value = value[1:]
		}
		if len(value) == 0 {
			break
		}
		if value[0] == '\'' || value[0] == '"' {
			quote := value[0]
			value = value[1:]
			i := strings.IndexByte(value, quote)
			if i < 0 {
				return nil, fmt.Errorf("unterminated %c string in GOFLAGS", quote)
			}
			fields = append(fields, value[:i])
			value = value[i+1:]
			continue
		}
		i := 0
		for i < len(value) && !isGoFlagSpace(value[i]) {
			i++
		}
		fields = append(fields, value[:i])
		value = value[i:]
	}
	return fields, nil
}

func isGoFlagSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func lastGoFlagsSkip(fields []string) (string, bool, error) {
	var pattern string
	var found bool
	for i := range fields {
		field := fields[i]
		if value, ok := strings.CutPrefix(field, "-skip="); ok {
			pattern, found = value, true
			continue
		}
		if value, ok := strings.CutPrefix(field, "--skip="); ok {
			pattern, found = value, true
			continue
		}
		if field == "-skip" || field == "--skip" {
			return "", false, fmt.Errorf("%s in GOFLAGS must use %s=value", field, field)
		}
	}
	return pattern, found, nil
}

func combinedTestSkipPattern(goFlags string, quarantine []quarantineEntry, testName string) (string, error) {
	fields, err := splitGoFlags(goFlags)
	if err != nil {
		return "", err
	}
	inherited, found, err := lastGoFlagsSkip(fields)
	if err != nil {
		return "", err
	}
	var patterns []string
	if found && inherited != "" {
		patterns = append(patterns, inherited)
	}
	if quarantined := skipPattern(quarantine); quarantined != "" {
		patterns = append(patterns, quarantined)
	}
	if testName != "" {
		patterns = append(patterns, "^"+testName+"$")
	}
	return strings.Join(patterns, "|"), nil
}

func withExplicitTestSkip(command []string, pattern string) []string {
	if pattern == "" || len(command) < 2 || command[1] != "test" {
		return command
	}
	result := make([]string, 0, len(command)+1)
	result = append(result, command[:2]...)
	result = append(result, "-skip="+pattern)
	result = append(result, command[2:]...)
	return result
}
