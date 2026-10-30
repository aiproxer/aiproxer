# Requirements Document

## Introduction

The terminal-semantics concern set (`clean_text`, `clean_tool_call`, `output_limit`, `upstream_error`, `usage_reported`) currently lives only in test-only registries under `internal/testkit/contract/frontend` and `internal/testkit/contract/backend`. Connector certification through the shared scenario corpus does not inherit that coverage. This slice promotes the five concerns into the public `BaselineScenarioCorpus` metadata so every certified connector executes terminal-semantics scenarios, while the test-only matrices remain the enforcement layer on top.

## V1 Slice

- **Delivers**: Five versioned terminal scenarios in the public corpus that connector certification executes green, with existing connector certifications still passing.
- **First consumer**: The connector contract test suite (`pkg/lipsdk/backendplugin/contracttest`) and the in-process backend-family TCK that already consume `BaselineScenarioCorpus`.
- **Safe degradation**: Subjects that lack a scenario's required capabilities select it negative (executed, rejected, no upstream activation) rather than failing certification.

## Deferred

- Cancellation, refusal, and empty-turn terminal concerns — follow-up: separate slice with its own capability negotiation.
- Provider error-taxonomy mapping beyond the terminal upstream-error signal — follow-up: canonical error-classification slice.
- WebSocket transport rows for the new scenarios — follow-up: transport-parity slice once a certified WS consumer exists.
- Executable-row coverage for optional `connectors/` backends beyond the shared TCK — follow-up: per-connector certification slice.
- New `SemanticFeature` values for terminal concerns — follow-up: only if reusing the existing feature labels proves ambiguous in review.

## Boundary Context

- **In scope**: Public scenario metadata in `pkg/lipsdk/contract`, capability/transport selection rules, connector TCK execution of the new scenarios, continued green certification of existing subjects.
- **Out of scope**: Changing canonical event semantics, changing terminal translation in adapters, adding transports, changing billing/usage policy.
- **Adjacent expectations**: The frontend and backend terminal-semantics matrices keep enforcing per-family behavior; this slice gives them corpus IDs to reference but does not move their builders. Core routing, B2BUA, and billing seams are untouched.
- **Boundary ownership**: SDK public contract (`pkg/lipsdk/contract` metadata only); runners own builders; core owns nothing new here.

### Requirement 1: Versioned terminal scenarios in the public corpus

**Objective:** As a connector author, I want stable versioned terminal scenario IDs in the public corpus, so that certification coverage is inherited rather than re-declared per connector.

#### Acceptance Criteria

1. When the corpus is enumerated, the scenario corpus shall contain exactly five new scenarios with IDs `terminal-clean-text`, `terminal-clean-tool-call`, `terminal-output-limit`, `terminal-upstream-error`, and `terminal-usage-reported`.
2. When an existing consumer pins a pre-promotion scenario ID, the scenario corpus shall resolve it with byte-identical ID, feature, and transport.
3. When a new terminal scenario is recorded, the scenario corpus shall label it with an existing feature value and declare explicit prerequisites and expected evidence.
4. If two scenarios share one corpus, the scenario corpus shall reject duplicate scenario IDs.

### Requirement 2: Transport mapping and selection rules

**Objective:** As a TCK runner, I want each terminal scenario to declare its transport and capability requirements, so that subjects select positive or negative deterministically.

#### Acceptance Criteria

1. When the corpus is enumerated, the terminal scenarios for clean text, clean tool call, and output limit shall declare HTTP transport, and the scenarios for upstream error and usage reporting shall declare streaming transport.
2. When a subject declares the tools capability, the scenario selector shall select `terminal-clean-tool-call` positive, and when the subject omits it, the selector shall select it negative.
3. When a subject declares the streaming capability, the scenario selector shall select `terminal-upstream-error` and `terminal-usage-reported` positive, and when the subject omits it, the selector shall select them negative.
4. When a subject omits a scenario's required capability, the scenario selector shall still list that scenario exactly once in the negative set rather than dropping it.
5. When a frontend-kind subject is evaluated, the scenario selector shall apply the existing kind-and-transport allowance unchanged for every pre-promotion scenario.

### Requirement 3: Connector certification inherits terminal coverage

**Objective:** As a release operator, I want connector certification to execute the terminal scenarios green, so that terminal semantics are proven at the connector boundary.

#### Acceptance Criteria

1. When a capable connector is certified, the connector TCK shall execute all five terminal scenarios with frame and terminal evidence and report zero failures.
2. When an incapable connector is certified, the connector TCK shall record the unmet terminal scenarios as executed hard negatives with no upstream activation.
3. When an existing certified connector is re-certified after promotion, the connector TCK shall still validate its artifact with exact corpus coverage and no new failures.
4. While a terminal scenario executes, the connector TCK shall observe usage evidence for `terminal-usage-reported` and error evidence for `terminal-upstream-error` before the terminal frame.

### Requirement 4: Test-only matrices stay the enforcement layer

**Objective:** As a backend-family maintainer, I want the per-family matrices to remain the enforcement layer, so that corpus promotion adds inheritance without removing family-specific proof.

#### Acceptance Criteria

1. When the corpus gains the terminal scenarios, the frontend and backend terminal-semantics matrices shall retain every existing concern cell and covering test.
2. When a matrix cell covers a promoted concern, the matrix cell shall reference the corresponding corpus scenario ID as its shared semantic anchor.
3. When the corpus changes, the scenario builders shall remain in the transport-specific runners and no builder logic shall move into the contract metadata package.
4. When the promotion lands, no core package shall gain an import on a concrete plugin, provider SDK, or test-only registry.
