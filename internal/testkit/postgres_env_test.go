package testkit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPostgresDSNEnvContract(t *testing.T) {
	cases := []struct {
		name              string
		admin             string
		runtime           string
		legacy            string
		requirePooler     string
		runtimeIsPooler   string
		wantAdminOK       bool
		wantAdmin         string
		wantRuntimeOK     bool
		wantRuntime       string
		wantLookupErr     bool
		wantFailClosed    bool
		errMustContain    string
		errMustNotContain string
	}{
		{
			name:          "both_missing",
			wantLookupErr: true, errMustContain: AIPTestPostgresAdminDSN, errMustNotContain: "postgres://",
		},
		{
			name: "missing_admin", runtime: "postgres://runtime/x", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			name: "missing_runtime", admin: "postgres://admin/x",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantLookupErr: true, errMustContain: AIPTestPostgresDSN, errMustNotContain: "postgres://",
		},
		{
			name: "both_present_with_attestation", admin: " postgres://admin/x ", runtime: " postgres://runtime/x ", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			// Ambient direct DSNs (make qa / local developer env) must not enter
			// pooled-only helpers without explicit topology attestation.
			name:  "ambient_dsns_without_attestation_skips_not_fail_closed",
			admin: "postgres://admin/x", runtime: "postgres://runtime/x",
			wantAdminOK: true, wantAdmin: "postgres://admin/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantLookupErr: true, wantFailClosed: false, errMustContain: AIPTestPostgresRuntimeIsPooler,
		},
		{
			name: "runtime_prefers_new_over_legacy", runtime: "postgres://a/a", legacy: "postgres://b/b", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://a/a",
			wantRuntimeOK: true, wantRuntime: "postgres://a/a",
		},
		{
			name: "legacy_runtime_alias", legacy: " postgres://legacy/x ", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://legacy/x",
			wantRuntimeOK: true, wantRuntime: "postgres://legacy/x",
		},
		{
			name: "admin_falls_back_to_preferred_runtime", runtime: "postgres://runtime/x", legacy: "postgres://legacy/x", runtimeIsPooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
		},
		{
			name: "require_pooler_without_attestation", admin: "", runtime: "postgres://runtime/x", requirePooler: "1",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantLookupErr: true, wantFailClosed: true, errMustContain: AIPTestPostgresRuntimeIsPooler,
		},
		{
			name: "require_pooler_with_attestation", admin: "", runtime: "postgres://runtime/x", requirePooler: "1", runtimeIsPooler: "true",
			wantAdminOK: true, wantAdmin: "postgres://runtime/x",
			wantRuntimeOK: true, wantRuntime: "postgres://runtime/x",
			wantFailClosed: true,
		},
		{
			name: "optional_gate_not_fail_closed", requirePooler: "",
			wantLookupErr: true, wantFailClosed: false,
			errMustContain: AIPTestPostgresAdminDSN,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(AIPTestPostgresAdminDSN, tc.admin)
			t.Setenv(AIPTestPostgresDSN, tc.runtime)
			t.Setenv(AIPManagedPostgresDSN, tc.legacy)
			t.Setenv(AIPRequirePostgresPooler, tc.requirePooler)
			t.Setenv(AIPTestPostgresRuntimeIsPooler, tc.runtimeIsPooler)

			admin, adminOK := PostgresAdminDSN()
			if adminOK != tc.wantAdminOK || admin != tc.wantAdmin {
				t.Fatalf("PostgresAdminDSN: ok=%v dsn=%q want ok=%v dsn=%q", adminOK, admin, tc.wantAdminOK, tc.wantAdmin)
			}
			runtime, runtimeOK := PostgresRuntimeDSN()
			if runtimeOK != tc.wantRuntimeOK || runtime != tc.wantRuntime {
				t.Fatalf("PostgresRuntimeDSN: ok=%v dsn=%q want ok=%v dsn=%q", runtimeOK, runtime, tc.wantRuntimeOK, tc.wantRuntime)
			}
			gate := EvaluateDualPlanePostgresGate()
			if tc.wantLookupErr {
				if gate.Err == nil {
					t.Fatal("expected lookup error")
				}
				if tc.errMustContain != "" && !strings.Contains(gate.Err.Error(), tc.errMustContain) {
					t.Fatalf("error %v must contain %q", gate.Err, tc.errMustContain)
				}
				if tc.errMustNotContain != "" && strings.Contains(gate.Err.Error(), tc.errMustNotContain) {
					t.Fatalf("error must not contain %q: %v", tc.errMustNotContain, gate.Err)
				}
			} else if gate.Err != nil {
				t.Fatalf("unexpected lookup error: %v", gate.Err)
			}
			if gate.FailClosed != tc.wantFailClosed {
				t.Fatalf("FailClosed=%v want %v", gate.FailClosed, tc.wantFailClosed)
			}
		})
	}
}

func TestPostgresRequired_truthy(t *testing.T) {
	t.Setenv(AIPRequirePostgres, "")
	if PostgresRequired() {
		t.Fatal("empty should not require")
	}
	t.Setenv(AIPRequirePostgres, "1")
	if !PostgresRequired() {
		t.Fatal("1 should require")
	}
}

func TestPostgresPoolerRequired_truthy(t *testing.T) {
	t.Setenv(AIPRequirePostgresPooler, "")
	if PostgresPoolerRequired() {
		t.Fatal("empty should not require")
	}
	t.Setenv(AIPRequirePostgresPooler, "true")
	if !PostgresPoolerRequired() {
		t.Fatal("true should require")
	}
}

func TestLookupDualPlanePostgresDSNs_bothPresent(t *testing.T) {
	t.Setenv(AIPTestPostgresAdminDSN, "postgres://admin/x")
	t.Setenv(AIPTestPostgresDSN, "postgres://runtime/x")
	t.Setenv(AIPManagedPostgresDSN, "")
	admin, runtime, err := LookupDualPlanePostgresDSNs()
	if err != nil {
		t.Fatalf("LookupDualPlanePostgresDSNs: %v", err)
	}
	if admin != "postgres://admin/x" || runtime != "postgres://runtime/x" {
		t.Fatalf("admin=%q runtime=%q", admin, runtime)
	}
}

func TestSkipUnlessPostgresContract(t *testing.T) {
	t.Run("ambient_dsn_without_require_skips", func(t *testing.T) {
		t.Setenv(AIPTestPostgresDSN, "postgres://unreachable:5432/db")
		t.Setenv(AIPManagedPostgresDSN, "")
		t.Setenv(AIPRequirePostgres, "")
		t.Run("sub", func(st *testing.T) {
			SkipUnlessPostgres(st)
			st.Error("should have skipped")
		})
	})

	t.Run("no_require_no_dsn_skips", func(t *testing.T) {
		t.Setenv(AIPTestPostgresDSN, "")
		t.Setenv(AIPManagedPostgresDSN, "")
		t.Setenv(AIPRequirePostgres, "")
		t.Run("sub", func(st *testing.T) {
			SkipUnlessPostgres(st)
			st.Error("should have skipped")
		})
	})

	t.Run("require_1_with_dsn_returns_dsn", func(t *testing.T) {
		expectedDSN := "postgres://user:pass@localhost:5432/db"
		t.Setenv(AIPTestPostgresDSN, expectedDSN)
		t.Setenv(AIPManagedPostgresDSN, "")
		t.Setenv(AIPRequirePostgres, "1")
		dsn := SkipUnlessPostgres(t)
		if dsn != expectedDSN {
			t.Fatalf("expected DSN %q, got %q", expectedDSN, dsn)
		}
	})

	t.Run("require_1_with_legacy_dsn_returns_dsn", func(t *testing.T) {
		expectedDSN := "postgres://legacy:pass@localhost:5432/legacydb"
		t.Setenv(AIPTestPostgresDSN, "")
		t.Setenv(AIPManagedPostgresDSN, expectedDSN)
		t.Setenv(AIPRequirePostgres, "1")
		dsn := SkipUnlessPostgres(t)
		if dsn != expectedDSN {
			t.Fatalf("expected DSN %q, got %q", expectedDSN, dsn)
		}
	})

	t.Run("require_1_without_dsn_contract", func(t *testing.T) {
		t.Setenv(AIPTestPostgresDSN, "")
		t.Setenv(AIPManagedPostgresDSN, "")
		t.Setenv(AIPRequirePostgres, "1")
		if !PostgresRequired() {
			t.Fatal("expected PostgresRequired() to be true")
		}
		dsn, ok := PostgresTestDSN()
		if ok || dsn != "" {
			t.Fatalf("expected PostgresTestDSN() to return ok=false, got dsn=%q ok=%v", dsn, ok)
		}
	})
}

// TestHelperSkipUnlessPostgresProcess is invoked in a subprocess to test fatal/skip execution paths.
func TestHelperSkipUnlessPostgresProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	SkipUnlessPostgres(t)
}

func testHelperSubprocessEnv(custom ...string) []string {
	var env []string
	for _, k := range []string{"SYSTEMROOT", "SystemRoot", "WINDIR", "PATH", "TMP", "TEMP"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "GO_WANT_HELPER_PROCESS=1")
	env = append(env, custom...)
	return env
}

func TestSkipUnlessPostgres_Subprocess(t *testing.T) {
	t.Run("mandatory_mode_missing_dsn_fails_actionably", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSkipUnlessPostgresProcess$", "-test.v")
		cmd.Env = testHelperSubprocessEnv(
			AIPRequirePostgres+"=1",
			AIPTestPostgresDSN+"=",
			AIPManagedPostgresDSN+"=",
			AIPTestPostgresAdminDSN+"=",
		)
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if err == nil {
			t.Fatalf("expected subprocess to fail (non-zero exit) when AIP_REQUIRE_POSTGRES=1 without DSN, got success:\n%s", outStr)
		}
		if strings.Contains(outStr, "--- SKIP:") {
			t.Fatalf("mandatory mode must FAIL, but output indicates test was SKIPPED:\n%s", outStr)
		}
		if !strings.Contains(outStr, "--- FAIL:") {
			t.Errorf("expected output to contain '--- FAIL:', got:\n%s", outStr)
		}
		if !strings.Contains(outStr, "PostgreSQL DSN is required: set AIP_TEST_POSTGRES_DSN or AIP_MANAGED_POSTGRES_DSN") {
			t.Errorf("expected actionable failure message, got:\n%s", outStr)
		}
	})

	t.Run("optional_mode_missing_dsn_skips_successfully", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSkipUnlessPostgresProcess$", "-test.v")
		cmd.Env = testHelperSubprocessEnv(
			AIPRequirePostgres+"=",
			AIPTestPostgresDSN+"=",
			AIPManagedPostgresDSN+"=",
			AIPTestPostgresAdminDSN+"=",
		)
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if err != nil {
			t.Fatalf("expected subprocess to succeed (exit 0) on skip, got err: %v\nOutput:\n%s", err, outStr)
		}
		if !strings.Contains(outStr, "--- SKIP:") {
			t.Fatalf("expected test to be SKIPPED, got output:\n%s", outStr)
		}
		if strings.Contains(outStr, "--- FAIL:") {
			t.Fatalf("optional mode without DSN must not fail, got:\n%s", outStr)
		}
		if !strings.Contains(outStr, "set AIP_TEST_POSTGRES_DSN (or legacy AIP_MANAGED_POSTGRES_DSN) to run PostgreSQL integration test") {
			t.Errorf("expected skip message naming DSN vars, got:\n%s", outStr)
		}
	})

	t.Run("optional_mode_with_ambient_dsn_skips_successfully", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSkipUnlessPostgresProcess$", "-test.v")
		cmd.Env = testHelperSubprocessEnv(
			AIPRequirePostgres+"=",
			AIPTestPostgresDSN+"=postgres://user:pass@localhost:5432/db",
			AIPManagedPostgresDSN+"=",
		)
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if err != nil {
			t.Fatalf("expected subprocess to succeed (exit 0) on skip, got err: %v\nOutput:\n%s", err, outStr)
		}
		if !strings.Contains(outStr, "--- SKIP:") {
			t.Fatalf("expected test to be SKIPPED, got output:\n%s", outStr)
		}
		if strings.Contains(outStr, "--- FAIL:") {
			t.Fatalf("optional mode with ambient DSN must not fail, got:\n%s", outStr)
		}
		if !strings.Contains(outStr, "set AIP_REQUIRE_POSTGRES=1 and AIP_TEST_POSTGRES_DSN to run PostgreSQL integration test") {
			t.Errorf("expected skip message naming require var, got:\n%s", outStr)
		}
	})

	t.Run("mandatory_mode_with_dsn_passes_successfully", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSkipUnlessPostgresProcess$", "-test.v")
		cmd.Env = testHelperSubprocessEnv(
			AIPRequirePostgres+"=1",
			AIPTestPostgresDSN+"=postgres://user:pass@localhost:5432/db",
			AIPManagedPostgresDSN+"=",
		)
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		if err != nil {
			t.Fatalf("expected subprocess to pass (exit 0), got err: %v\nOutput:\n%s", err, outStr)
		}
		if !strings.Contains(outStr, "--- PASS:") {
			t.Fatalf("expected test to PASS, got output:\n%s", outStr)
		}
		if strings.Contains(outStr, "--- FAIL:") || strings.Contains(outStr, "--- SKIP:") {
			t.Fatalf("expected clean PASS without FAIL or SKIP, got:\n%s", outStr)
		}
	})
}
