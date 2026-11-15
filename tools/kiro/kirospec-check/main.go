// Command kirospec-check validates spec lifecycle, budgets and stale
// references without third-party modules for the spec-only pre-commit path.
package main

import (
	"flag"
	"fmt"
	"os"

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
	fmt.Println("kirospec-check: Kiro specs OK")
}

func validate(root string) []string {
	if err := kirospec.ValidateGitMetadata(root); err != nil {
		return []string{err.Error()}
	}
	return kirospec.ValidateSpecs(root)
}
