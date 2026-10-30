package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFailureDetail_stripsPathsTimingsHashes(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`C:/Users/Mateusz/source/repos/aiproxer-example-project-backend-connector-arch`)
	raw := root + `/pkg FAILED` + "\n" +
		"ok\tgithub.com/x/y\t1.019s\n" +
		"copy root -> C:\\Users\\Mateusz\\AppData\\Local\\Temp\\aip-isolated-root-12345\n" +
		"binary sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n" +
		"2026-07-26T15:48:58+02:00 done\n" +
		"token=sk-abcSECRET\n"
	got := sanitizeFailureDetail(root, raw)
	if got == "" || got == raw {
		t.Fatalf("expected sanitized non-empty detail, got %q", got)
	}
	for _, bad := range []string{
		root,
		"1.019s",
		"aip-isolated-root-12345",
		"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"2026-07-26T15:48:58",
		"sk-abcSECRET",
		`C:\Users`,
		"AppData",
	} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitized detail still contains %q: %q", bad, got)
		}
	}
	if !strings.HasPrefix(got, "failed:") {
		t.Fatalf("want failed: prefix, got %q", got)
	}
}

func TestEnsureDeterministicReport_rejectsNondeterministic(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`C:/repo/go-llm`)
	cases := []struct {
		name string
		raw  string
	}{
		{"timestamp_field", `{"timestamp":"x","schema":"aip.release.gates/v1"}`},
		{"iso_time", `{"notes":"2026-07-26T15:48:58+02:00"}`},
		{"win_abs", `{"detail":"C:\\Users\\x\\Temp\\x"}`},
		{"posix_tmp", `{"detail":"/tmp/aip-isolated-root-abc"}`},
		{"duration", `{"detail":"ok pkg 1.019s"}`},
		{"sha64", `{"detail":"sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}`},
		{"root_embed", `{"detail":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ensureDeterministicReport([]byte(tc.raw), root); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestEnsureDeterministicReport_allowsRequirementIDsAndSchema(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "schema": "aip.release.gates/v1",
  "mode": "static",
  "requirement_count": 116,
  "traceability": [{"id":"1.1","gate":"archtest_backend_plugin_release_gates","status":"pending"},{"id":"12.11","gate":"native_linux_macos_windows_ci","status":"external_blocker"}],
  "gate_results": [{"gate":"requirements_parse","ok":true,"status":"local_executable","command":"builtin:requirements_parse","detail":"116 acceptance criteria"}]
}
`)
	if err := ensureDeterministicReport(raw, filepath.FromSlash(`C:/repo/go-llm`)); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeReport_successAndFailureStable(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`C:/repo/go-llm`)
	rep := &report{
		Schema: reportSchema,
		Mode:   "full",
		GateResults: []gateResult{
			{Gate: "adapter_stream_session", OK: true, Status: "local_executable", Command: `go test .\pkg`, Detail: "ok\tpkg\t1.019s"},
			{Gate: "isolated_root_qa", OK: false, Status: "failed", Command: `make isolated-root-qa`, Detail: root + `/tmp/aip-isolated-root-zz FAIL 2.5s`},
		},
		ModuleResults: []moduleResult{
			{Module: "connectors/localstub", OK: false, Steps: []string{"test:fail"}, Error: "test: boom\n" + root + "\n1.2s\n"},
		},
		Traceability: []traceRow{
			{ID: "6.1", Gate: "adapter_stream_session", Status: "local_executable", Notes: "ok\tpkg\t1.019s"},
		},
	}
	sanitizeReport(rep, root)
	if rep.GateResults[0].Detail != "ok" {
		t.Fatalf("success detail=%q", rep.GateResults[0].Detail)
	}
	if strings.Contains(rep.GateResults[0].Command, `\`) {
		t.Fatalf("command not normalized: %q", rep.GateResults[0].Command)
	}
	if rep.GateResults[1].Detail == "" || strings.Contains(rep.GateResults[1].Detail, root) || strings.Contains(rep.GateResults[1].Detail, "2.5s") {
		t.Fatalf("failure detail not sanitized: %q", rep.GateResults[1].Detail)
	}
	if strings.Contains(rep.ModuleResults[0].Error, root) || strings.Contains(rep.ModuleResults[0].Error, "1.2s") {
		t.Fatalf("module error not sanitized: %q", rep.ModuleResults[0].Error)
	}
	if rep.Traceability[0].Notes != "" {
		t.Fatalf("trace notes must not carry gate stdout: %q", rep.Traceability[0].Notes)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDeterministicReport(b, root); err != nil {
		t.Fatal(err)
	}
}

func TestTwoEquivalentReports_byteIdentical(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash(`C:/repo/go-llm`)
	mk := func(timing, tmp, hash string) *report {
		return &report{
			Schema:           reportSchema,
			Mode:             "full",
			Modules:          []string{"connectors/localstub", "connectors/codex"},
			RootIndependent:  true,
			Gates:            []string{"adapter_stream_session", "isolated_root_qa"},
			RequirementCount: 116,
			GateResults: []gateResult{
				{Gate: "adapter_stream_session", OK: true, Status: "local_executable", Command: "go test ./internal/infra/backendplugins/adapter/", Detail: "ok\tpkg\t" + timing},
				{Gate: "isolated_root_qa", OK: true, Status: "local_executable", Command: "make isolated-root-qa", Detail: "copy -> " + tmp + " sha256=" + hash},
			},
			Traceability: []traceRow{
				{ID: "6.1", Gate: "adapter_stream_session", Status: "local_executable"},
				{ID: "11.8", Gate: "isolated_root_qa", Status: "local_executable"},
			},
		}
	}
	a := mk("0.111s", `/tmp/aip-isolated-root-aaa`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := mk("9.999s", `/tmp/aip-isolated-root-bbb`, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	sanitizeReport(a, root)
	sanitizeReport(b, root)
	ba, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	bb, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDeterministicReport(ba, root); err != nil {
		t.Fatal(err)
	}
	if err := ensureDeterministicReport(bb, root); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ba, bb) {
		t.Fatalf("reports differ after sanitize\n---a---\n%s\n---b---\n%s", ba, bb)
	}
	if bytes.Contains(ba, []byte("native_host")) {
		t.Fatal("native_host must not appear in deterministic report")
	}
}

func TestValidateSelectors_IncludesRecvStress(t *testing.T) {
	t.Parallel()
	found := false
	for _, c := range selectorChecks() {
		if strings.Contains(c.pkg, "adapter") && strings.Contains(c.pattern, "TestRecv_Stress") {
			found = true
		}
	}
	if !found {
		t.Fatal("adapter selector metadata must include TestRecv_Stress")
	}
}

func TestWriteReport_sanitizesThenValidates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	rep := &report{
		Schema: reportSchema,
		Mode:   "full",
		GateResults: []gateResult{
			{Gate: "x", OK: true, Status: "local_executable", Command: `go test .\pkg`, Detail: "ok pkg 1.019s"},
		},
	}
	if err := writeReport(out, rep, filepath.FromSlash(`C:/repo/go-llm`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "1.019s") || strings.Contains(string(raw), `.\pkg`) || strings.Contains(string(raw), `"native_host"`) {
		t.Fatalf("leaky report: %s", raw)
	}
	if !strings.Contains(string(raw), `"detail": "ok"`) {
		t.Fatalf("expected stable ok detail: %s", raw)
	}
}
