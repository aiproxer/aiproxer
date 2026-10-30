package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// === stdout vs stderr ===
// stdout: Program output (data, results). This is what gets piped.
// stderr: Logs, progress, errors, diagnostics. Not piped by default.

func outputExample(cmd *cobra.Command, result string) error {
	// Output data to stdout (pipeable)
	_, err := fmt.Fprintln(cmd.OutOrStdout(), result)

	// Logs and errors to stderr (use slog)
	// slog.Error("operation failed", "error", err)
	return err
}

// === Detecting Pipe vs Terminal ===

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// === Machine-Readable Output ===
// Support --output flag for different output formats.

type User struct {
	ID   string
	Name string
}

func printUsers(cmd *cobra.Command, users []User) error {
	format, _ := cmd.Flags().GetString("output")
	switch format {
	case "json":
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(users)
	case "plain":
		for _, u := range users {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", u.ID, u.Name); err != nil {
				return err
			}
		}
	default: // "table"
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME")
		for _, u := range users {
			fmt.Fprintf(w, "%s\t%s\n", u.ID, u.Name)
		}
		return w.Flush()
	}
	return nil
}

// === Colors ===
// If adopted by the product, use a color library's writer-aware API and terminal
// detection for the configured output destination; global color printers bypass
// Cobra writers and can corrupt machine-readable output.
