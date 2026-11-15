// Command kirocheck validates Kiro specs, agent policy documents and
// Dependabot ignores. The separate spec-only hook command has no third-party
// module dependencies, so it can run offline with a cold module cache.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/aiproxer/aiproxer/internal/qa/kirospec"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	errs := validate(*root)
	for _, err := range errs {
		fmt.Fprintln(os.Stderr, err)
	}
	if len(errs) != 0 {
		os.Exit(1)
	}
	fmt.Println("kirocheck: Kiro specs and agent policy documents OK")
}

func validate(root string) []string {
	if err := kirospec.ValidateGitMetadata(root); err != nil {
		return []string{err.Error()}
	}
	errs := kirospec.Validate(root)
	errs = append(errs, validateDependabotIgnores(root)...)
	sort.Strings(errs)
	return errs
}
