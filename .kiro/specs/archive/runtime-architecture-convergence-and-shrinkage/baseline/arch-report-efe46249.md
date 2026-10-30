# Architecture report

Advisory only; produced by `make arch-report`.

Module: `github.com/matdev83/go-llm-interactive-proxy`

## Non-test lines by package (top 25)

| Package | Lines |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime` | 13767 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle` | 9898 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/archtest` | 7452 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk` | 5891 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex` | 5089 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp` | 4983 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore` | 4677 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions` | 4249 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/config` | 4210 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app` | 3831 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app` | 3186 |
| `github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi` | 3127 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost` | 3056 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore` | 2766 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins` | 2729 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses` | 2446 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane` | 2373 |
| `github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane` | 2297 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/routing` | 2270 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair` | 2213 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp` | 2106 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/testkit` | 2031 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation` | 2001 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore` | 1954 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/testkit/reasoninge2e` | 1910 |

## Hotspot files (critical-file budgets)

| File | Lines |
| --- | --- |
| `internal/core/runtime/executor.go` | 125 |
| `internal/infra/runtimebundle/build.go` | 105 |
| `internal/infra/runtimebundle/options.go` | 233 |
| `internal/standardplugins/standard_table.go` | 283 |
| `internal/pluginreg/reg.go` | 312 |
| `internal/stdhttp/server.go` | 167 |

## Direct internal import fan-out (top 20)

| Package | DirectInternalImports |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle` | 82 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins` | 55 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime` | 40 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance` | 32 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp` | 20 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses` | 18 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/testkit` | 18 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy` | 16 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex` | 15 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses` | 14 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic` | 14 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini` | 14 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat` | 13 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy` | 11 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages` | 10 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon` | 9 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/diag` | 7 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics` | 7 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic` | 7 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama` | 7 |

## Direct internal import fan-in (top 20)

| Package | ImportedBy |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend` | 33 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/routing` | 31 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool` | 21 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/config` | 20 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/diag` | 15 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/stream` | 15 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain` | 13 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua` | 12 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/identity` | 12 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle` | 11 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app` | 10 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app` | 10 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog` | 9 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/utils` | 9 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate` | 8 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence` | 8 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app` | 8 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/infra/db` | 8 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/auth` | 7 |
| `github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload` | 7 |

## Exported symbols (public contracts)

| Package | ExportedSymbols |
| --- | --- |
| `pkg/lipapi` | 319 |
| `pkg/lipsdk` | 39 |

## Hexagonal baseline classifications

| Package | Class | Role | Retirement target / next extraction |
| --- | --- | --- | --- |
| `pkg/lipapi` | aligned | - |  |
| `cmd/lipstd` | aligned | - |  |
| `internal/stdhttp` | aligned | - |  |
| `internal/pluginreg` | aligned | - |  |
| `internal/standardplugins` | aligned | - |  |
| `internal/featurebundle` | aligned | - |  |
| `internal/infra/runtimebundle` | aligned | composition_root | aligned (composition-root allowance) → Import narrowing is out of scope for v1; composition root legitimately imports wide core surface after Phase 2 decomposition |
| `internal/core/runtime` | aligned | - |  |
| `internal/core/routing` | aligned | - |  |
| `internal/core/execbackend` | aligned | - |  |
| `internal/core/extensions` | aligned | - | aligned → Feature merge moved to featurebundle; extensions package is core extension execution only |
