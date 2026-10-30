// cmd/myapp/main.go
package main

import (
	"fmt"
	"os"
)

func main() {
	// Execute owns resource cleanup before it returns; os.Exit skips main's defers.
	if err := Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err) // root uses SilenceErrors: true
		os.Exit(1)
	}
}
