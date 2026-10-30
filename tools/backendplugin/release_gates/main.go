package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

const reportSchema = "aip.release.gates/v1"

const releaseCommandTimeout = 8 * time.Minute

type moduleResult struct {
	Module string   `json:"module"`
	OK     bool     `json:"ok"`
	Steps  []string `json:"steps"`
	Error  string   `json:"error,omitempty"`
}

type traceRow struct {
	ID     string `json:"id"`
	Gate   string `json:"gate"`
	Status string `json:"status"`
	Notes  string `json:"notes,omitempty"`
}

type report struct {
	Schema           string         `json:"schema"`
	Mode             string         `json:"mode"`
	Modules          []string       `json:"modules"`
	ModuleResults    []moduleResult `json:"module_results,omitempty"`
	GateResults      []gateResult   `json:"gate_results,omitempty"`
	RootIndependent  bool           `json:"root_independent"`
	Gates            []string       `json:"gates"`
	Traceability     []traceRow     `json:"traceability"`
	RequirementCount int            `json:"requirement_count"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	outPath := flag.String("out", "", "write release report JSON (required)")
	mode := flag.String("mode", "static", "static|modules|full")
	selectCSV := flag.String("select", "", "optional comma-separated module paths")
	flag.Parse()
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(os.Stderr, "release_gates: -out is required")
		os.Exit(2)
	}
	var selectSet map[string]struct{}
	if s := strings.TrimSpace(*selectCSV); s != "" {
		selectSet = map[string]struct{}{}
		for p := range strings.SplitSeq(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				selectSet[p] = struct{}{}
			}
		}
	}
	absRoot, absErr := filepath.Abs(*root)
	if absErr != nil {
		fmt.Fprintf(os.Stderr, "release_gates: %v\n", absErr)
		os.Exit(1)
	}
	rep, err := run(*root, *mode, selectSet)
	if err != nil {
		if rep != nil {
			_ = writeReport(*outPath, rep, absRoot)
		}
		fmt.Fprintf(os.Stderr, "release_gates: %v\n", err)
		os.Exit(1)
	}
	if err := writeReport(*outPath, rep, absRoot); err != nil {
		fmt.Fprintf(os.Stderr, "release_gates: write report: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

func writeReport(path string, rep *report, root string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	sanitizeReport(rep, root)
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := ensureDeterministicReport(b, root); err != nil {
		return err
	}
	return os.WriteFile(abs, b, 0o644)
}

func run(root, mode string, selectSet map[string]struct{}) (*report, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	reqIDs, err := parseRequirementIDs(absRoot)
	if err != nil {
		return nil, fmt.Errorf("parse requirements: %w", err)
	}
	if err := validateRequirementGateCoverage(reqIDs); err != nil {
		return nil, err
	}
	mods, err := discoverModules(absRoot)
	if err != nil {
		return nil, err
	}
	if selectSet != nil {
		filtered := make([]string, 0, len(mods))
		for _, m := range mods {
			if _, ok := selectSet[m]; ok {
				filtered = append(filtered, m)
			}
		}
		mods = filtered
	}
	rootOK, err := rootIndependent(absRoot)
	if err != nil {
		return nil, err
	}
	if !rootOK {
		rep := baseReport(mode, mods, reqIDs, false, nil, nil)
		return rep, fmt.Errorf("root go.mod must not require connectors/ or connector-support/")
	}
	if err := validateSelectors(absRoot); err != nil {
		return baseReport(mode, mods, reqIDs, true, nil, nil), err
	}

	observed := map[string]gateResult{}
	markBuiltin(observed, "root_go_mod_independence", "root go.mod independent of connectors", true)
	markBuiltin(observed, "structural_module_discovery", fmt.Sprintf("%d modules", len(mods)), len(mods) > 0)
	markBuiltin(observed, "requirements_parse", fmt.Sprintf("%d acceptance criteria", len(reqIDs)), len(reqIDs) > 0)

	switch mode {
	case "static":
		// Only builtins observed; remaining local gates stay pending.
		rep := baseReport(mode, mods, reqIDs, true, nil, observed)
		rep.Traceability = buildTraceability(reqIDs, observed, mode)
		return rep, nil
	case "modules", "full":
		results, modErr := runModuleMatrix(absRoot, mods)
		modOK := modErr == nil
		for _, r := range results {
			if !r.OK {
				modOK = false
				break
			}
		}
		markBuiltin(observed, "module_gowork_off_test_build_vet_tidy", "all modules list/vet/tidy/test/build", modOK)
		confOK := true
		for _, r := range results {
			for _, st := range r.Steps {
				if st == "conformance_filter:skip_no_tests" || st == "conformance_filter:fail" {
					confOK = false
				}
			}
			if !r.OK {
				confOK = false
			}
		}
		markBuiltin(observed, "module_advertised_capability_conformance_filters", "every connectors/* module ran conformance matches", confOK)
		if modErr != nil {
			rep := baseReport(mode, mods, reqIDs, true, results, observed)
			rep.Traceability = buildTraceability(reqIDs, observed, mode)
			return rep, modErr
		}
		if mode == "modules" {
			rep := baseReport(mode, mods, reqIDs, true, results, observed)
			rep.Traceability = buildTraceability(reqIDs, observed, mode)
			return rep, nil
		}
		// full: execute remaining catalog gates
		cat := catalogByName()
		var firstErr error
		for _, g := range rootGateCatalog() {
			if g.Kind == "builtin" {
				continue
			}
			res := runGate(absRoot, g, observed)
			if res.Status == "failed" && firstErr == nil {
				firstErr = fmt.Errorf("gate %s failed: %s", g.Name, res.Detail)
			}
			_ = cat
		}
		// security_fuzz_subset is covered by security-checks make target when that gate passes
		if sec, ok := observed["backend_plugin_security_checks"]; ok && sec.OK {
			markBuiltin(observed, "security_fuzz_subset", "FuzzManifest/FuzzServerFrame via backend-plugin-security-checks", true)
		}
		rep := baseReport(mode, mods, reqIDs, true, results, observed)
		rep.Traceability = buildTraceability(reqIDs, observed, mode)
		if firstErr != nil {
			return rep, firstErr
		}
		if err := assertFullTraceComplete(rep); err != nil {
			return rep, err
		}
		return rep, nil
	default:
		return nil, fmt.Errorf("unknown mode %q (want static|modules|full)", mode)
	}
}

func baseReport(mode string, mods, reqIDs []string, rootOK bool, results []moduleResult, observed map[string]gateResult) *report {
	gates := make([]string, 0, len(rootGateCatalog()))
	for _, g := range rootGateCatalog() {
		gates = append(gates, g.Name)
	}
	var gr []gateResult
	if observed != nil {
		names := make([]string, 0, len(observed))
		for n := range observed {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			gr = append(gr, observed[n])
		}
	}
	return &report{
		Schema:           reportSchema,
		Mode:             mode,
		Modules:          mods,
		ModuleResults:    results,
		GateResults:      gr,
		RootIndependent:  rootOK,
		Gates:            gates,
		RequirementCount: len(reqIDs),
	}
}

func buildTraceability(reqIDs []string, observed map[string]gateResult, mode string) []traceRow {
	refs := requirementGateMap()
	out := make([]traceRow, 0, len(reqIDs))
	for _, id := range reqIDs {
		ref, ok := refs[id]
		if !ok {
			out = append(out, traceRow{ID: id, Gate: "unmapped", Status: "unsupported", Notes: "missing gate mapping"})
			continue
		}
		row := traceRow{ID: id, Gate: ref.Gate, Notes: ref.Notes}
		if g, ok := catalogByName()[ref.Gate]; ok && g.Kind == "external" {
			row.Status = "external_blocker"
			if row.Notes == "" {
				row.Notes = g.Notes
			}
			out = append(out, row)
			continue
		}
		if res, ok := observed[ref.Gate]; ok {
			row.Status = res.Status
			out = append(out, row)
			continue
		}
		if mode == "static" || mode == "modules" {
			row.Status = "pending"
			out = append(out, row)
			continue
		}
		row.Status = "unsupported"
		row.Notes = "gate not observed"
		out = append(out, row)
	}
	return out
}

func assertFullTraceComplete(rep *report) error {
	var bad []string
	for _, row := range rep.Traceability {
		switch row.Status {
		case "local_executable", "external_blocker":
			continue
		default:
			bad = append(bad, fmt.Sprintf("%s=%s", row.ID, row.Status))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("full mode leaves non-executable trace rows: %s", strings.Join(bad, ", "))
	}
	return nil
}

func validateRequirementGateCoverage(reqIDs []string) error {
	refs := requirementGateMap()
	cat := catalogByName()
	var missing, unknownGate, extra []string
	inReq := map[string]struct{}{}
	for _, id := range reqIDs {
		inReq[id] = struct{}{}
		ref, ok := refs[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if _, ok := cat[ref.Gate]; !ok {
			unknownGate = append(unknownGate, id+":"+ref.Gate)
		}
	}
	for id := range refs {
		if _, ok := inReq[id]; !ok {
			extra = append(extra, id)
		}
	}
	if len(missing) > 0 || len(unknownGate) > 0 || len(extra) > 0 {
		return fmt.Errorf("gate map mismatch missing=%v unknownGate=%v extra=%v", missing, unknownGate, extra)
	}
	return nil
}

func discoverModules(root string) ([]string, error) {
	var out []string
	for _, base := range []string{"connectors", "connector-support"} {
		dir := filepath.Join(root, base)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "go.mod")); err != nil {
				continue
			}
			out = append(out, filepath.ToSlash(filepath.Join(base, e.Name())))
		}
	}
	sort.Strings(out)
	return out, nil
}

func rootIndependent(root string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false, err
	}
	body := string(b)
	for _, needle := range []string{
		"/connectors/",
		"/connector-support/",
		"\nconnectors/",
		"\nconnector-support/",
	} {
		if strings.Contains(body, needle) {
			return false, nil
		}
	}
	for line := range strings.SplitSeq(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "github.com/aiproxer/aiproxer/connectors/") ||
			strings.HasPrefix(trim, "github.com/aiproxer/aiproxer/connector-support/") {
			return false, nil
		}
	}
	return true, nil
}

func runModuleMatrix(root string, mods []string) ([]moduleResult, error) {
	results := make([]moduleResult, 0, len(mods))
	var firstErr error
	for _, mod := range mods {
		res := moduleResult{Module: mod, OK: true}
		modRoot := filepath.Join(root, filepath.FromSlash(mod))
		steps := []struct {
			name string
			args []string
		}{
			{"list", []string{"list", "./..."}},
			{"vet", []string{"vet", "./..."}},
			{"tidy_diff", nil},
			{"test", []string{"test", "-count=1", "-timeout=15m", "./..."}},
		}
		for _, st := range steps {
			if st.name == "tidy_diff" {
				out, err := checkModuleTidy(modRoot)
				if err != nil {
					res.OK = false
					fmt.Fprintf(os.Stderr, "release_gates: %s tidy_diff:\n%s\n%v\n", mod, out, err)
					res.Error = sanitizeFailureDetail(root, fmt.Sprintf("tidy_diff: %v", err))
					res.Steps = append(res.Steps, "tidy_diff:fail")
					if firstErr == nil {
						firstErr = fmt.Errorf("%s: tidy_diff failed", mod)
					}
					break
				}
				res.Steps = append(res.Steps, "tidy_diff:ok")
				continue
			}
			result := runner.Run(context.Background(), runner.Request{
				Argv:    append([]string{"go"}, st.args...),
				Dir:     modRoot,
				Env:     []string{"GOWORK=off"},
				Timeout: releaseCommandTimeout,
				Output:  taskrunner.Capture,
				Label:   fmt.Sprintf("release_gates:%s:%s", mod, st.name),
			})
			if result.Kind != taskrunner.Success {
				res.OK = false
				fmt.Fprintf(os.Stderr, "release_gates: %s %s:\n%s\n", mod, st.name, result.Stdout)
				res.Error = sanitizeFailureDetail(root, fmt.Sprintf("%s: %v", st.name, runner.Error(result)))
				res.Steps = append(res.Steps, st.name+":fail")
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %s failed", mod, st.name)
				}
				break
			}
			res.Steps = append(res.Steps, st.name+":ok")
		}
		if res.OK && strings.HasPrefix(mod, "connectors/") {
			step, _, err := runConformanceFilter(modRoot)
			res.Steps = append(res.Steps, step)
			if err != nil {
				res.OK = false
				fmt.Fprintf(os.Stderr, "release_gates: %s conformance:\n%v\n", mod, err)
				res.Error = sanitizeFailureDetail(root, err.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: conformance failed", mod)
				}
			}
		}
		if res.OK {
			if err := buildModuleCmds(modRoot, &res); err != nil {
				res.OK = false
				fmt.Fprintf(os.Stderr, "release_gates: %s build:\n%v\n", mod, err)
				res.Error = sanitizeFailureDetail(root, err.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: build failed", mod)
				}
			}
		}
		results = append(results, res)
	}
	return results, firstErr
}

func buildModuleCmds(modRoot string, res *moduleResult) error {
	cmdDir := filepath.Join(modRoot, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		if os.IsNotExist(err) {
			res.Steps = append(res.Steps, "build:skip_no_cmd")
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := "./cmd/" + e.Name()
		outBin := filepath.Join(os.TempDir(), "aip-release-gates-"+e.Name())
		if runtime.GOOS == "windows" {
			outBin += ".exe"
		}
		result := runner.Run(context.Background(), runner.Request{
			Argv:    []string{"go", "build", "-o", outBin, rel},
			Dir:     modRoot,
			Env:     []string{"GOWORK=off", "CGO_ENABLED=0"},
			Timeout: releaseCommandTimeout,
			Output:  taskrunner.Capture,
			Label:   "release_gates:" + filepath.ToSlash(rel),
		})
		_ = os.Remove(outBin)
		if result.Kind != taskrunner.Success {
			res.Steps = append(res.Steps, "build:"+e.Name()+":fail")
			return fmt.Errorf("build %s: %w", rel, runner.Error(result))
		}
		res.Steps = append(res.Steps, "build:"+e.Name()+":ok")
	}
	return nil
}
