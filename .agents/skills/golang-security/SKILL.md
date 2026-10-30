---
name: golang-security
description: "Go security review and implementation: trust boundaries, injection, crypto, filesystem, network, auth, secrets, privacy, and supply chain."
---

# Go security

Use this skill for a security review, threat model, or security-sensitive implementation. Trace data from its source through the complete call path and deployment boundary. A scanner finding is evidence to investigate, not a severity by itself; a validation step upstream can reduce exploitability but should not justify unsafe code in a reusable boundary.

## Triage workflow

1. Identify assets, attacker capabilities, trust boundaries, and required security properties (confidentiality, integrity, availability, authenticity, authorization, auditability).
2. Trace untrusted input into interpreters, filesystem, network, crypto, logs, templates, and resource-consuming operations.
3. Check authentication, authorization, validation, limits, timeout/cancellation, error handling, and observability around each boundary.
4. Reproduce a finding with a focused test or minimal input; state preconditions, impact, and existing mitigations.
5. For an implementation/fix request, correct the narrowest root cause and rerun applicable tests/scans. A review remains read-only: report severity, confidence, file/symbol, reachable trigger, impact, minimal remedy, and executed versus inferred evidence. Separate introduced/worsened findings from existing debt and state coverage gaps. Verify failure behavior and sensitive-data exposure.

Use a threat model such as STRIDE and a severity rubric appropriate to the product. DREAD can help compare findings, but do not treat its score as a universal risk standard. See [threat modeling](references/threat-modeling.md) and the [review checklist](references/checklist.md).

## High-value defaults

| Boundary | Safer approach |
| --- | --- |
| SQL | Parameterized queries and typed arguments; never concatenate untrusted SQL fragments. |
| Commands | `exec.CommandContext` with separate fixed executable and arguments; avoid a shell. |
| HTML | `html/template` for HTML contexts; contextual escaping does not make unsafe URLs or JavaScript safe. |
| Files | `os.OpenRoot`/`os.Root` for a scoped tree where available; otherwise resolve and validate with symlink-aware, platform-correct logic. |
| URLs | Parse and allow-list scheme, host, port, and destination; defend SSRF against loopback, link-local, private, and redirected targets. |
| Secrets | Load from a secret manager or controlled configuration; never commit or log them. |
| Tokens | Use `crypto/rand`; authenticate and authorize server-side; compare secret bytes with constant-time functions where timing matters. |
| Passwords | Use a current password-hashing design such as Argon2id or bcrypt with a reviewed cost, salt, and upgrade plan. |
| Encryption | Use an authenticated construction such as AES-GCM with nonce discipline and key management; do not invent crypto. |
| HTTP | Set request/body limits, deadlines, TLS policy, cookie flags, and security headers appropriate to the deployment. |
| Shared state | Bound queues/maps/goroutines and protect mutable state; validate with focused tests and the race detector. |

## Correctness traps

- `encoding/xml` does not fetch external entities by default; still constrain input size and reject formats/features your application does not need. String scanning is not an XXE defense.
- Go's `encoding/gob` decodes typed data and is not a Java-style object-execution mechanism. Treat untrusted gob as a parser/resource-exhaustion risk and prefer a deliberately specified wire format at an external boundary.
- JWT validation must pin the exact expected signing method (for example, an explicit RS256 method), validate issuer/audience/expiry and key identity, then perform authorization. Accepting any RSA method type is insufficient.
- Rate limiting keyed by attacker-controlled client IDs needs bounded storage/eviction and a trusted identity strategy; an unbounded map is a memory-exhaustion bug.
- Integer checks must use the actual operand type (`strconv.IntSize`, `math.MaxInt`/`MinInt` where available, or checked arithmetic) and reject negative values when the domain requires non-negative input. Do not use `math.MaxInt64` as an `int` bound on every target.
- Use `url.UserPassword` or driver-provided DSN construction rather than interpolating raw credentials into a connection string.
- A stable internal ID or unsalted/truncated hash remains linkable data. Minimize it, access-control it, and use a keyed construction only when correlation is justified and documented.

## Web and filesystem review

Limit request bodies before decoding, enforce timeouts on servers and outbound clients, validate redirects, and protect debug/pprof endpoints. Cookies carrying session state generally need `Secure`, `HttpOnly`, and an intentional `SameSite`; use `__Host-` only with `Secure`, `Path=/`, and no `Domain`.

For uploads and archive extraction, reject absolute paths and traversal, bound decompressed size/count, preserve restrictive permissions, and use a directory-scoped API or verified path resolution that handles symlinks and platform semantics. A lexical `HasPrefix` check is not confinement: `/safe2` matches `/safe`, separators matter, and symlinks can escape.

## Verification

```sh
go test ./...
go test -race ./...             # where the target/platform supports it
go vet ./...
govulncheck ./...
gosec ./...                    # if adopted by the repository
```

Add fuzz or property tests for parsers and boundary validators. Keep dependencies, action/tool versions, and generated code under review. Do not log passwords, tokens, private keys, full authorization headers, raw personal data, or attacker-controlled strings without a safe encoding/redaction policy. See [cryptography](references/cryptography.md), [injection](references/injection.md), [filesystem](references/filesystem.md), [network](references/network.md), [cookies](references/cookies.md), [secrets](references/secrets.md), [logging](references/logging.md), [memory safety](references/memory-safety.md), [architecture](references/architecture.md), and [third-party data](references/third-party.md).

Related local skills: `golang-dependency-management`, `golang-continuous-integration`, `golang-observability`, `golang-error-handling`, and `golang-testing`.
