# Security Review Checklist

Assign severity from reachable input, attacker capability, impact, and deployed mitigations; checklist patterns are investigation prompts.

## Input Handling

- [ ] All user input validated at system boundaries — internal code trusts the boundary
- [ ] Input uses allowlists, not blocklists — blocklists always miss something
- [ ] Sanitized on output (HTML, SQL, shell) — context-dependent escaping
- [ ] Length limits enforced — prevents buffer abuse and DoS

## Database

- [ ] SQL queries use parameterized placeholders — keeps data and code separate
- [ ] ORM/library protects against SQL injection
- [ ] No direct SQL construction with user input

## Code Execution

- [ ] No `exec.Command()` with shell arguments — metacharacters enable injection
- [ ] Review interpreters and reflection-assisted dispatch for attacker-controlled method/type selection; reflection alone is not code execution
- [ ] Bound untrusted decoding by size/shape/type and codec behavior; Go gob does not invoke arbitrary Java-style constructors

## Cryptography

- [ ] Uses `crypto/rand` for security-critical randomness — `math/rand` is predictable
- [ ] Uses vetted algorithms (AES-GCM, Argon2id, bcrypt) — custom crypto hasn't been analyzed
- [ ] Proper key management — hardcoded secrets leak through VCS, logs, and backups
- [ ] HMAC for message authentication — prevents tampering

## Web Security

- [ ] TLS 1.2+ configured correctly — older versions have known attacks
- [ ] Security headers set (HSTS, CSP, X-Frame-Options) — prevents framing, sniffing, downgrade
- [ ] CSRF protection for state-changing requests — prevents cross-origin action forgery
- [ ] Open redirects validated — attackers use your domain to redirect to phishing
- [ ] XSS protected via `html/template` auto-escaping

## Authentication/Authorization

- [ ] Passwords hashed with Argon2id (preferred) or bcrypt — intentionally slow to resist brute-force
- [ ] Sessions use secure tokens from `crypto/rand`
- [ ] Authorization checked on every privileged action — not just at login
- [ ] JWT tokens validated (algorithm, claims, expiry) — unsigned JWTs bypass auth
- [ ] Expired/invalid sessions invalidated server-side

## Error Handling

- [ ] Generic error messages to users — detailed errors help attackers map your system
- [ ] Detailed errors logged server-side only
- [ ] Stack traces not leaked to clients
- [ ] Database errors not exposed — reveals schema and query structure

## Dependency Security

- [ ] `govulncheck` passes — catches known CVEs in your dependency tree
- [ ] Known reachable dependency vulnerabilities have a reviewed remediation or mitigation policy
- [ ] Third-party libraries reviewed for security posture

## HTTP Security Headers

- [ ] `Content-Security-Policy` set — restricts resource sources to prevent XSS
- [ ] `X-Frame-Options: DENY` — prevents clickjacking via iframe embedding
- [ ] `X-Content-Type-Options: nosniff` — prevents MIME-type sniffing attacks
- [ ] `Strict-Transport-Security` with `includeSubDomains` — forces HTTPS, prevents downgrade
- [ ] `Referrer-Policy` set — controls referrer header leakage to external sites
- [ ] `Permissions-Policy` set — restricts browser features (camera, mic, geolocation)

## Rate Limiting & DoS Prevention

- [ ] HTTP server has a timeout/body-limit policy appropriate to bounded requests and streaming
- [ ] Request body size limited with `http.MaxBytesReader` — prevents memory exhaustion
- [ ] Rate limiting on authentication endpoints — prevents brute-force and credential stuffing
- [ ] Rate limiting on expensive operations (search, export, file upload)

## Concurrency

- [ ] `-race` detector passes — races cause data corruption and can bypass auth checks
- [ ] Shared state properly synchronized
- [ ] No data races on global variables
