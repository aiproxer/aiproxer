# Requirements traceability (base-api-connector-porting)

Mapping each **N.M** acceptance criterion from [requirements.md](requirements.md) to primary implementation or test evidence. Generated for implementation validation (2026-04-24).

| ID | Primary evidence |
|----|------------------|
| **1.1** | Standard bundle installs four hosted backends: `internal/pluginreg/standardbundle`, `internal/plugins/backends/{openairesponses,openailegacy,anthropic,gemini}` |
| **1.2** | Out-of-scope OAuth connectors not modified; scope stated in requirements §Out of scope |
| **1.3** | `internal/pluginreg/bedrock_acp_install_regression_test.go` — Bedrock/ACP paths unchanged by hosted YAML key normalization |
| **2.1** | Conformance + integration tests exercise canonical mapping/streaming (`internal/testkit/conformance`, backend `integration_test.go` files) |
| **2.2** | `internal/plugins/backends/credpool` — pool-local credential IDs; `internal/pluginreg` — one backend instance, multiple keys |
| **2.3** | Conformance harness uses deterministic fixtures; no Python topology in Go adapters |
| **3.1** | `internal/pluginreg/backends_install.go` — instance fields (base URL, provider params) separate from `api_keys` |
| **3.2** | `internal/pluginreg/hosted_backend_build_test.go`, `effective_api_keys_test.go` — single instance id with key list |
| **3.3** | Same + `credpool.New` from merged key list in each official backend `plugin.go` |
| **3.4** | `internal/core/runtime/executor_backend_credentials_test.go` — candidate/backend identity without per-credential routing ids |
| **4.1** | `credpool.Pool` state vs backend id in `plugin.go` factories |
| **4.2** | `credpool` cooldown state (`StateCooldown`), not new backend instances |
| **4.3** | `credpool/retry_after.go`, `retry_after_test.go` |
| **4.4** | `credpool` auth-invalid vs exhaustion; `ErrNoUsableCredential` when none remain |
| **5.1** | Backend `invoke.go` rotation loops before first output; integration tests in each backend package |
| **5.2** | `executor_backend_credentials_test.go` — post-output behavior / no silent credential switch |
| **5.3** | `ErrNoUsableCredential` handling in `openairesponses`, `openailegacy`, `anthropic`, `gemini` `plugin.go` |
| **5.4** | Architecture: core `internal/core/runtime` vs adapter `invoke.go`; executor tests |
| **6.1** | Hosted YAML decode + build tests preserve base URL / params |
| **6.2** | `hosted_yaml_api_keys_test.go`, `hosted_backend_build_test.go` |
| **6.3** | `internal/pluginreg/keys.go`, `resolve_upstream_api_keys_test.go` (e.g. `OPENAI_API_KEY_2`) |
| **6.4** | `config/config.multi-instance.example.yaml`; multi-instance vs multi-key distinction |
| **7.1** | `internal/plugins/backends/openaicred` — shared OpenAI-family credential seam |
| **7.2** | `openaicred` used by `openairesponses` and `openailegacy` |
| **7.3** | Separate `anthropic`, `gemini` packages with provider-specific `invoke.go` / classifiers |
| **7.4** | Thin overlays: `stream_prepend.go`, `errors_classify.go` per provider |
| **7.5** | Narrow `credpool` + `openaicred` boundaries (no catch-all shared connector package) |
| **8.1** | Capability negotiation failures preserved in backend integration tests |
| **8.2** | Same; explicit errors vs silent drop |
| **8.3** | Anthropic/Gemini-specific capability paths in `invoke.go` / tests |
| **9.1** | Streaming-first `invoke.go` patterns + conformance stream tests |
| **9.2** | `map_events_*_test.go`, conformance event-order checks |
| **9.3** | Non-stream collected from stream path (existing backend design) |
| **9.4** | `internal/testkit/conformance/backend_credentials_test.go` — tools, multimodal, usage |
| **10.1** | `internal/refbackend/*` servers + `internal/refclient/*` tests |
| **10.2** | `refbackend/*/server_test.go` forced 401/429; backend `integration_test.go` against ref servers |
| **10.3** | Documented limits only where applicable (conformance comments if any); default is full emulator coverage |
| **11.1** | Test-first task list; packages above contain failing-then-passing regression tests |
| **11.2** | `credpool/pool_test.go`, `pool_snapshot_test.go`, backend integration rotation + `executor_backend_credentials_test.go` |
| **11.3** | `executor_backend_credentials_test.go` |
| **11.4** | `openaicred` tests + `openairesponses`/`openailegacy` integration tests proving shared seam without mapping drift |

## Test commands (validation gate)

- `go test -parallel=8 ./...`
- `go test -parallel=8 -tags=precommit ./...`

Both passed on 2026-04-24.
