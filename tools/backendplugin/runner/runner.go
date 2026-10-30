package runner

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

// Request is the small backend-plugin-facing subset of taskrunner.Request.
type Request struct {
	Argv           []string
	Dir            string
	Env            []string
	Timeout        time.Duration
	Output         taskrunner.OutputMode
	StreamOut      io.Writer
	StreamErr      io.Writer
	Label          string
	StdoutLimit    int
	StderrLimit    int
	AggregateLimit int
	HeadLimit      int
	TailLimit      int
}

func Run(ctx context.Context, req Request) taskrunner.Result {
	return taskrunner.Run(ctx, taskrunner.Request{
		Argv:           req.Argv,
		Dir:            req.Dir,
		Env:            req.Env,
		Timeout:        req.Timeout,
		Output:         req.Output,
		StreamOut:      req.StreamOut,
		StreamErr:      req.StreamErr,
		Label:          req.Label,
		StdoutLimit:    req.StdoutLimit,
		StderrLimit:    req.StderrLimit,
		AggregateLimit: req.AggregateLimit,
		HeadLimit:      req.HeadLimit,
		TailLimit:      req.TailLimit,
	})
}

// Error keeps the old command diagnostics while making timeout and cleanup
// classifications visible to callers.
func Error(result taskrunner.Result) error {
	var detail []string
	if len(result.Stdout) > 0 {
		detail = append(detail, string(result.Stdout))
	}
	if len(result.Stderr) > 0 {
		detail = append(detail, string(result.Stderr))
	}
	if result.Err != nil {
		detail = append(detail, result.Err.Error())
	}
	if result.Cleanup.Err != nil {
		detail = append(detail, fmt.Sprintf("cleanup: %v", result.Cleanup.Err))
	}
	message := string(result.Kind)
	if result.Label != "" {
		message = result.Label + ": " + message
	}
	if len(detail) > 0 {
		return fmt.Errorf("%s\n%s", message, strings.TrimSpace(strings.Join(detail, "\n")))
	}
	return fmt.Errorf("%s", message)
}
