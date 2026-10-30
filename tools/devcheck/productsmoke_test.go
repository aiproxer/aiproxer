package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/productsmoke"
	"github.com/aiproxer/aiproxer/tools/internal/sourcestate"
)

// testPackage is the package the product smoke compiles with the productsmoke
// build tag.
const testPackage = "github.com/aiproxer/aiproxer/internal/testkit/productsmoke"

// productTestEvent renders one test-level go test -json event.
func productTestEvent(action, test string, elapsed float64) string {
	event := map[string]any{
		"Action":  action,
		"Package": testPackage,
		"Test":    test,
		"Elapsed": elapsed,
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// productOutputEvent renders one package-level go test -json output event.
func productOutputEvent(output string) string {
	event := map[string]any{
		"Action":  "output",
		"Package": testPackage,
		"Output":  output,
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// productTestOutputEvent renders one go test -json output event attributed to a
// running test, as Go 1.26 emits the test-binary timeout panic.
func productTestOutputEvent(test, output string) string {
	event := map[string]any{
		"Action":  "output",
		"Package": testPackage,
		"Test":    test,
		"Output":  output,
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func productStream(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// testProductRequirements mirrors the required-journey shape the reconciler
// receives after platform filtering and quarantine mapping.
func testProductRequirements() []productSmokeJourney {
	return []productSmokeJourney{
		{ID: "TestProduct_Startup/ready"},
		{ID: "TestProduct_Startup/invalid_config"},
		{ID: "TestProduct_Protocols/openai-legacy/stream"},
		{ID: "TestProduct_Protocols/openai-legacy/collect"},
		{ID: "TestProduct_Lifecycle/canary_credential"},
	}
}

// passingStream records a run in which every test in requirements passes.
func passingStream(required []productSmokeJourney) string {
	var lines []string
	for _, journey := range required {
		lines = append(lines, productTestEvent("run", journey.ID, 0))
		lines = append(lines, productTestEvent("pass", journey.ID, 0.25))
	}
	lines = append(lines, productTestEvent("run", "TestHarnessSelfCheck", 0))
	lines = append(lines, productTestEvent("pass", "TestHarnessSelfCheck", 0.01))
	lines = append(lines, productOutputEvent("ok  \t"+testPackage+"\t0.260s\n"))
	return productStream(lines...)
}

// passingStreamWithout is passingStream minus one journey's events.
func passingStreamWithout(required []productSmokeJourney, missing string) string {
	var lines []string
	for _, journey := range required {
		if journey.ID == missing {
			continue
		}
		lines = append(lines, productTestEvent("run", journey.ID, 0))
		lines = append(lines, productTestEvent("pass", journey.ID, 0.25))
	}
	lines = append(lines, productOutputEvent("ok  \t"+testPackage+"\t0.260s\n"))
	return productStream(lines...)
}

// passingStreamWithoutRun is passingStream minus one journey's run event. Go
// does not emit this sequence for a fresh execution; it is malformed telemetry
// that a terminal PASS must not be accepted from.
func passingStreamWithoutRun(required []productSmokeJourney, missing string) string {
	var lines []string
	for _, journey := range required {
		if journey.ID != missing {
			lines = append(lines, productTestEvent("run", journey.ID, 0))
		}
		lines = append(lines, productTestEvent("pass", journey.ID, 0.25))
	}
	lines = append(lines, productOutputEvent("ok  \t"+testPackage+"\t0.260s\n"))
	return productStream(lines...)
}

func replayProductStream(t *testing.T, stream string) productObservation {
	t.Helper()
	var observation productObservation
	decoder := json.NewDecoder(strings.NewReader(stream))
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			return observation
		}
		if err != nil {
			t.Fatalf("decode recorded stream: %v", err)
		}
		observation.observe(event)
	}
}

func journeyStatus(report productSmokeReport, id string) productSmokeJourneyResult {
	for _, journey := range report.Journeys {
		if journey.ID == id {
			return journey
		}
	}
	return productSmokeJourneyResult{}
}

func TestProductSmokeReconcileVerdicts(t *testing.T) {
	t.Parallel()
	required := testProductRequirements()
	allPassing := passingStream(required)

	for _, tc := range []struct {
		name         string
		stream       string
		required     []productSmokeJourney
		cut          bool
		want         map[string]string
		wantDetail   map[string]string
		wantProblems []string
		wantPass     bool
	}{
		{
			name:     "every required journey passed",
			stream:   allPassing,
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready":                   productJourneyPassed,
				"TestProduct_Startup/invalid_config":          productJourneyPassed,
				"TestProduct_Protocols/openai-legacy/stream":  productJourneyPassed,
				"TestProduct_Protocols/openai-legacy/collect": productJourneyPassed,
				"TestProduct_Lifecycle/canary_credential":     productJourneyPassed,
			},
			wantPass: true,
		},
		{
			name:     "missing required journey",
			stream:   passingStreamWithout(required, "TestProduct_Lifecycle/canary_credential"),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready":                   productJourneyPassed,
				"TestProduct_Startup/invalid_config":          productJourneyPassed,
				"TestProduct_Protocols/openai-legacy/stream":  productJourneyPassed,
				"TestProduct_Protocols/openai-legacy/collect": productJourneyPassed,
				"TestProduct_Lifecycle/canary_credential":     productJourneyFailed,
			},
			wantDetail: map[string]string{
				"TestProduct_Lifecycle/canary_credential": "not observed",
			},
			wantPass: false,
		},
		{
			name: "skipped required journey",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestEvent("skip", "TestProduct_Startup/ready", 0.01),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready": productJourneyFailed,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready": "skipped",
			},
			wantPass: false,
		},
		{
			name: "empty run",
			stream: productStream(
				`{"Action":"start","Package":"`+testPackage+`"}`,
				productOutputEvent("ok  \t"+testPackage+"\t0.001s\n"),
				`{"Action":"pass","Package":"`+testPackage+`","Elapsed":0.001}`,
			),
			required:     required,
			wantProblems: []string{"no test events observed"},
			wantPass:     false,
		},
		{
			name:   "quarantined required journey is never a pass",
			stream: passingStreamWithout(required, "TestProduct_Startup/ready"),
			required: func() []productSmokeJourney {
				journeys := testProductRequirements()
				journeys[0].QuarantineIssue = "https://github.com/aiproxer/aiproxer-enterprise/issues/7"
				return journeys
			}(),
			want: map[string]string{
				"TestProduct_Startup/ready": productJourneyQuarantined,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready": "issues/7",
			},
			wantPass: true,
		},
		{
			name: "failed required journey",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestEvent("fail", "TestProduct_Startup/ready", 0.4),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready": productJourneyFailed,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready": "failed",
			},
			wantPass: false,
		},
		{
			name:     "pass without a run event is inconsistent",
			stream:   passingStreamWithoutRun(required, "TestProduct_Startup/ready"),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready": productJourneyFailed,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready": "inconsistent",
			},
			wantProblems: []string{"inconsistent go test event stream for TestProduct_Startup/ready"},
			wantPass:     false,
		},
		{
			name: "contradictory terminal transitions are inconsistent",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestEvent("pass", "TestProduct_Startup/ready", 0.2),
				productTestEvent("fail", "TestProduct_Startup/ready", 0.3),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready": productJourneyFailed,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready": "inconsistent",
			},
			wantProblems: []string{"inconsistent go test event stream for TestProduct_Startup/ready"},
			wantPass:     false,
		},
		{
			name: "deadline cut leaves unfinished journeys",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestEvent("pass", "TestProduct_Startup/ready", 0.2),
				productTestEvent("run", "TestProduct_Startup/invalid_config", 0),
				productTestEvent("pass", "TestProduct_Startup/invalid_config", 0.2),
				productTestEvent("run", "TestProduct_Protocols/openai-legacy/stream", 0),
			),
			required: required,
			cut:      true,
			want: map[string]string{
				"TestProduct_Startup/ready":                   productJourneyPassed,
				"TestProduct_Startup/invalid_config":          productJourneyPassed,
				"TestProduct_Protocols/openai-legacy/stream":  productJourneyUnfinished,
				"TestProduct_Protocols/openai-legacy/collect": productJourneyUnfinished,
				"TestProduct_Lifecycle/canary_credential":     productJourneyUnfinished,
			},
			wantDetail: map[string]string{
				"TestProduct_Lifecycle/canary_credential": "no terminal event",
			},
			wantPass: false,
		},
		{
			name: "parent failure without a subtest terminal is never a pass",
			stream: productStream(
				productTestEvent("run", "TestProduct_Protocols", 0),
				productTestEvent("run", "TestProduct_Protocols/openai-legacy", 0),
				productTestEvent("run", "TestProduct_Protocols/openai-legacy/stream", 0),
				productTestEvent("fail", "TestProduct_Protocols/openai-legacy", 0.5),
				productTestEvent("run", "TestProduct_Protocols/openai-legacy/collect", 0),
				productTestEvent("pass", "TestProduct_Protocols/openai-legacy/collect", 0.2),
				productTestEvent("fail", "TestProduct_Protocols", 0.7),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Protocols/openai-legacy/stream":  productJourneyFailed,
				"TestProduct_Protocols/openai-legacy/collect": productJourneyPassed,
			},
			wantDetail: map[string]string{
				"TestProduct_Protocols/openai-legacy/stream": "parent TestProduct_Protocols/openai-legacy failed",
			},
			wantPass: false,
		},
		{
			name: "cached result cannot pass",
			stream: productStream(
				append(strings.Split(strings.TrimSuffix(allPassing, "\n"), "\n"),
					productOutputEvent("ok  \t"+testPackage+"\t(cached)\n"))...,
			),
			required:     required,
			wantProblems: []string{"cache"},
			wantPass:     false,
		},
		{
			name: "non-required failure does not hide behind passing journeys",
			stream: productStream(
				append(strings.Split(strings.TrimSuffix(allPassing, "\n"), "\n"),
					productTestEvent("fail", "TestHarnessSelfCheck", 0.01))...,
			),
			required:     required,
			wantProblems: []string{"non-required test TestHarnessSelfCheck failed"},
			wantPass:     false,
		},
		{
			name: "go test timeout panic leaves journeys unfinished",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestEvent("pass", "TestProduct_Startup/ready", 0.2),
				productOutputEvent("panic: test timed out after 9m0s\n"),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready":               productJourneyPassed,
				"TestProduct_Lifecycle/canary_credential": productJourneyUnfinished,
			},
			wantPass: false,
		},
		{
			name: "attributed go test timeout panic leaves every journey without a terminal unfinished",
			stream: productStream(
				productTestEvent("run", "TestProduct_Startup/ready", 0),
				productTestOutputEvent("TestProduct_Startup/ready", "panic: test timed out after 1s\n"),
			),
			required: required,
			want: map[string]string{
				"TestProduct_Startup/ready":                   productJourneyUnfinished,
				"TestProduct_Startup/invalid_config":          productJourneyUnfinished,
				"TestProduct_Protocols/openai-legacy/stream":  productJourneyUnfinished,
				"TestProduct_Protocols/openai-legacy/collect": productJourneyUnfinished,
				"TestProduct_Lifecycle/canary_credential":     productJourneyUnfinished,
			},
			wantDetail: map[string]string{
				"TestProduct_Startup/ready":               "timeout",
				"TestProduct_Lifecycle/canary_credential": "no terminal event",
			},
			wantProblems: []string{"go test -timeout"},
			wantPass:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report := reconcileProductSmoke(replayProductStream(t, tc.stream), tc.required, tc.cut)
			for id, want := range tc.want {
				journey := journeyStatus(report, id)
				if journey.Status != want {
					t.Errorf("%s status = %q, want %q (detail %q)", id, journey.Status, want, journey.Detail)
				}
			}
			for id, detail := range tc.wantDetail {
				if got := journeyStatus(report, id).Detail; !strings.Contains(got, detail) {
					t.Errorf("%s detail = %q, want it to contain %q", id, got, detail)
				}
			}
			problems := strings.Join(report.Problems, "; ")
			for _, want := range tc.wantProblems {
				if !strings.Contains(problems, want) {
					t.Errorf("problems = %q, want it to contain %q", problems, want)
				}
			}
			if got := report.pass(); got != tc.wantPass {
				t.Errorf("report pass = %t, want %t (problems %q)", got, tc.wantPass, problems)
			}
		})
	}
}

func TestProductSmokeTimeoutVerdictNamesTheTimeout(t *testing.T) {
	t.Parallel()
	required := testProductRequirements()
	observation := replayProductStream(t, productStream(
		productTestEvent("run", "TestProduct_Startup/ready", 0),
		productTestOutputEvent("TestProduct_Startup/ready", "panic: test timed out after 1s\n"),
	))
	report := reconcileProductSmoke(observation, required, false)
	summaryPath := filepath.Join(t.TempDir(), productSmokeSummaryFile)
	finishErr := finishProductSmoke(
		summaryPath,
		productSmokeSummary{Task: productSmokeTask, OS: "linux"},
		report,
		nil,
		"go test",
		errors.New("exit status 1"),
		time.Now(),
		io.Discard,
	)
	if finishErr == nil {
		t.Fatal("the timed-out run reported success")
	}
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary productSmokeSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	if summary.Verdict != productSmokeFailed {
		t.Errorf("verdict = %q, want %q", summary.Verdict, productSmokeFailed)
	}
	if !strings.Contains(summary.Reason, "go test -timeout") {
		t.Errorf("reason = %q; want it to name the go test -timeout", summary.Reason)
	}
	for _, journey := range summary.Journeys {
		if journey.Status != productJourneyUnfinished {
			t.Errorf("%s status = %q; a run cut by the test timeout must leave it unfinished", journey.ID, journey.Status)
		}
	}
}

func TestProductSmokeReconcileEmptyRunNeverPasses(t *testing.T) {
	t.Parallel()
	required := testProductRequirements()
	report := reconcileProductSmoke(productObservation{}, required, false)
	if report.pass() {
		t.Fatal("an empty run passed")
	}
	if len(report.Journeys) != len(required) {
		t.Fatalf("journeys = %d, want %d", len(report.Journeys), len(required))
	}
	for _, journey := range report.Journeys {
		if journey.Status == productJourneyPassed {
			t.Errorf("%s passed without any observed event", journey.ID)
		}
	}
}

func TestProductSmokeRequiredJourneysFilterPlatformAndMapQuarantine(t *testing.T) {
	t.Parallel()
	journeys := []productsmoke.Journey{
		{ID: "TestProduct_Startup/ready"},
		{ID: "TestProduct_Startup/invalid_config"},
		{ID: "TestProduct_Shutdown/graceful", POSIXOnly: true},
	}
	entries := []quarantineEntry{{
		Test:  "TestProduct_Startup",
		Issue: "https://github.com/aiproxer/aiproxer-enterprise/issues/42",
	}}

	linux := requiredProductSmokeJourneys(journeys, entries, "linux")
	if len(linux) != 3 {
		t.Fatalf("linux journeys = %d, want 3", len(linux))
	}
	for _, journey := range linux[:2] {
		if !strings.Contains(journey.QuarantineIssue, "issues/42") {
			t.Errorf("%s quarantine issue = %q; a quarantined top-level test must cover its required subtests", journey.ID, journey.QuarantineIssue)
		}
	}
	if linux[2].QuarantineIssue != "" {
		t.Errorf("unrelated journey gained quarantine issue %q", linux[2].QuarantineIssue)
	}

	windows := requiredProductSmokeJourneys(journeys, entries, "windows")
	if len(windows) != 2 {
		t.Fatalf("windows journeys = %d, want 2", len(windows))
	}
	for _, journey := range windows {
		if journey.ID == "TestProduct_Shutdown/graceful" {
			t.Fatal("a POSIX-only journey reached the Windows required list")
		}
	}
}

func TestProductSmokeTestTimeout(t *testing.T) {
	t.Parallel()
	if got, err := productSmokeTestTimeout(570 * time.Second); err != nil || got != 540*time.Second {
		t.Fatalf("full budget timeout = %s, %v; want 540s", got, err)
	}
	if got, err := productSmokeTestTimeout(productSmokeReportReserve + productSmokeMinTestWindow + 500*time.Millisecond); err != nil || got != productSmokeMinTestWindow {
		t.Fatalf("minimum-window timeout = %s, %v; want %s", got, err, productSmokeMinTestWindow)
	}
	for _, remaining := range []time.Duration{
		productSmokeReportReserve + productSmokeMinTestWindow - time.Second,
		productSmokeReportReserve,
		0,
		-time.Second,
	} {
		if _, err := productSmokeTestTimeout(remaining); err == nil || !strings.Contains(err.Error(), "minimum") {
			t.Errorf("remaining %s error = %v; want a minimum-window failure naming the budget", remaining, err)
		}
	}
}

func TestProductSmokeTestArgv(t *testing.T) {
	t.Parallel()
	argv := productSmokeTestArgv(9 * time.Minute)
	for _, want := range []string{"go", "test", "-json", "-count=1", "-tags=productsmoke", "-timeout=9m0s", "./internal/testkit/productsmoke"} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

func TestProductSmokeJourneySlugMatchesHarness(t *testing.T) {
	t.Parallel()
	got := productJourneySlug("TestProduct_Protocols/openai-legacy/stream")
	if want := "TestProduct_Protocols_openai-legacy_stream"; got != want {
		t.Fatalf("journey slug = %q, want %q", got, want)
	}
}

func TestProductSmokeResetEvidenceClearsPreviousVerdict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	summaryPath := filepath.Join(dir, productSmokeSummaryFile)
	journeyRoot := filepath.Join(dir, productSmokeJourneyDir)
	required := []productSmokeJourney{
		{ID: "TestProduct_Startup/ready"},
		{ID: "TestProduct_Startup/invalid_config"},
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(summaryPath, `{"verdict":"pass"}`)
	write(filepath.Join(journeyRoot, productJourneySlug(required[0].ID), "evidence.json"), "previous pass")
	write(filepath.Join(journeyRoot, "TestProduct_Shutdown_graceful", "evidence.json"), "not a required journey")
	write(filepath.Join(dir, "operator-note.txt"), "unrelated")

	if err := clearProductSmokeSummary(summaryPath); err != nil {
		t.Fatal(err)
	}
	if err := resetProductSmokeJourneyEvidence(journeyRoot, required); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(summaryPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("previous summary stat = %v, want it removed at startup", err)
	}
	if _, err := os.Stat(filepath.Join(journeyRoot, productJourneySlug(required[0].ID))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("required journey evidence stat = %v, want it reset at startup", err)
	}
	if _, err := os.Stat(filepath.Join(journeyRoot, "TestProduct_Shutdown_graceful", "evidence.json")); err != nil {
		t.Errorf("unrequired journey evidence = %v, want it preserved", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "operator-note.txt")); err != nil {
		t.Errorf("unrelated operator file = %v, want it preserved", err)
	}
}

// TestProductSmokeRejectsEvidenceInsideSource proves the artifact root cannot
// change the source fingerprint: an in-repository destination is rejected
// before it is created, and AIP_DEV_TMPDIR gets the same validation.
func TestProductSmokeRejectsEvidenceInsideSource(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "smoke-evidence")
	_, err := productSmokeEvidenceDir(root, inside)
	if err == nil || !strings.Contains(err.Error(), "inside the source tree") {
		t.Fatalf("in-repository evidence error = %v; want an inside-source rejection", err)
	}
	if _, statErr := os.Stat(inside); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("in-repository evidence stat = %v; validation must run before creation", statErr)
	}

	outside := t.TempDir()
	dir, err := productSmokeEvidenceDir(root, outside)
	if err != nil {
		t.Fatalf("outside evidence was rejected: %v", err)
	}
	if dir != outside {
		t.Fatalf("outside evidence dir = %q, want %q", dir, outside)
	}

	t.Setenv("AIP_DEV_TMPDIR", filepath.Join(root, "tmp"))
	if _, err := productSmokeEvidenceDir(root, ""); err == nil || !strings.Contains(err.Error(), "inside the source tree") {
		t.Fatalf("in-repository AIP_DEV_TMPDIR error = %v; want an inside-source rejection", err)
	}

	// A symlink above two nonexistent components must still resolve to the
	// source tree; resolving only the leaf or its immediate parent misses this.
	linkRoot := t.TempDir()
	link := filepath.Join(linkRoot, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Logf("symlink unavailable on this host: %v", err)
		return
	}
	nested := filepath.Join(link, "nested", "evidence")
	if _, err := productSmokeEvidenceDir(root, nested); err == nil || !strings.Contains(err.Error(), "inside the source tree") {
		t.Fatalf("nested symlink evidence error = %v; want an inside-source rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "nested")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("nested in-repository evidence stat = %v; validation must run before creation", statErr)
	}
}

// TestProductSmokeMalformedQuarantineNamesBaselineJourneys proves a quarantine
// parse failure still accounts for every required journey. The baseline list is
// platform-filtered before the file is read; quarantine only annotates it.
func TestProductSmokeMalformedQuarantineNamesBaselineJourneys(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "test-quarantine.txt"), []byte("not a valid entry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence := t.TempDir()
	err := runProductSmoke(context.Background(), root, evidence, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("a malformed quarantine file reported success")
	}
	summary := readProductSmokeSummary(t, evidence)
	baseline := requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
	if len(summary.Journeys) != len(baseline) {
		t.Fatalf("journeys = %d, want the %d-journey baseline", len(summary.Journeys), len(baseline))
	}
	for _, journey := range summary.Journeys {
		if journey.Status != productJourneyFailed {
			t.Errorf("%s status = %q; a pre-build quarantine failure leaves it not started", journey.ID, journey.Status)
		}
	}
	if problems := strings.Join(summary.Problems, "; "); !strings.Contains(problems, "test-quarantine") {
		t.Errorf("problems = %q; want the quarantine parse error", problems)
	}
}

// TestProductSmokeSourceMustStayStable proves a PASS cannot be bound to a tree
// that changed after the source snapshot: the post-run check compares HEAD,
// index and working bytes, not only the commit id.
func TestProductSmokeSourceMustStayStable(t *testing.T) {
	root := initProductSmokeGitRepo(t)
	before, err := sourcestate.Snapshot(context.Background(), root)
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}
	if err := verifyProductSmokeSource(root, before); err != nil {
		t.Fatalf("stable source was rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyProductSmokeSource(root, before)
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("changed source error = %v; want a source-change failure", err)
	}
}

// initProductSmokeGitRepo creates a one-commit repository so source snapshots
// have a real HEAD, index and working tree.
func initProductSmokeGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "product-smoke@example.invalid")
	git("config", "user.name", "Product Smoke")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-q", "-m", "fixture")
	return root
}

// TestProductSmokeDeadlineAccountsForStartupCompilation proves the controller
// spends the remaining part of the command budget, not a fresh 570 seconds,
// when `go run` took 40 seconds: the 30-second figure is a reporting reserve,
// not an assumption about how long startup may take.
func TestProductSmokeDeadlineAccountsForStartupCompilation(t *testing.T) {
	now := time.Now()
	t.Setenv(productSmokeDeadlineEnv, strconv.FormatInt(now.Add(560*time.Second).Unix(), 10))
	workDeadline, wallStarted, absolute, err := productSmokeDeadlines(now)
	if err != nil {
		t.Fatalf("deadlines: %v", err)
	}
	if !absolute {
		t.Fatal("absolute deadline was not recognized")
	}
	if remaining := workDeadline.Sub(now); remaining < 529*time.Second || remaining > 531*time.Second {
		t.Fatalf("remaining work budget = %s, want about 530s after a 40s startup", remaining)
	}
	if elapsed := now.Sub(wallStarted); elapsed < 39*time.Second || elapsed > 41*time.Second {
		t.Fatalf("reported command start = %s before now, want about 40s", elapsed)
	}
}

// TestProductSmokeLateStartReportsEveryJourneyUnfinished simulates a startup
// compilation that consumed the command budget: the controller starts after its
// work deadline and must still write a summary naming every required journey as
// unfinished rather than exiting with only an error.
func TestProductSmokeLateStartReportsEveryJourneyUnfinished(t *testing.T) {
	t.Setenv(productSmokeDeadlineEnv, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	root := t.TempDir()
	evidence := t.TempDir()
	err := runProductSmoke(context.Background(), root, evidence, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("a controller that started after its deadline reported success")
	}
	summary := readProductSmokeSummary(t, evidence)
	if summary.Verdict != productSmokeFailed {
		t.Errorf("verdict = %q, want %q", summary.Verdict, productSmokeFailed)
	}
	required := requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
	if len(summary.Journeys) != len(required) {
		t.Fatalf("journeys = %d, want %d: every required journey must be named", len(summary.Journeys), len(required))
	}
	for _, journey := range summary.Journeys {
		if journey.Status != productJourneyUnfinished {
			t.Errorf("%s status = %q; a late start leaves it unfinished", journey.ID, journey.Status)
		}
	}
	if !strings.Contains(summary.Reason, "deadline elapsed before the product smoke started") {
		t.Errorf("reason = %q; want it to name the command deadline", summary.Reason)
	}
}

// TestProductSmokeInfrastructureFailureWritesSummary proves the centralized
// abort path: a malformed deadline is reported in summary.json even though the
// failure happens before source inspection or the build.
func TestProductSmokeInfrastructureFailureWritesSummary(t *testing.T) {
	t.Setenv(productSmokeDeadlineEnv, "not-a-timestamp")
	root := t.TempDir()
	evidence := t.TempDir()
	err := runProductSmoke(context.Background(), root, evidence, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("a malformed deadline reported success")
	}
	summary := readProductSmokeSummary(t, evidence)
	if summary.Verdict != productSmokeFailed {
		t.Errorf("verdict = %q, want %q", summary.Verdict, productSmokeFailed)
	}
	problems := strings.Join(summary.Problems, "; ")
	if !strings.Contains(problems, "is not a positive Unix timestamp") {
		t.Errorf("problems = %q; want the malformed deadline cause", problems)
	}
	required := requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
	if len(summary.Journeys) != len(required) {
		t.Fatalf("journeys = %d, want %d: an infrastructure failure must still name the required journeys", len(summary.Journeys), len(required))
	}
	for _, journey := range summary.Journeys {
		if journey.Status != productJourneyFailed {
			t.Errorf("%s status = %q; a pre-build failure leaves it not started", journey.ID, journey.Status)
		}
	}
}

func readProductSmokeSummary(t *testing.T, evidence string) productSmokeSummary {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evidence, productSmokeSummaryFile))
	if err != nil {
		t.Fatalf("read product-smoke summary: %v", err)
	}
	var summary productSmokeSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode product-smoke summary: %v", err)
	}
	return summary
}

func TestProductSmokeSummaryShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	started := time.Now().UTC().Add(-time.Minute)
	summary := productSmokeSummary{
		SchemaVersion: 1,
		Task:          productSmokeTask,
		Verdict:       productSmokeFailed,
		Reason:        "required journey did not pass",
		StartedAt:     started,
		FinishedAt:    started.Add(12 * time.Second),
		WallSeconds:   12.5,
		Head:          "0123456789abcdef0123456789abcdef01234567",
		Dirty:         true,
		OS:            "linux",
		EvidenceDir:   dir,
		Binary:        filepath.Join(dir, "aipstd"),
		BinarySHA256:  "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		GoTestTimeout: "8m0s",
		Journeys: []productSmokeJourneyResult{{
			ID:             "TestProduct_Startup/ready",
			Status:         productJourneyFailed,
			Detail:         "not observed",
			ElapsedSeconds: 0.25,
			Evidence:       filepath.Join(dir, "journeys", "TestProduct_Startup_ready"),
		}},
		Problems: []string{"no test events observed"},
	}
	if err := writeProductSmokeSummary(path, summary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	for _, key := range []string{
		"schema_version", "task", "verdict", "reason", "started_at", "finished_at",
		"wall_seconds", "head", "source_fingerprint", "dirty", "os", "evidence_dir", "binary",
		"binary_sha256", "go_test_timeout", "journeys", "problems",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("summary is missing %q", key)
		}
	}
	journeys, ok := raw["journeys"].([]any)
	if !ok || len(journeys) != 1 {
		t.Fatalf("journeys = %#v, want one entry", raw["journeys"])
	}
	entry, ok := journeys[0].(map[string]any)
	if !ok {
		t.Fatalf("journey = %#v, want an object", journeys[0])
	}
	for _, key := range []string{"id", "status", "detail", "elapsed_seconds", "evidence"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("journey is missing %q", key)
		}
	}
	var decoded productSmokeSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Head != summary.Head || decoded.Dirty != summary.Dirty || decoded.EvidenceDir != summary.EvidenceDir {
		t.Errorf("decoded source = %q/%t/%q, want %q/%t/%q", decoded.Head, decoded.Dirty, decoded.EvidenceDir, summary.Head, summary.Dirty, summary.EvidenceDir)
	}
	if decoded.Journeys[0].Status != productJourneyFailed || decoded.Journeys[0].ElapsedSeconds != 0.25 {
		t.Errorf("decoded journey = %+v", decoded.Journeys[0])
	}
}
