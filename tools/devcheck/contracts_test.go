package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
)

func TestContracts_MissingTestsCannotPass(t *testing.T) {
	t.Parallel()
	names := []string{"TestOne", "TestTwo"}
	if err := validateListedContracts(names, "TestOne\nTestTwo\nok example 0.1s\n"); err != nil {
		t.Fatal(err)
	}
	if err := validateListedContracts(names, "TestOne\nok example\n"); err == nil {
		t.Fatal("missing test reported as checked")
	}
	pattern := regexp.MustCompile(contractPattern(names))
	if !pattern.MatchString("TestOne") || pattern.MatchString("TestOneOther") {
		t.Fatalf("filter does not select exact contracts: %s", pattern)
	}
}

func TestContracts_ManifestRecordsFailedCommandAndLog(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVCHECK_TEST_HELPER", "fail")
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "contracts", evidence.Scope{Kind: "changed"}, ".")
	err = runContractCommandRecorded(t.Context(), ".", []string{exe, "-test.run=^TestDevcheckHelperProcess$"}, true, io.Discard, io.Discard, recorder)
	if err == nil {
		t.Fatal("failed contract reported as success")
	}
	if err := recorder.finish(err); err != nil {
		t.Fatal(err)
	}
	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeFailed || len(manifest.Steps) != 1 || manifest.Steps[0].ExitCode != 7 || manifest.Steps[0].Tests == nil || manifest.Steps[0].Tests.Failed != 1 {
		t.Fatalf("contract evidence lost the failure: %+v", manifest)
	}
	data, err := os.ReadFile(manifest.Steps[0].LogPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("missing actual contract log: %v", err)
	}
}

func TestContracts_ChildFailureCannotPass(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVCHECK_TEST_HELPER", "fail")
	for _, test := range []bool{false, true} {
		if err := runContractCommand(context.Background(), ".", []string{exe, "-test.run=^TestDevcheckHelperProcess$"}, test, io.Discard, io.Discard); err == nil {
			t.Fatalf("failed contract/lint reported as success (test=%v)", test)
		}
	}
}
