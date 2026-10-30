# `codex-client-compat` behavior disposition matrix

Task 9.2 assessment for `internal/plugins/features/codexclientcompat`. This is a
classification, not the removal: Task 12.2 remains deferred behind the human gate
(Requirement 12.1 prohibits pre-gate removal; approval is recorded by Task 11.5),
so every behavior below stays in place until that gate and a replacement
disposition. Nothing in this matrix authorizes retargeting the feature to
`openai-chatgpt-plan` or copying an `openai_codex.*` key into the new connector.

Dispositions:

- **KEEP** — behavior remains justified after the gate; no migration needed.
- **GENERALIZE** — provider-neutral fix that may belong at a canonical,
  frontend, or generic Responses seam. Not moved here: no failing generic-path
  evidence exists yet, so it stays feature-owned until Task 12.2 / Task 11.3
  produce it.
- **REMOVE** — Codex-only targeting, prompt compensation, or extension. Delete
  after the gate and the legacy connector deletion required by Requirement 11.5.

## Hazard: the target is a configured instance id, not a connector kind

`requestPartHook.HandleRequestParts` compares `sdk.PartMeta.BackendID` with the
literal `"openai-codex"` (`plugin.go`). Core populates that field from
`c.Primary.Backend` (`internal/core/runtime/executor_open_attempt.go`), which is
the configured backend instance id (`PluginConfig.ID`), not the factory kind
(`PluginConfig.FactoryID()`, `internal/core/config/model.go`). `sdk.PartMeta`
has no kind field, so the hook cannot distinguish the two.

Configuration-only consequences:

1. A migrated `openai-chatgpt-plan` row that keeps the instance id
   `openai-codex` is still targeted. Task 9.1 configures the new connector as
   `plan-harness` with the feature disabled
   (`TestHostIntegration_OpenAIChatGPTPlanHarnessRequestsWithoutCodexPromptBridges`);
   it proves the disabled baseline, not a kind-level exemption.
2. A legacy `openai-codex` row renamed to another instance id (for example
   `codex-primary`) is not targeted, so the bridges silently disappear.
3. When targeted, the hook adds the bridge to `Instructions` and prepends the
   same block as the first system `Messages` item. The new projector folds both
   (`projectInstructions`, `connectors/openaichatgptplan/internal/responses/payload.go`)
   and does not deduplicate exact blocks, so the new provider request would carry
   the bridge twice. Legacy `instructionHasBlock`
   (`connectors/codex/internal/codex/payload_input.go:119–127`) deduplicates only
   an exact single `\n\n`-delimited block: it compares the whole incoming system
   text with each split paragraph. The tool-bearing OpenCode, Pi, and Droid
   bridges contain a `\n\n` before their critical-instruction paragraph
   (`bridge_opencode.go:204–215`, `bridge_pi.go:78`, `bridge_droid.go:74`), so
   that multi-paragraph bridge matches no single paragraph and is duplicated by
   legacy too. The hook also writes `openai_codex.ignore_unsupported_gen_params`
   on every targeted call and `openai_codex.tool_strict=false` for Hermes. The new
   preview policy classifies those keys as unsupported protocol extensions
   (omitted in `interoperable` mode; refused in `strict` mode when the call
   reaches extension admission), and independently omits any unsupported
   generation options with explicit `Omission`s
   (`TestPreviewPolicyDecidesProtocolExtensions`,
   `TestPreviewPolicyDoesNotAdmitCodexCompatExtensionKeys`,
   `TestPreviewPolicyOmitsEveryUnsupportedGenerationOption`).
4. Source-level, not live-verified: the registered generic OpenResponses
   frontend uses `internal/plugins/protocols/openresponses/decode_request.go`,
   which sets `Call.Items` (lines 233–240), so an item-authority call can reach
   this connector in-tree. The hook always adds
   `openai_codex.ignore_unsupported_gen_params` on a targeted call, but it does
   not always add `Instructions`/`Messages`, even when a bridge is selected.
   When a targeted item-authority call does receive those additions, core
   rejects the mixed authority in post-hook `attempt.Validate`
   (`internal/core/runtime/executor_attempt_transform.go:111–112`) before
   provider preview admission. Final call adaptation
   (`executor_open_attempt.go:988–1016`) happens later and does not replace the
   hook's input. This remains source-level analysis, not a live failure.

Focused characterization: `TestRequestPartHook_targetsConfiguredInstanceNameNotConnectorKind`.
Runtime evidence that the field is the route instance id:
`TestExecutor_requestPartHook_metaIncludesBLeg` in
`internal/core/runtime/executor_test.go`.

Safe follow-up for Task 12.2: remove the name-based targeting instead of replacing it
with `openai-chatgpt-plan`; select any surviving behavior by a backend-neutral
capability or feature-owned condition, and add a negative test with a new-kind
instance named `openai-codex`.

## Matrix

### Targeting and inputs

| # | Current behavior | Evidence | Disposition | Reason / follow-up |
|---|---|---|---|---|
| 1 | Hook acts only when `PartMeta.BackendID == "openai-codex"` | `TestTargetBackendIDMatchesOpenAICodexID`; `TestRequestPartHook_noopWhenBackendNotOpenAICodex`; `TestRequestPartHook_mutatesWhenBackendIsOpenAICodex`; new targeting test; runtime meta test | **REMOVE** (post-gate) | Instance-name targeting is the collision hazard. Do not retarget it to the new kind (Requirement 11.2). |
| 2 | Identity inputs from the `agent`, `user_agent`, `openai_codex.agent`, and `headers` canonical extensions | `TestDetectOpenCodeFromExtensionAgent`; `TestDetectHermesFromExtensionAgent`; new headers test; repository search found no in-tree production writer for any of the four keys; the legacy connector only reads them for continuation partitioning | **REMOVE** (post-gate) | No current in-tree producer. If identity evidence is still needed, read canonical `Call.Invocation.ClientUserAgent` or session-classification evidence instead of bespoke extensions (Requirement 11.4). |
| 3 | Agent-family matching through `internal/agentfacts` plus the feature's legacy substring fallbacks | `TestAgentMatchersUseSharedIdentityRules`; `TestAgentMatchersPreserveLegacySubstringFallbacks`; `TestPiAgentMatchRejectsSharedOnlyVersionPrefixes`; `TestApplyCompatLeavesCallsWithSharedOnlyPiVersionPrefixesUnchanged` | **REMOVE** with the identity inputs | Matching exists only to select a bridge. The shared catalog is already provider-neutral; this feature's looser fallbacks are not. |
| 4 | Prompt-signature detection for OpenCode, Pi, Droid, and Hermes | `TestDetectPiFromPromptMarkers`; `TestDetectDroidIgnoresToolListOnly`; `TestDetectHermesFromExactIdentityPrompt`; `TestDetectHermesIgnoresGenericMention`; `TestApplyCompat_openCodeWinsOverDroidTools` | **REMOVE** (post-gate) | Client-name/prompt compensation; Requirements 5.7 and 11.5. The new connector must work without family detection (Task 9.1 / Task 11.3). |
| 5 | Removal of harness-identifying system messages from `Instructions` and `Messages` | `TestApplyOpenCodeCompat_dedupBridgeOrphanToolOutput`; `TestApplyPiCompat_dedupBridgeNotDuplicated`; `TestApplyDroidCompat_bridgeFromTools`; new no-tools test | **REMOVE** (post-gate) | Deletes client-authored system content. The new projector folds lawful system text into `instructions`; it does not need a client-name filter (Requirements 5.3/5.4). |

### Prompt bridges

| # | Current behavior | Evidence | Disposition | Reason / follow-up |
|---|---|---|---|---|
| 6 | OpenCode bridge: generic tool guidance, no-tools textual-tool-call guard, `apply_patch`/plan-tool guidance, harness-prompt deduplication | `TestApplyOpenCodeCompat_dedupBridgeOrphanToolOutput`; `TestApplyOpenCodeCompat_noToolsAddsTextualToolCallGuard`; `TestApplyCompat_noMarkerNoToolsPlainChatDoesNotAddOpenCodeGuard`; `TestRequestPartHook_openCodeCompatPayloadShape`; Task 9.1 baseline | **REMOVE** (post-gate) | Task 9.1 proves an OpenCode-shaped request completes with no bridge when the feature is disabled. Requirement 11.5. |
| 7 | Pi bridge: shell/edit tool-shape guidance and critical instruction | `TestApplyPiCompat_dedupBridgeNotDuplicated`; new no-tools test | **REMOVE** (post-gate) | Prompt compensation. Pi is not covered by Task 9.1; re-evaluate only if the Task 11.3 human harness gate selects Pi and it fails without the bridge. |
| 8 | Droid bridge: native tool-name catalogue and argument shapes | `TestApplyDroidCompat_bridgeFromTools`; `TestDetectDroidIgnoresToolListOnly`; new no-tools test | **REMOVE** (post-gate) | Prompt/tool-name compensation. Re-evaluate only if the Task 11.3 human harness gate selects Droid and it fails without the bridge. Tool names only shape the bridge prose; they never select a bridge. |
| 9 | Hermes bridge plus `openai_codex.tool_strict=false` | `TestApplyHermesCompat_idempotentBridgeCountOne`; `TestApplyHermesCompat_preservesInstructionIdentity`; `TestApplyHermesCompat_payloadShapeIncludesBridgeAndToolStrictFalse`; `TestRequestPartHook_noopForHermesOnNonCodexBackend`; new no-tools test; legacy `payload.go` reader | **REMOVE** (post-gate) | Task 9.1 proves a Hermes-shaped request completes with no bridge. `tool_strict` is a legacy Codex payload switch; never copy the key (Requirement 11.3). |
| 10 | Legacy `You are Codex...` identity fallback when a bridge is selected and instructions are empty | New `TestApplyCompat_emptyInstructionsUsesLegacyCodexIdentity`; the legacy connector has its own equivalent default | **REMOVE** (post-gate) | Explicitly prohibited prompt compensation (Requirements 5.1/11.5). The hook should never invent instructions. |

### Tool history, generation options, and wiring

| # | Current behavior | Evidence | Disposition | Reason / follow-up |
|---|---|---|---|---|
| 11 | Convert tool results whose call id is absent from assistant history into system text (`Prior tool output...`) while preserving known results | `TestConvertOrphanedToolResultsPreservesPartOrder`; `TestConvertOrphanedToolResultsLeavesKnownOnlyMessagesUntouched`; `TestApplyOpenCodeCompat_keepsToolResultForChatCompletionsToolCall`; `TestApplyOpenCodeCompat_preservesMatchedToolProtocolWithTools` | **GENERALIZE** (candidate, deferred) | Provider-neutral tool-history normalization. The new connector has no equivalent and no generic-path failure has been demonstrated, so do not move it now. Reassess at Task 12.2 / Task 11.3. |
| 12 | Marker-less fallback to the OpenCode guard for a no-tools call that carries a structured tool transcript | `TestApplyCompat_noMarkerNoToolsToolHistoryAddsTextualToolCallGuard`; `TestApplyCompat_noMarkerNoToolsPlainChatDoesNotAddOpenCodeGuard`; `fallbackCompatBridge` | **GENERALIZE** (candidate, deferred) | The trigger is transcript shape, not client name, but the output is OpenCode-specific prose. Same evidence gate as row 11; Requirement 11.6 prefers a canonical/frontend/generic Responses seam when justified. |
| 13 | Write `openai_codex.ignore_unsupported_gen_params=true` on every targeted call; all in-process backends install the feature decoder as `IgnoresAuthorityMaxOutputTokensClamp` | `TestApplyCompat_setsIgnoreUnsupportedGenParamsExt`; `TestRequestPartHook_codexBackendSetsIgnoreUnsupportedGenParamsWithoutClientMarker`; `TestRequestPartHook_nonCodexBackendDoesNotSetIgnoreUnsupportedGenParamsWithoutClientMarker`; `TestIgnoresClampViaCodexUnsupportedGenParams`; `TestPreviewPolicyDoesNotAdmitCodexCompatExtensionKeys`; decoder installs in `openaicompat`, `openai-legacy`, `openai-responses`, `anthropic`, `gemini`, `bedrock`, and the external-adapter bridge | **REMOVE** feature key; **KEEP** the provider-neutral callback seam | The new connector's preview policy already omits unsupported generation options with explicit `Omission`s (`TestPreviewPolicyOmitsEveryUnsupportedGenerationOption`) and refuses the legacy keys as protocol metadata. Keep `execbackend.IgnoresAuthorityMaxOutputTokensClamp` as a generic seam, but detach the codex-named decoder from generic backends and never add the key to the new connector. |
| 14 | Feature registration, `order` config, example config, and the featurehost neutrality fixture that requires this hook on the `openai-codex` instance | `TestDecodeConfig_emptyDocumentNode`; `internal/standardplugins/features_install.go`; `config/examples/opencode-codex.yaml`; `sessionclassification_bundled_feature_neutrality_test.go` (requires this hook on `openai-codex`) | **REMOVE** with the feature (post-gate) | Task 12.2 must replace the neutrality fixture's characterized feature or backend id and remove the example stanza. The `order` knob has no migration value. |
| 15 | Docs claim the hook detects clients from "tool names" and no-ops for other backends | `docs/openai-codex-backend.md`; `TestDetectDroidIgnoresToolListOnly`; `selectCompatBridge`; `plugin.go:33–41` | **REMOVE** with the feature (correct both statements earlier if the doc survives until Task 12.2) | Tool names never select a bridge; they only shape Droid prose. The hook compares the configured instance id only, irrespective of factory kind, so the "other backends" no-op is really "other configured instance IDs": a new-kind instance named `openai-codex` remains targeted. |

## Verification

```sh
go test ./internal/plugins/features/codexclientcompat/... -count=1
go test ./internal/core/runtime -run '^TestExecutor_requestPartHook_metaIncludesBLeg$' -count=1
go test ./internal/testkit/backendplugin -run '^TestHostIntegration_OpenAIChatGPTPlanHarnessRequestsWithoutCodexPromptBridges$' -count=1
go -C connectors/openaichatgptplan test ./internal/responses -run '^TestPreviewPolicy' -count=1
go test ./internal/plugins/frontends/openailegacy ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openresponses -count=1
```

The `TestPreviewPolicy*` cases run in the separate connector module; the last
command covers the targeted frontend packages.

No production source changed for this matrix. The legacy feature, its extensions,
and its config remain in place until the human gate (Requirement 12.1) and
Task 12.2.
