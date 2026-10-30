package testscope

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/internal/scopeplan"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func commandOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	return taskrunner.Output(ctx, taskrunner.Request{
		Argv: append([]string{name}, args...), Dir: dir,
		Env: append(gitscope.Environ(), "GOWORK=off"), ClearEnv: true, Timeout: 2 * time.Minute,
	})
}

func changedPaths(ctx context.Context, root, base string) ([]string, string, error) {
	return scopeplan.Changes(ctx, root, "branch", base)
}

func statusPaths(out []byte) ([]string, error) {
	var paths []string
	for record := range strings.SplitSeq(string(out), "\x00") {
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' || strings.ContainsAny(record[:2], "RC") {
			return nil, fmt.Errorf("unexpected Git status record: %q", record)
		}
		paths = append(paths, record[3:])
	}
	return paths, nil
}
