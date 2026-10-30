package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// quarantineFile lists tests that fail on main. Each entry is skipped by local
// and CI test runs until its tracking issue is fixed, so feature branches do
// not absorb unrelated repairs.
const quarantineFile = ".github/test-quarantine.txt"

var (
	quarantineName  = regexp.MustCompile(`^(Test|Fuzz|Benchmark|Example)[A-Za-z0-9_]*$`)
	quarantineIssue = regexp.MustCompile(`^https://github\.com/aiproxer/aiproxer(-enterprise)?/issues/[0-9]+$`)
)

type quarantineEntry struct {
	Test  string
	Issue string
}

func parseQuarantine(input io.Reader) ([]quarantineEntry, error) {
	var entries []quarantineEntry
	seen := map[string]bool{}
	scanner := bufio.NewScanner(input)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#") {
			// The Makefile reads every word that looks like a test name.
			for word := range strings.FieldsSeq(text) {
				if quarantineName.MatchString(word) {
					return nil, fmt.Errorf("%s:%d: comment word %q looks like a test name", quarantineFile, line, word)
				}
			}
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 || !quarantineName.MatchString(fields[0]) || !quarantineIssue.MatchString(fields[1]) {
			return nil, fmt.Errorf("%s:%d: want \"<TestName> <issue-url>\", got %q", quarantineFile, line, text)
		}
		if seen[fields[0]] {
			return nil, fmt.Errorf("%s:%d: duplicate entry %s", quarantineFile, line, fields[0])
		}
		seen[fields[0]] = true
		entries = append(entries, quarantineEntry{Test: fields[0], Issue: fields[1]})
	}
	return entries, scanner.Err()
}

func loadQuarantine(root string) (entries []quarantineEntry, err error) {
	file, err := os.Open(filepath.Join(root, quarantineFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return parseQuarantine(file)
}

// skipPattern returns a go test -skip expression matching exactly the
// quarantined top-level tests, or "" when nothing is quarantined.
func skipPattern(entries []quarantineEntry) string {
	if len(entries) == 0 {
		return ""
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Test
	}
	return "^(" + strings.Join(names, "|") + ")$"
}

func withQuarantine(command []string, entries []quarantineEntry) []string {
	pattern := skipPattern(entries)
	if pattern == "" || len(command) < 2 || command[1] != "test" {
		return command
	}
	return append([]string{command[0], command[1], "-skip=" + pattern}, command[2:]...)
}

// checkQuarantine fails when an entry no longer names a test in the repository,
// so fixed or renamed tests leave the list instead of staying silently skipped.
func checkQuarantine(ctx context.Context, root string, entries []quarantineEntry) error {
	var stale []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd := exec.Command("git", "grep", "-q", "--untracked", "-E", `^func `+entry.Test+`\(`, "--", "*_test.go")
		cmd.Dir = root
		if _, err := execute(ctx, cmd, false, io.Discard, nil); err != nil {
			if ctx.Err() != nil {
				return err
			}
			stale = append(stale, entry.Test)
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("%s names tests that no longer exist: %s", quarantineFile, strings.Join(stale, ", "))
	}
	return nil
}
