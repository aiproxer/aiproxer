# Implementation Plan

One PR. TDD per task: write the failing test, then the smallest change. Recheck cited paths on the implementation branch (rename #641 may move packages; the `aipca1_` value contract does not change).

- [x] 1. Feature derivation and transform
  - Create `internal/plugins/features/downstreamcacheaffinity/transform.go` with `ID`, `Derive`, `Transform` exactly as design §1.1–1.2.
  - Tests: fixed derivation vector, length 50, URL-safe alphabet, backend/session separation; transform table (explicit legacy PCK, semantic-extension PCK, alias conflict untouched, no authoritative session, client hint only, generated).
  - _Requirements: 2.1–2.5, 3.1–3.3, 5.3_
  - _Boundary: feature package_

- [x] 2. Register the opt-in feature
  - Add `featureDownstreamCacheAffinity` in `internal/standardplugins/features_install.go` and the row in `StandardBundle().Features` (`standard_table.go`).
  - Tests: enabled → exactly one `PlaneAttemptTransforms` occupant; absent and `enabled: false` → zero occupants; non-empty config rejected.
  - _Requirements: 1.1–1.5_
  - _Boundary: standardplugins_
  - _Depends: 1_

- [x] 3. End-to-end hint delivery
  - One standard-runtime test: feature enabled, authoritative session, fake backend → `Open` receives `Derive(backendID, sessionID)` and empty session fields; explicit client PCK reaches `Open` unchanged.
  - If any production stage after attempt transforms overwrites PCK, STOP and report; do not add a new stage.
  - _Requirements: 3.4, 5.1_
  - _Depends: 2_

- [x] 4. Forward PCK in direct OpenAI serializers
  - `openairesponses.ParamsForCall` and `openailegacy.ParamsForCall` set the typed `PromptCacheKey` from `PromptCacheKeyValue()`; conflict returns an error; empty omits.
  - Tests: explicit, empty, conflict for both.
  - _Requirements: 4.1_

- [x] 5. Gate compatible-family forwarding
  - Add `InvokeRequest.ForwardPromptCacheKey`; `OpenChat`/`OpenResponses` clear PCK when false. Thread one bool through `BuildCompatibleWithHeaders`; plain custom-compatible construction passes `false`. This gate prevents sending undocumented fields that may be rejected; actual rejection follows existing pre-output error policy.
  - Tests: plain custom Chat and Responses rows omit PCK.
  - _Requirements: 4.2, 4.5_
  - _Depends: 4_

- [x] 6. Profile quirk and catalog
  - Add `QuirkOpenAIPromptCacheKey = "openai.prompt_cache_key"` allowed for the two OpenAI families; turn `quirkAllowed` (`schema.go:496-500`) from its single-expression form into a switch, or the new quirk fails validation as `unknown quirk`. The two profile family builders pass it to the constructor from the compiled profile.
  - Add `"quirks": ["openai.prompt_cache_key"]` to the `mistral` and `deepinfra` rows in `catalog.json`. Do not add it to any other row; the evidence table in `research.md` records why each candidate is excluded.
  - Tests: quirk accepted/rejected per family; profile with quirk forwards through the real `provider-profile` → `wrapCompatibleLifecycle` build path; same family without quirk omits; the `groq`, `deepseek-openai` and `alibaba` rows omit.
  - _Requirements: 4.2–4.5_
  - _Depends: 5_

- [x] 7. Config example and close-out
  - Add a short README section in `internal/plugins/features/downstreamcacheaffinity/` with the YAML example and the large-payload trade-off.
  - Run `make dev-test-changed`, `make quality-checks`; record results in the PR. Open follow-up issues for the Deferred list only when requested.
  - Archive this spec after merge.
  - _Depends: 1–6_

## Completion Status

- [x] Implementation merged in [PR #915](https://github.com/matdev83/go-llm-interactive-proxy/pull/915), published head `a59c79ad866050587d3be408c76cde7a069d908b`, squash merge and certified `main` baseline `e1559d2d91f4c81f079fbb97dc51f76ec6e781e3` (no additional arrivals).
- [x] Configuration example and canonical-path/large-payload trade-off are merged in `internal/plugins/features/downstreamcacheaffinity/README.md`. Task 7 local changed-module and quality results are recorded in PR #915: `make dev-test-changed DEV_BASE=1c11b1ac` passed across 42 maintained modules; canonical root/parity tests passed and selected-scope quality lint reported zero issues. These are implementation-head results, not fresh archive-worktree runs or exhaustive all-module lint certification.
- [x] Fresh merged-main verification passed using normal PATH `go`, `GOTOOLCHAIN=go1.26.9`, `TMPDIR=/var/tmp/lip` (ext4):

  ```sh
  go test -count=1 ./internal/plugins/features/downstreamcacheaffinity ./internal/plugins/backends/openaicompat ./internal/plugins/backends/openailegacy ./internal/plugins/backends/openairesponses ./internal/providerprofiles ./internal/standardplugins
  go build -o /tmp/opencode/cache-affinity-sdd/merged-main/lipstd ./cmd/lipstd
  make installed-plugin-smoke
  go run ./tools/kiro/kirocheck
  ```

  Source-bound results/logs: `/tmp/opencode/cache-affinity-sdd/merged-main-impl.json` and `/tmp/opencode/cache-affinity-sdd/merged-main/`. The profile-registry HTTP tests certify explicit and generated keys for both Chat and Responses families: quirked forwarding, unquirked/custom omission, and omission for embedded `groq`, `deepseek-openai`, `alibaba`. Only the documented `mistral` and `deepinfra` Chat catalog rows opt in; synthetic Responses-family certification does not add a catalog opt-in.
- [x] Whole five-file bundle moved to `.kiro/specs/archive/downstream-cache-affinity-hinting/`, with completion metadata and approvals preserved. Archive delivery remains subject to independent review and subsequent publication/merged-main verification.

Feature delivery is a layered in-process admitted-session/executor test plus real profile-adapter HTTP capture, not a single feature-enabled built-CLI client-to-provider test. Installed-plugin smoke separately spawns `lipstd`, installs executable connectors, and exercises config/inspect/doctor/serve with the same binary; it does not enable this feature. No live-provider or local race certification is claimed; remote race evidence is not implied by PR success. Advisory hints do not guarantee cache hits, and only actual pre-output rejection follows existing classification and policy-controlled retry/failover.

The requirements' Deferred list is unchanged; no successor/core changes or new follow-up issues are part of this archive. Issue #573 closure and owned-resource cleanup remain controller-owned separate operations; this spec completion is not whole-session closeout.
