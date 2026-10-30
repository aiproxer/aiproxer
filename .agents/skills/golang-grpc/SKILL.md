---
name: golang-grpc
description: "Implement and review Go gRPC/protobuf contracts, status errors, streaming lifecycle, metadata, interceptors, deadlines, credentials, balancing, and tests."
---

# Go gRPC

Treat protobuf as a versioned API contract. Pin and review protoc, protoc-gen-go, protoc-gen-go-grpc, Buf, and grpc-go versions as a compatible set. Generated files are build artifacts whose source schema and generation command must be reproducible.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Compare old/new schemas and generated method sets: field presence, oneof changes, enum defaults/unknown values, reserved tags, service names, and HTTP/wire compatibility.
- Trace context and metadata across interceptors and outgoing calls, including context-error-to-status mapping, authentication, cancellation, and safe status details.
- For streams, verify one sender and one receiver at a time, half-close versus EOF semantics, bounded backpressure, terminal errors, and client cancellation on early return.
- Inspect resolver/balancer registration, TLS credentials, retry/service config, connection reuse, and bounded GracefulStop fallback; committed stream data constrains retry behavior.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Protobuf and generation

Use a stable go_package that matches the module path and keep package/version namespaces deliberate. Reserve removed field numbers and names. Prefer additive evolution; changing a field type, reuse of a number, or changing oneof meaning is a compatibility event.

The protoc-gen-validate (PGV) project and Buf Protovalidate are different systems. PGV uses generated validation code and its own options/generator; Protovalidate uses Buf's CEL-backed runtime and option schema. Do not combine their syntax or describe one as the other. Run the generator specified by the repository and inspect generated imports before committing.

## Server and client boundaries

Use generated registration functions and return gRPC status errors with codes that describe the caller-visible outcome. Preserve wrapped causes in internal logs, but do not expose database or credential details in status messages. Interceptors should be small and ordered deliberately: authentication/identity, request limits, metrics/tracing, then logging or recovery according to the service policy.

Every unary call does not require the same timeout. Set a deadline for bounded request/response operations based on the service budget. Long-lived streams need cancellation, keepalive/backpressure, and an explicit maximum lifetime or shutdown policy rather than an arbitrary short deadline. Always propagate the incoming context to downstream work.

For streaming, define who owns send/receive loops, how one side signals completion, and how errors close the stream. Do not concurrently call operations that grpc-go documents as unsafe, and bound message size and queue growth.

## Client connection and balancing

Use a resolver and service config that match deployment. If configuring round_robin with grpc-go, import the registration package so the balancer is linked:

~~~go
import _ "google.golang.org/grpc/balancer/roundrobin"

conn, err := grpc.NewClient(
    target,
    grpc.WithTransportCredentials(creds), // configured TLS/mTLS credentials
    grpc.WithDefaultServiceConfig(
        "{\"loadBalancingPolicy\":\"round_robin\"}",
    ),
)
~~~

The exact client constructor/options depend on the pinned grpc-go release; current grpc-go uses NewClient while older code may use Dial. Verify before editing. Supply deployment-appropriate transport credentials and check construction errors; the owner closes conn. NewClient creates a channel without proving endpoint readiness, so a successful constructor is not a connectivity check. A balancer does not replace health checks, retry policy, or endpoint security.

Use TLS/mTLS and credential configuration appropriate to deployment. Do not put tokens in metadata logs. Retry only methods marked idempotent by the API contract, and distinguish transport retries from application-level failover.

## Validation and tests

Validate at the boundary, before expensive work. Decide whether malformed input becomes InvalidArgument, FailedPrecondition, or another code, and test that mapping. Test status code and error identity, cancellation, deadlines, metadata, unary and streaming interceptors, and compatibility of old clients with new schemas.

Use bufconn for fast in-process transport tests when network behavior is not under test. Start the server synchronously, fail on setup errors, and close listeners/connections in cleanup. Integration tests should exercise real TLS, resolver, proxy, or network behavior separately.
