// Command kirocheck validates the Kiro spec tree (lifecycle, slice budgets,
// stale archived-spec references). The pre-commit hook runs it with `go run`
// so spec-only commits do not queue for a heavy Go slot.
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
	errs := kirospec.Validate(*root)
	for _, err := range errs {
		fmt.Fprintln(os.Stderr, err)
	}
	if len(errs) != 0 {
		os.Exit(1)
	}
	fmt.Println("kirocheck: Kiro specs OK")
}
