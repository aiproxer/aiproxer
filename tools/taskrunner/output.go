package taskrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// Output collects complete machine-readable stdout while retaining process-tree
// ownership and the request's timeout. Unlike diagnostic Capture, it must not
// truncate package graphs or Git's NUL-delimited path lists.
func Output(ctx context.Context, req Request) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	req.Output, req.StreamOut, req.StreamErr = Stream, &stdout, &stderr
	result := Run(ctx, req)
	err := errors.Join(result.Err, result.Cleanup.Err, result.AccountingErr)
	if err != nil {
		err = fmt.Errorf("%v: %w: %s", req.Argv, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), err
}
