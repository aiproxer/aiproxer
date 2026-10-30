# Release gates (Go core)

Normative criteria for merge-to-main and local pre-push checks. Commands assume repo root.

## Summary

| Gate | Criterion | Command |
|------|-----------|---------|
| Conformance | 100% of matrix tests in `internal/testkit/conformance` pass | `make parity-checks` (runs contract TCKs, conformance matrix, connector parity, and bounded sentinel; configurable via `GO_TEST_FLAGS`) |
| Database dialect parity (Req. 8.5, 9.1–9.5) | 100% of the registered dual-dialect persistence components (the families in package `internal/testkit/dbparity` via `dbparity.DefaultCatalog()`) pass stable SQLite and PostgreSQL-direct parity contracts | `make test-db-parity` (or `make test-db-parity-sqlite` / `make test-db-parity-postgres-direct`); PR CI runs `db-parity` job with ephemeral direct PostgreSQL (`postgres:17-alpine`) |
| Race (Req. 14.6) | Full suite under race on Linux | `bash scripts/race-check.sh --strict` (local Linux / nightly CI); on Windows `make test-race` is a no-op (race disabled locally). Not part of PR `qa.yml`. |
| Critical fuzz (Req. 15.4 + design) | Bounded smoke for each listed `Fuzz*` below | `make test-fuzz` or `make release-gates` locally; nightly CI via `.github/workflows/race-fuzz-nightly.yml`. Not part of PR `qa.yml`. |
| Migration fixtures (Req. 15.13) | Exactly **3** golden JSON files under `testdata/migration/` with fixed names | Enforced by `TestMigrationGoldenFixtureInventory` in conformance; see [testdata/migration/README.md](../testdata/migration/README.md) |

## API parity (LLM surfaces)

Normative matrices and row IDs: [.kiro/specs/archive/llm-api-parity/design.md](../.kiro/specs/archive/llm-api-parity/design.md). A protocol may be marked **parity-ready** only when every matrix row for that protocol is `implemented` or explicitly `out_of_scope`, with automated evidence at the layers named in the spec.

- **Fast conformance slice:** `make parity-checks` runs contract TCKs, protocol conformance matrices (`-tags=precommit,integration`), external connector suites, and the bounded sentinel (configurable via `GO_TEST_FLAGS`).
- **OpenResponses protocol-owned gate:** `make test-openresponses-compliance` (`make qa` runs the static wiring check).
- **Full release gate** remains `make release-gates` (conformance + Tier-1 fuzz).

**Parity-ready checklist (before claiming a protocol row is green):**

1. `design.md` row status is `implemented` or explicitly `out_of_scope` / `wire_only` with a reason.
2. [refclient-spec-matrix.md](../.kiro/specs/archive/go-core-reimplementation-v1/refclient-spec-matrix.md) and [refbackend-spec-matrix.md](../.kiro/specs/archive/go-core-reimplementation-v1/refbackend-spec-matrix.md) cite the same tests or deferrals as `design.md` (no contradictions).
3. `make parity-checks` passes locally; PR CI runs unit test matrix and direct database parity in `.github/workflows/ci.yml` while `.github/workflows/qa.yml` enforces architecture and policy guardrails.

## Fuzz tiers

**Tier 1 (release / nightly CI):** explicit targets below (`make test-fuzz`). Each `go test -fuzz=...` uses a trailing `$` on the fuzz name regex so only one fuzz runs per package when multiple `Fuzz*` exist. Nightly CI runs each with the same `FUZZTIME` (default `500ms` locally; override e.g. `FUZZTIME=3s make release-gates`).

| Fuzz function | Package | Role |
|---------------|---------|------|
| `FuzzJSONRoundTrip` | `internal/testkit` | JSON normalize / compare helpers |
| `FuzzParseSelector` | `internal/core/routing` | Route selector parser (printable corpus) |
| `FuzzParseSelectorFromBytes` | `internal/core/routing` | Route selector parser (arbitrary bytes as string) |
| `FuzzDecodeCreateRequest` | `internal/plugins/frontends/openairesponses` | Responses API body + packed route selector |
| `FuzzDecodeMessageRequest` | `internal/plugins/frontends/anthropic` | Anthropic Messages decode |
| `FuzzDecodeGenerateContentRequest` | `internal/plugins/frontends/gemini` | Gemini generateContent decode |
| `FuzzDecodeChatRequest` | `internal/plugins/frontends/openailegacy` | Legacy chat decode |
| `FuzzWriteNonStreamJSON_toolArguments` | `internal/plugins/frontends/anthropic` | Encode path `json.Unmarshal` on tool args |
| `FuzzBuildGenerateContentResponse_toolJSON` | `internal/plugins/frontends/gemini` | Encode path tool JSON |
| `FuzzCallValidateJSON` | `pkg/aipapi` | Canonical `Call` JSON + `Validate` |
| `FuzzMergeRouteQueryGenerationOptions` | `pkg/aipapi` | Route query → generation options |
| `FuzzCollectWithLimitsProgram` | `pkg/aipapi` | Stream aggregation limits |
| `FuzzStableCallIdentity` | `internal/core/diag` | Stable trace helpers on decoded calls |
| `FuzzParamsForCall` | `internal/plugins/backends/openairesponses` | Canonical call → Responses params |
| `FuzzHandleResponseStreamUnion` | `internal/plugins/backends/openairesponses` | Responses SSE union → events |
| `FuzzBuildToolsParametersJSON` | `internal/plugins/backends/openairesponses` | Tool JSON schema unmarshal |
| `FuzzHandleMessageStreamEventUnion` | `internal/plugins/backends/protocols/anthropicmessages` | Anthropic stream union → events |
| `FuzzToolInputSchemaParametersJSON` | `internal/plugins/backends/protocols/anthropicmessages` | Anthropic tool schema unmarshal |
| `FuzzHandleChatCompletionChunk` | `internal/plugins/backends/openailegacy` | Chat completion chunk → events |
| `FuzzBuildChatToolsParametersJSON` | `internal/plugins/backends/openailegacy` | Chat tools JSON unmarshal |
| `FuzzHandleGenerateContentResponse` | `internal/plugins/backends/protocols/geminigenerate` | Gemini response JSON → events |
| `FuzzBuildToolsParametersJSON` | `internal/plugins/backends/protocols/geminigenerate` | Gemini tool params unmarshal |
| `FuzzMessageToContentToolResultJSON` | `internal/plugins/backends/protocols/geminigenerate` | Tool result JSON in invoke |
| `FuzzAssistantPartsToContentBlocksJSON` | `internal/plugins/backends/bedrock` | Assistant JSON part → Converse blocks |
| `FuzzParseNDJSONLine` | `connector-support/acp` | ACP NDJSON line mapping |
| `FuzzMapSessionUpdateToEvents` | `connector-support/acp` | ACP session/update map |
| `FuzzMergeHandshakeProfileExtensions` | `connector-support/acp` | Handshake extensions + session id |
| `FuzzHookMutationValidators` | `internal/core/hooks` | Post-hook call + event validation |
| `FuzzAcceptClientUserAgent` | `internal/core/identity` | User-Agent accept bounds / controls |
| `FuzzAcceptClientAppURL` | `internal/core/identity` | OpenRouter app URL accept shape |
| `FuzzAcceptClientAppTitle` | `internal/core/identity` | OpenRouter app title accept bounds |
| `FuzzValidateIdentityYAML` | `internal/core/identity` | Identity YAML decode + Validate |
| `FuzzCaptureClientUserAgent` | `internal/plugins/frontends/identitywire` | Frontend UA capture into invocation |
| `FuzzCompleteJSONSuffix` | `internal/plugins/features/toolcallrepair/repair` | Append-only JSON suffix completion (ADR 0007) |
| `FuzzSchemaPreScanCompile` | `internal/plugins/features/toolcallrepair/repair` | Offline schema pre-scan/compile bounds |
| `FuzzEngineRepair` | `internal/plugins/features/toolcallrepair/repair` | Deterministic tool-call repair engine |
| `FuzzComputeAnchor` | `internal/plugins/features/reasoningpreservation` | Exact non-reasoning anchor hash stability (issue #157) |
| `FuzzDecodeConfig` | `internal/plugins/features/reasoningpreservation` | Feature YAML decode/validation bounds (issue #157) |
| `FuzzLeaseSet_OccupiesCapacity` | `internal/core/concurrencyauthority/domain` | Lease-set state occupancy model |
| `FuzzIsAmbiguousRenewError` | `internal/core/concurrencyauthority/app` | Renew ambiguity classification |
| `FuzzWorkItem_TransitionSequence` | `internal/core/terminalwork` | Terminal-work state machine |
| `FuzzOwner_CommandSequences` | `internal/core/terminal` | Terminal owner command sequences |
| `FuzzParseDecimalToNano` | `pkg/aipsdk/economics` | Money decimal parse bounds |
| `FuzzPhase32_SourceEventKey_DelimiterSafety` | `pkg/aipsdk/metering` | Fact source-key delimiter safety |
| `FuzzPhase32_MoneyPresentCurrency` | `pkg/aipsdk/metering` | Money present/currency invariants |

## Time budget

- Local default: `FUZZTIME=500ms` per target (wall time scales with the number of rows in the table above).
- Nightly CI: `.github/workflows/race-fuzz-nightly.yml` sets `FUZZTIME=6s` per target for `make test-fuzz` (raise over ad-hoc local smoke when validating releases).
- PR CI (`.github/workflows/ci.yml` and `.github/workflows/qa.yml`) does **not** run race or Tier-1 fuzz; it keeps merge feedback fast. `ci.yml` runs cross-platform unit tests and the `db-parity` job against an ephemeral direct PostgreSQL container (`postgres:17-alpine`) executing `make test-db-parity`. PRs with changes classified as non-test-relevant by `scripts/ci-scope.sh` report an explicit bypass; the required `repo-hygiene` aggregate status check reports success. `qa.yml` runs fast preflight, policy checks, module tidy preflights, provider-profile ratchet, vet, and architecture guardrails.

## Fuzz seed corpus (committed)

Native fuzz loads extra seeds from **`testdata/fuzz/FuzzFunctionName/`** next to the **package under test** (same rule as `go test` `testdata/`). One file = one seed input: raw bytes for `[]byte` fuzz parameters, UTF-8 file body for `string` parameters.

- Index and format rules: [testdata/fuzz/README.md](../testdata/fuzz/README.md) (files must use the `go test fuzz v1` encoding, not raw JSON-only blobs).
- After long local runs, copy minimized or interesting inputs from the fuzz cache into the right `testdata/fuzz/FuzzName/` tree; keep files small and non-secret.

## Single entry point

- `make release-gates` — conformance package tests (with **`-tags=integration`** for full matrix/parity), then `make test-fuzz` (all Tier 1 targets). This target does **not** run the race detector; use `make test-race` locally on Linux/macOS or rely on nightly CI (`bash scripts/race-check.sh --strict` in `.github/workflows/race-fuzz-nightly.yml`; Windows skips race via `scripts/race-check.ps1`).
- Full local QA remains `make qa` (quality checks + full tagged test pass with `-tags=precommit,integration` + lint + vuln + static release gates). PR CI runs the cross-platform unit matrix and dedicated `db-parity` job in `ci.yml` (ephemeral direct PostgreSQL) along with architecture and hygiene gates in `qa.yml`. Race and Tier-1 fuzz run on the nightly workflow (and via `workflow_dispatch`). The race detector's `billing` lane (35–58 min) runs weekly (Mondays); every other lane runs nightly, and `workflow_dispatch` runs all lanes.

## Runtime evidence and concurrency gates

Normative completion gates for the metering / authority / concurrency runtime (requirements **15.9**, **17.1–17.9**). These complement the Tier-1 table above; they do not replace `make release-gates`. Prefer existing harnesses over new protocol stacks.

| Gate | Criterion | Command / evidence |
|------|-----------|-------------------|
| Cross-protocol baseline (17.1, 17.3) | OpenAI Responses/legacy, Anthropic, Gemini FE×BE matrix remains green (the optional runtime features default-off compatible) | `make parity-checks` |
| Shared checkpoint contract | Supported frontend `Operation` values share the same executor frontend-ingress checkpoint boundary/lifecycle | `go test ./internal/core/runtime/ -run SharedCheckpointAcrossFrontend` |
| Parallel race benches (16.6) | Parallel routing under concurrency authority with 2/4/8 legs | `go test ./internal/core/runtime/ -run '^$' -bench BenchmarkParallelRaceLegsAuthority` |
| Critical fuzz | Tier-1 protocol/decode fuzz plus the concurrency fuzz targets (`FuzzLeaseSet_OccupiesCapacity`, `FuzzIsAmbiguousRenewError`, `FuzzWorkItem_TransitionSequence`, `FuzzOwner_CommandSequences`) and the metering fact seeds | `make test-fuzz` or `make release-gates` |
| Fault campaign (13.3–13.4, 13.7–13.8) | Deterministic panic/timeout/malformed/outage/ambiguous/partial/restart campaign + concurrent terminal race suite | `go test ./internal/core/runtime/ -run 'TestPhase72_'` |
| Control-plane readiness + alerts (12.x, 13.10) | Named OSS technical posture + operator alert/budget doc (not commercial billing) | `go test ./pkg/aipsdk/controlplane/ -run TestPhase73_`; [dual-plane-readiness-alerts.md](dual-plane-readiness-alerts.md) (moves to the Enterprise tree) |
| Five-slot lease-set bench | Atomic AcquireSet five-slot contention suitable for benchstat | `go test ./internal/infra/concurrencyauthority/leasestore/ -run '^$' -bench BenchmarkMemoryAcquireSetFiveSlotHundredContenders` |
| Race (17.9) | Full suite under race on Linux (includes Phase 7.2 race suite) | `bash scripts/race-check.sh --strict` (local Linux / nightly CI); Windows `make test-race` is a documented no-op |
| PostgreSQL direct runtime (9.9, 17.9) | Cross-instance durable authority, lease, journal, and terminal-work proofs through a direct/admin-capable endpoint | `make test-authority-postgres-direct` with `AIP_TEST_POSTGRES_DSN` |
| PostgreSQL migrations (18.9-18.10) | Explicit admin migration followed by read-only schema verification | `make test-postgres-migrations`; prefers `AIP_MIGRATION_POSTGRES_DSN`, then test admin/runtime DSNs |
| PostgreSQL aggregate | Migration, direct runtime, and pooled runtime gates all pass | `make test-authority-postgres` |
| PostgreSQL pooled runtime (18.14–18.16) | Dual-endpoint pooled contracts for authority, lease, journal, terminal-work, and runtimebundle (admin DSN `AIP_TEST_POSTGRES_ADMIN_DSN` + transaction-pooled `AIP_TEST_POSTGRES_DSN` with attestation `AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`). Proves migrate/open split and pooler-safe DML. | `make test-authority-postgres-pooled` (`AIP_REQUIRE_POSTGRES_POOLER=1` fails closed). |
| Migration fixtures (15.13 / 17.2) | Golden inventory under `testdata/migration/` | conformance `TestMigrationGoldenFixtureInventory` via `make parity-checks` |
| Panic / malformed isolation (15.9) | Injected request/attempt/concurrency providers map panic and unknown decision/lease kinds through fail-closed `ErrUnavailable` (advisory may degrade); release compensate panics are isolated | `go test ./internal/core/authoritycoord/ -run Isolates` |
| Privacy (17.5–17.6) | No raw bearer/API-key/header leakage in default authority evidence | `go test ./internal/core/usageauthority/app/ -run ProjectAuthorityEvidence` |
| Crash / cancel / late correction | Reconcile orphans, journal corrections, authority release on cancel paths | `go test ./internal/core/metering/reconcile/ ./internal/infra/metering/journalstore/ ./internal/core/runtime/ -run 'Reconcile\|Correction\|AuthorityRelease\|Cancel'` |
| Compatibility when disabled (17.1) | Default dogfood path with metering/authority off remains functional | `go run ./cmd/aipstd check-config --config config/examples/dogfood-local-stub.yaml` + `testdata/enterprise_module` |
| Explicit non-goals (17.8) | No web GUI, payments, invoices, tax, SSO/SAML/SCIM, CSP, or compression algorithms in this feature | design / requirements exclusions |
| Architecture | The Enterprise module stays public-only | `go test ./internal/archtest/ -run EnterpriseModule` |
| Migration / rollout / rollback (11.x, 7.4) | Ordering, local vs distributed posture, terminal-work drain, open-core | [dual-plane-migration-rollout.md](dual-plane-migration-rollout.md) (moves to the Enterprise tree); `go test ./internal/qa/ -run TestPhase74_` |

The usage-limit rule store, its privacy evidence projection, and the money certification tier are planned to become Enterprise-owned and to stop being OSS release gates: they move to the Enterprise composition, are certified there, and their operator documentation moves to the Enterprise tree (the stub stays here). That move is not finished — the rule store and the certification package are still in this tree, so those gates still run here today.

## Database dialect parity (persistence gates)

Normative completion gates for dual-dialect SQLite and PostgreSQL persistence (requirements **8.5**, **9.1–9.5**). The authoritative persistence scope is defined in package [`internal/testkit/dbparity`](../internal/testkit/dbparity/catalog.go) via [`dbparity.DefaultCatalog()`](../internal/testkit/dbparity/catalog.go), covering the production component families of this tree, including `continuity`, `secure-sessions`, `session-classification`, `control-plane-ledger`, `concurrency-authority`, `metering-journal`, `proxy-memory`, and `terminal-work`. The catalog still registers the billing and usage-authority families as well: their implementations have not been relocated to the Enterprise tree yet.

| Gate | Criterion | Command / evidence |
|------|-----------|-------------------|
| SQLite dialect parity | 100% of registered components pass stable SQLite parity wrappers, migration history checks, and logical schema invariants | `make test-db-parity-sqlite` |
| PostgreSQL direct parity | 100% of registered components pass stable direct PostgreSQL parity wrappers and schema verifications with fail-closed environment | `make test-db-parity-postgres-direct` (requires direct `AIP_TEST_POSTGRES_DSN` with runner fallback to `AIP_TEST_POSTGRES_ADMIN_DSN`; Make sets `AIP_REQUIRE_POSTGRES=1`) |
| Canonical repository parity | Sequential SQLite + PostgreSQL-direct parity across all cataloged components | `make test-db-parity` |
| PR CI direct parity | Ephemeral direct PostgreSQL container (`postgres:17-alpine`), fail-closed aggregation into required `repo-hygiene` status, explicit bypass for changes classified as non-test-relevant by `scripts/ci-scope.sh` | `.github/workflows/ci.yml` (`db-parity` job) |
| Architecture discovery | Unregistered versioned migration roots and packages containing discovered deterministic dialect-sensitive indicators fail closed | `go test ./internal/archtest -run DatabaseParity` |
| Policy & CI wiring | QA verifies workflow fail-closed wiring and required aggregate dependency | `go test ./internal/qa -run DatabaseParity` |

Specialized PostgreSQL gates (`make test-authority-postgres-direct`, `make test-authority-postgres-pooled`, and `make test-postgres-migrations`) remain separate and require dedicated topology attestation (runtime pooler DSN `AIP_TEST_POSTGRES_DSN`, admin DSN `AIP_TEST_POSTGRES_ADMIN_DSN`, and `AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1` for transaction poolers).

## Runtime architecture convergence (ownership docs)

Canonical ownership (see [architecture.md](architecture.md)): **one process runtime / ProcessServices**, **one generation runtime**, **one host** (private-field `runtimebundle.Host` from `runtimebundle.BuildHost` / `Host.Close`; Manager-owned retirement), **one reload contract** (`pkg/aipsdk/configreload`). Candidate assembly is private/temporary. `ValidateDistribution` is true unpublished validation. Public Options are registration-only ([legacy-options-migration.md](legacy-options-migration.md)). Stale dual-bootstrap, attachment, `Built`/`RunWithRuntime`, and legacy public Options paths must stay absent from operator docs.

| Gate | Criterion | Command / evidence |
|------|-----------|-------------------|
| Docs ownership contract | Architecture/package-map/runtime-flow/reload/enterprise/release docs name the four ownership surfaces | `go test ./internal/qa/... -run 'Docs_Architecture'` |
| Legacy Options absent | Registration-only Options; no current-major legacy claims; historical map in `docs/legacy-options-migration.md` | `go test ./internal/qa/... -run 'LegacyAbsent\|Docs_Legacy'` |
| Architecture symbols | Deleted symbols stay absent; facade/host close gates hold | `go test ./internal/archtest/... -run 'LegacyAbsent\|RuntimeFacade\|HostClose\|CriticalFile\|PackageTree\|Budget\|Freeze'` |

## Versioned runtime config reload

Operator contract: [runtime-config-reload.md](runtime-config-reload.md) (`pkg/aipsdk/configreload`). ADR: [adr/0008-versioned-runtime-config-reload.md](adr/0008-versioned-runtime-config-reload.md). Spec: `.kiro/specs/archive/versioned-runtime-reloadable-proxy-configuration/`.

| Gate | Criterion | Command / evidence |
|------|-----------|-------------------|
| Docs / examples | Operator guide and dogfood `check-config` stay green | `go test ./cmd/aipstd/... ./internal/qa/... -run 'ConfigExample\|Docs\|Reload'`; `go run ./cmd/aipstd check-config --config config/examples/dogfood-local-stub.yaml` |
| Ownership / no watcher | Reload ownership gates; no filesystem watcher package | `go test ./internal/archtest/... -run 'Reload\|Ownership'` |
| Management / signal | SIGHUP + opt-in `AIP_RELOAD_MANAGEMENT_ADDRESS` contracts | `go test ./cmd/aipstd/... ./internal/stdhttp/admin/configreload/... -run 'Reload\|Management\|SIGHUP\|Signal'` |
| Source integrity | Atomic rename / non-atomic reject / size / strict YAML | `go test ./internal/infra/configsource/... ./internal/core/config/... -run 'FixedSource\|Strict\|NonAtomic\|Oversize'` |
| Last-good / restart-required | Pre-publication failures leave active generation unchanged | `go test ./internal/core/configreload/... ./internal/infra/runtimehost/... -run 'RestartRequired\|LastGood\|Retention\|Rollback'` |

Do **not** claim Windows race-green without Linux evidence (local or nightly CI). Do **not** claim PostgreSQL green without a recorded `AIP_TEST_POSTGRES_DSN` run. Do **not** treat `make parity-checks` as dual-plane checkpoint proof; that proof is the shared executor checkpoint test above.
