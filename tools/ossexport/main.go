// Command ossexport builds the deterministic open-source export of an
// AIProxer repository revision.
//
// The export is the published AIProxer tree: the repository revision without
// the paths the repository excludes from publication (Git export-ignore
// attributes). The tool archives exactly the requested revision, refuses an
// export whose published tree can still reach the Enterprise tree, and proves
// the published tree builds and vets.
//
// The archive is a function of the commit alone: two exports of one revision
// produce byte-identical archives. The tool never reads the working tree.
//
// The extraction refuses an archive entry that would leave the export root and
// refuses every entry that is not a regular file or a directory, so a symlink
// is reported with its name instead of being dropped silently. The repository
// tracks no symlink, so the export carries none.
//
// The link scan reads Markdown documents as CommonMark and HTML documents as
// HTML, and refuses a document that carries a base element with a destination,
// because the export does not resolve base URLs. The published tree of the
// repository carries no such document.
//
// Usage:
//
//	ossexport -rev <revision> -out <archive.tar> [-repo <dir>] [-extract-dir <dir>]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const toolName = "ossexport"

func main() {
	opts, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// A help request is not a failure, the flag package prints the
			// usage itself.
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", toolName, err)
		os.Exit(2)
	}
	res, err := export(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", toolName, err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stdout,
		"revision=%s\nmodule=%s\narchive=%s\nsha256=%s\nfiles=%d\nextracted_tree=%s\n",
		res.Revision, res.Module, res.Archive, res.Digest, res.Files, res.Tree)
}

// options configures one export run.
type options struct {
	repo       string
	rev        string
	out        string
	extractDir string
}

// result describes a completed export.
type result struct {
	Revision string
	Module   string
	Archive  string
	Digest   string
	Files    int
	Tree     string
}

// parseArgs reads the command line.
func parseArgs(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.rev, "rev", "HEAD", "revision to export; resolved to a commit, the working tree is ignored")
	fs.StringVar(&opts.out, "out", "", "path of the tar archive to write (required)")
	fs.StringVar(&opts.repo, "repo", "", "repository to export from (default: the repository of the current directory)")
	fs.StringVar(&opts.extractDir, "extract-dir", "", "directory that keeps the extracted export tree (default: a staging directory that is removed on exit)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s -rev <revision> -out <archive.tar> [-repo <dir>] [-extract-dir <dir>]\n\n", toolName)
		_, _ = fmt.Fprintf(stderr, "The export archives the requested revision, refuses an export that can still reach the\n")
		_, _ = fmt.Fprintf(stderr, "Enterprise tree, and proves the exported tree builds and vets.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.out == "" {
		return options{}, errors.New("-out is required")
	}
	return opts, nil
}

// violation names one offending exported file and the reason it is refused.
type violation struct {
	File   string
	Reason string
}

// rejectionError reports a refused export. It lists every offending file.
type rejectionError struct {
	violations []violation
}

func (e *rejectionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "export rejected: %d violation(s)", len(e.violations))
	for _, v := range e.violations {
		fmt.Fprintf(&b, "\n  %s: %s", v.File, v.Reason)
	}
	return b.String()
}

// sortViolations orders the disposition by file and reason so two runs of the
// same revision report the same list.
func sortViolations(violations []violation) {
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Reason < violations[j].Reason
	})
}
