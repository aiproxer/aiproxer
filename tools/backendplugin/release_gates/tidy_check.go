package main

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

// canonicalModuleFile normalizes recognized text line endings (CRLF/CR → LF)
// so go.mod / go.sum comparisons are portable across Windows checkouts.
func canonicalModuleFile(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return b
}

// moduleFilesDrift reports semantic drift between current and tidy-produced
// module file bytes after line-ending canonicalization.
func moduleFilesDrift(name string, current, tidied []byte) error {
	if bytes.Equal(canonicalModuleFile(current), canonicalModuleFile(tidied)) {
		return nil
	}
	return fmt.Errorf("%s differs after go mod tidy", name)
}

// isGoDownloadingProgressLine reports whether line is exactly
// `go: downloading <non-space-module> <non-space-version>` (Windows CombinedOutput
// may emit zero or more of these before the unified tidy -diff).
func isGoDownloadingProgressLine(line []byte) bool {
	const prefix = "go: downloading "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return false
	}
	rest := line[len(prefix):]
	sp := bytes.IndexByte(rest, ' ')
	if sp <= 0 || sp == len(rest)-1 {
		return false
	}
	module, version := rest[:sp], rest[sp+1:]
	if len(module) == 0 || len(version) == 0 {
		return false
	}
	if bytes.ContainsAny(module, " \t") || bytes.ContainsAny(version, " \t") {
		return false
	}
	return true
}

// tidyDiffOnlyLineEndings reports whether a failed `go mod tidy -diff` output
// is solely CRLF↔LF rewriting of go.mod / go.sum (same logical lines).
// Non-diff failures (for example checksum security errors) return false.
// Benign `go: downloading …` progress lines are allowed only before the first
// `diff ` header; any other pre-diff diagnostic, or any diagnostic after diff
// processing begins, fails closed.
func tidyDiffOnlyLineEndings(diff []byte) bool {
	if !bytes.Contains(diff, []byte("diff current/")) {
		return false
	}
	var minus, plus [][]byte
	flush := func() bool {
		if len(minus) != len(plus) {
			return false
		}
		for i := range minus {
			if !bytes.Equal(minus[i], plus[i]) {
				return false
			}
		}
		minus, plus = nil, nil
		return true
	}
	sawFile := false
	inHunk := false
	for line := range bytes.SplitSeq(diff, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("diff ")):
			if sawFile && !flush() {
				return false
			}
			sawFile = true
			inHunk = false
		case bytes.HasPrefix(line, []byte("--- ")),
			bytes.HasPrefix(line, []byte("+++ ")):
			// file headers
		case bytes.HasPrefix(line, []byte("@@")):
			inHunk = true
		case bytes.HasPrefix(line, []byte("-")):
			minus = append(minus, canonicalModuleFile(line[1:]))
		case bytes.HasPrefix(line, []byte("+")):
			plus = append(plus, canonicalModuleFile(line[1:]))
		case bytes.HasPrefix(line, []byte(" ")) && inHunk:
			// Unified-diff context (unchanged logical line). Accept only inside
			// a recognized @@ hunk; never fold into +/- comparison.
		case bytes.HasPrefix(line, []byte("\\")):
			// "\ No newline at end of file" — ignore
		case len(bytes.TrimSpace(line)) == 0:
			// blank
		case !sawFile && isGoDownloadingProgressLine(line):
			// Windows may print download progress before the unified diff.
		default:
			// unexpected non-diff diagnostics mixed in
			return false
		}
	}
	if !sawFile {
		return false
	}
	return flush()
}

// checkModuleTidy runs `go mod tidy -diff` in modRoot (so local replace
// directives keep resolving) and treats CRLF↔LF-only rewrites as tidy-clean.
// go mod tidy -diff restores module files itself; this gate does not leave
// lasting worktree mutations.
func checkModuleTidy(modRoot string) ([]byte, error) {
	result := runner.Run(context.Background(), runner.Request{
		Argv:    []string{"go", "mod", "tidy", "-diff"},
		Dir:     modRoot,
		Env:     []string{"GOWORK=off"},
		Timeout: 8 * time.Minute,
		Output:  taskrunner.Capture,
		Label:   "release_gates:" + modRoot + ":tidy_diff",
	})
	out := result.Stdout
	if result.Kind == taskrunner.Success {
		return out, nil
	}
	if tidyDiffOnlyLineEndings(out) {
		return out, nil
	}
	return out, fmt.Errorf("go mod tidy -diff: %w", runner.Error(result))
}
