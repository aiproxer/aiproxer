package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aiproxer/aiproxer/internal/testkit/nativecontext"
)

func main() {
	jsonOnly := flag.Bool("json-only", false, "write only the machine-readable report")
	flag.Parse()
	report := nativecontext.DeterministicReport()
	raw, err := report.JSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaluation report invalid: %v\n", err)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(append(raw, '\n'))
	if !*jsonOnly {
		fmt.Fprintf(os.Stderr, "native-context evaluation: tasks=%d modes=%d paired=%t break_even_turns=%d quality_claim=%s\n",
			report.Summary.TaskCount, report.Summary.ModeCount, report.Summary.Paired,
			report.Summary.BreakEvenTurns, report.Summary.QualityClaim)
	}
}
