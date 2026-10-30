# Requirements Document

## Introduction
The Go LLM Interactive Proxy needs database persistence options that prepare future deployments for both local single-node operation and managed database operation. This feature defines the expected behavior for configurable persistence across continuity and secure-session stores while preserving existing store contracts, current SQLite behavior, runtime behavior, and operator-safe failure modes.

## Boundary Context
- **In scope**: selectable persistence backends for continuity and secure sessions, durable behavior parity across supported database backends, configuration validation, startup failure behavior, operator-facing documentation of supported settings, and optional validation for externally managed databases.
- **Out of scope**: automatic migration of existing data between database products, changes to client-facing LLM protocols, changes to canonical request or event contracts, new routing semantics, distributed coordination guarantees beyond the selected store backend, and changes to public store contracts.
- **Adjacent expectations**: existing continuity and secure-session store contracts remain the behavioral source of truth; external database availability and credentials are supplied by the operator; current local durable behavior remains supported; future implementation design may choose the internal abstraction details without changing these requirements.

## Requirements

### Requirement 1: Configurable Persistence Selection
**Objective:** As an operator, I want to choose the persistence backend for continuity and secure sessions independently, so that deployments can match local development, single-node, or managed database needs.

#### Acceptance Criteria
1. When the operator configures continuity persistence, the LLM Interactive Proxy shall accept the supported values for in-memory, local durable database, and managed durable database operation.
2. When the operator configures secure-session persistence, the LLM Interactive Proxy shall accept the supported values for in-memory, local durable database, and managed durable database operation.
3. When the operator selects different supported persistence backends for continuity and secure sessions, the LLM Interactive Proxy shall configure each store independently.
4. When the operator omits optional persistence settings, the LLM Interactive Proxy shall preserve the existing default behavior for continuity and secure sessions.
5. When the operator selects local durable database operation, the LLM Interactive Proxy shall preserve compatibility with existing local durable deployments.
6. When the operator selects managed durable database operation, the LLM Interactive Proxy shall require connection settings for the selected managed database.
7. If the operator configures an unsupported persistence value, then the LLM Interactive Proxy shall reject the configuration with a clear validation error naming the invalid setting.
8. If the operator selects a durable backend without the required connection setting for that backend, then the LLM Interactive Proxy shall reject the configuration with a clear validation error naming the missing setting.

### Requirement 2: Continuity Store Behavior Parity
**Objective:** As an operator, I want continuity persistence to behave consistently across supported durable backends, so that routing recovery and attempt lineage remain reliable regardless of deployment database choice.

#### Acceptance Criteria
1. When continuity uses a durable backend, the LLM Interactive Proxy shall preserve A-leg records across process restarts.
2. When continuity uses a durable backend, the LLM Interactive Proxy shall preserve B-leg sequence allocation and attempt lineage across process restarts.
3. When multiple workers or requests allocate B-legs for the same A-leg, the LLM Interactive Proxy shall preserve monotonic B-leg sequence behavior for supported durable backends.
4. When a client request creates multiple backend attempts before visible output, the LLM Interactive Proxy shall record each attempt in continuity lineage with the same observable semantics across supported durable backends.
5. When continuity attempts are loaded for an existing A-leg, the LLM Interactive Proxy shall return them in sequence order across supported durable backends.
6. If the configured continuity durable backend is unavailable during startup, then the LLM Interactive Proxy shall fail startup with an operator-visible error instead of silently falling back to another backend.

### Requirement 3: Secure-Session Store Behavior Parity
**Objective:** As an operator, I want secure-session persistence to behave consistently across supported durable backends, so that session resume, audit, transcript, and usage evidence remain dependable.

#### Acceptance Criteria
1. When secure sessions use a durable backend, the LLM Interactive Proxy shall preserve session records across process restarts.
2. When secure sessions use a durable backend, the LLM Interactive Proxy shall preserve resume fingerprints, A-leg links, activity timestamps, policy metadata, transcript entries, audit entries, attempt evidence, and usage totals with the same observable semantics across supported durable backends.
3. When secure-session activity is updated concurrently, the LLM Interactive Proxy shall preserve monotonic activity and evidence behavior for supported durable backends.
4. When secure-session diagnostics summaries are requested, the LLM Interactive Proxy shall report summaries consistently across supported durable backends.
5. When mandatory durable audit is configured with any supported durable secure-session backend, the LLM Interactive Proxy shall allow startup if all other secure-session requirements are satisfied.
6. If mandatory durable audit is configured with a non-durable secure-session backend, then the LLM Interactive Proxy shall reject the configuration with a clear validation error.

### Requirement 4: Operator-Safe Database Configuration
**Objective:** As an operator, I want database connection settings to be explicit and validated, so that deployment mistakes fail early and do not expose sensitive details unnecessarily.

#### Acceptance Criteria
1. When the operator supplies database connection settings, the LLM Interactive Proxy shall validate required fields before serving traffic.
2. If a database connection setting is malformed or unsafe for its selected backend, then the LLM Interactive Proxy shall reject the configuration with a clear validation error.
3. When startup fails because a database cannot be opened or prepared, the LLM Interactive Proxy shall report which configured store failed without exposing full secret material from connection settings.
4. When startup fails because a configured database cannot be opened or prepared, the LLM Interactive Proxy shall not silently fall back to a different persistence backend.
5. Where database pool tuning is included, the LLM Interactive Proxy shall reject invalid tuning values before serving traffic.
6. When sample configuration is provided, the LLM Interactive Proxy shall document the supported durable backend settings in a way operators can adapt without changing client integrations.

### Requirement 5: Backward Compatibility and Explicit Non-Migration
**Objective:** As a maintainer, I want the new persistence options to preserve existing behavior and contracts, so that deployments can adopt them without breaking current integrations.

#### Acceptance Criteria
1. When existing configurations use in-memory continuity, the LLM Interactive Proxy shall preserve the existing in-memory continuity behavior.
2. When existing configurations use local durable continuity, the LLM Interactive Proxy shall preserve the existing local durable continuity behavior.
3. When existing configurations use in-memory or local durable secure sessions, the LLM Interactive Proxy shall preserve the existing secure-session behavior.
4. When current callers use continuity or secure-session stores, the LLM Interactive Proxy shall preserve the existing store contracts.
5. When local durable database behavior is used, the LLM Interactive Proxy shall not require operators to migrate existing local durable data as part of adopting this feature.
6. The LLM Interactive Proxy shall not automatically migrate existing data between database products as part of this feature.

### Requirement 6: Verification and Scope Guardrails
**Objective:** As a maintainer, I want persistence behavior to be verified across store choices, so that future changes do not regress continuity or secure-session guarantees.

#### Acceptance Criteria
1. When a supported durable continuity backend is available in automated validation, the LLM Interactive Proxy shall verify the continuity behaviors required by this specification for that backend.
2. When a supported durable secure-session backend is available in automated validation, the LLM Interactive Proxy shall verify the secure-session behaviors required by this specification for that backend.
3. If an optional external database validation environment is unavailable, then the LLM Interactive Proxy shall skip that external validation with an explicit test skip reason instead of failing unrelated validation.
4. When automated validation runs without an external managed database, the LLM Interactive Proxy shall still verify in-memory and local durable behavior.
5. The LLM Interactive Proxy shall not change client-facing protocol behavior as part of this persistence feature.
6. The LLM Interactive Proxy shall not change canonical request or event contracts as part of this persistence feature.
7. The LLM Interactive Proxy shall not expose the chosen database abstraction through public API or plugin contracts as part of this persistence feature.
