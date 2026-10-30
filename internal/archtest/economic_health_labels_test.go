package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 16.3B static authority gate: the aip_economic_ Prometheus series
// namespace originates only from the metrics ports. The open-source metrics
// bundle no longer owns the collector; the Enterprise metrics package is the
// single owner after the 5.4-C cutover. No production handler, store or core
// file may hard-code economics metric names, so label and metric cardinality
// stay owned by one bounded allowlist. Runtime label bucketing and the
// 128-series cap are certified by the metrics package tests.
func TestEconomicHealthMetricOriginRestricted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	metricsDirs := []string{
		filepath.Join(root, "internal", "infra", "metrics"),
		filepath.Join(root, "enterprise", "internal", "infra", "metrics"),
	}
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, metricsDir := range metricsDirs {
			if strings.HasPrefix(path, metricsDir+string(os.PathSeparator)) {
				return nil
			}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "aip_economic_") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("aip_economic_ series outside the metrics port: %v", offenders)
	}
}
