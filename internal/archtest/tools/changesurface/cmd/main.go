package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aiproxer/aiproxer/internal/archtest/tools/changesurface"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("changesurface", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	base := flags.String("base", "", "classify the complete diff from this Git revision")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	githubOutput := flags.String("github-output", "", "append CI output variables to this file")
	profileOnly := flags.Bool("profile-only", false, "fail if shared, core, canonical, ABI, or extension-owned paths are changed")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	var report changesurface.Report
	var err error
	if *base == "" {
		report, err = changesurface.GitStatusReport(*root)
	} else {
		report, err = changesurface.GitBaseReport(*root, *base)
	}
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}
	if *jsonOutput {
		data, err := report.JSON()
		if err != nil {
			if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
				return 2
			}
			return 2
		}
		if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
			return 2
		}
	} else {
		if _, err := fmt.Fprint(stdout, changesurface.FormatHuman(report)); err != nil {
			return 2
		}
	}
	if *githubOutput != "" {
		if err := appendCIOutputs(*githubOutput, report.CIOutputs()); err != nil {
			if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
				return 2
			}
			return 2
		}
	}
	if *profileOnly {
		if err := report.ValidateProfileOnly(); err != nil {
			if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
				return 2
			}
			return 1
		}
	}
	return 0
}

func appendCIOutputs(path string, outputs map[string]string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open CI output %q: %w", path, err)
	}
	for _, key := range []string{"code", "profile"} {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, outputs[key]); err != nil {
			return fmt.Errorf("write CI output %q: %w", key, err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close CI output %q: %w", path, err)
	}
	return nil
}
