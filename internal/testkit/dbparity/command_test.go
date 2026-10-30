package dbparity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestRunnerMode_Validation(t *testing.T) {
	for _, mode := range dbparity.ValidRunnerModes() {
		if !mode.IsValid() {
			t.Errorf("expected mode %q to be valid", mode)
		}
	}

	invalidModes := []dbparity.RunnerMode{"", "foo", "SQLITE", "postgres", "direct"}
	for _, mode := range invalidModes {
		if mode.IsValid() {
			t.Errorf("expected mode %q to be invalid", mode)
		}
	}

	parsed, err := dbparity.ParseRunnerMode("sqlite")
	if err != nil || parsed != dbparity.ModeSQLite {
		t.Fatalf("expected ModeSQLite, got %v, err: %v", parsed, err)
	}

	parsed, err = dbparity.ParseRunnerMode("  postgres-direct  ")
	if err != nil || parsed != dbparity.ModePostgresDirect {
		t.Fatalf("expected ModePostgresDirect, got %v, err: %v", parsed, err)
	}

	_, err = dbparity.ParseRunnerMode("unknown-mode")
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown runner mode") || !strings.Contains(err.Error(), "valid modes are") {
		t.Errorf("expected actionable error message listing valid modes, got: %v", err)
	}
}

func TestPlan_ListMode(t *testing.T) {
	plans, err := dbparity.Plan(dbparity.ModeList, dbparity.PlanOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected 0 plans for list mode, got %d", len(plans))
	}
}

func expectedCatalogPackages(cat dbparity.Catalog) []string {
	seen := make(map[string]bool)
	var pkgs []string
	for _, comp := range cat.Components {
		for _, tp := range comp.TestPackages {
			if !seen[tp] {
				seen[tp] = true
				pkgs = append(pkgs, tp)
			}
		}
	}
	return pkgs
}

func TestPlan_SQLiteMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs) {
		t.Fatalf("expected %d plans, got %d", len(expectedPkgs), len(plans))
	}

	for i, plan := range plans {
		if plan.Backend != "sqlite" {
			t.Errorf("plan[%d] backend = %q, want 'sqlite'", i, plan.Backend)
		}
		if plan.Package != expectedPkgs[i] {
			t.Errorf("plan[%d] package = %q, want %q", i, plan.Package, expectedPkgs[i])
		}
		if plan.ComponentID == "" {
			t.Errorf("plan[%d] component ID is empty", i)
		}

		argsStr := strings.Join(plan.Args, " ")
		if !strings.HasPrefix(argsStr, "go test") {
			t.Errorf("plan[%d] args do not start with 'go test': %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-run ^TestDBParity_SQLite$") {
			t.Errorf("plan[%d] missing SQLite run selector: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-count=1") {
			t.Errorf("plan[%d] missing -count=1 flag: %s", i, argsStr)
		}
		expectedPkgPath := "./" + expectedPkgs[i]
		if !strings.HasSuffix(argsStr, expectedPkgPath) {
			t.Errorf("plan[%d] expected package arg %q, got args: %s", i, expectedPkgPath, argsStr)
		}
	}
}

func TestPlan_PostgresDirectMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	baseEnv := []string{
		"AIP_TEST_POSTGRES_ADMIN_DSN=postgres://admin:secret@localhost:5432/admin_db",
	}

	plans, err := dbparity.Plan(dbparity.ModePostgresDirect, dbparity.PlanOptions{
		BaseEnv: baseEnv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs) {
		t.Fatalf("expected %d plans, got %d", len(expectedPkgs), len(plans))
	}

	for i, plan := range plans {
		if plan.Backend != "postgres-direct" {
			t.Errorf("plan[%d] backend = %q, want 'postgres-direct'", i, plan.Backend)
		}
		if plan.Package != expectedPkgs[i] {
			t.Errorf("plan[%d] package = %q, want %q", i, plan.Package, expectedPkgs[i])
		}
		if plan.ComponentID == "" {
			t.Errorf("plan[%d] component ID is empty", i)
		}

		argsStr := strings.Join(plan.Args, " ")
		if !strings.HasPrefix(argsStr, "go test") {
			t.Errorf("plan[%d] args do not start with 'go test': %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-tags=integration") {
			t.Errorf("plan[%d] missing -tags=integration: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-run ^TestDBParity_PostgresDirect$") {
			t.Errorf("plan[%d] missing PostgresDirect run selector: %s", i, argsStr)
		}
		if strings.Contains(argsStr, "-skip") || strings.Contains(argsStr, "Pooled") {
			t.Errorf("plan[%d] should not contain -skip or Pooled flags: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-count=1") {
			t.Errorf("plan[%d] missing -count=1 flag: %s", i, argsStr)
		}
		expectedPkgPath := "./" + expectedPkgs[i]
		if !strings.HasSuffix(argsStr, expectedPkgPath) {
			t.Errorf("plan[%d] expected package arg %q, got args: %s", i, expectedPkgPath, argsStr)
		}

		// Verify child environment handling
		hasRequire := false
		hasTestDSN := false
		for _, env := range plan.Env {
			if env == "AIP_REQUIRE_POSTGRES=1" {
				hasRequire = true
			}
			if strings.HasPrefix(env, "AIP_TEST_POSTGRES_DSN=") {
				hasTestDSN = true
				if env != "AIP_TEST_POSTGRES_DSN=postgres://admin:secret@localhost:5432/admin_db" {
					t.Errorf("plan[%d] expected AIP_TEST_POSTGRES_DSN fallback to admin DSN, got %q", i, env)
				}
			}
		}
		if !hasRequire {
			t.Errorf("plan[%d] missing AIP_REQUIRE_POSTGRES=1 in Env", i)
		}
		if !hasTestDSN {
			t.Errorf("plan[%d] missing AIP_TEST_POSTGRES_DSN fallback in Env", i)
		}
	}
}

func TestPlan_PostgresDirect_DSNPrecedenceAndChildEnv(t *testing.T) {
	testDSN := "postgres://runtime:runpass@localhost:5432/runtime_db"
	legacyDSN := "postgres://legacy:legacypass@localhost:5432/legacy_db"
	adminDSN := "postgres://admin:adminpass@localhost:5432/admin_db"

	tests := []struct {
		name         string
		baseEnv      []string
		wantDSN      string
		expectedDesc string
	}{
		{
			name: "case 1: test DSN only",
			baseEnv: []string{
				"PATH=/usr/bin",
				"AIP_TEST_POSTGRES_DSN=" + testDSN,
			},
			wantDSN:      testDSN,
			expectedDesc: "direct test DSN",
		},
		{
			name: "case 2: managed legacy DSN only",
			baseEnv: []string{
				"PATH=/usr/bin",
				"AIP_MANAGED_POSTGRES_DSN=" + legacyDSN,
			},
			wantDSN:      legacyDSN,
			expectedDesc: "legacy managed DSN promoted to canonical test DSN",
		},
		{
			name: "case 3: admin DSN fallback only",
			baseEnv: []string{
				"PATH=/usr/bin",
				"AIP_TEST_POSTGRES_ADMIN_DSN=" + adminDSN,
			},
			wantDSN:      adminDSN,
			expectedDesc: "admin DSN fallback promoted to canonical test DSN",
		},
		{
			name: "precedence: test DSN beats managed legacy and admin fallback",
			baseEnv: []string{
				"PATH=/usr/bin",
				"AIP_TEST_POSTGRES_ADMIN_DSN=" + adminDSN,
				"AIP_MANAGED_POSTGRES_DSN=" + legacyDSN,
				"AIP_TEST_POSTGRES_DSN=" + testDSN,
			},
			wantDSN:      testDSN,
			expectedDesc: "highest precedence test DSN wins",
		},
		{
			name: "precedence: managed legacy beats admin fallback",
			baseEnv: []string{
				"PATH=/usr/bin",
				"AIP_TEST_POSTGRES_ADMIN_DSN=" + adminDSN,
				"AIP_MANAGED_POSTGRES_DSN=" + legacyDSN,
			},
			wantDSN:      legacyDSN,
			expectedDesc: "legacy managed DSN beats admin fallback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := dbparity.Plan(dbparity.ModePostgresDirect, dbparity.PlanOptions{
				BaseEnv: tc.baseEnv,
			})
			if err != nil {
				t.Fatalf("unexpected Plan error for %s: %v", tc.expectedDesc, err)
			}
			if len(plans) == 0 {
				t.Fatalf("expected non-empty plans for %s", tc.expectedDesc)
			}

			for i, plan := range plans {
				// 1. plan.Env must ALWAYS contain AIP_TEST_POSTGRES_DSN=<effective> and AIP_REQUIRE_POSTGRES=1
				foundPlanTestDSN := false
				var planDSNVal string
				foundPlanRequire := false
				for _, env := range plan.Env {
					k, v, ok := strings.Cut(env, "=")
					if !ok {
						continue
					}
					if strings.EqualFold(k, "AIP_TEST_POSTGRES_DSN") {
						foundPlanTestDSN = true
						planDSNVal = v
					}
					if strings.EqualFold(k, "AIP_REQUIRE_POSTGRES") && v == "1" {
						foundPlanRequire = true
					}
				}

				if !foundPlanTestDSN {
					t.Errorf("plan[%d].Env missing AIP_TEST_POSTGRES_DSN for %s", i, tc.expectedDesc)
				} else if planDSNVal != tc.wantDSN {
					t.Errorf("plan[%d].Env AIP_TEST_POSTGRES_DSN = %q, want %q for %s", i, planDSNVal, tc.wantDSN, tc.expectedDesc)
				}
				if !foundPlanRequire {
					t.Errorf("plan[%d].Env missing AIP_REQUIRE_POSTGRES=1 for %s", i, tc.expectedDesc)
				}

				// 2. Cmd(ctx, baseEnv).Env must contain AIP_TEST_POSTGRES_DSN=<effective> exactly once
				cmd := plan.Cmd(context.Background(), tc.baseEnv)
				if cmd == nil {
					t.Fatalf("plan[%d].Cmd returned nil", i)
				}

				dsnCount := 0
				var cmdDSNVal string
				requireCount := 0
				for _, env := range cmd.Env {
					k, v, ok := strings.Cut(env, "=")
					if !ok {
						continue
					}
					if strings.EqualFold(k, "AIP_TEST_POSTGRES_DSN") {
						dsnCount++
						cmdDSNVal = v
					}
					if strings.EqualFold(k, "AIP_REQUIRE_POSTGRES") {
						requireCount++
					}
				}

				if dsnCount != 1 {
					t.Errorf("plan[%d] cmd.Env expected exactly 1 AIP_TEST_POSTGRES_DSN, got %d for %s", i, dsnCount, tc.expectedDesc)
				}
				if cmdDSNVal != tc.wantDSN {
					t.Errorf("plan[%d] cmd.Env AIP_TEST_POSTGRES_DSN = %q, want %q for %s", i, cmdDSNVal, tc.wantDSN, tc.expectedDesc)
				}
				if requireCount != 1 {
					t.Errorf("plan[%d] cmd.Env expected exactly 1 AIP_REQUIRE_POSTGRES, got %d for %s", i, requireCount, tc.expectedDesc)
				}
			}
		})
	}
}

func TestPlan_AllMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	plans, err := dbparity.Plan(dbparity.ModeAll, dbparity.PlanOptions{
		BaseEnv: []string{"AIP_TEST_POSTGRES_DSN=postgres://user:pass@localhost:5432/db"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs)*2 {
		t.Fatalf("expected %d plans (sqlite + postgres-direct), got %d", len(expectedPkgs)*2, len(plans))
	}

	// First half is SQLite, second half is PostgresDirect
	for i := range expectedPkgs {
		if plans[i].Backend != "sqlite" {
			t.Errorf("plans[%d] backend = %q, want 'sqlite'", i, plans[i].Backend)
		}
	}
	for i := len(expectedPkgs); i < len(plans); i++ {
		if plans[i].Backend != "postgres-direct" {
			t.Errorf("plans[%d] backend = %q, want 'postgres-direct'", i, plans[i].Backend)
		}
	}
}

func TestPlan_ComponentFilter(t *testing.T) {
	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "concurrency-authority",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan for concurrency-authority, got %d", len(plans))
	}
	if plans[0].ComponentID != "concurrency-authority" || plans[0].Package != "internal/infra/concurrencyauthority/leasestore" {
		t.Errorf("unexpected plan: %+v", plans[0])
	}

	// secure-sessions has 2 test packages
	plans, err = dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "secure-sessions",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans for secure-sessions, got %d", len(plans))
	}

	// unknown component returns actionable error
	_, err = dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent component")
	}
	if !strings.Contains(err.Error(), "unknown component") || !strings.Contains(err.Error(), "concurrency-authority") {
		t.Errorf("expected actionable error naming valid components, got: %v", err)
	}
}

func TestPlan_GoTestFlagsPassthrough(t *testing.T) {
	flags := []string{"-timeout=5m", "-parallel=4"}
	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		GoTestFlags: flags,
		ComponentID: "concurrency-authority",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}

	argsStr := strings.Join(plans[0].Args, " ")
	if !strings.Contains(argsStr, "-timeout=5m") || !strings.Contains(argsStr, "-parallel=4") {
		t.Errorf("flags not passed through to args: %s", argsStr)
	}
}

func TestPlan_GoTestFlagsQuotedGroupPreservation(t *testing.T) {
	rawFlags := `-run "Test Name" -count=1 -timeout='10m'`
	parsedFlags, err := dbparity.ParseFlagWords(rawFlags)
	if err != nil {
		t.Fatalf("ParseFlagWords failed: %v", err)
	}
	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		GoTestFlags: parsedFlags,
		ComponentID: "concurrency-authority",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	plan := plans[0]

	foundQuotedGroup := false
	for _, arg := range plan.Args {
		if arg == "Test Name" {
			foundQuotedGroup = true
		}
		if arg == "Test" || arg == "Name" {
			t.Errorf("found split individual argument %q in plan.Args: %#v", arg, plan.Args)
		}
	}
	if !foundQuotedGroup {
		t.Errorf("plan.Args missing preserved grouped argument %q: %#v", "Test Name", plan.Args)
	}
}

func TestPlan_Deduplication(t *testing.T) {
	customCat := dbparity.Catalog{
		Components: []dbparity.Component{
			{
				ID:           "comp-a",
				TestPackages: []string{"pkg/shared", "pkg/a"},
			},
			{
				ID:           "comp-b",
				TestPackages: []string{"pkg/shared", "pkg/b"},
			},
		},
	}

	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		Catalog: customCat,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != 3 {
		t.Fatalf("expected 3 unique package plans (shared, a, b), got %d", len(plans))
	}
	if plans[0].Package != "pkg/shared" || plans[1].Package != "pkg/a" || plans[2].Package != "pkg/b" {
		t.Errorf("unexpected deduplicated order: %+v", plans)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard postgres URL with user and password",
			input:    "failed connecting to postgres://user:secret123@localhost:5432/dbname",
			expected: "failed connecting to postgres://user:***@localhost:5432/dbname",
		},
		{
			name:     "postgresql URL scheme",
			input:    "connect postgresql://admin:p%40ssw0rd@db.internal:5432/test?sslmode=disable",
			expected: "connect postgresql://admin:***@db.internal:5432/test?sslmode=disable",
		},
		{
			name:     "password only without user",
			input:    "dsn is postgres://:secret@localhost:5432/db",
			expected: "dsn is postgres://:***@localhost:5432/db",
		},
		{
			name:     "URL without password",
			input:    "url postgres://user@localhost:5432/db",
			expected: "url postgres://user@localhost:5432/db",
		},
		{
			name:     "key value DSN with unquoted password",
			input:    "host=localhost port=5432 user=app password=mysecret dbname=appdb",
			expected: "host=localhost port=5432 user=app password=*** dbname=appdb",
		},
		{
			name:     "key value DSN with single-quoted password and spaces",
			input:    "host=localhost password = 'secret value' user=app",
			expected: "host=localhost password = '***' user=app",
		},
		{
			name:     "key value DSN with uppercase double-quoted password",
			input:    `PASSWORD="secret value" dbname=test`,
			expected: `PASSWORD="***" dbname=test`,
		},
		{
			name:     "key value DSN with spaced unquoted password",
			input:    "password = secret",
			expected: "password = ***",
		},
		{
			name:     "key value DSN with mixed casing and quotes",
			input:    "Password = 'secret value'",
			expected: "Password = '***'",
		},
		{
			name:     "key value DSN with empty quotes",
			input:    `password="" password=''`,
			expected: `password="***" password='***'`,
		},
		{
			name:     "avoid over-redacting notpassword",
			input:    "notpassword=secret not_password='secret'",
			expected: "notpassword=secret not_password='secret'",
		},
		{
			name:     "mixed surrounding text with multiple passwords",
			input:    `error connecting: PASSWORD = "another secret" and password=unquoted in logs`,
			expected: `error connecting: PASSWORD = "***" and password=*** in logs`,
		},
		{
			name:     "single-quoted password with escaped quote",
			input:    "password = 'secret\\'quote' dbname=appdb",
			expected: "password = '***' dbname=appdb",
		},
		{
			name:     "double-quoted password with escaped quote",
			input:    `PASSWORD="secret\"quote" dbname=appdb`,
			expected: `PASSWORD="***" dbname=appdb`,
		},
		{
			name:     "single-quoted password with escaped backslash",
			input:    `password='secret\\value' dbname=appdb`,
			expected: `password='***' dbname=appdb`,
		},
		{
			name:     "unterminated single-quoted password redacts to end",
			input:    "failed with password='unterminated secret value",
			expected: "failed with password='***",
		},
		{
			name:     "unterminated double-quoted password redacts to end",
			input:    `failed with PASSWORD="unterminated secret value`,
			expected: `failed with PASSWORD="***`,
		},
		{
			name:     "non-sensitive error message",
			input:    "dbparity: test failed for component \"billing\" package \"internal/infra/billingstore\"",
			expected: "dbparity: test failed for component \"billing\" package \"internal/infra/billingstore\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dbparity.RedactDSN(tc.input)
			if got != tc.expected {
				t.Errorf("RedactDSN(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
			if !strings.Contains(tc.expected, "secret") && strings.Contains(got, "secret") {
				t.Errorf("RedactDSN(%q) leaked secret: %q", tc.input, got)
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	textOut := dbparity.FormatList(cat)

	for _, comp := range cat.Components {
		if !strings.Contains(textOut, comp.ID) {
			t.Errorf("FormatList output missing component ID %q", comp.ID)
		}
		for _, tp := range comp.TestPackages {
			if !strings.Contains(textOut, tp) {
				t.Errorf("FormatList output missing test package %q for component %q", tp, comp.ID)
			}
		}
	}

	jsonOut, err := dbparity.FormatListJSON(cat)
	if err != nil {
		t.Fatalf("FormatListJSON failed: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("FormatListJSON returned invalid JSON: %v", err)
	}
	if len(parsed) != len(cat.Components) {
		t.Errorf("expected %d entries in JSON list, got %d", len(cat.Components), len(parsed))
	}
}

func TestCommandPlan_Cmd(t *testing.T) {
	plan := dbparity.CommandPlan{
		ComponentID: "billing",
		Package:     "internal/infra/billingstore",
		Backend:     "postgres-direct",
		Args:        []string{"go", "test", "-tags=integration", "./internal/infra/billingstore"},
		Env:         []string{"AIP_REQUIRE_POSTGRES=1"},
	}

	baseEnv := []string{"PATH=/usr/bin"}
	cmd := plan.Cmd(context.Background(), baseEnv)
	if cmd == nil {
		t.Fatal("expected non-nil *exec.Cmd")
	}

	foundRequire := false
	foundPath := false
	for _, e := range cmd.Env {
		if e == "AIP_REQUIRE_POSTGRES=1" {
			foundRequire = true
		}
		if e == "PATH=/usr/bin" {
			foundPath = true
		}
	}
	if !foundRequire || !foundPath {
		t.Errorf("cmd.Env missing expected vars: %v", cmd.Env)
	}
}

func TestCommandPlan_Cmd_NormalizesEnvironmentWithoutDuplicates(t *testing.T) {
	plan := dbparity.CommandPlan{
		ComponentID: "billing",
		Package:     "internal/infra/billingstore",
		Backend:     "postgres-direct",
		Args:        []string{"go", "test", "-tags=integration", "./internal/infra/billingstore"},
		Env: []string{
			"AIP_REQUIRE_POSTGRES=1",
			"AIP_TEST_POSTGRES_DSN=postgres://admin:secret@localhost:5432/admin_db",
		},
	}

	baseEnv := []string{
		"PATH=/usr/bin",
		"AIP_REQUIRE_POSTGRES=0",
		"AIP_TEST_POSTGRES_DSN=",
		"OTHER_VAR=value",
	}

	cmd := plan.Cmd(context.Background(), baseEnv)
	if cmd == nil {
		t.Fatal("expected non-nil *exec.Cmd")
	}

	requireCount := 0
	var requireVal string
	dsnCount := 0
	var dsnVal string
	pathCount := 0
	otherCount := 0

	for _, e := range cmd.Env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			k = e
		}
		switch {
		case strings.EqualFold(k, "AIP_REQUIRE_POSTGRES"):
			requireCount++
			requireVal = v
		case strings.EqualFold(k, "AIP_TEST_POSTGRES_DSN"):
			dsnCount++
			dsnVal = v
		case strings.EqualFold(k, "PATH"):
			pathCount++
		case strings.EqualFold(k, "OTHER_VAR"):
			otherCount++
		}
	}

	if requireCount != 1 {
		t.Errorf("expected exactly 1 AIP_REQUIRE_POSTGRES in cmd.Env, got %d (all env: %v)", requireCount, cmd.Env)
	}
	if requireVal != "1" {
		t.Errorf("expected AIP_REQUIRE_POSTGRES=1 in cmd.Env, got %q", requireVal)
	}

	if dsnCount != 1 {
		t.Errorf("expected exactly 1 AIP_TEST_POSTGRES_DSN in cmd.Env, got %d (all env: %v)", dsnCount, cmd.Env)
	}
	if dsnVal != "postgres://admin:secret@localhost:5432/admin_db" {
		t.Errorf("expected AIP_TEST_POSTGRES_DSN to be overridden with admin DSN fallback, got %q", dsnVal)
	}

	if pathCount != 1 {
		t.Errorf("expected PATH to be preserved, got count %d", pathCount)
	}
	if otherCount != 1 {
		t.Errorf("expected OTHER_VAR to be preserved, got count %d", otherCount)
	}
}

func TestCommandPlan_Cmd_WindowsPseudoEnvVars(t *testing.T) {
	t.Run("preserves distinct pseudo-vars with latest-wins and deterministic overrides", func(t *testing.T) {
		plan := dbparity.CommandPlan{
			ComponentID: "billing",
			Package:     "internal/infra/billingstore",
			Backend:     "postgres-direct",
			Args:        []string{"go", "test", "-tags=integration", "./internal/infra/billingstore"},
			Env: []string{
				"OVERRIDE_VAR=overridden_value",
				"INJECTED_VAR_A=injected_val_a",
				"INJECTED_VAR_B=injected_val_b",
			},
		}

		baseEnv := []string{
			"=C:=C:\\repo_old",
			"=D:=D:\\work",
			"FOO=first",
			"BAR=bar_val",
			"foo=second",
			"=C:=C:\\repo",
			"OVERRIDE_VAR=original_value",
			"PATH=C:\\Windows\\System32",
		}

		cmd := plan.Cmd(context.Background(), baseEnv)
		if cmd == nil {
			t.Fatal("expected non-nil *exec.Cmd")
		}

		expected := []string{
			"=C:=C:\\repo",
			"=D:=D:\\work",
			"foo=second",
			"BAR=bar_val",
			"OVERRIDE_VAR=overridden_value",
			"PATH=C:\\Windows\\System32",
			"INJECTED_VAR_A=injected_val_a",
			"INJECTED_VAR_B=injected_val_b",
		}

		if !slices.Equal(cmd.Env, expected) {
			t.Errorf("cmd.Env mismatch:\ngot:  %#v\nwant: %#v", cmd.Env, expected)
		}
	})

	t.Run("pseudo-vars with preflight and plan inspection", func(t *testing.T) {
		baseEnv := []string{
			"=C:=C:\\repo",
			"=D:=D:\\work",
			"aip_test_postgres_dsn=postgres://user:pass@localhost:5432/db",
		}
		opts := dbparity.PlanOptions{
			BaseEnv: baseEnv,
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("unexpected Plan error: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected non-empty plans")
		}
		cmd := plans[0].Cmd(context.Background(), baseEnv)
		foundC := false
		foundD := false
		for _, e := range cmd.Env {
			if e == "=C:=C:\\repo" {
				foundC = true
			}
			if e == "=D:=D:\\work" {
				foundD = true
			}
		}
		if !foundC || !foundD {
			t.Errorf("cmd.Env missing pseudo vars: %v", cmd.Env)
		}
	})
}

func TestRun_ListMode(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), dbparity.ModeList, dbparity.PlanOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error running list mode: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr, got: %s", stderr.String())
	}
	out := stdout.String()
	for _, comp := range dbparity.DefaultCatalog().Components {
		if !strings.Contains(out, comp.ID) {
			t.Errorf("expected stdout to contain component %q", comp.ID)
		}
	}
}

func TestRun_InvalidMode(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), "invalid-mode", dbparity.PlanOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "unknown runner mode") {
		t.Errorf("expected unknown runner mode error, got: %v", err)
	}
}

func TestRun_ComponentFilterError(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "invalid-comp",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for invalid component")
	}
	if !strings.Contains(err.Error(), "unknown component") {
		t.Errorf("expected unknown component error, got: %v", err)
	}
}

type stubExitError struct {
	code int
	msg  string
}

func (s *stubExitError) Error() string {
	if s.msg != "" {
		return s.msg
	}
	return fmt.Sprintf("exit status %d", s.code)
}

func (s *stubExitError) ExitCode() int {
	return s.code
}

func TestRunStepError_UnwrapAndRedaction(t *testing.T) {
	inner := &stubExitError{
		code: 42,
		msg:  "failed connecting to postgres://user:secret@localhost:5432/db: exit status 42",
	}

	stepErr := &dbparity.RunStepError{
		Component: "billing",
		Package:   "internal/infra/billingstore",
		Backend:   "postgres-direct",
		Err:       inner,
	}

	// Verify Unwrap preserves cause
	if !errors.Is(stepErr, inner) {
		t.Errorf("expected errors.Is(stepErr, inner) to be true")
	}

	var exitCoder dbparity.ExitCoder
	if !errors.As(stepErr, &exitCoder) {
		t.Fatalf("expected errors.As(stepErr, &exitCoder) to match")
	}
	if exitCoder.ExitCode() != 42 {
		t.Errorf("exitCoder.ExitCode() = %d, want 42", exitCoder.ExitCode())
	}

	// Verify Error() redacts credentials
	errStr := stepErr.Error()
	if strings.Contains(errStr, "secret") {
		t.Errorf("stepErr.Error() leaked credentials: %s", errStr)
	}
	if !strings.Contains(errStr, "postgres://user:***@localhost:5432/db") {
		t.Errorf("stepErr.Error() missing redacted DSN: %s", errStr)
	}
}

func TestMapExitStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{
			name:     "nil error returns 0",
			err:      nil,
			expected: 0,
		},
		{
			name:     "direct context.Canceled returns 130",
			err:      context.Canceled,
			expected: 130,
		},
		{
			name: "wrapped context.Canceled in RunStepError returns 130",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       context.Canceled,
			},
			expected: 130,
		},
		{
			name:     "direct ExitCoder returns its code",
			err:      &stubExitError{code: 42},
			expected: 42,
		},
		{
			name: "wrapped ExitCoder in RunStepError returns its code",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       &stubExitError{code: 77},
			},
			expected: 77,
		},
		{
			name:     "generic error returns 1",
			err:      errors.New("generic execution failure"),
			expected: 1,
		},
		{
			name: "wrapped generic error in RunStepError returns 1",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       errors.New("generic execution failure"),
			},
			expected: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dbparity.MapExitStatus(tc.err)
			if got != tc.expected {
				t.Errorf("MapExitStatus(%v) = %d, want %d", tc.err, got, tc.expected)
			}
		})
	}
}

func TestRun_ErrorPropagation_PreservesExitCode(t *testing.T) {
	var stdout, stderr strings.Builder
	stubErr := &stubExitError{code: 42}

	opts := dbparity.PlanOptions{
		ComponentID: "concurrency-authority",
		CmdRunner: func(cmd *exec.Cmd) error {
			return stubErr
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run to return an error")
	}

	var stepErr *dbparity.RunStepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
	}

	if stepErr.Component != "concurrency-authority" {
		t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
	}
	if stepErr.Backend != "sqlite" {
		t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
	}

	exitCode := dbparity.MapExitStatus(err)
	if exitCode != 42 {
		t.Errorf("dbparity.MapExitStatus(err) = %d, want 42", exitCode)
	}
}

func TestRun_ErrorPropagation_PreservesContextCanceled(t *testing.T) {
	var stdout, stderr strings.Builder

	opts := dbparity.PlanOptions{
		ComponentID: "concurrency-authority",
		CmdRunner: func(cmd *exec.Cmd) error {
			return context.Canceled
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run to return an error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
	}

	exitCode := dbparity.MapExitStatus(err)
	if exitCode != 130 {
		t.Errorf("dbparity.MapExitStatus(err) = %d, want 130", exitCode)
	}
}

func TestRun_ErrorPropagation_ContextCancellationWithExitCoder(t *testing.T) {
	// Scenario A: cancel the parent context then have CmdRunner return an *exec.ExitError-like ExitCoder;
	// assert errors.Is(err, context.Canceled) == true, MapExitStatus == 130, and RunStepError metadata is preserved.
	t.Run("canceled context with exit coder wraps context.Canceled", func(t *testing.T) {
		var stdout, stderr strings.Builder
		ctx, cancel := context.WithCancel(context.Background())

		stubErr := &stubExitError{code: 1, msg: "signal: killed"}
		opts := dbparity.PlanOptions{
			ComponentID: "concurrency-authority",
			CmdRunner: func(cmd *exec.Cmd) error {
				cancel()
				<-ctx.Done()
				return stubErr
			},
		}

		err := dbparity.Run(ctx, dbparity.ModeSQLite, opts, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected Run to return an error")
		}

		var stepErr *dbparity.RunStepError
		if !errors.As(err, &stepErr) {
			t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
		}
		if stepErr.Component != "concurrency-authority" {
			t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
		}
		if stepErr.Backend != "sqlite" {
			t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
		}

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
		}
		if stepErr.Err != context.Canceled {
			t.Errorf("stepErr.Err = %v, want context.Canceled", stepErr.Err)
		}

		exitCode := dbparity.MapExitStatus(err)
		if exitCode != 130 {
			t.Errorf("dbparity.MapExitStatus(err) = %d, want 130", exitCode)
		}
	})

	// Scenario B: ctx alive + failing ExitCoder -> errors.Is false and exact code preserved.
	t.Run("alive context with failing exit coder preserves exit code and is not canceled", func(t *testing.T) {
		var stdout, stderr strings.Builder
		ctx := context.Background()

		stubErr := &stubExitError{code: 42, msg: "exit status 42"}
		opts := dbparity.PlanOptions{
			ComponentID: "concurrency-authority",
			CmdRunner: func(cmd *exec.Cmd) error {
				return stubErr
			},
		}

		err := dbparity.Run(ctx, dbparity.ModeSQLite, opts, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected Run to return an error")
		}

		var stepErr *dbparity.RunStepError
		if !errors.As(err, &stepErr) {
			t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
		}
		if stepErr.Component != "concurrency-authority" {
			t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
		}
		if stepErr.Backend != "sqlite" {
			t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
		}

		if errors.Is(err, context.Canceled) {
			t.Errorf("expected errors.Is(err, context.Canceled) to be false")
		}

		exitCode := dbparity.MapExitStatus(err)
		if exitCode != 42 {
			t.Errorf("dbparity.MapExitStatus(err) = %d, want 42", exitCode)
		}
	})
}

func TestPlan_PostgresDirect_MissingDSN_FailsImmediately(t *testing.T) {
	testCases := []struct {
		name    string
		baseEnv []string
	}{
		{
			name:    "scrubbed empty environment",
			baseEnv: []string{},
		},
		{
			name:    "unrelated environment variables only",
			baseEnv: []string{"PATH=/usr/bin", "FOO=BAR", "USER=tester"},
		},
		{
			name:    "whitespace only DSN environment variables",
			baseEnv: []string{"AIP_TEST_POSTGRES_DSN=   ", "AIP_TEST_POSTGRES_ADMIN_DSN=\t \n", "AIP_MANAGED_POSTGRES_DSN=  "},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := dbparity.PlanOptions{
				BaseEnv: tc.baseEnv,
			}
			plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
			if err == nil {
				t.Fatalf("expected Plan(ModePostgresDirect) to fail without DSN, got plans: %+v", plans)
			}
			if len(plans) != 0 {
				t.Errorf("expected 0 plans on error, got %d", len(plans))
			}

			errMsg := err.Error()
			// Must name the accepted environment variables
			if !strings.Contains(errMsg, "AIP_TEST_POSTGRES_DSN") {
				t.Errorf("error message %q should name AIP_TEST_POSTGRES_DSN", errMsg)
			}
			if !strings.Contains(errMsg, "AIP_TEST_POSTGRES_ADMIN_DSN") {
				t.Errorf("error message %q should name AIP_TEST_POSTGRES_ADMIN_DSN", errMsg)
			}
			if !strings.Contains(errMsg, "AIP_MANAGED_POSTGRES_DSN") {
				t.Errorf("error message %q should name AIP_MANAGED_POSTGRES_DSN", errMsg)
			}
			// Must be actionable and secret-safe
			if strings.Contains(errMsg, "password=") || strings.Contains(errMsg, "postgres://") {
				t.Errorf("error message leaked secret or DSN: %q", errMsg)
			}
		})
	}
}

func TestPlan_AllMode_MissingDSN_FailsImmediately(t *testing.T) {
	opts := dbparity.PlanOptions{
		BaseEnv: []string{},
	}
	plans, err := dbparity.Plan(dbparity.ModeAll, opts)
	if err == nil {
		t.Fatalf("expected Plan(ModeAll) to fail immediately without DSN, got plans: %+v", plans)
	}
	if len(plans) != 0 {
		t.Errorf("expected 0 plans on error, got %d", len(plans))
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "AIP_TEST_POSTGRES_DSN") {
		t.Errorf("error message %q should name AIP_TEST_POSTGRES_DSN", errMsg)
	}
}

func TestRun_PostgresDirect_MissingDSN_NoCommandInvocation(t *testing.T) {
	var stdout, stderr strings.Builder
	var cmdInvocationCount int

	opts := dbparity.PlanOptions{
		BaseEnv: []string{},
		CmdRunner: func(cmd *exec.Cmd) error {
			cmdInvocationCount++
			return nil
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModePostgresDirect, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run(ModePostgresDirect) to fail without DSN")
	}

	if cmdInvocationCount != 0 {
		t.Fatalf("expected 0 commands invoked when DSN is missing, got %d", cmdInvocationCount)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "AIP_TEST_POSTGRES_DSN") {
		t.Errorf("error message %q should name AIP_TEST_POSTGRES_DSN", errMsg)
	}
}

func TestRun_AllMode_MissingDSN_NoCommandInvocation(t *testing.T) {
	var stdout, stderr strings.Builder
	var cmdInvocationCount int

	opts := dbparity.PlanOptions{
		BaseEnv: []string{},
		CmdRunner: func(cmd *exec.Cmd) error {
			cmdInvocationCount++
			return nil
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModeAll, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run(ModeAll) to fail without DSN")
	}

	if cmdInvocationCount != 0 {
		t.Fatalf("expected 0 commands invoked when DSN is missing in all mode (not even SQLite should run), got %d", cmdInvocationCount)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "AIP_TEST_POSTGRES_DSN") {
		t.Errorf("error message %q should name AIP_TEST_POSTGRES_DSN", errMsg)
	}
}

func TestPlan_PostgresDirect_AcceptedAliases(t *testing.T) {
	t.Run("runtime DSN alias", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"AIP_TEST_POSTGRES_DSN=postgres://app:secret@localhost:5432/appdb"},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("unexpected error with AIP_TEST_POSTGRES_DSN: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected non-empty plans")
		}
	})

	t.Run("legacy managed DSN alias", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"AIP_MANAGED_POSTGRES_DSN=postgres://legacy:secret@localhost:5432/legacydb"},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("unexpected error with AIP_MANAGED_POSTGRES_DSN: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected non-empty plans")
		}
	})

	t.Run("admin DSN fallback alias injects runtime DSN", func(t *testing.T) {
		adminDSN := "postgres://admin:secret@localhost:5432/admindb"
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"AIP_TEST_POSTGRES_ADMIN_DSN=" + adminDSN},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("unexpected error with AIP_TEST_POSTGRES_ADMIN_DSN: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected non-empty plans")
		}
		foundFallback := false
		for _, env := range plans[0].Env {
			if env == "AIP_TEST_POSTGRES_DSN="+adminDSN {
				foundFallback = true
			}
		}
		if !foundFallback {
			t.Errorf("expected plan.Env to contain AIP_TEST_POSTGRES_DSN fallback to admin DSN, got: %v", plans[0].Env)
		}
	})
}

func TestPlan_PostgresDirect_ExactSelectorWithoutSkip(t *testing.T) {
	opts := dbparity.PlanOptions{
		BaseEnv: []string{"AIP_TEST_POSTGRES_DSN=postgres://user:pass@localhost:5432/db"},
	}
	plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, plan := range plans {
		argsStr := strings.Join(plan.Args, " ")
		if !strings.Contains(argsStr, "-run ^TestDBParity_PostgresDirect$") {
			t.Errorf("plan[%d] missing exact direct run selector: %s", i, argsStr)
		}
		if slices.Contains(plan.Args, "-skip") || strings.Contains(argsStr, "Pooled") {
			t.Errorf("plan[%d] direct plan must not contain -skip or Pooled flags: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-tags=integration") {
			t.Errorf("plan[%d] missing integration build tag: %s", i, argsStr)
		}
	}
}

func TestPlan_SQLiteAndList_DoNotRequireDSN(t *testing.T) {
	opts := dbparity.PlanOptions{
		BaseEnv: []string{}, // Scrubbed environment
	}

	// SQLite mode succeeds without any PostgreSQL DSN
	sqlitePlans, err := dbparity.Plan(dbparity.ModeSQLite, opts)
	if err != nil {
		t.Fatalf("Plan(ModeSQLite) should succeed with empty baseEnv, got: %v", err)
	}
	if len(sqlitePlans) == 0 {
		t.Fatal("Plan(ModeSQLite) returned 0 plans")
	}

	// List mode succeeds without any PostgreSQL DSN
	listPlans, err := dbparity.Plan(dbparity.ModeList, opts)
	if err != nil {
		t.Fatalf("Plan(ModeList) should succeed with empty baseEnv, got: %v", err)
	}
	if len(listPlans) != 0 {
		t.Fatalf("Plan(ModeList) should return 0 plans, got %d", len(listPlans))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}

// validatePostgresDirectWrapperAST inspects parsed AST files of a test package to ensure:
// 1. Exactly one canonical TestDBParity_PostgresDirect test function is declared.
// 2. TestDBParity_PostgresDirect (or its local helper call graph) invokes SkipUnlessPostgres.
// 3. TestDBParity_PostgresDirect (or its local helper call graph) NEVER invokes SkipUnlessPostgresPooled.
func validatePostgresDirectWrapperAST(files []*ast.File) error {
	localFuncs := make(map[string]*ast.FuncDecl)
	var directDecls []*ast.FuncDecl

	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			localFuncs[fn.Name.Name] = fn
			if fn.Name.Name == "TestDBParity_PostgresDirect" {
				directDecls = append(directDecls, fn)
			}
		}
	}

	if len(directDecls) == 0 {
		return errors.New("missing canonical TestDBParity_PostgresDirect function declaration")
	}
	if len(directDecls) > 1 {
		return fmt.Errorf("multiple (%d) declarations of TestDBParity_PostgresDirect found", len(directDecls))
	}

	fn := directDecls[0]
	if fn.Body == nil {
		return errors.New("TestDBParity_PostgresDirect has empty function body")
	}

	visitedFuncs := make(map[string]bool)
	callsDirectGate := false
	callsPooledGate := false

	var inspectBody func(body *ast.BlockStmt)
	inspectBody = func(body *ast.BlockStmt) {
		if body == nil {
			return
		}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				selName := fun.Sel.Name
				if selName == "SkipUnlessPostgresPooled" {
					callsPooledGate = true
				}
				if selName == "SkipUnlessPostgres" {
					callsDirectGate = true
				}
			case *ast.Ident:
				identName := fun.Name
				if identName == "SkipUnlessPostgresPooled" {
					callsPooledGate = true
				}
				if identName == "SkipUnlessPostgres" {
					callsDirectGate = true
				}
				if helper, exists := localFuncs[identName]; exists && !visitedFuncs[identName] {
					visitedFuncs[identName] = true
					inspectBody(helper.Body)
				}
			}
			return true
		})
	}

	inspectBody(fn.Body)

	if callsPooledGate {
		return errors.New("TestDBParity_PostgresDirect invokes SkipUnlessPostgresPooled (direct parity must never use pooler gate)")
	}
	if !callsDirectGate {
		return errors.New("TestDBParity_PostgresDirect does not invoke SkipUnlessPostgres")
	}

	return nil
}

func TestValidatePostgresDirectWrapper_RejectsPooledGate(t *testing.T) {
	fset := token.NewFileSet()

	t.Run("direct_pooled_gate_call_rejected", func(t *testing.T) {
		src := `package testpkg_test
import (
	"testing"
	"github.com/aiproxer/aiproxer/internal/testkit"
)
func TestDBParity_PostgresDirect(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	_ = adminDSN
	_ = runtimeDSN
}
`
		f, err := parser.ParseFile(fset, "dbparity_postgres_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		err = validatePostgresDirectWrapperAST([]*ast.File{f})
		if err == nil {
			t.Fatal("expected validation to reject wrapper calling SkipUnlessPostgresPooled directly")
		}
		if !strings.Contains(err.Error(), "SkipUnlessPostgresPooled") {
			t.Errorf("expected error mentioning SkipUnlessPostgresPooled, got: %v", err)
		}
	})

	t.Run("helper_pooled_gate_call_rejected", func(t *testing.T) {
		src := `package testpkg_test
import (
	"testing"
	"github.com/aiproxer/aiproxer/internal/testkit"
)
func setupPooled(t *testing.T) {
	testkit.SkipUnlessPostgresPooled(t)
}
func TestDBParity_PostgresDirect(t *testing.T) {
	setupPooled(t)
}
`
		f, err := parser.ParseFile(fset, "dbparity_postgres_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		err = validatePostgresDirectWrapperAST([]*ast.File{f})
		if err == nil {
			t.Fatal("expected validation to reject wrapper delegating to helper calling SkipUnlessPostgresPooled")
		}
		if !strings.Contains(err.Error(), "SkipUnlessPostgresPooled") {
			t.Errorf("expected error mentioning SkipUnlessPostgresPooled, got: %v", err)
		}
	})

	t.Run("missing_wrapper_rejected", func(t *testing.T) {
		src := `package testpkg_test
import "testing"
func TestOtherThing(t *testing.T) {}
`
		f, err := parser.ParseFile(fset, "other_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		err = validatePostgresDirectWrapperAST([]*ast.File{f})
		if err == nil {
			t.Fatal("expected validation to fail when TestDBParity_PostgresDirect is missing")
		}
		if !strings.Contains(err.Error(), "missing canonical") {
			t.Errorf("expected missing canonical error, got: %v", err)
		}
	})

	t.Run("no_gate_call_rejected", func(t *testing.T) {
		src := `package testpkg_test
import "testing"
func TestDBParity_PostgresDirect(t *testing.T) {
	t.Log("no gate call here")
}
`
		f, err := parser.ParseFile(fset, "dbparity_postgres_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		err = validatePostgresDirectWrapperAST([]*ast.File{f})
		if err == nil {
			t.Fatal("expected validation to fail when TestDBParity_PostgresDirect does not call SkipUnlessPostgres")
		}
		if !strings.Contains(err.Error(), "does not invoke SkipUnlessPostgres") {
			t.Errorf("expected error about missing SkipUnlessPostgres call, got: %v", err)
		}
	})
}

func TestValidatePostgresDirectWrapper_AcceptsValidDirectWrappers(t *testing.T) {
	fset := token.NewFileSet()

	t.Run("direct_call_accepted", func(t *testing.T) {
		src := `package testpkg_test
import (
	"testing"
	"github.com/aiproxer/aiproxer/internal/testkit"
)
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	_ = dsn
}
`
		f, err := parser.ParseFile(fset, "dbparity_postgres_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		if err := validatePostgresDirectWrapperAST([]*ast.File{f}); err != nil {
			t.Errorf("expected valid direct wrapper to pass, got: %v", err)
		}
	})

	t.Run("helper_delegation_accepted", func(t *testing.T) {
		src := `package testpkg_test
import (
	"testing"
	"github.com/aiproxer/aiproxer/internal/testkit"
)
func setupDirect(t *testing.T) string {
	return testkit.SkipUnlessPostgres(t)
}
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := setupDirect(t)
	_ = dsn
}
`
		f, err := parser.ParseFile(fset, "dbparity_postgres_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		if err := validatePostgresDirectWrapperAST([]*ast.File{f}); err != nil {
			t.Errorf("expected helper delegation to pass, got: %v", err)
		}
	})
}

func TestPostgresDirect_AllCatalogPackagesUseDirectGate(t *testing.T) {
	root := repoRoot(t)
	cat := dbparity.DefaultCatalog()

	for _, comp := range cat.Components {
		for _, pkg := range comp.TestPackages {
			absPkg := filepath.Join(root, filepath.FromSlash(pkg))
			entries, err := os.ReadDir(absPkg)
			if err != nil {
				t.Fatalf("read dir %s: %v", absPkg, err)
			}
			fset := token.NewFileSet()
			var files []*ast.File
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				fpath := filepath.Join(absPkg, entry.Name())
				f, err := parser.ParseFile(fset, fpath, nil, 0)
				if err != nil {
					t.Fatalf("parse %s: %v", fpath, err)
				}
				files = append(files, f)
			}
			if err := validatePostgresDirectWrapperAST(files); err != nil {
				t.Errorf("component %q package %q direct wrapper AST validation failed: %v", comp.ID, pkg, err)
			}
		}
	}
}

func TestPlan_PostgresDirect_MixedCaseBaseEnv(t *testing.T) {
	t.Run("lowercase_runtime_dsn", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"aip_test_postgres_dsn=postgres://user:pass@localhost:5432/db"},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("expected lowercase aip_test_postgres_dsn to pass preflight, got error: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected plans to be generated for lowercase aip_test_postgres_dsn")
		}
	})

	t.Run("mixed_case_admin_dsn_injects_runtime", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"Aip_Test_Postgres_Admin_Dsn=postgres://admin:pass@localhost:5432/admin"},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("expected mixed-case Aip_Test_Postgres_Admin_Dsn to pass preflight, got error: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected plans to be generated")
		}
		if !slices.Contains(plans[0].Env, "AIP_TEST_POSTGRES_DSN=postgres://admin:pass@localhost:5432/admin") {
			t.Errorf("expected plans[0].Env to contain injected runtime DSN from mixed-case admin DSN, got: %v", plans[0].Env)
		}
	})

	t.Run("mixed_case_legacy_managed_dsn", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{"aip_managed_postgres_dsn=postgres://legacy:pass@localhost:5432/legacy"},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("expected mixed-case aip_managed_postgres_dsn to pass preflight, got error: %v", err)
		}
		if len(plans) == 0 {
			t.Fatal("expected plans to be generated")
		}
	})
}

func TestPlan_PostgresDirect_DuplicateAliasesAndPrecedence(t *testing.T) {
	t.Run("duplicate_aliases_latest_wins", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{
				"AIP_TEST_POSTGRES_DSN=postgres://first:pass@localhost:5432/first",
				"aip_test_postgres_dsn=postgres://second:pass@localhost:5432/second",
			},
		}
		plans, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err != nil {
			t.Fatalf("unexpected Plan error: %v", err)
		}
		cmd := plans[0].Cmd(context.Background(), opts.BaseEnv)
		count := 0
		for _, envVar := range cmd.Env {
			if strings.HasPrefix(strings.ToUpper(envVar), "AIP_TEST_POSTGRES_DSN=") {
				count++
				if envVar != "AIP_TEST_POSTGRES_DSN=postgres://second:pass@localhost:5432/second" {
					t.Errorf("expected effective value to be second DSN with canonical key, got: %s", envVar)
				}
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 AIP_TEST_POSTGRES_DSN in cmd.Env, got %d", count)
		}
	})

	t.Run("duplicate_aliases_latest_cleared_fails_preflight", func(t *testing.T) {
		opts := dbparity.PlanOptions{
			BaseEnv: []string{
				"aip_test_postgres_dsn=postgres://first:secret_password@localhost:5432/first",
				"AIP_TEST_POSTGRES_DSN=",
			},
		}
		_, err := dbparity.Plan(dbparity.ModePostgresDirect, opts)
		if err == nil {
			t.Fatal("expected Plan to fail when latest duplicate alias is empty")
		}
		if strings.Contains(err.Error(), "secret_password") {
			t.Errorf("preflight error leaked DSN credentials: %s", err.Error())
		}
	})
}

func allModeOpts(runner func(*exec.Cmd) error) dbparity.PlanOptions {
	return dbparity.PlanOptions{
		BaseEnv:   []string{dbparity.EnvTestPostgresDSN + "=postgres://u:p@localhost/db"},
		CmdRunner: runner,
	}
}

func cmdBackend(cmd *exec.Cmd) string {
	if slices.Contains(cmd.Args, "-tags=integration") {
		return "postgres-direct"
	}
	return "sqlite"
}

func TestRun_AllMode_RunsBackendsConcurrently(t *testing.T) {
	var stdout, stderr strings.Builder
	started := map[string]chan struct{}{"sqlite": make(chan struct{}), "postgres-direct": make(chan struct{})}
	var once sync.Map

	err := dbparity.Run(context.Background(), dbparity.ModeAll, allModeOpts(func(cmd *exec.Cmd) error {
		self := cmdBackend(cmd)
		if _, loaded := once.LoadOrStore(self, true); !loaded {
			close(started[self])
		}
		other := "sqlite"
		if self == other {
			other = "postgres-direct"
		}
		select {
		case <-started[other]:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("the other backend never started: backends ran sequentially")
		}
	}), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run(ModeAll) = %v, want both backends running at the same time", err)
	}
}

func TestRun_AllMode_KeepsPerBackendOrderAndCoversEveryPlan(t *testing.T) {
	var stdout, stderr strings.Builder
	var mu sync.Mutex
	got := map[string][]string{}

	err := dbparity.Run(context.Background(), dbparity.ModeAll, allModeOpts(func(cmd *exec.Cmd) error {
		mu.Lock()
		defer mu.Unlock()
		backend := cmdBackend(cmd)
		got[backend] = append(got[backend], cmd.Args[len(cmd.Args)-1])
		return nil
	}), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run(ModeAll) = %v", err)
	}

	plans, err := dbparity.Plan(dbparity.ModeAll, allModeOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	for _, p := range plans {
		want[p.Backend] = append(want[p.Backend], p.Args[len(p.Args)-1])
	}
	if !maps.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("executed commands per backend = %v, want %v", got, want)
	}
}

func TestRun_AllMode_FailureStopsTheOtherBackendAndIsReported(t *testing.T) {
	var stdout, stderr strings.Builder
	stubErr := &stubExitError{code: 7}
	failed := make(chan struct{})
	sqliteStarted := make(chan struct{})
	var startOnce sync.Once
	var sqliteRuns atomic.Int32

	err := dbparity.Run(context.Background(), dbparity.ModeAll, allModeOpts(func(cmd *exec.Cmd) error {
		if cmdBackend(cmd) == "postgres-direct" {
			<-sqliteStarted
			close(failed)
			return stubErr
		}
		sqliteRuns.Add(1)
		startOnce.Do(func() { close(sqliteStarted) })
		select {
		case <-failed:
			// Give the failure time to cancel the group before this command
			// returns, so the next sqlite command must not start.
			time.Sleep(100 * time.Millisecond)
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("postgres never ran")
		}
	}), &stdout, &stderr)

	var stepErr *dbparity.RunStepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("Run(ModeAll) = %T (%v), want *RunStepError", err, err)
	}
	if stepErr.Backend != "postgres-direct" {
		t.Errorf("reported backend = %q, want the failing postgres-direct", stepErr.Backend)
	}
	if got := dbparity.MapExitStatus(err); got != 7 {
		t.Errorf("exit status = %d, want 7", got)
	}
	if n := sqliteRuns.Load(); n != 1 {
		t.Errorf("sqlite commands started = %d, want 1: the failure must stop the other backend", n)
	}
}

func TestRun_AllMode_CallerCancellationIsReported(t *testing.T) {
	var stdout, stderr strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := dbparity.Run(ctx, dbparity.ModeAll, allModeOpts(func(*exec.Cmd) error { return nil }), &stdout, &stderr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(ModeAll) with a canceled context = %v, want context.Canceled", err)
	}
}

func TestRun_AllMode_CommandOutputIsNotInterleaved(t *testing.T) {
	var stdout, stderr strings.Builder
	const perCommand = 50
	line := strings.Repeat("x", 1000)

	err := dbparity.Run(context.Background(), dbparity.ModeAll, allModeOpts(func(cmd *exec.Cmd) error {
		marker := cmdBackend(cmd)[:1]
		for range perCommand {
			_, _ = fmt.Fprintln(cmd.Stdout, marker+line)
		}
		return nil
	}), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines)%perCommand != 0 {
		t.Fatalf("got %d lines, want a multiple of %d", len(lines), perCommand)
	}
	for i := 0; i < len(lines); i += perCommand {
		for _, l := range lines[i : i+perCommand] {
			if l[0] != lines[i][0] {
				t.Fatalf("one command's output was interleaved with another's at line %d", i)
			}
		}
	}
}
