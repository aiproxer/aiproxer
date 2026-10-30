.PHONY: help test test-cost test-fast test-unit billing-convergence-certify profile-only-check precommit-full test-precommit-extra test-postgres-migrations test-authority-postgres test-authority-postgres-direct test-authority-postgres-pooled qa-tests test-race test-fuzz test-reasoning-e2e-soak parity-checks parity-acp-plugin parity-cursorcliacp-plugin parity-cli-acp-plugins parity-openrouter-plugin parity-hosted-compatible-plugins parity-ollama-plugins parity-opencode-plugins parity-codex-plugins parity-local-compatible-plugins test-local-compatible-plugin-modules release-gates bench pgo-profile pgo-build node-independence quality-checks regex-hotpath-check arch-report qa vet lint vuln run hooks-install check-change-size backend-plugin-module-checks backend-plugin-absence-checks backend-plugin-security-checks backend-plugin-cross-platform-qa backend-plugin-release-gates-static backend-plugin-release-gates package-minimal package-full package-plugin-smoke docs-check knowledge-check example-config-check backend-plugin-example-check kiro-spec-check isolated-root-qa installed-plugin-smoke feature-stub-smoke verify-product tmp-clean test-openresponses-compliance test-openresponses-compliance-static

GO ?= go

# Explicit development scope, shared across Bash and PowerShell. No implicit
# full-suite fallback: agents must choose the package(s) they are iterating on.
PKGS ?=
MODULE ?= .
# Local shared VM budgets; CI and Windows retain their existing defaults.
ifneq ($(OS),Windows_NT)
AIP_LOCAL_AGENT_DEV := $(shell bash scripts/dev-cpu-defaults.sh --local)
endif
ifeq ($(AIP_LOCAL_AGENT_DEV),yes)
DEV_JOBS ?= 1
GOMAXPROCS ?= 2
AIP_TEST_PACKAGES ?= 1
AIP_TEST_PARALLEL ?= 2
export GOMAXPROCS
endif
DEV_JOBS ?= 4
DEV_REPEAT ?= 1
DEV_TIMEOUT ?= 30m
DEV_OUTPUT ?= stream
BENCH_BASELINE ?=
BENCH_CANDIDATE ?=
BENCH_PATTERN ?=
BENCH_FIXTURES ?=
BENCH_OUT ?=
BENCH_SAMPLES ?= 10
BENCH_TIME ?= 1s
DEV_BASE ?=
DEV_PLAN ?= 0
DEV_FULL ?= 0
DEV_FRESH ?= 0
# Optional verification manifest. Set DEV_EVIDENCE=<path> to record the revision,
# dirty tree, scope, per-command results and per-step logs of a dev check.
DEV_EVIDENCE ?=
DEV_HEAD ?=
DEV_CONSUMER ?=

.PHONY: dev-test dev-test-changed dev-contract-check dev-delivery-plan dev-build dev-lint dev-doctor dev-setup dev-setup-check
SETUP_APPLY ?= 0
DOCTOR_POSTGRES ?= 0
dev-setup:
	bash scripts/dev-setup.sh $(if $(filter 1,$(SETUP_APPLY)),--apply,)
dev-setup-check:
	bash scripts/test-dev-setup.sh
dev-test:
	$(GO) run -buildvcs=false ./tools/devcheck -task=test -module="$(MODULE)" -packages="$(PKGS)" -jobs=$(DEV_JOBS) -repeat=$(DEV_REPEAT) -timeout="$(DEV_TIMEOUT)" -output="$(DEV_OUTPUT)" -evidence="$(DEV_EVIDENCE)"
dev-test-changed:
	$(GO) run -buildvcs=false ./tools/devcheck -task=test -scope=changed -module="$(MODULE)" -packages="$(PKGS)" -base="$(DEV_BASE)" -plan=$(DEV_PLAN) -full=$(DEV_FULL) -fresh=$(DEV_FRESH) -jobs=$(DEV_JOBS) -repeat=$(DEV_REPEAT) -timeout="$(DEV_TIMEOUT)" -output="$(DEV_OUTPUT)" -evidence="$(DEV_EVIDENCE)"
dev-contract-check:
	$(GO) run -buildvcs=false ./tools/devcheck -task=contracts -scope=changed -module="$(MODULE)" -packages="$(PKGS)" -base="$(DEV_BASE)" -plan=$(DEV_PLAN) -full=$(DEV_FULL) -fresh=$(DEV_FRESH) -jobs=$(DEV_JOBS) -repeat=$(DEV_REPEAT) -timeout="$(DEV_TIMEOUT)" -output="$(DEV_OUTPUT)" -evidence="$(DEV_EVIDENCE)"
dev-delivery-plan:
	@$(GO) run -buildvcs=false ./tools/devcheck -task=delivery -base="$(DEV_BASE)" -head="$(DEV_HEAD)" -consumer="$(DEV_CONSUMER)" -timeout="$(DEV_TIMEOUT)" -evidence="$(DEV_EVIDENCE)"
dev-build:
	$(GO) run -buildvcs=false ./tools/devcheck -task=build -module="$(MODULE)" -packages="$(PKGS)" -jobs=$(DEV_JOBS) -repeat=$(DEV_REPEAT) -timeout="$(DEV_TIMEOUT)" -output="$(DEV_OUTPUT)" -evidence="$(DEV_EVIDENCE)"
dev-lint:
	$(GO) run -buildvcs=false ./tools/devcheck -task=lint -module="$(MODULE)" -packages="$(PKGS)" -jobs=$(DEV_JOBS) -repeat=$(DEV_REPEAT) -timeout="$(DEV_TIMEOUT)" -output="$(DEV_OUTPUT)" -evidence="$(DEV_EVIDENCE)"
dev-doctor:
ifneq ($(OS),Windows_NT)
	bash scripts/dev-setup.sh --doctor $(if $(filter 1,$(DOCTOR_POSTGRES)),--postgres,)
endif
	$(GO) run -buildvcs=false ./tools/devcheck -task=doctor -module="$(MODULE)" -timeout="$(DEV_TIMEOUT)" -evidence="$(DEV_EVIDENCE)"

# Bounded product smoke: build aipstd once and drive real client journeys
# through it under one 600-second command budget. The outer supervisor includes
# the `go run` compilation; it passes the absolute deadline to devcheck, which
# uses the remaining time and reports before the supervisor's hard stop.
# PRODUCT_SMOKE_EVIDENCE names the directory that receives the per-journey
# artifacts and summary.json; the default is a fresh run directory under the dev
# temp root, outside the repo.
PRODUCT_SMOKE_EVIDENCE ?=
ifeq ($(OS),Windows_NT)
verify-product:
	@powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File scripts/verify-product.ps1 -Go "$(GO)" -Evidence "$(PRODUCT_SMOKE_EVIDENCE)"
else
verify-product:
	@bash scripts/verify-product.sh 600 "$(PRODUCT_SMOKE_EVIDENCE)" $(GO) run -buildvcs=false ./tools/devcheck -task=product-smoke -evidence="$(PRODUCT_SMOKE_EVIDENCE)"
endif

.PHONY: bench-compare
bench-compare:
	$(GO) run -buildvcs=false ./tools/benchcompare -baseline="$(BENCH_BASELINE)" -candidate="$(BENCH_CANDIDATE)" -module="$(MODULE)" -packages="$(PKGS)" -bench="$(BENCH_PATTERN)" -fixtures="$(BENCH_FIXTURES)" -out="$(BENCH_OUT)" -samples=$(BENCH_SAMPLES) -benchtime="$(BENCH_TIME)"

# Dry by default; cleanup is a deliberate operation, never an iteration step.
.PHONY: dev-cache-maintenance
dev-cache-maintenance:
	bash scripts/dev-cache-maintenance.sh $(if $(filter 1,$(CACHE_APPLY)),--apply,)

# Create a task worktree at the validated location. WORKTREE_BRANCH takes a
# branch name; the directory is derived by folding slashes to dashes, matching
# the existing layout. SETUP=1 additionally runs the repository's doctor.
WORKTREE_BRANCH ?=
WORKTREE_BASE ?= origin/main
WORKTREE_APPLY ?= 0
WORKTREE_PATH ?=
WORKTREE_PR ?=
WORKTREE_DISCARD_CODEGRAPH ?= 0
.PHONY: worktree-create
worktree-create:
	@test -n '$(WORKTREE_BRANCH)' || { echo 'worktree-create: set WORKTREE_BRANCH=<name>' >&2; exit 2; }
	bash scripts/worktree-create.sh "$(WORKTREE_BRANCH)" --base "$(WORKTREE_BASE)" $(if $(filter 1,$(WORKTREE_APPLY)),--apply,) $(if $(filter 1,$(SETUP)),--setup,)

.PHONY: worktree-cleanup
worktree-cleanup:
	$(GO) run -buildvcs=false ./tools/worktreeclean -path="$(WORKTREE_PATH)" -branch="$(WORKTREE_BRANCH)" -pr=$(WORKTREE_PR) $(if $(filter 1,$(WORKTREE_APPLY)),-apply,) $(if $(filter 1,$(WORKTREE_DISCARD_CODEGRAPH)),-discard-codegraph,) $(if $(filter 1,$(WORKTREE_ALLOW_UNCERTAIN_USERS)),-allow-uncertain-users,)

.PHONY: worktree-status
worktree-status:
	$(GO) run ./tools/worktreestatus -base="$(WORKTREE_BASE)"

# Read-only delivery status for one PR. Exit codes are the contract:
# 0 ready, 1 failed, 2 usage, 3 blocked (pending/draft/base unmerged/branch
# gone), 4 stale (a green verdict whose revision has since moved).
PR ?=
.PHONY: pr-status pr-watch
pr-status:
	bash scripts/pr-status.sh status "$(PR)"

pr-watch:
	bash scripts/pr-status.sh watch "$(PR)"

# Test parallelism defaults to the machine's logical core count. The previous
# fixed -parallel=8 left >=8-core dev boxes half idle for t.Parallel-heavy
# suites (measured ~2.3x faster on a 16-core box). Override with
# AIP_TEST_PARALLEL=<n>, or replace the whole flag string via GO_TEST_FLAGS.
ifeq ($(OS),Windows_NT)
AIP_TEST_PARALLEL ?= $(if $(NUMBER_OF_PROCESSORS),$(NUMBER_OF_PROCESSORS),8)
else
AIP_TEST_PARALLEL ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
endif
# Package processes and parallel tests are separate budgets. Leave package
# concurrency at Go's native default unless measurements justify an override.
AIP_TEST_PACKAGES ?=
GO_TEST_FLAGS ?= -parallel=$(strip $(AIP_TEST_PARALLEL)) -timeout=10m $(if $(strip $(AIP_TEST_PACKAGES)),-p=$(strip $(AIP_TEST_PACKAGES)))
export GO_TEST_FLAGS
export AIP_TEST_PACKAGES
export AIP_TEST_PARALLEL

# Tests listed in .github/test-quarantine.txt fail on main and are skipped until
# their linked issue is fixed. GOFLAGS carries the -skip so every go test run
# from make targets and the scripts they call honours it without shell quoting.
AIP_TEST_QUARANTINE := $(filter Test% Fuzz% Benchmark% Example%,$(file < .github/test-quarantine.txt))
ifneq ($(AIP_TEST_QUARANTINE),)
AIP_EMPTY :=
AIP_SPACE := $(AIP_EMPTY) $(AIP_EMPTY)
GOFLAGS += -skip=^($(subst $(AIP_SPACE),|,$(AIP_TEST_QUARANTINE)))$$
export GOFLAGS
endif

# The Windows test-cost ratchet is explicit and opt-in.  CI supplies the PR
# base SHA; local callers can override these values when comparing a known
# base or retaining artifacts outside the checkout.
TEST_COST_BASE_SHA ?=
TEST_COST_OUTPUT_ROOT ?=
TEST_COST_PARALLEL ?= 0

ifeq ($(OS),Windows_NT)
WINDOWS_TASK = powershell -NoProfile -ExecutionPolicy Bypass -File scripts/windows-task.ps1 -Target
endif

help:
	@echo "Targets:"
	@echo "  make dev-test/dev-build/dev-lint PKGS='./path/...' [MODULE=connectors/name] [DEV_JOBS=4] [DEV_REPEAT=2] - scoped, measured iteration"
	@echo "  make dev-test-changed [DEV_BASE=origin/main] [DEV_PLAN=1] [DEV_FULL=1] [DEV_FRESH=1] - local affected-package tests; CI remains comprehensive"
	@echo "  make dev-contract-check [DEV_BASE=<ref>] [DEV_PLAN=1] - selected existing contracts and direct-package lint"
	@echo "  make dev-delivery-plan [DEV_BASE=<ref>] [DEV_HEAD=<commit>] [DEV_CONSUMER='<consumer>'] - JSON slice metrics, not certification"
	@echo "  make dev-setup [SETUP_APPLY=1] - plan/apply canonical hooks, host policy, Go guard and ext4 scratch (POSIX)"
	@echo "  make dev-doctor [MODULE=.] [DOCTOR_POSTGRES=1] - prerequisites, instrumentation, storage, optional SQL readiness and Go/cache diagnostics"
	@echo "  make dev-test/dev-build/dev-lint [DEV_EVIDENCE=<path>] - additionally record revision, scope, results and per-step logs as a verification manifest"
	@echo "  make worktree-create WORKTREE_BRANCH=fix/short-description [WORKTREE_BASE=origin/main] [SETUP=1] - validated task worktree under <container>/worktrees/"
	@echo "  make worktree-status [WORKTREE_BASE=origin/main] - read-only overview of registered worktrees: branch, HEAD, dirtiness, ahead/behind, HEAD date"
	@echo "  make pr-status PR=<n> - read-only PR delivery status: head/base SHAs, mergeability, checks, stacking, stale-evidence detection"
	@echo "  make pr-watch  PR=<n> - poll pr-status; stops on ready/failed/stale, or at once when the branch needs a base update (rebase)"
	@echo "  make quality-checks  - generate-feature-planes -check, gofmt, go mod tidy (no drift), go build, go vet, guard scripts, archtest; mod verify in CI or with AIP_VERIFY_MODULE_CACHE=1"
	@echo "  make profile-only-check [PROFILE_ONLY_BASE=<git-rev>] - fail-closed provider-profile change-surface ratchet"
	@echo "  make regex-hotpath-check - forbid regexp.MustCompile in frontends/runtime (see scripts/)"
	@echo "  make proto-check     - buf lint + breaking + generation freshness for api/backendplugin/v1"
	@echo "  make test            - quality-checks, full unit tests, and conformance parity checks"
	@echo "  make test-cost [TEST_COST_BASE_SHA=<git-rev>] [TEST_COST_OUTPUT_ROOT=<dir>] [TEST_COST_PARALLEL=<n>] - Windows-authoritative test-cost ratchet (opt-in; not part of make test)"
	@echo "  make test-fast       - quality-checks then tests for staged packages (or all)"
	@echo "  make test-quick      - single one-pass go test ./... for inner-loop feedback (no parity re-tags)"
	@echo "  make test-live-connectors - optional live provider probes (needs COMMANDCODE_API_KEY and provider quota)"
	@echo "  make precommit-full  - run the optional full local lint + vulnerability scan before commit"
	@echo "  make test-unit       - go test $(GO_TEST_FLAGS) ./... (excludes //go:build precommit tests)"
	@echo "  make test-billing-schema - complete exhaustive schema and maximum-depth replay certification"
	@echo "  make billing-convergence-certify - fail-fast final billing architecture, integration, quality, docs, and race certification"
	@echo "  make test-db-parity-sqlite - canonical SQLite database parity tests across all registered components"
	@echo "  make test-db-parity-postgres-direct - repository-wide fail-closed direct PostgreSQL parity (DSN; Make sets AIP_REQUIRE_POSTGRES=1)"
	@echo "  make test-db-parity  - repository-wide SQLite and direct PostgreSQL parity gate (the two backends run concurrently)"
	@echo "  make test-postgres-migrations - apply and verify dual-plane PostgreSQL migrations"
	@echo "  PostgreSQL gates are intentional opt-in: make test-authority-postgres-direct needs only a configured DSN (Make sets AIP_REQUIRE_POSTGRES=1); pooled/aggregate proof also requires AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1"
	@echo "  make test-authority-postgres-direct - direct PostgreSQL runtime proof (DSN; Make sets require flag)"
	@echo "  make test-authority-postgres-pooled - transaction-pooled runtime proof (requires AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1)"
	@echo "  make test-authority-postgres - aggregate direct + pooled proof (pooled attestation required)"
	@echo "  make test-precommit-extra - hygiene + executor matrices (-tags=precommit; also in pre-commit hook + CI)"
	@echo "  make test-race       - race scan via scripts/race-check.sh (skipped on Windows and on development hosts, even with --strict; runs on ci-stand-in hosts, see docs/development-iteration.md)"
	@echo "  make test-fuzz       - short fuzz smoke (FUZZTIME=500ms locally; nightly CI uses 2s per target in .github/workflows/race-fuzz-nightly.yml)"
	@echo "  make test-reasoning-e2e-soak - opt-in reasoning preservation full-HTTP soak (sets AIP_REASONING_E2E_SOAK=1; not a PR/default gate; see docs/reasoning-output-preservation.md)"
	@echo "  make parity-checks   - TCK certifications, protocol-owned parity suites, and bounded integration evidence"
	@echo "  make release-gates   - conformance package + all critical fuzz targets (race is separate: test-race / CI; see docs/release-gates.md)"
	@echo "  make bench           - benchmarks (testkit, stream, core runtime/routing/diag/toolcallrepair, frontend encoders)"
	@echo "  make pgo-profile     - collect default.pgo from core benches (move under cmd/aipstd before build)"
	@echo "  make pgo-build       - build cmd/aipstd (uses cmd/aipstd/default.pgo when present)"
	@echo "  make qa              - quality-checks + one full test pass (-tags=precommit,integration) + lint + vuln + release-gates-static + OpenResponses compliance static gate"
	@echo "  make lint            - mandatory correctness lint (golangci-lint if installed, else staticcheck)"
	@echo "  make lint-advisory   - on-demand uncapped full lint incl. tagged files and advisory style checks (modernize, paralleltest, thelper)"
	@echo "  make lint-advisory-ratchet - only new advisory findings on changed lines vs LINT_BASE (default origin/main); fails on new findings"
	@echo "  make hooks-install   - canonical scripts/hooks pre-commit, commit-msg, and pre-push"
	@echo "  make check-change-size - reject staged changes over 100 modified Go files (AIP_ALLOW_LARGE_CHANGE=1 to override)"
	@echo "  make kiro-spec-check SPEC=<name> - validate a Kiro spec development gate"
	@echo "  make isolated-root-qa - GOWORK=off QA on a temp root copy without connectors/support/Node/artifacts"
	@echo "  make node-independence - Linux: host build/unit/quality/package/CLI/smoke with every Node entry point masked out"
	@echo "  make installed-plugin-smoke - one aipstd binary; install release artifacts; same-binary inspect/doctor/invoke"
	@echo "  make feature-stub-smoke [SCENARIO=tool-name-correction] - spawned-distribution feature smoke against a scripted stub backend"
	@echo "  make verify-product  - bounded product smoke: build aipstd once, run every required client journey, print per-journey verdicts"
	@echo "  make docs-check      - backend-plugin and extension-authoring documentation tests"
	@echo "  make knowledge-check - steering/ADR hybrid consistency"
	@echo "  make example-config-check - operator/example YAML + config/examples bootstrap inspect"
	@echo "  make backend-plugin-security-checks - executable-plugin threat-model adversarial + bounded fuzz"
	@echo "  make backend-plugin-cross-platform-qa - connector platform matrix compile/package + native lifecycle gates"
	@echo "  make backend-plugin-release-gates-static - release report/traceability/arch wiring (used by make qa)"
	@echo "  make backend-plugin-release-gates - full connector/support module matrix + root release suites"
	@echo "  make run             - go run ./cmd/aipstd"
	@echo "  make tmp-clean       - dry-run scan of stale project-owned TEMP residue (Windows; TMP_CLEAN_APPLY=1 or scripts/tmp-clean.ps1 -Apply to delete)"

# Validate a provider-profile-only change surface against a Git base. This is
# explicit rather than part of every quality run because the normal branch may
# intentionally contain core/ABI/composition work alongside profile work.
PROFILE_ONLY_BASE ?=
profile-only-check:
ifeq ($(strip $(PROFILE_ONLY_BASE)),)
	$(GO) run ./internal/archtest/tools/changesurface/cmd -profile-only -json
else
	$(GO) run ./internal/archtest/tools/changesurface/cmd -base "$(PROFILE_ONLY_BASE)" -profile-only -json
endif

quality-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/quality-checks.ps1
else
	@bash scripts/quality-checks.sh
endif

# Advisory architecture metrics report (non-failing). Run on demand to spot
# hotspot/line/import drift.
arch-report:
	@$(GO) run ./scripts/arch-report.go

regex-hotpath-check:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/regex-hotpath-check.ps1
else
	@bash scripts/regex-hotpath-check.sh
endif

proto-check:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/proto-check.ps1
else
	@bash scripts/proto-check.sh
endif

quality-checks-fast: export AIP_SKIP_GO_COMPILE_CHECKS=1
quality-checks-fast: export AIP_SKIP_ARCHTEST=1
quality-checks-fast:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/quality-checks.ps1
else
	@bash scripts/quality-checks.sh
endif

# The ratchet's PowerShell orchestrator is the Windows-authoritative
# comparison of the policy targets.  Fail closed on other hosts so a local
# POSIX invocation cannot masquerade as authoritative evidence.
test-cost:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-cost-ratchet.ps1 -BaseSHA "$(TEST_COST_BASE_SHA)" -OutputRoot "$(TEST_COST_OUTPUT_ROOT)" -Parallel "$(TEST_COST_PARALLEL)"
else
	@echo "make test-cost is Windows-only; Windows is authoritative for this ratchet." >&2
	@exit 1
endif

test: quality-checks-fast test-unit parity-checks

test-fast: quality-checks-fast
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-staged.ps1
else
	@bash scripts/test-staged.sh
endif

precommit-full:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/precommit-full.ps1
else
	@AIP_PRECOMMIT_FULL=1 bash scripts/quality-gate.sh
endif

billing-convergence-certify:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/billing-convergence-certify.ps1
else
	@bash scripts/billing-convergence-certify.sh
endif

# Exhaustive billing schema certification preserves every generated case and seam.
# It is separate from the default unit budget and remains part of integration QA.
BILLING_SCHEMA_TIMEOUT ?= 30m

# qa-tests runs the integration-tagged graph, which admits the exhaustive billing
# certification sweep (~636k production ratings, measured at ~90% of a 10m budget
# on an otherwise idle host). CI already isolates that sweep behind
# BILLING_SCHEMA_TIMEOUT via `make test-billing-schema`; the wide local gate must
# carry the same budget or it fails on cost, not on a defect. Only the -timeout
# value is replaced: every other flag, package list, and test selection stays
# unchanged.
QA_TESTS_GO_TEST_FLAGS = $(filter-out -timeout=%,$(GO_TEST_FLAGS)) -timeout=$(BILLING_SCHEMA_TIMEOUT)

.PHONY: test-billing-schema
test-billing-schema:
	$(GO) test -count=1 -timeout=$(BILLING_SCHEMA_TIMEOUT) -tags=integration -run '^(TestGeneratedSchema|TestSchemaModel(Structure|Commercial)Sweep|TestSchemaModelOrderInvariance|TestMetamorphic(PricingMetamorphism|StructuralVerdictAgreesWithModel)|TestSupportAgreementShadowPredicate|TestReplayDeepestPublishableChainTraversalIsBounded)' ./enterprise/internal/core/billing

test-unit:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) test-unit
else
	$(GO) test $(GO_TEST_FLAGS) ./...
endif

# Inner-loop fast path: exactly one untagged repository test run, without the
# parity/conformance re-tag passes `make test` adds. `make test-unit` has the
# same selection; this alias exists so the fast path is discoverable and
# documented. Use `make test` (comprehensive) or `make qa` (wide) before
# delivery.
test-quick: test-unit

# Canonical repository-wide database dialect parity targets.
# Delegated directly to the catalog-driven runner (no duplicate package lists).
.PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity
test-db-parity-sqlite:
	$(GO) run ./internal/testkit/dbparity/cmd sqlite

test-db-parity-postgres-direct:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES','1','Process'); if (-not [Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_MANAGED_POSTGRES_DSN','Process'),'Process') }; if (-not [Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' run ./internal/testkit/dbparity/cmd postgres-direct"
else
	@AIP_REQUIRE_POSTGRES=1 AIP_TEST_POSTGRES_DSN="$${AIP_TEST_POSTGRES_DSN:-$${AIP_MANAGED_POSTGRES_DSN:-$$AIP_TEST_POSTGRES_ADMIN_DSN}}" $(GO) run ./internal/testkit/dbparity/cmd postgres-direct
endif

test-db-parity:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES','1','Process'); if (-not [Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_MANAGED_POSTGRES_DSN','Process'),'Process') }; if (-not [Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' run ./internal/testkit/dbparity/cmd all"
else
	@AIP_REQUIRE_POSTGRES=1 AIP_TEST_POSTGRES_DSN="$${AIP_TEST_POSTGRES_DSN:-$${AIP_MANAGED_POSTGRES_DSN:-$$AIP_TEST_POSTGRES_ADMIN_DSN}}" $(GO) run ./internal/testkit/dbparity/cmd all
endif

# PostgreSQL is the required proof surface for cross-instance authority
# semantics. The test helper fails instead of skipping when this target is
# invoked without a configured DSN.
test-authority-postgres-direct:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES','1','Process'); if ([Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_ADMIN_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -skip 'Pooled' ./enterprise/internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore"
else
	@AIP_REQUIRE_POSTGRES=1 AIP_TEST_POSTGRES_DSN="$${AIP_TEST_POSTGRES_ADMIN_DSN:-$$AIP_TEST_POSTGRES_DSN}" $(GO) test $(GO_TEST_FLAGS) -tags=integration -skip 'Pooled' ./enterprise/internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore
endif

test-postgres-migrations:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "if (-not [Environment]::GetEnvironmentVariable('AIP_MIGRATION_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_MIGRATION_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; if (-not [Environment]::GetEnvironmentVariable('AIP_MIGRATION_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('AIP_MIGRATION_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_DSN','Process'),'Process') }; & '$(GO)' run ./cmd/aipstd migrate --components concurrency,metering"
else
	@AIP_MIGRATION_POSTGRES_DSN="$${AIP_MIGRATION_POSTGRES_DSN:-$${AIP_TEST_POSTGRES_ADMIN_DSN:-$$AIP_TEST_POSTGRES_DSN}}" $(GO) run ./cmd/aipstd migrate --components concurrency,metering
endif

# Transaction-pooled runtime proof. Requires AIP_TEST_POSTGRES_DSN to be an
# actual transaction-pooler endpoint and explicit topology attestation.
# Uses normal parallelism (-parallel=8 by default); do not force -parallel=1.
test-authority-postgres-pooled:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES_POOLER','1','Process'); if ([Environment]::GetEnvironmentVariable('AIP_TEST_POSTGRES_RUNTIME_IS_POOLER','Process') -ne '1') { throw 'set AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when AIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint' }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -run 'Pooled' ./enterprise/internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore ./internal/infra/runtimebundle"
else
	@test "$${AIP_TEST_POSTGRES_RUNTIME_IS_POOLER:-}" = "1" || { echo "set AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when AIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint" >&2; exit 1; }
	@AIP_REQUIRE_POSTGRES_POOLER=1 $(GO) test $(GO_TEST_FLAGS) -tags=integration -run 'Pooled' ./enterprise/internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore ./internal/infra/runtimebundle
endif

test-authority-postgres: test-postgres-migrations test-authority-postgres-direct test-authority-postgres-pooled

test-precommit-extra:
	$(GO) test $(GO_TEST_FLAGS) -tags=precommit ./internal/qa/... ./internal/core/runtime/...

test-race:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/race-check.ps1
else
	@bash scripts/race-check.sh
endif

# Short fuzz smoke (extend FUZZTIME locally, e.g. FUZZTIME=30s make test-fuzz)
# POSIX runs scripts/fuzz-smoke.sh: the canonical target list lives in
# scripts/fuzz-targets.tsv (shared with the Windows runner) and targets execute
# concurrently instead of paying per-target link/spawn cost serially.
FUZZTIME ?= 500ms
# Route each fuzz invocation through a wrapper that tolerates the Go fuzz
# engine's spurious "context deadline exceeded" at -fuzztime expiry
# (golang/go#75804, Go 1.25-1.26.x). On Windows `go test` runs directly: local
# fuzz smoke is short and a rare flake is cheap to re-run, and bash may be absent.
ifeq ($(OS),Windows_NT)
FUZZ_WRAPPER := $(GO) test
else
FUZZ_WRAPPER := bash "$(CURDIR)/scripts/fuzz-run.sh"
endif
# Opt-in reasoning-preservation full HTTP soak (1000├Ś100 default). Not part of
# make test / test-unit / qa / PR gates. Overrides: AIP_REASONING_E2E_SEEDS,
# AIP_REASONING_E2E_TURNS, AIP_REASONING_E2E_WORKERS. Single-seed replay:
# AIP_REASONING_E2E_MODE + AIP_REASONING_E2E_SEED.
REASONING_E2E_SOAK_TIMEOUT ?= 6h
test-reasoning-e2e-soak:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('AIP_REASONING_E2E_SOAK','1','Process'); & '$(GO)' test -parallel=8 -timeout=$(REASONING_E2E_SOAK_TIMEOUT) -tags=precommit -run '^TestReasoningPreservationHTTP_Soak$$' -count=1 ./internal/stdhttp/"
else
	@AIP_REASONING_E2E_SOAK=1 $(GO) test -parallel=8 -timeout=$(REASONING_E2E_SOAK_TIMEOUT) -tags=precommit -run '^TestReasoningPreservationHTTP_Soak$$' -count=1 ./internal/stdhttp/
endif

test-fuzz:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) test-fuzz
else
	@bash "$(CURDIR)/scripts/fuzz-smoke.sh"
endif

ifeq ($(OS),Windows_NT)
parity-checks:
	@$(WINDOWS_TASK) parity-checks
else
parity-checks: parity-tcks parity-protocol-suites parity-connectors parity-sentinel
endif

parity-tcks:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-tcks
else
	$(GO) test $(GO_TEST_FLAGS) ./internal/testkit/contract/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/providerprofiles/...
	$(GO) test $(GO_TEST_FLAGS) ./pkg/aipsdk/backendplugin/contracttest/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/testkit/compatibleparity/... -run 'CompatibleParity'
endif

parity-protocol-suites:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-protocol-suites
else
	$(GO) test $(GO_TEST_FLAGS) '-tags=precommit,integration' ./internal/testkit/conformance/...
endif

parity-connectors:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-connectors
else
	@$(MAKE) parity-acp-plugin
	@$(MAKE) parity-openrouter-plugin
	@$(MAKE) parity-hosted-compatible-plugins
endif

parity-sentinel:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-sentinel
else
	$(GO) test $(GO_TEST_FLAGS) '-tags=integration' ./internal/testkit/conformance -run '^TestBoundedSentinel'
endif

# Phase 6 ACP external connector parity (testemu/scripted ACP; not live Cursor/Gemini/Agy CLIs).
parity-acp-plugin:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-acp-plugin
else
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted' ./...
	cd connectors/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_' ./...
endif

parity-cursorcliacp-plugin:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-cursorcliacp-plugin
else
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile' ./...
	cd connectors/cursorcliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
endif

parity-cli-acp-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-cli-acp-plugins
else
	cd connectors/geminicliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
	cd connectors/claudeagentacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
	cd connectors/agycliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
	cd connectors/cursorcliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
endif

# Phase 7 OpenAI-compatible external connectors (deterministic emulators; not live providers).
parity-openrouter-plugin:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-openrouter-plugin
else
	cd connector-support/openaicompat && GOWORK=off $(GO) test $(GO_TEST_FLAGS) ./...
	cd connectors/openrouter && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestBilling_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'OpenRouter|Phase7_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle -run 'TestPhase7_OpenRouter'
endif

parity-hosted-compatible-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-hosted-compatible-plugins
else
	cd connectors/nvidia && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/huggingface && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Phase7_'
endif

parity-ollama-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-ollama-plugins
else
	cd connectors/ollama && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
endif

parity-opencode-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-opencode-plugins
else
	cd connectors/opencode && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'OpenCode|Phase8_'
endif

parity-codex-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-codex-plugins
else
	cd connectors/codex && GOWORK=off $(GO) test $(GO_TEST_FLAGS) ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Codex|Phase8_.*Codex|TestCodex_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle -run 'TestPhase8_Codex'
endif

test-local-compatible-plugin-modules:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) test-local-compatible-plugin-modules
else
	cd connectors/llamacpp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/lmstudio && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/vllm && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
endif

# The three local-compatible module tests run exactly once per parity
# invocation. On POSIX the prerequisite owns them and the recipe owns the Phase 7
# archtest; on Windows the windows-task.ps1 parity case owns both in one script
# call, so this prerequisite is guarded POSIX-only to avoid a double run.
ifneq ($(OS),Windows_NT)
parity-local-compatible-plugins: test-local-compatible-plugin-modules
endif
parity-local-compatible-plugins:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) parity-local-compatible-plugins
else
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Phase7_'
endif

backend-plugin-absence-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-absence-checks.ps1
else
	@bash scripts/backend-plugin-absence-checks.sh
endif

release-gates: parity-checks quality-checks
	$(GO) test $(GO_TEST_FLAGS) ./enterprise/internal/core/billing -run '^TestPhase7_DeterministicShadowRatingAcceptanceGate$$'
	@$(MAKE) test-fuzz

bench:
	$(GO) test -bench=. -benchmem -run=Benchmark ./internal/testkit/... ./internal/core/stream/... \
		./internal/core/securesession/... \
		./internal/core/runtime/... ./internal/core/routing/... ./internal/core/diag/... \
		./internal/plugins/features/toolcallrepair/... \
		./internal/infra/concurrencyauthority/leasestore/... \
		./internal/infra/metering/journalstore/... \
		./enterprise/internal/infra/usageauthority/authoritystore/... \
		./internal/plugins/frontends/openailegacy/... \
		./internal/plugins/frontends/gemini/... \
		./internal/plugins/frontends/openairesponses/... \
		./internal/plugins/frontends/anthropic/...

# Collect a CPU profile suitable for placing as cmd/aipstd/default.pgo, then rebuild with PGO.
# Requires a representative bench/workload; do not commit synthetic profiles by default.
pgo-profile:
	$(GO) test -cpuprofile=default.pgo -bench=. -benchtime=3s -run=^$$ ./internal/core/runtime/... ./internal/core/stream/...
	@echo "Move default.pgo next to the main package (e.g. cmd/aipstd/default.pgo) before building."

pgo-build:
	$(GO) build -o bin/aipstd ./cmd/aipstd

# Single test invocation matches CI (go test -tags=precommit,integration ./...) and avoids compiling twice.
# Static release-gate wiring only (no recursive full backend-plugin-release-gates / module matrix).
# The OpenResponses compliance static gate (Task 8.5) verifies the wiring and the
# release-ready evidence without re-running the huge tagged suites that qa-tests
# already covers; the full `test-openresponses-compliance` script remains the
# standalone Task 8.5 gate.
# The comprehensive lint target below owns lint once; the preliminary quality
# guards retain formatting, generation, module and repository-policy checks.
qa: export AIP_SKIP_LINT=1
qa: quality-checks-fast qa-tests lint vuln backend-plugin-release-gates-static test-openresponses-compliance-static

qa-tests:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) qa-tests
else
	@if [ "$$AIP_SKIP_QA_TESTS" = "1" ]; then \
		echo "Skipping duplicate root tests pass (AIP_SKIP_QA_TESTS=1); running tagged delta packages..."; \
		$(GO) test $(QA_TESTS_GO_TEST_FLAGS) -tags=precommit,integration ./internal/qa/... ./internal/core/runtime/... ./internal/stdhttp/... ./internal/testkit/conformance/... ./tools/backendplugin/... ./enterprise/internal/core/billing/...; \
	else \
		$(GO) test $(QA_TESTS_GO_TEST_FLAGS) -tags=precommit,integration ./...; \
	fi
endif

vet:
	$(GO) vet ./...

# Install golangci-lint (preferred) or staticcheck: https://golangci-lint.run/
lint:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) lint
else
	@bash scripts/lint-all-modules.sh
endif

# On-demand advisory style lint (modernize/paralleltest/thelper). The canonical
# `make lint`/`quality-checks`/`qa`/`precommit-full` gates enforce the mandatory
# correctness linters only (scripts/lint-all-modules.* pass
# --disable=modernize,paralleltest,thelper); this target reports the full set
# without treating repository-wide style debt as a release blocker. The report
# is uncapped and includes integration/precommit-tagged files.
lint-advisory:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/lint-all-modules.ps1 -Advisory
else
	@bash scripts/lint-all-modules.sh --advisory
endif

# Changed-lines ratchet for the advisory set: report only findings on lines
# changed since LINT_BASE and fail when a new one appears. Existing repository
# debt remains visible through `make lint-advisory`, never as a new blocker.
LINT_BASE ?= origin/main
lint-advisory-ratchet:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/lint-all-modules.ps1 -Advisory -Ratchet -Base "$(LINT_BASE)" -RatchetBase "$(LINT_BASE)"
else
	@bash scripts/lint-all-modules.sh --advisory --ratchet --base "$(LINT_BASE)" --ratchet-base "$(LINT_BASE)"
endif

vuln:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) vuln
else
	$(GO) tool govulncheck ./...
endif

run:
	$(GO) run ./cmd/aipstd --config ./config/config.yaml

# Conservative cleanup of stale project-owned temp residue left by hard-killed
# tests. Dry-run by default; pass TMP_CLEAN_APPLY=1 to delete. Never invoked
# automatically by tests or serve. See scripts/tmp-clean.ps1.
tmp-clean:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/tmp-clean.ps1 $(if $(TMP_CLEAN_APPLY),-Apply,)
else
	@echo "tmp-clean is Windows-only. On POSIX, review \$${TMPDIR:-/tmp} manually and remove only the allowlisted project prefixes listed in scripts/tmp-clean.ps1."
endif

hooks-install:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/install-hooks.ps1
else
	@bash scripts/install-hooks.sh
endif

check-change-size:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-change-size.ps1 --staged
else
	@bash scripts/check-change-size.sh --staged
endif


# Phase 5: structural connector module discovery + GOWORK=off isolation (no recursive make).
backend-plugin-module-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-module-checks.ps1
else
	@AIP_DISABLE_VCS_STAMPING=1 bash scripts/backend-plugin-module-checks.sh
endif

PACKAGE_DEST ?= $(CURDIR)/.aip-package-staging

package-minimal:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-plugins.ps1 -Profile minimal -Dest "$(PACKAGE_DEST)/minimal"
else
	@bash scripts/package-plugins.sh minimal "$(PACKAGE_DEST)/minimal"
endif

package-full:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-plugins.ps1 -Profile full -Dest "$(PACKAGE_DEST)/full"
else
	@bash scripts/package-plugins.sh full "$(PACKAGE_DEST)/full"
endif

package-plugin-smoke: package-minimal package-full
	@matches=$$($(GO) test $(GO_TEST_FLAGS) -tags=integration -list 'TestPackage_|TestDiscoverModules_' ./tools/backendplugin/ | awk '/^Test[A-Za-z0-9_]+$$/ { count++ } END { print count+0 }'); test "$$matches" -gt 0 || { echo "backend-plugin package selector matched zero tests" >&2; exit 1; }; $(GO) test $(GO_TEST_FLAGS) -tags=integration ./tools/backendplugin/ -run 'TestPackage_|TestDiscoverModules_'

docs-check:
	$(GO) test $(GO_TEST_FLAGS) ./docs/backend-plugins/ -run 'TestDocs|TestExample|TestOperator|TestExampleConfig|TestThreat'

# Phase 9.3: executable-plugin threat model adversarial suite + bounded fuzz.
# Pair with `make test-fuzz` and `make test-race` (Windows race is skip-only).
backend-plugin-security-checks:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) backend-plugin-security-checks
else
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/...
	$(GO) test $(GO_TEST_FLAGS) ./pkg/aipsdk/backendplugin/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/diagredact/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle/ -run 'TestBuild_localOnly|TestBuild_unknownBackendCredential|TestBuild_oauthUser|TestBuild_unsupportedBackend|TestBuild_staticBackend|TestBuild_noneBackend|TestBuild_strictAuthoritative'
	$(GO) test $(GO_TEST_FLAGS) ./docs/backend-plugins/ -run 'TestThreat|TestOperator_|TestDocs_'
	$(FUZZ_WRAPPER) -fuzz=^FuzzManifest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/infra/backendplugins/manifest
	$(FUZZ_WRAPPER) -fuzz=^FuzzServerFrame$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/aipsdk/backendplugin
endif

# Phase 9.4: structural connector/support discovery, claimed GOOS/GOARCH compile matrix,
# host secure-profile false-claim rejection, package matrix match, native lifecycle/IPC gates.
# Emits machine-readable unsupported pairs to .aip-crossplatform-matrix.json (gitignored).
backend-plugin-cross-platform-qa:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) backend-plugin-cross-platform-qa
else
	$(GO) run ./tools/backendplugin/crossplatform_qa -root . -out .aip-crossplatform-matrix.json -skip-native $(if $(filter 1,$(CROSS_PLATFORM_SKIP_COMPILE)),-skip-compile,) $(if $(strip $(CROSS_PLATFORM_SELECT)),-select "$(CROSS_PLATFORM_SELECT)",)
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/... -run 'TestAdversarial_|TestActivate_|TestStream_|TestDigest|TestManifest|TestDiscover|TestShutdown|TestReap|TestPeer|TestChannel|TestExact|TestUpgrade|TestRollback|TestUninstall|TestConfig|TestSecrecy|TestUnauthorized|TestProtected|TestLaunch|TestKill|TestCancel'
	$(GO) test $(GO_TEST_FLAGS) ./pkg/aipsdk/backendplugin/... -run 'Test'
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile|Cancel' ./...
	$(MAKE) package-plugin-smoke
	@matches=$$($(GO) test $(GO_TEST_FLAGS) -tags=integration -list 'TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_' ./tools/backendplugin/ | awk '/^Test[A-Za-z0-9_]+$$/ { count++ } END { print count+0 }'); test "$$matches" -gt 0 || { echo "backend-plugin cross-platform selector matched zero tests" >&2; exit 1; }; $(GO) test $(GO_TEST_FLAGS) -tags=integration ./tools/backendplugin/ -run 'TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest/ -run 'TestBackendPluginCrossPlatform_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/processhost/ -run 'TestHostSecureProfiles_'
endif

# Phase 9.5 (fast): structural discovery + deterministic release report/traceability + wiring tests.
# Integrated into `make qa` without re-running the full module matrix or nested `make qa`.
backend-plugin-release-gates-static:
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) backend-plugin-release-gates-static
else
	$(GO) run ./tools/backendplugin/release_gates -root . -out .aip-release-gates-report.json -mode=static
	@matches=$$($(GO) test $(GO_TEST_FLAGS) -tags=integration -list 'TestReleaseGates_' ./tools/backendplugin/ | awk '/^Test[A-Za-z0-9_]+$$/ { count++ } END { print count+0 }'); test "$$matches" -gt 0 || { echo "backend-plugin release-gates selector matched zero tests" >&2; exit 1; }; $(GO) test $(GO_TEST_FLAGS) -tags=integration ./tools/backendplugin/ -run 'TestReleaseGates_'
	$(GO) test $(GO_TEST_FLAGS) ./tools/backendplugin/release_gates/ -run 'TestParseRequirementIDs_|TestListMatchingTests_|TestValidateSelectors_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest/ -run 'TestBackendPluginReleaseGates_'
endif

# Phase 9.5 (full local): orchestrated by release_gates -mode=full (module matrix + root package
# gates + package/security/absence/isolated/installed smoke + race honesty). Avoids fragile
# Makefile -run filters; selectors are validated via go test -list inside the tool.
backend-plugin-release-gates: backend-plugin-release-gates-static
ifeq ($(OS),Windows_NT)
	@$(WINDOWS_TASK) backend-plugin-release-gates
else
	$(GO) run ./tools/backendplugin/release_gates -root . -out .aip-release-gates-report.json -mode=full
endif

# Steering/ADR hybrid consistency (Phase 9.1).
knowledge-check:
	$(GO) test $(GO_TEST_FLAGS) ./docs/knowledge/ -run 'TestKnowledge_'

# Operator guides + every config/examples YAML via bootstrap inspect (Phase 9.2).
example-config-check: docs-check
	GO="$(GO)" bash scripts/run-named-go-tests.sh ./internal/infra/runtimebundle/ '^TestConfigExamples_passInspectRoutes$$' $(GO_TEST_FLAGS)

# Offline spawned-stack acceptance; the fake runtime exists only in a test build overlay.
.PHONY: claude-turn-acceptance
claude-turn-acceptance:
	GO="$(GO)" python3 connectors/claudeagentacp/acceptance/run.py

# Explicit certification fails closed when the exact operator-installed runtime is absent.
.PHONY: claude-gateway-certify
claude-gateway-certify:
	@test -n "$$AIP_GATEWAY_CERT_RUNTIME" || { echo 'AIP_GATEWAY_CERT_RUNTIME is required' >&2; exit 1; }
	@test "$$( $(GO) env GOOS )/$$( $(GO) env GOARCH )" = linux/amd64 || { echo 'gateway certification requires Linux/amd64' >&2; exit 1; }
	cd connectors/claudeagentacp && GO="$(GO)" bash ../../scripts/run-named-go-tests.sh ./certgateway '^TestCertGateway_RealRuntime$$' -count=1 $(GO_TEST_FLAGS)

# Optional live provider probes. They need a real provider credential, network
# access and provider quota, so the integration build tag keeps them out of the
# default hermetic path. Run explicitly; a provider error fails the probe.
.PHONY: test-live-connectors
test-live-connectors:
	@echo "Live provider probes need COMMANDCODE_API_KEY and remaining provider quota."
	cd connectors/commandcode-openai && $(GO) test $(GO_TEST_FLAGS) -tags=integration -run '^TestLive_' -count=1 ./...
	cd connectors/commandcode-anthropic && $(GO) test $(GO_TEST_FLAGS) -tags=integration -run '^TestLive_' -count=1 ./...

# Validate Kiro specification artifacts. SPEC is required (e.g. cursor-sdk-backend).
kiro-spec-check:
ifndef SPEC
	$(error SPEC is required, e.g. make kiro-spec-check SPEC=<spec-name>)
endif
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/kiro-spec-check.ps1 -Spec "$(SPEC)"
else
	@bash scripts/kiro-spec-check.sh "$(SPEC)"
endif

backend-plugin-example-check: docs-check
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-example-check.ps1
else
	@bash scripts/backend-plugin-example-check.sh
endif

# Phase 8.5: isolated root module QA (no connectors/connector-support in the copied tree).
isolated-root-qa:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/isolated-root-qa.ps1
else
	@bash scripts/isolated-root-qa.sh
endif

# Host no-Node verification (spec cursor-sdk-standalone, task 5.1): replays the
# documented host build/verification surface with every Node entry point removed
# inside an unprivileged private mount namespace, proven by negative controls.
# Linux-authoritative: no Windows host can provide the required namespaces, so the
# Windows route reports that instead of degrading to a PATH-only proof.
node-independence:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-node-independence.ps1
else
	@bash scripts/check-node-independence.sh --set full
endif

# Phase 8.5: unchanged aipstd binary gains optional kinds solely via installed artifacts.
installed-plugin-smoke:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/installed-plugin-smoke.ps1
else
	@bash scripts/installed-plugin-smoke.sh
endif

# Spawned-distribution feature smoke: built aipstd plus a scripted stub backend
# (tools/stubbackend) serve one scenario from testdata/feature-stub-smoke and
# check wire/backend/log assertions. POSIX-only; Windows coverage for these
# paths comes from the in-process dogfood tests (internal/stdhttp).
SCENARIO ?= tool-name-correction
feature-stub-smoke:
ifeq ($(OS),Windows_NT)
	@echo "feature-stub-smoke is POSIX-only; on Windows use the in-process dogfood tests." >&2
	@exit 1
else
	@bash scripts/feature-stub-smoke.sh --config-template "testdata/feature-stub-smoke/$(SCENARIO)/config.yaml.tmpl" --responses "testdata/feature-stub-smoke/$(SCENARIO)/responses" --request "testdata/feature-stub-smoke/$(SCENARIO)/request.json" --frontend-path /v1/chat/completions --backend-requests 2 --assert-file "testdata/feature-stub-smoke/$(SCENARIO)/assertions.txt"
endif

# OpenResponses full-path compliance suite (spec Phase 8, Task 8.5): independent
# client -> frontend -> core -> OpenResponses backend -> independent provider,
# the direct independent-emulator wire suites, the 45-cell matrix, and the
# emulator boundary gates.
test-openresponses-compliance:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-openresponses-compliance.ps1
else
	@bash scripts/test-openresponses-compliance.sh
endif

# Fast Task 8.5 wiring/evidence gate wired into `make qa`: verifies the
# compliance scripts, Makefile wiring, and docs reference exist and runs the
# default-build evidence validators plus the emulator boundary gates, without
# re-running the huge tagged conformance/integration suites that qa-tests covers.
test-openresponses-compliance-static:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-openresponses-compliance.ps1 -Static
else
	@bash scripts/test-openresponses-compliance.sh -static
endif
