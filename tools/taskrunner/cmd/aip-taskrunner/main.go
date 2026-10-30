package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "profile" {
		return runProfile(args[1:], stdout, stderr)
	}
	request, err := parseRequest(args, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid request: %v\n", err)
		return 4
	}
	result := taskrunner.Run(context.Background(), request)
	if request.Output == taskrunner.Capture && result.Kind == taskrunner.Success {
		if len(result.Stdout) > 0 {
			_, _ = stdout.Write(result.Stdout)
		}
		if len(result.Stderr) > 0 {
			_, _ = stderr.Write(result.Stderr)
		}
	}
	printResult(stderr, result)
	return resultExitCode(result)
}

func parseRequest(args []string, stdout, stderr io.Writer) (taskrunner.Request, error) {
	var request taskrunner.Request
	var envValues []string
	var timeoutText, outputText string
	flags := flag.NewFlagSet("aip-taskrunner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Var((*stringList)(&envValues), "env", "child environment override")
	flags.StringVar(&request.Dir, "cwd", "", "child working directory")
	flags.StringVar(&timeoutText, "timeout", "", "child timeout")
	flags.StringVar(&request.Label, "label", "", "diagnostic label")
	flags.StringVar(&outputText, "output", "stream", "stream or capture")
	flags.IntVar(&request.StdoutLimit, "stdout-limit", 64*1024, "captured stdout limit")
	flags.IntVar(&request.StderrLimit, "stderr-limit", 64*1024, "captured stderr limit")
	flags.IntVar(&request.AggregateLimit, "aggregate-limit", 256*1024, "combined captured output limit")
	flags.IntVar(&request.HeadLimit, "head-limit", 32*1024, "captured output head limit")
	flags.IntVar(&request.TailLimit, "tail-limit", 32*1024, "captured output tail limit")
	flags.BoolVar(&request.ClearEnv, "clear-env", false, "start the child with an empty environment")
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return taskrunner.Request{}, errors.New("command must follow --")
	}
	if err := flags.Parse(args[:separator]); err != nil {
		return taskrunner.Request{}, err
	}
	if timeoutText == "" {
		return taskrunner.Request{}, errors.New("--timeout is required")
	}
	parsedTimeout, err := time.ParseDuration(timeoutText)
	if err != nil {
		return taskrunner.Request{}, fmt.Errorf("--timeout: %w", err)
	}
	if parsedTimeout <= 0 {
		return taskrunner.Request{}, errors.New("--timeout must be a positive duration")
	}
	request.Timeout = parsedTimeout
	request.Env = append([]string(nil), envValues...)
	switch outputText {
	case "stream":
		request.Output = taskrunner.Stream
		request.StreamOut = stdout
		request.StreamErr = stderr
	case "capture":
		request.Output = taskrunner.Capture
	default:
		return taskrunner.Request{}, errors.New("--output must be stream or capture")
	}
	command := args[separator+1:]
	if len(command) == 0 {
		return taskrunner.Request{}, errors.New("command must follow --")
	}
	request.Argv = append([]string(nil), command...)
	return request, nil
}

func runProfile(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("profile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "profile name")
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(args); err != nil {
		return 4
	}
	if *name != "windows-full-release" {
		_, _ = fmt.Fprintln(stderr, "invalid request: --name must be windows-full-release")
		return 4
	}
	rootPath, err := filepath.Abs(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "profile start failure: %v\n", err)
		return 3
	}
	profileCtx, cancel := context.WithTimeout(context.Background(), 120*time.Minute)
	defer cancel()
	run := func(ctx context.Context, phase string) taskrunner.Result {
		return taskrunner.Run(ctx, taskrunner.Request{
			Argv:      []string{"make", phase},
			Dir:       rootPath,
			Timeout:   120 * time.Minute,
			Context:   ctx,
			Output:    taskrunner.Stream,
			StreamOut: stdout,
			StreamErr: stderr,
			Label:     "windows-full-release:" + phase,
		})
	}
	return runProfilePhases(profileCtx, windowsFullReleasePhases, run, func(result taskrunner.Result) {
		printResult(stderr, result)
	})
}

func printResult(w io.Writer, result taskrunner.Result) {
	if result.Kind == taskrunner.Success {
		return
	}
	label := result.Label
	if label == "" {
		label = "taskrunner"
	}
	_, _ = fmt.Fprintf(w, "taskrunner label=%q outcome=%s exit=%d duration=%s", label, result.Kind, result.ExitCode, result.DurationClass)
	if result.Err != nil {
		_, _ = fmt.Fprintf(w, " error=%v", result.Err)
	}
	if result.Cleanup.Err != nil {
		_, _ = fmt.Fprintf(w, " cleanup_error=%v", result.Cleanup.Err)
	}
	if len(result.Stdout) > 0 {
		_, _ = fmt.Fprintf(w, " stdout=%q", string(result.Stdout))
	}
	if len(result.Stderr) > 0 {
		_, _ = fmt.Fprintf(w, " stderr=%q", string(result.Stderr))
	}
	_, _ = fmt.Fprintln(w)
}

func resultExitCode(result taskrunner.Result) int {
	switch {
	case result.Kind == taskrunner.InvalidRequest:
		return 4
	case result.Kind == taskrunner.DeadlineExceeded:
		return 2
	case result.Kind == taskrunner.ChildFailure && result.Cleanup.Err == nil:
		return 1
	case result.Kind == taskrunner.Success:
		return 0
	default:
		return 3
	}
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if !strings.Contains(value, "=") {
		return errors.New("--env must use KEY=VALUE")
	}
	*s = append(*s, value)
	return nil
}
