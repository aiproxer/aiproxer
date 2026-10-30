# Requirements Document

## Introduction
Operators and users of the LIP HTTP server need crash containment so that failures caused by a single request, connection, plugin extension, backend attempt, or owned runtime worker do not crash the whole process or corrupt future request processing. This feature defines the expected behavior for isolating unexpected application panics and crash-like failures that the server can observe, while preserving the product's existing streaming-first routing, failover, and observability guarantees.

## Boundary Context
- **In scope**: request and connection crash containment, panic-to-error behavior at extension and backend boundaries, post-output failure visibility, continued availability for unrelated requests, shared runtime state integrity, and operator-visible diagnostics for isolated crashes.
- **Out of scope**: operating-system process supervision after fatal runtime failures, unrecoverable process termination, host-level kills, cross-process high availability, distributed coordination, transparent recovery after client-visible output begins, and prevention of all resource exhaustion caused by intentionally misconfigured global limits.
- **Adjacent expectations**: existing routing and B2BUA semantics still apply to recoverable pre-output failures; this feature must not weaken the no-retry-after-first-output invariant or silently hide capability mismatches.

## Architecture Constraints
- **Core ownership**: The runtime owns panic classification, routing/failover decisions, hook policy application, and attempt lineage outcomes.
- **Adapter ownership**: HTTP recovery, client-safe response formatting, metrics emission, and concrete server worker reporting stay in HTTP/infra/composition packages.
- **Provider isolation**: Provider SDK, vendor error, wire, SQL, ORM, and transport request/response types must not cross into core orchestration contracts.
- **Public API stability**: This feature must use existing `pkg/lipapi` and `pkg/lipsdk` contracts unless implementation proves an internal-only mapping cannot satisfy a requirement.
- **Port discipline**: New interfaces are allowed only for real consumer-owned seams; do not add adapter-defined interfaces solely for tests or mocks.

## Requirements

### Requirement 1: Request and Connection Crash Containment
**Objective:** As an operator, I want failures in one client request or connection to be contained, so that other clients and future requests remain available.

#### Acceptance Criteria
1. When a single request triggers an unexpected panic before any client-visible response is committed, the LIP HTTP server shall return a protocol-appropriate internal error response for that request without terminating the server process.
2. When a single request triggers an unexpected panic after client-visible output has started, the LIP HTTP server shall stop processing that request or connection without attempting transparent failover for that committed output.
3. If one request is isolated after an unexpected panic, then the LIP HTTP server shall continue accepting and processing unrelated new requests.
4. While one request is stalled, canceled, or isolated because of its own failure, the LIP HTTP server shall not block unrelated requests except where they depend on the same configured external limit or shared backend condition.
5. The LIP HTTP server shall treat crash containment as a per-request or per-connection behavior, not as a normal shutdown of the whole server.
6. If request crash containment handles an unexpected panic, then the LIP HTTP server shall preserve the ability to record the final status or failure class for that request when request metrics or access logging are enabled.

### Requirement 2: Extension Boundary Failure Containment
**Objective:** As a platform operator, I want plugin and hook failures to be isolated, so that optional extensibility cannot destabilize the core runtime.

#### Acceptance Criteria
1. When an enabled request extension raises an unexpected panic during request preparation, the LIP runtime shall convert the failure into a bounded request error or extension failure according to the extension's configured failure behavior.
2. When an enabled response extension raises an unexpected panic while processing a stream event, the LIP runtime shall isolate the failure to the affected stream and preserve whether client-visible output had already started.
3. When an enabled tool event extension raises an unexpected panic, the LIP runtime shall apply the configured tool extension error behavior without terminating unrelated requests.
4. If an extension failure is configured to fail open, then the LIP runtime shall continue the affected request only when continuing does not violate canonical validation or streaming safety rules.
5. If an extension failure is configured to fail closed, then the LIP runtime shall surface a request-scoped failure and shall not continue processing that request as if the extension had succeeded.

### Requirement 3: Backend Attempt Failure Containment
**Objective:** As an operator, I want backend crashes to follow existing routing safety rules, so that pre-output recovery remains useful without hiding committed failures.

#### Acceptance Criteria
1. When a backend attempt raises an unexpected panic before client-visible output starts, the LIP runtime shall classify the attempt as a failed pre-output attempt and evaluate it under the existing bounded failover policy.
2. When a backend attempt raises an unexpected panic after client-visible output starts, the LIP runtime shall surface the failure on the affected stream and shall not perform transparent retry or failover for that request.
3. If a backend attempt is isolated after an unexpected panic, then the LIP runtime shall preserve the logical request lineage for attempts that were started before the failure was isolated.
4. When a backend capability decision raises an unexpected panic, the LIP runtime shall fail the affected request or candidate evaluation explicitly rather than silently selecting an unsafe fallback.
5. The LIP runtime shall not treat panic containment as permission to drop required semantics, bypass capability negotiation, or exceed the configured attempt budget.
6. If closing or abandoning a failed backend attempt raises an unexpected panic, then the LIP runtime shall isolate the close failure and shall not replace the request outcome that was already determined for the affected request.

### Requirement 4: Shared Runtime State Integrity
**Objective:** As an operator, I want isolated crashes to leave shared server state safe for future work, so that one failed request does not poison later routing or diagnostics.

#### Acceptance Criteria
1. When a request-scoped failure is isolated, the LIP runtime shall not leave shared routing, continuity, hook ordering, or diagnostics state in a condition that prevents later unrelated requests from processing correctly.
2. If a failure occurs while a request is updating attempt lineage, then the LIP runtime shall preserve a consistent observable outcome for completed updates and shall not expose partially corrupted lineage data.
3. While crash containment is active, the LIP runtime shall maintain deterministic event ordering for events that are successfully emitted before a failure is surfaced.
4. When an isolated failure affects a request-local stream, the LIP runtime shall release or close request-local resources that are no longer usable for that request.
5. The LIP runtime shall keep immutable startup configuration and registered extension ordering stable across isolated request failures.

### Requirement 5: Owned Worker Crash Containment
**Objective:** As an operator, I want owned server workers to fail safely, so that internal goroutine failures do not silently stop critical behavior or crash the process.

#### Acceptance Criteria
1. When an owned request-scoped worker raises an unexpected panic, the LIP runtime shall surface the failure to the affected request or stream instead of terminating the whole process.
2. When an owned server-level worker raises an unexpected panic, the LIP server shall report the failure as a server-level error and begin orderly shutdown or remain available only when the worker is not required for correctness.
3. If a worker failure is isolated to one stream, then the LIP runtime shall not leak that stream's unfinished work into later requests.
4. While a request context is canceled, request-scoped workers for that request shall stop contributing output to the client after cancellation is observed.
5. The LIP runtime shall not launch unowned background work as a crash-containment mechanism.

### Requirement 6: Operator Diagnostics for Isolated Crashes
**Objective:** As an operator, I want isolated crashes to be observable, so that I can diagnose faulty plugins, backends, or request paths without exposing internals to clients.

#### Acceptance Criteria
1. When the LIP runtime isolates an unexpected panic, the LIP server shall record operator-visible diagnostics that identify the request or attempt context when that context is available.
2. When an isolated crash is reported to a client, the LIP server shall return a safe protocol-appropriate error message that does not expose stack traces or sensitive internal details.
3. If diagnostics or observability features are enabled, then the LIP server shall distinguish isolated crash failures from ordinary client validation errors and expected upstream errors.
4. When an isolated failure occurs in a backend attempt, the LIP runtime shall make the surfaced or swallowed attempt outcome visible through existing attempt diagnostics where attempt diagnostics are available.
5. The LIP server shall preserve existing trace or request correlation identifiers for isolated crash reports when those identifiers are available.
6. When the LIP runtime records an isolated crash, the LIP server shall identify the failed boundary class, such as HTTP request handling, extension execution, backend attempt execution, stream processing, or owned worker execution.
