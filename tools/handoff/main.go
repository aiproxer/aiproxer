// Command handoff validates task-result artifacts and records restart pointers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := runHandoff(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runHandoff(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("handoff", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	task := flags.String("task", "validate", "snapshot, run, assemble, guard, validate, record, or closeout")
	var records []string
	flags.Func("command-record", "assemble: repeat for each recorder JSON sidecar", func(name string) error {
		abs, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		records = append(records, abs)
		return nil
	})
	repo := flags.String("repo", ".", "source repository")
	name := flags.String("file", "", "result JSON artifact")
	index := flags.String("index", "", "controller-owned scratch execution index")
	current := flags.Bool("current", true, "require matching current source; false is historical validation only")
	log := flags.String("log", "", "scratch output log for task=run")
	purpose := flags.String("purpose", "verification", "run evidence purpose: red, verification, baseline")
	outputMode := flags.String("output", "stream", "run output: stream or summary (full log always retained)")
	phase := flags.String("phase", "complete", "closeout phase: submitted or complete")
	indexFile := flags.String("index-file", "", "guard: explicit alternate staged index used by a partial commit")
	inventory := flags.String("inventory", "", "record: link an explicit session inventory to the existing execution index")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (*outputMode != "stream" && *outputMode != "summary") || (*task != "run" && *outputMode != "stream") {
		return errors.New("output=summary requires task=run")
	}
	if *task == "assemble" {
		if *name == "" || len(records) == 0 || flags.NArg() != 0 || *index != "" || *log != "" || !*current || *inventory != "" || *indexFile != "" {
			return errors.New("assemble requires file and command-record flags with current source")
		}
		state, err := snapshot(ctx, *repo)
		if err != nil {
			return err
		}
		return assembleResult(*name, state, records)
	}
	if len(records) != 0 {
		return errors.New("command-record requires task=assemble")
	}
	if *task == "guard" {
		if flags.NArg() == 0 || *name != "" || *index != "" || *log != "" {
			return errors.New("guard requires explicit command after --, no artifact options")
		}
		return guardCommand(ctx, *repo, *indexFile, flags.Args(), diagnostics)
	}
	if *indexFile != "" {
		return errors.New("index-file requires task=guard")
	}
	if *task == "closeout" {
		var attempts []VerificationAttempt
		if flags.NArg() != 0 || (*name == "") == (*index == "") || *log != "" || !*current || *inventory != "" {
			return errors.New("closeout requires a session file and current live evidence")
		}
		if *index != "" {
			file, err := os.Open(*index)
			if err != nil {
				return err
			}
			var state executionIndex
			decodeErr := decodeStrict(file, &state)
			closeErr := file.Close()
			if err := errors.Join(decodeErr, closeErr); err != nil {
				return err
			}
			if state.Version != 1 || state.Entries == nil || !filepath.IsAbs(state.Inventory) {
				return errors.New("execution index lacks a valid explicit inventory")
			}
			*name = state.Inventory
			attempts = state.VerificationAttempts
		}
		file, err := os.Open(*name)
		if err != nil {
			return err
		}
		var session Session
		decodeErr := decodeStrict(file, &session)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return err
		}
		session.VerificationAttempts = append(session.VerificationAttempts, attempts...)
		query := func(ctx context.Context, args ...string) ([]byte, error) { return queryGH(ctx, *repo, args...) }
		report, err := checkSession(ctx, *repo, session, *phase, query)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
		if len(report.Pending) != 0 {
			return errors.New("declared closeout obligations remain unresolved")
		}
		return nil
	}
	if *inventory != "" && *task != "record" {
		return errors.New("inventory requires task=record")
	}
	if *task == "run" {
		if flags.NArg() == 0 || *log == "" || *name != "" {
			return errors.New("run requires log and command argv after --; no file")
		}
		if *index != "" {
			absolute, err := filepath.Abs(*log)
			if err != nil {
				return err
			}
			for _, path := range []string{absolute, absolute + ".record.json"} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					return errors.New("indexed runs require fresh logs; retained attempts cannot be overwritten")
				}
			}
			// Register before execution: a crash, cancellation or source mutation
			// leaves a missing receipt, which blocks closeout instead of vanishing.
			if err := recordVerificationAttempt(*index, absolute+".record.json"); err != nil {
				return err
			}
		}
		if *outputMode == "summary" {
			diagnostics = io.Discard
		}
		return captureCommand(ctx, *repo, *log, *purpose, flags.Args(), output, diagnostics)
	}
	if flags.NArg() != 0 || *log != "" {
		return errors.New("use named flags")
	}
	if *task == "snapshot" {
		if *name != "" || *index != "" {
			return errors.New("snapshot does not accept file/index")
		}
		state, err := snapshot(ctx, *repo)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(state)
	}
	if (*task != "validate" && *task != "record") || *name == "" || (*task == "record" && *index == "") || (*task == "validate" && *index != "") {
		return errors.New("validate requires file; record requires file and index")
	}
	file, err := os.Open(*name)
	if err != nil {
		return err
	}
	result, decodeErr := decodeResult(file)
	closeErr := file.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return err
	}
	if err := result.Validate(filepath.Dir(*name)); err != nil {
		return err
	}
	if *current {
		state, err := snapshot(ctx, *repo)
		if err != nil {
			return err
		}
		if state != result.Source {
			return errors.New("source changed since evidence capture; re-verify before accepting")
		}
	}
	if *task == "record" {
		if !*current {
			return errors.New("record requires current-source validation; historical evidence cannot advance the index")
		}
		if err := recordResultWithInventory(*index, *name, result, *inventory); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output, "validated "+result.Task+" "+result.Role+" "+result.Status+" (artifact integrity only)")
	return err
}
