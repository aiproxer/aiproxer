// Command external-billing-binding runs the external billing certification
// smoke: binding validation, token-independent quoting, and submission
// rating through public packages only.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := VerifyContract(); err != nil {
		fmt.Fprintf(os.Stderr, "external_billing_binding: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("external_billing_binding: ok")
}

// VerifyContract executes the run-mode certification smoke.
func VerifyContract() error {
	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		return fmt.Errorf("AssembleBinding: %w", err)
	}
	if err := fix.Binding.Validate(); err != nil {
		return fmt.Errorf("binding invalid: %w", err)
	}
	small, err := testQuoter.Quote(context.Background(), quoteInput(10))
	if err != nil {
		return fmt.Errorf("small quote: %w", err)
	}
	large, err := testQuoter.Quote(context.Background(), quoteInput(10_000_000))
	if err != nil {
		return fmt.Errorf("large quote: %w", err)
	}
	if small.Maximum != large.Maximum {
		return fmt.Errorf("token-dependent quote: %+v vs %+v", small.Maximum, large.Maximum)
	}
	if tracker.totalStarts() != 0 || tracker.totalCloses() != 0 {
		return fmt.Errorf("validation must not start resources: %+v", tracker.snapshot())
	}
	return nil
}
