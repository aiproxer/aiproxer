package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/archtest/tools/changesurface"
)

type lineChanges struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

type deliveryReport struct {
	Base          string               `json:"base_ref"`
	Comparison    string               `json:"merge_base_sha"`
	Head          string               `json:"head_sha"`
	Working       bool                 `json:"includes_working_tree"`
	Files         int                  `json:"changed_files"`
	GoFiles       int                  `json:"changed_go_files"`
	Production    lineChanges          `json:"production_go"`
	Tests         lineChanges          `json:"test_go"`
	Paths         []string             `json:"paths"`
	PreviousPaths []string             `json:"previous_paths"`
	Binary        []string             `json:"binary_paths"`
	Surfaces      changesurface.Report `json:"dependency_review_surfaces"`
	Consumer      string               `json:"declared_first_consumer"`
	BuildVerified bool                 `json:"independent_build_verified"`
	HardLimit     struct {
		Limit    int  `json:"go_files"`
		Exceeded bool `json:"exceeded"`
	} `json:"hard_limit"`
	Advisory []string `json:"advisory_budget_signals"`
	Pending  []string `json:"required_review"`
}

func writeDeliveryReport(repo, base, head, consumer string, output io.Writer) error {
	if base == "" {
		base = "origin/main"
	}
	working := head == ""
	if working {
		head = "HEAD"
	}
	resolvedHead, err := gitOutput(repo, "rev-parse", "--verify", head+"^{commit}")
	if err != nil {
		return err
	}
	comparison, err := gitOutput(repo, "merge-base", "--", base, strings.TrimSpace(string(resolvedHead)))
	if err != nil {
		return err
	}
	r := deliveryReport{Base: base, Comparison: strings.TrimSpace(string(comparison)), Head: strings.TrimSpace(string(resolvedHead)), Working: working, Consumer: consumer}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--numstat", "-z", r.Comparison}
	if !working {
		args = append(args, r.Head)
	}
	args = append(args, "--")
	out, err := gitOutput(repo, args...)
	if err != nil {
		return err
	}
	if err := r.addNumstat(out); err != nil {
		return err
	}
	if working {
		conflicts, err := gitOutput(repo, "diff", "--name-only", "--diff-filter=U", "-z")
		if err != nil {
			return err
		}
		if len(conflicts) != 0 {
			return fmt.Errorf("resolve unmerged paths before reporting delivery")
		}
		out, err = gitOutput(repo, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return err
		}
		for _, name := range splitGitNames(out) {
			data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			binary := bytes.Contains(data, []byte{0})
			lines := bytes.Count(data, []byte{'\n'})
			if len(data) > 0 && data[len(data)-1] != '\n' {
				lines++
			}
			r.addFile(name, lines, 0, binary)
		}
	}
	slices.Sort(r.Paths)
	r.Paths = slices.Compact(r.Paths)
	r.Files = len(r.Paths)
	r.GoFiles = uniqueGoPathCount(r.Paths)
	r.HardLimit.Limit = DefaultLimit
	r.HardLimit.Exceeded = r.GoFiles > DefaultLimit
	r.Surfaces = changesurface.Build(append(slices.Clone(r.Paths), r.PreviousPaths...))
	r.Pending = []string{"Build/test each slice independently at its intended base; this report runs neither.", "Review dependencies and first-consumer semantics; path categories and declarations do not prove them."}
	if r.Files > 40 {
		r.Advisory = append(r.Advisory, "More than ~40 changed files; split the slice.")
	}
	if r.Production.Added > 1500 {
		r.Advisory = append(r.Advisory, "More than ~1500 added non-test Go lines; split the slice.")
	}
	if r.Tests.Added > 5*r.Production.Added {
		r.Advisory = append(r.Advisory, "Test additions exceed ~5x production additions; review proportionality (test-only slices need judgment).")
	}
	if consumer == "" {
		r.Pending = append(r.Pending, "For a substrate, declare its immediate real consumer with --consumer.")
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

func (r *deliveryReport) addNumstat(data []byte) error {
	records := splitGitNames(data)
	for i := 0; i < len(records); i++ {
		record := records[i]
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			return fmt.Errorf("invalid numstat record %q", record)
		}
		if fields[2] == "" {
			if i+2 >= len(records) {
				return fmt.Errorf("incomplete numstat rename %q", record)
			}
			r.PreviousPaths = append(r.PreviousPaths, records[i+1])
			fields[2] = records[i+2]
			i += 2
		}
		if fields[0] == "-" && fields[1] == "-" {
			r.addFile(fields[2], 0, 0, true)
			continue
		}
		added, err := strconv.Atoi(fields[0])
		if err != nil {
			return err
		}
		deleted, err := strconv.Atoi(fields[1])
		if err != nil || added < 0 || deleted < 0 {
			return fmt.Errorf("invalid numstat counts %q", record)
		}
		r.addFile(fields[2], added, deleted, false)
	}
	return nil
}

func (r *deliveryReport) addFile(name string, added, deleted int, binary bool) {
	r.Paths = append(r.Paths, name)
	if binary {
		r.Binary = append(r.Binary, name)
	}
	if !strings.HasSuffix(name, ".go") {
		return
	}
	if binary {
		return
	}
	counts := &r.Production
	if strings.HasSuffix(name, "_test.go") {
		counts = &r.Tests
	}
	counts.Added += added
	counts.Deleted += deleted
}
