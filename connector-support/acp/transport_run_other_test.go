//go:build !linux

package acp_test

import (
	"bytes"
	"os"
	"testing"

	acp "github.com/aiproxer/aiproxer/connector-support/acp"
)

func TestRun_UnsupportedPlatformDoesNotSpawn(t *testing.T) {
	if os.Getenv("ACP_RUN_UNSUPPORTED") == "1" {
		_, _ = os.Stdout.WriteString("unexpected spawn")
		os.Exit(0)
	}
	var out bytes.Buffer
	err := (acp.OSProcessStarter{}).Run(t.Context(), []string{
		os.Args[0], "-test.run=TestRun_UnsupportedPlatformDoesNotSpawn$",
	}, "", []string{"ACP_RUN_UNSUPPORTED=1"}, &out, &out)
	if err == nil || out.Len() != 0 {
		t.Fatalf("unsupported runner must reject before child output: err=%v output=%q", err, out.String())
	}
}
