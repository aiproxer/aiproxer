package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// processHostTestDir is the process-host package relative to this tool.
const processHostTestDir = "../../../internal/infra/backendplugins/processhost"

// TestNativeProcessHostGate_SelectsEveryMandatoryTest proves the native gate
// selects every mandatory process-host test by name.
//
// The native cross-platform gate is the only place the Windows process-host
// guarantees can be executed. A selection that silently skipped them would
// report a green native run without proving the diagnostic budget, the output
// capture or the process-tree termination.
func TestNativeProcessHostGate_SelectsEveryMandatoryTest(t *testing.T) {
	t.Parallel()
	if len(nativeProcessHostGateTests) == 0 {
		t.Fatal("the native gate must select at least one process-host test")
	}
	pattern := nativeProcessHostGatePattern()
	if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")$") {
		t.Fatalf("the native gate pattern must be anchored: %q", pattern)
	}
	re := nativeProcessHostGateRegex()
	seen := map[string]struct{}{}
	for _, name := range nativeProcessHostGateTests {
		if _, dup := seen[name]; dup {
			t.Fatalf("the native gate lists %q twice", name)
		}
		seen[name] = struct{}{}
		if !re.MatchString(name) {
			t.Fatalf("the native gate pattern %q does not select %q", pattern, name)
		}
		// A literal name must not match a wider pattern: an entry that
		// matches more than itself would run unrelated tests under the
		// mandatory label.
		if matched := re.FindString(name); matched != name {
			t.Fatalf("the native gate pattern %q matches %q for %q", pattern, matched, name)
		}
	}
}

// TestNativeProcessHostGate_EveryMandatoryTestExists proves every name in
// the mandatory list still exists in the process-host package.
//
// A renamed or deleted test would otherwise leave a stale entry that matches
// nothing, and the native run would report green without running it.
func TestNativeProcessHostGate_EveryMandatoryTestExists(t *testing.T) {
	t.Parallel()
	present := processHostTestNames(t)
	for _, name := range nativeProcessHostGateTests {
		if _, ok := present[name]; !ok {
			t.Fatalf("the native gate requires %q, which the process-host package does not define", name)
		}
	}
}

// TestNativeGateSteps_RunTheMandatoryProcessHostTests proves the native gate
// actually invokes the mandatory selection, so removing the step fails here
// instead of silently narrowing the native evidence.
func TestNativeGateSteps_RunTheMandatoryProcessHostTests(t *testing.T) {
	t.Parallel()
	pattern := nativeProcessHostGatePattern()
	found := false
	for _, step := range nativeGateSteps(t.TempDir()) {
		targetsProcessHost := false
		for _, arg := range step.args {
			if strings.Contains(arg, "internal/infra/backendplugins/processhost") {
				targetsProcessHost = true
			}
		}
		if !targetsProcessHost {
			continue
		}
		for i, arg := range step.args {
			if arg == "-run" && i+1 < len(step.args) && step.args[i+1] == pattern {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no native gate step runs the mandatory process-host selection")
	}
}

// processHostTestNames returns the top-level test function names declared in
// the process-host package test files.
func processHostTestNames(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(processHostTestDir)
	if err != nil {
		t.Fatalf("read process-host test dir: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)
	names := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(processHostTestDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, match := range decl.FindAllStringSubmatch(string(raw), -1) {
			names[match[1]] = struct{}{}
		}
	}
	if len(names) == 0 {
		t.Fatalf("no process-host test functions found under %s", processHostTestDir)
	}
	return names
}
