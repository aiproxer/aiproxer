# Go Agent Skills Audit

Audit date: 2026-08-20

Scope: the 37 tracked `golang-*` skills under `.agents/skills/`; findings for seven third-party-library skills removed since are omitted

Change policy: report only; no skill content was edited

## Outcome

The catalog is structurally loadable, but it is not yet safe to treat as authoritative guidance.

| Verdict | Skills | Meaning |
|---|---:|---|
| MAJOR | 28 | Contains guidance likely to cause incorrect, insecure, non-compiling, or materially outdated work. |
| MINOR | 6 | Fundamentally usable, but contains overbroad rules, stale metadata, or misleading details. |
| PASS | 3 | No material correctness issue found in this audit. This is not a proof of completeness. |

The highest-risk areas are security, concurrency/lifecycle, database examples, current third-party APIs, CI supply-chain guidance, and stale Go runtime/tooling claims.

## Method

- Read every `SKILL.md` and its bundled references/assets; evaluated trigger and orchestration guidance as well as technical content.
- Checked all 37 entrypoints with the repository's skill validator and parsed bundled JSON/eval files.
- Compared factual and API claims with current primary documentation and current released modules as of 2026-08-20.
- Classified a skill by its highest-severity finding.
- Did not execute every example as a complete standalone program; obvious signature/API defects were checked against the relevant package source or documentation.

## Cross-cutting findings

### 1. Cross-skill references are not portable

Thirty-four of 37 skills contain package-qualified references such as `samber/cc-skills-golang@...`. Several referenced names do not exist in the normalized catalog, including `golang-linter`, `golang-pkg-go-dev`, `golang-spf13-cobra`, `golang-spf13-viper`, `golang-gopls`, `golang-refactoring`, and `promql-cli`. Even references to installed skills use syntax that the supported agents are not guaranteed to resolve.

The catalog should use canonical local skill names and should not depend on the upstream package manager's resolver semantics.

### 2. Domain skills prescribe agent-specific orchestration

Thirteen skills embed instructions involving `ultracode`, sub-agents, an `Agent` tool, or `EnterWorktree`. Examples include `golang-documentation`, `golang-modernize`, `golang-lint`, `golang-performance`, and `golang-error-handling`. These rules conflict with cross-agent portability and with repository-level orchestration policy. Domain skills should define the technical workflow; the active agent and repository instructions should own delegation and worktree behavior.

### 3. Progressive disclosure is weak

- `golang-hexagonal-architecture/SKILL.md` is 808 lines, above the skill-authoring recommendation of 500 lines.
- Ninety-three reference/assets files exceed 100 lines without a table of contents.
- The full Go catalog contains roughly 24,000 Markdown lines, making selective loading and navigation important.

This is primarily a context-efficiency and maintainability defect, not evidence that the affected guidance is technically wrong.

### 4. Local links are broken

- `golang-documentation/references/library.md:186`
- `golang-documentation/references/project-docs.md:7,30,48`
- `golang-troubleshooting/references/methodology.md:236`

The documentation links expect templates beside the reference file, but the templates live under `assets/templates/`; the troubleshooting link should traverse to `../SKILL.md`.

### 5. The remembered CodeRabbit complaint is not present on the import PR

The CodeRabbit review on PR 140 was skipped because the PR contained 239 files, above its 150-file review limit. It did not provide technical findings about these skills. This audit therefore does not attribute the findings below to CodeRabbit.

## Per-skill verdicts

| Skill | Verdict | Principal reason |
|---|---|---|
| `golang-simplify` | PASS | Behavior-preserving scope and triggers are clear; no material issue found. |
| `golang-benchmark` | MAJOR | Incorrect benchmark-compilation and obsolete `allocfreetrace` guidance. |
| `golang-cli` | MINOR | Overbroad Cobra/Viper rules and examples that bypass Cobra output/error handling. |
| `golang-code-style` | MINOR | Nil collection and numeric pointer/value thresholds are presented as universal rules. |
| `golang-concurrency` | MAJOR | Unsafe cancellation/error-propagation example and unconditional ownership rules. |
| `golang-context` | MAJOR | Recommends unbounded detached work with `context.WithoutCancel`. |
| `golang-continuous-integration` | MAJOR | Calls mutable action tags pinned and emits them in workflow templates. |
| `golang-data-structures` | MAJOR | Documents obsolete map internals and incorrect unsafe/GC behavior. |
| `golang-database` | MAJOR | A bundled mock does not compile; locking/performance claims are overstated. |
| `golang-dependency-injection` | MAJOR | Recommends archived Wire. |
| `golang-dependency-management` | MINOR | Mischaracterizes `go.sum` and understates upgrade risk. |
| `golang-design-patterns` | MAJOR | Makes false `runtime.AddCleanup` cycle guarantees. |
| `golang-documentation` | MAJOR | Encourages unapproved private-code publication and contains invalid release-link guidance. |
| `golang-error-handling` | MAJOR | Contains invalid Go and treats formatting as an error-exposure boundary. |
| `golang-grpc` | MAJOR | Confuses protoc-gen-validate with Protovalidate and omits balancer registration. |
| `golang-hexagonal-architecture` | PASS | No material factual or architectural defect found. |
| `golang-lint` | MINOR | Unsafe concurrent autofix orchestration and contradictory/universal config claims. |
| `golang-modernize` | MAJOR | Dates `ReverseProxy.Rewrite` incorrectly and gives unreliable PGO instructions. |
| `golang-naming` | MAJOR | Treats method/package naming conventions as language/interface requirements. |
| `golang-observability` | MAJOR | Incorrect runtime configuration and privacy/compliance claims. |
| `golang-performance` | MAJOR | Incorrect complexity and receiver/inlining claims plus unsupported thresholds. |
| `golang-popular-libraries` | MAJOR | Multiple false maintainer, async, lifecycle, and OpenAPI capability claims. |
| `golang-project-layout` | MAJOR | Presents optional Go repository conventions as mandatory tool rules. |
| `golang-safety` | MAJOR | Incorrect constant arithmetic example and error-losing `sync.Once` initialization. |
| `golang-security` | MAJOR | Contains unsafe XSS, path-confinement, JWT, limiter, and privacy guidance. |
| `golang-solid-principle-review` | PASS | Evidence model is sound and explicitly rejects non-idiomatic over-abstraction. |
| `golang-stretchr-testify` | MINOR | Misstates `assert.Equal` pointer behavior. |
| `golang-structs-interfaces` | MAJOR | Overbroad constructor/interface, JSON-tag, copy, and vet claims. |
| `golang-testing` | MAJOR | Integration example is flaky and ignores readiness/fixture errors. |
| `golang-troubleshooting` | MAJOR | Recommends invalid `go vet -shadow` and repeats stale timer-leak/path-security advice. |

## Detailed findings

Only the highest-value findings are listed here. Line numbers refer to the audited catalog at this report's commit.

### Language, runtime, and testing

#### `golang-simplify` — PASS

No material correctness, trigger, or link defect was found. Its behavior-preservation rule and scope constraints are appropriate.

#### `golang-code-style` — MINOR

- `SKILL.md:59-64` says slices and maps must never be nil. Nil slices are valid and append/range-safe; nil maps are valid for reads and range. Initialization should depend on mutation and wire-format requirements. See [Go slices](https://go.dev/blog/slices).
- `SKILL.md:188` and `references/details.md:60-73` turn an approximately 128-byte pointer/value threshold into a rule. Escape behavior and performance are compiler-, architecture-, and workload-dependent. See [Go allocation optimizations](https://go.dev/blog/allocation-optimizations).
- `SKILL.md:195` restricts blank imports to `main` and tests, excluding legitimate documented registration side effects in libraries. See [import declarations](https://go.dev/ref/spec#Import_declarations).

#### `golang-concurrency` — MAJOR

- `references/pipelines.md:161-177` can block forever acquiring a semaphore, discards `process` errors, and always returns `nil`. Acquisition should honor cancellation and errors should be propagated.
- `SKILL.md:38,42` makes “send copies, not pointers” and selecting `ctx.Done()` universal. Correctness depends on ownership/immutability and whether the operation is cancellation-aware. See the [Go memory model](https://go.dev/ref/mem) and [pipeline guidance](https://go.dev/blog/pipelines).
- `references/sync-primitives.md:65` calls `sync/atomic` lock-free, although its public contract guarantees atomicity, not a universal implementation strategy. See [`sync/atomic`](https://pkg.go.dev/sync/atomic).

#### `golang-context` — MAJOR

- `SKILL.md:35` and `references/cancellation.md:149-172` detach a goroutine using `context.WithoutCancel` without a deadline, supervisor, queue, or shutdown wait. This can leak unbounded background work. Detached work needs explicit bounded lifecycle ownership. See [`context.WithoutCancel`](https://pkg.go.dev/context#WithoutCancel).

#### `golang-data-structures` — MAJOR

- `SKILL.md:76-77` and `references/map-internals.md:5-32` document the pre-Go-1.24 bucket/overflow implementation as current. Current Go uses Swiss Tables. See [Go 1.24 release notes](https://go.dev/doc/go1.24) and [Swiss Tables in Go](https://go.dev/blog/swisstable).
- `references/pointers.md:34-76` misstates the documented `unsafe.Pointer` patterns and says the GC may move objects between statements. The real `uintptr` concern is loss of pointer semantics/liveness. See [`unsafe.Pointer`](https://pkg.go.dev/unsafe#Pointer).
- `references/pointers.md:86` dates `unsafe.SliceData` to Go 1.17; it was added in Go 1.20.

#### `golang-error-handling` — MAJOR

- `SKILL.md:38` and `references/error-wrapping.md:18-33` use `%v` as the mechanism for controlling external error exposure. Formatting neither creates a security/API boundary nor guarantees safe text; it also drops `errors.Is/As` behavior. Explicitly map internal failures to public error contracts. See [`fmt.Errorf`](https://pkg.go.dev/fmt#Errorf) and [`errors`](https://pkg.go.dev/errors).

#### `golang-modernize` — MAJOR

- `SKILL.md:110` labels `ReverseProxy.Director` to `Rewrite` as a Go 1.26 migration. `Rewrite` arrived in Go 1.20. See [Go 1.20 release notes](https://go.dev/doc/go1.20) and [`ReverseProxy`](https://pkg.go.dev/net/http/httputil#ReverseProxy).
- `references/tooling.md:41-52` gives fixed PGO gains and a generic `go test -cpuprofile=default.pgo -bench=. ./...` workflow. Profiles must represent the main workload and be validated per program. See [Go PGO](https://go.dev/doc/pgo).
- The skill otherwise covers released Go 1.26 features; Go 1.27 notes are still explicitly a draft as of this audit. See [Go 1.26](https://go.dev/doc/go1.26) and [Go 1.27 draft](https://go.dev/doc/go1.27).

#### `golang-naming` — MAJOR

- `references/types-errors.md:41-50` says any type with a `Read` method must implement `io.Reader`. Method names are not reserved; the signature matters only when the type intends to implement that interface. See [`io.Reader`](https://pkg.go.dev/io#Reader).
- `references/packages-files.md:44-56` treats `cmd/` as mandatory and simplifies `internal/` visibility to a module rule. `cmd/` is a convention; internal visibility follows the parent directory tree. See [internal packages](https://go.dev/doc/go1.4#internalpackages).

#### `golang-safety` — MAJOR

- `SKILL.md:129-134` says `0.1 + 0.2 == 0.3` is false, but the shown operands are exact untyped constants, so the comparison is true. A float warning example must use converted/typed floating-point values. See [Go constants](https://go.dev/ref/spec#Constants).
- `SKILL.md:226-230` discards `sql.Open`'s error inside `sync.Once`, permanently caching failure. Store and return the error or use an appropriate result-caching primitive. See [`database/sql.Open`](https://pkg.go.dev/database/sql#Open).
- `SKILL.md:106` incorrectly prohibits all concurrent map access; concurrent read-only access is safe when there is no mutation.

#### `golang-structs-interfaces` — MAJOR

- `SKILL.md:78-87` makes “never return interfaces from constructors” universal. Returning an interface can intentionally hide an implementation or stabilize a public contract.
- `SKILL.md:291-308` says all exported serialized fields require tags and misstates `omitempty`. The standard encoder has valid default names and exact, type-dependent omission rules. See [`encoding/json.Marshal`](https://pkg.go.dev/encoding/json#Marshal).
- `SKILL.md:329-346` treats channels as non-copyable and `noCopy` as a public contract. Copy restrictions apply to particular stateful types after use; vet's `copylocks` analyzer is pattern-based. See [`go vet` copylocks](https://pkg.go.dev/cmd/vet#hdr-copylocks).

#### `golang-testing` — MAJOR

- `references/integration-testing.md:94-129` sleeps for readiness, treats `sql.Open` as a connection check, ignores fixture read errors, and ignores teardown failures. Use bounded polling/`PingContext`, fail on fixture errors, and report cleanup failures. See [`DB.PingContext`](https://pkg.go.dev/database/sql#DB.PingContext).
- `SKILL.md:43-51` makes named subtests, integration build tags, and a sub-millisecond unit target mandatory. These are repository/workload defaults, not universal correctness rules.

#### `golang-troubleshooting` — MAJOR

- `references/common-go-bugs.md:93` recommends `go vet -shadow`, which is not a current `go vet` flag; shadow is a separate analyzer. See [`cmd/vet`](https://pkg.go.dev/cmd/vet) and the [shadow analyzer](https://pkg.go.dev/golang.org/x/tools/go/analysis/passes/shadow).
- `references/code-review-flags.md:10` and `references/concurrency-debug.md:49-57` call `time.After` in a loop an unavoidable memory leak. Since Go 1.23, unreachable timers can be collected; hot-loop allocation/churn can still justify reuse. See [Go 1.23 timer changes](https://go.dev/doc/go1.23).
- `references/common-go-bugs.md:715-720` uses a lexical path-prefix check that is insufficient against symlink/platform traversal. Prefer directory-scoped APIs such as [`os.Root`](https://pkg.go.dev/os#Root).

### Tooling, delivery, documentation, and operations

#### `golang-benchmark` — MAJOR

- `references/benchstat.md:382` says compilation contaminates benchmark timing and recommends precompilation. Go benchmark timing excludes compilation; total command latency is a different measurement. See [`testing.B`](https://pkg.go.dev/testing).
- `references/tools.md:22` recommends `GODEBUG=allocfreetrace=1`, absent from current documented GODEBUG settings. See [GODEBUG history](https://go.dev/doc/godebug).
- `references/pprof.md:810` treats increasing `inuse_space` as proof of a leak; it is a leak suspect only under controlled, comparable workload snapshots.

#### `golang-continuous-integration` — MAJOR

- `SKILL.md:219` and bundled workflows use mutable major action tags while calling them pinned. Third-party actions should be pinned to immutable full commit SHAs and updated through tooling. See [GitHub secure use](https://docs.github.com/en/actions/reference/security/secure-use).
- `SKILL.md:73` says every CI must run `-race`, ignoring supported-platform and resource constraints. Race testing should be a suitable platform gate and is dynamic, not complete proof. See the [Go race detector](https://go.dev/doc/articles/race_detector).
- `SKILL.md:194` calls QEMU required for multi-platform images, although native nodes and cross-compilation are alternatives. See [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/).

#### `golang-dependency-management` — MINOR

- `SKILL.md:31,83-88` describes `go get -u` as safe, understating compatibility and supply-chain review required for upgrades. See [Go security best practices](https://go.dev/doc/security/best-practices).
- `SKILL.md:44-45` describes `go.sum` like a lockfile. The module reference explicitly says it is not one. See the [Go modules reference](https://go.dev/ref/mod).
- `references/workspaces.md:25` universally forbids committing `go.work.sum`; this should be a repository policy with stated trade-offs.

#### `golang-documentation` — MAJOR

- `SKILL.md:172,177` recommends publishing Playground links and registering even private libraries with external documentation services. This requires explicit authorization and data-disclosure review; tested `Example` functions are the portable executable-doc mechanism. See [Go doc comments](https://go.dev/doc/comment).
- `SKILL.md:226` generates a release download URL inconsistent with the bundled GoReleaser artifact naming and likely to 404. See [GoReleaser archives](https://www.goreleaser.com/customization/package/archives/).
- Four bundled template links are broken, as listed in the cross-cutting section.

#### `golang-lint` — MINOR

- `SKILL.md:28` directs a background agent to run `golangci-lint run --fix` while implementation continues, allowing uncontrolled concurrent mutations.
- `SKILL.md:41,67` contradicts itself on linter counts and says every project must have `.golangci.yml`, although configuration is optional and multiple filenames/formats are supported. See [golangci-lint configuration](https://golangci-lint.run/docs/configuration/file/).

#### `golang-observability` — MAJOR

- `SKILL.md:46` and `references/profiling.md:17,63` claim environment variables can toggle profiling without redeployment. A running process changes only if the application implements reload/dynamic control.
- `references/rum.md:26,148,177,245` says internal user IDs are not personal data and self-hosting removes most GDPR concerns. Linkable identifiers can remain personal data, and hosting location does not remove obligations. See the [European Commission data-protection guide](https://commission.europa.eu/law/law-topic/data-protection/information-individuals_en).
- `SKILL.md:37` requires JSON logs universally; `log/slog` also supports structured text and custom handlers. See [`log/slog`](https://pkg.go.dev/log/slog).

#### `golang-performance` — MAJOR

- `references/caching.md:104` calls repeated append without preallocation O(n²) amortized copying. Typical geometric capacity growth makes append amortized linear, though growth is not a language guarantee. See [append semantics](https://go.dev/ref/spec#Appending_and_copying_slices).
- `references/cpu.md:37-45` claims value receivers enable inlining while pointer receivers block it and attaches a fixed 80% gain. Inlining is compiler analysis; receiver choice also affects copying and escape behavior. See [compiler optimizations](https://go.dev/wiki/CompilerOptimizations).
- `references/memory.md:121,171,222` presents fixed map, pooling, and receiver-size thresholds as Go rules rather than measured heuristics.

#### `golang-project-layout` — MAJOR

- `SKILL.md:42-75` makes separate modules/workspaces, repository-matching paths, singular packages, and placing every `main` under `cmd/` mandatory. These are context-dependent conventions. See [organizing a Go module](https://go.dev/doc/modules/layout).
- `references/testing-layout.md:10,80,86` treats `_bench_test.go` and `_example_test.go` as required. Go recognizes `_test.go`; function names determine benchmarks/examples. See [`testing`](https://pkg.go.dev/testing).

#### `golang-security` — MAJOR

- `SKILL.md:81-82` and `references/filesystem.md:40-43,241-244` recommend `text/template` for XSS defense and lexical path-prefix checks for confinement. Use [`html/template`](https://pkg.go.dev/html/template) and directory-scoped/symlink-aware access such as [`os.Root`](https://pkg.go.dev/os#Root).
- `references/network.md:203-218` claims `encoding/xml` resolves external entities and recommends string scanning as an XXE defense. Go's decoder does not resolve them by default. See [`encoding/xml`](https://pkg.go.dev/encoding/xml).
- `references/injection.md:290-301` says gob decoding can execute code, importing a Java-style threat model that does not describe Go gob. Resource exhaustion and untrusted data shape remain relevant. See [`encoding/gob`](https://pkg.go.dev/encoding/gob).
- `references/architecture.md:48-56` uses an unbounded per-client limiter map; unique attacker-controlled keys can exhaust memory.
- `references/architecture.md:149-160` accepts any RSA JWT signing-method type rather than pinning the exact expected algorithm.
- `references/memory-safety.md:31-42` uses `math.MaxInt64` for `int` overflow checks and does not handle negative operands/32-bit targets.
- `references/secrets.md:83-88` interpolates a raw password into a MySQL DSN instead of using driver-safe construction/escaping.
- `references/third-party.md:84-91` says internal IDs are not personal data and recommends unsalted truncated SHA-256 for correlation. Linkability remains; use minimization and a keyed construction where correlation is justified.

### Architecture, APIs, and third-party libraries

#### `golang-cli` — MINOR

- `SKILL.md:27` presents Cobra/Viper as the normal choice rather than a scale-dependent option.
- `SKILL.md:71-73` makes `SilenceErrors` and hook choices universal, while bundled examples can silence errors without printing them.
- `SKILL.md:116-123` omits Viper's explicit override layer from precedence. See [Viper precedence](https://github.com/spf13/viper#why-viper).
- `assets/examples/completion.go:20-27` writes directly to `os.Stdout`, bypassing Cobra's configured writer and test capture.

#### `golang-database` — MAJOR

- `references/testing.md:9-10,39,52` defines a mock `GetByID` with two returns while its interface requires three; the example does not compile.
- `SKILL.md:56` gives an unsupported fixed “30-50% faster” pgx claim.
- `SKILL.md:41-45` presents locking, serializable isolation, and pool settings as universal. Transaction/isolation choices depend on the data invariant and database behavior. See [`database/sql`](https://pkg.go.dev/database/sql) and [PostgreSQL locking](https://www.postgresql.org/docs/current/explicit-locking.html).

#### `golang-dependency-injection` — MAJOR

- `SKILL.md:32,113-134` and `references/google-wire.md` recommend Wire as current; its official repository is archived/read-only. See [google/wire](https://github.com/google/wire).

#### `golang-design-patterns` — MAJOR

- `references/resource-management.md:62` says `runtime.AddCleanup` runs even when the object participates in a cycle. The API explicitly does not guarantee cleanup in that case. See [`runtime.AddCleanup`](https://pkg.go.dev/runtime#AddCleanup).
- `SKILL.md:109` treats initialization-file order as a dependable contract. See [package initialization](https://go.dev/ref/spec#Package_initialization).

#### `golang-grpc` — MAJOR

- `references/protoc-reference.md:67-71` labels protoc-gen-validate syntax as Buf Protovalidate. The generators and configurations are distinct. See [Buf generation](https://buf.build/docs/generate/overview/) and [Protovalidate installation](https://buf.build/docs/protovalidate/installation/).
- `SKILL.md:95-115` configures `round_robin` without documenting grpc-go's balancer registration import. See [`roundrobin`](https://pkg.go.dev/google.golang.org/grpc/balancer/roundrobin).
- `SKILL.md:94` requires a timeout for every call, including intentionally long-lived streams; deadline policy should reflect operation semantics.

#### `golang-hexagonal-architecture` — PASS

No material factual, link, or API defect was found. The strict dependency direction is explicit architectural policy. The 808-line entrypoint should still be split for progressive disclosure.

#### `golang-popular-libraries` — MAJOR

- `references/libraries.md:31` falsely says `go-sql-driver/mysql` is maintained by the Go team.
- `references/libraries.md:39` describes the MongoDB Go driver's API as asynchronous; it is synchronous with context-aware operations.
- `references/libraries.md:175,181,183` overstates swag's OpenAPI 3 support, lists archived Wire as current, and gives dig lifecycle-management responsibility it does not have.
- `references/stdlib.md:9` gives stale/incorrect `encoding/json/v2 (golang.org/x/exp/json)` guidance.

#### `golang-solid-principle-review` — PASS

No material factual, API, trigger, or link defect was found. Its evidence and severity model is sound and it explicitly rejects Java-style over-abstraction.

#### `golang-stretchr-testify` — MINOR

- `SKILL.md:183` says `assert.Equal(ptr1, ptr2)` compares addresses. Testify performs deep equality of pointed-to values; use `assert.Same` for identity. See [`assert.Equal`](https://pkg.go.dev/github.com/stretchr/testify/assert#Equal).

## Recommended correction order

1. Quarantine or prominently warn on the MAJOR skills until corrected; prioritize `golang-security`, `golang-concurrency`, `golang-context`, `golang-database`, and all current-library/API skills.
2. Fix non-compiling and nonexistent-API examples, then add lightweight compile/API checks for bundled examples.
3. Correct security and lifecycle claims against primary sources; add targeted eval cases for the corrected traps.
4. Replace package-qualified/missing cross-skill references with canonical local names.
5. Remove agent/model/worktree orchestration from domain skills and defer to repository instructions.
6. Convert universal MUST/NEVER language to invariant-backed rules or qualified defaults.
7. Split oversized entrypoints and add navigation to long references.
8. Re-run the audit and require zero MAJOR findings before presenting the catalog as authoritative.

## Verification and limitations

- All 37 skills passed structural validation before this report was written.
- All bundled JSON/eval files parsed successfully.
- Current local Go version during the audit was Go 1.26.6; the repository also declares Go 1.26.6.
- Third-party API findings target the current released versions observed during the audit. Future releases may require revalidation.
- Privacy/legal findings identify misleading technical assertions; they are not legal advice.
- No agent skill file was modified by this audit.
