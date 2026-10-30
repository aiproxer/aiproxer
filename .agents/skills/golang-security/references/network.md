# Network/Web Security Rules

Network and web security vulnerabilities can lead to data leakage and unauthorized access.

**Rules:**

1. Redirects MUST be validated against an allowlist of domains.
2. HTTP servers need a reviewed timeout/body-limit policy. Fixed `WriteTimeout` values can break long-lived streams; use streaming-aware deadlines where required.
3. Pprof endpoints MUST NEVER be exposed publicly.
4. Treat XML as untrusted: Go's `encoding/xml` does not resolve external entities by default; still bound input, decode into a typed schema, and reject unsupported constructs if the application requires it.

---

## Open Redirect Vulnerability

Redirects to unvalidated URLs can be used for phishing.

**Bad:**

```go
target := r.URL.Query().Get("url")
http.Redirect(w, r, target, http.StatusFound) // DON'T
```

**Good:**

```go
target := r.URL.Query().Get("url")
u, err := url.Parse(target)
if err != nil { return errors.New("invalid redirect URL") }
// Only allow http/https
if u.Scheme != "http" && u.Scheme != "https" {
    return errors.New("invalid scheme")
}
// Block javascript/data schemes
if strings.HasPrefix(target, "javascript:") || strings.HasPrefix(target, "data:") {
    return errors.New("blocked scheme")
}
// Check against whitelist
if !isAllowedDomain(u.Host) {
    return errors.New("invalid domain")
}
http.Redirect(w, r, target, http.StatusFound)
```

---

## Listener exposure

Binding to `0.0.0.0` is expected for many containerized services; it is not a vulnerability by itself. Trace ingress, firewall/proxy policy, authentication, and intended audience. Private debug endpoints usually need a separate restricted listener or authenticated route.

```go
// Public application listener when ingress/security policy permits it.
listener, err := net.Listen("tcp", ":8080")

// Local diagnostic listener; still inspect port forwarding and proxy exposure.
debugListener, err := net.Listen("tcp", "127.0.0.1:6060")
```

---

## Slowloris Attack Vulnerability

Slowloris attacks exhaust connection pools.

**Bad:**

```go
server := &http.Server{
    Addr: ":8080",
    // Missing ReadTimeout, WriteTimeout, IdleTimeout
}
```

**Good:**

```go
server := &http.Server{
    Addr:         ":8080",
    ReadTimeout:  5 * time.Second,
    WriteTimeout: 10 * time.Second,
    IdleTimeout:  120 * time.Second,
    MaxHeaderBytes: 1 << 20, // Limit header size to 1MB
}
```

---

## Insecure HTTP Server Configuration

Running HTTP servers without proper security settings.

**Bad:**

```go
http.ListenAndServe(":8080", handler) // DON'T: No security hardening
```

**Good:**

```go
server := &http.Server{
    Addr:         ":443",
    ReadTimeout:  5 * time.Second,
    WriteTimeout: 10 * time.Second,
    IdleTimeout:  120 * time.Second,
    MaxHeaderBytes: 1 << 20,
}
server.ListenAndServeTLS("cert.pem", "key.pem")
```

---

## Observable Timing (Timing Attacks)

Timing differences can leak sensitive information.

**Bad:**

```go
func checkPassword(input, secret string) bool {
    return input == secret // DON'T: Short-circuit leaks length
}
```

**Good:**

```go
import "crypto/subtle"

// For comparing tokens, MACs, or hashes (same-length values):
func checkToken(input, expected string) bool {
    return subtle.ConstantTimeCompare([]byte(input), []byte(expected)) == 1
}

// For passwords, use Argon2id (preferred) or bcrypt — they handle hashing
// and constant-time comparison internally:
// argon2: hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
// bcrypt: err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))

// For HMAC verification:
import "crypto/hmac"
func verifyHMAC(message, messageMAC, key []byte) bool {
    mac := hmac.New(sha256.New, key)
    mac.Write(message)
    expectedMAC := mac.Sum(nil)
    return hmac.Equal(messageMAC, expectedMAC) // Constant-time
}
```

---

## Exposed pprof Profiling Endpoints

Debug pprof endpoints expose sensitive runtime information.

**Bad:**

```go
import _ "net/http/pprof" // DON'T: Automatically registers /debug/pprof
http.ListenAndServe(":8080", handler)
```

**Good:**

```go
// Option 1: Use build tags to exclude pprof from production builds
// File: debug_pprof.go
//go:build !production

package main

import _ "net/http/pprof"

// Option 2: Serve pprof on a separate internal-only listener
func startDebugServer() {
    debugMux := http.NewServeMux()
    debugMux.HandleFunc("/debug/pprof/", pprof.Index)
    debugMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
    debugMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
    debugMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
    debugMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
    go http.ListenAndServe("127.0.0.1:6060", debugMux) // localhost only
}
```

---

## XML external entities and resource limits

Some XML implementations process external entity references. Go's standard `encoding/xml` decoder does not fetch external entities by default, but untrusted XML can still consume resources or carry data your schema does not expect.

**Bad:**

```go
decoder := xml.NewDecoder(bytes.NewReader(xmlData))
if err := decoder.Decode(&person); err != nil { return err }
```

**Good:**

```go
decoder := xml.NewDecoder(bytes.NewReader(xmlData))
decoder.Strict = true
if err := decoder.Decode(&person); err != nil { return err }
```

Use a bounded reader, reject unknown fields or unsupported tokens at the schema boundary, and choose an XML library with explicit external-entity controls when the application needs features beyond the standard decoder. String scanning is not a general XXE defense.

---

## Permissive Regex Validation

Weak regex validation can allow malicious input.

**Bad:**

```go
matched, _ := regexp.MatchString(`.+@.+\..+`, email) // DON'T: Too permissive
```

**Good:**

```go
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
if !emailRegex.MatchString(email) {
    return errors.New("invalid email")
}
// Also block injection patterns
```

---

## CWE References

- **CWE-601**: Open Redirect
- **CWE-208**: Observable Timing Discrepancy
- **CWE-611**: Improper Restriction of XML External Entity Reference
- **CWE-770**: Allocation of Resources Without Limits
- **CWE-20**: Improper Input Validation
- **CWE-200**: Exposure of Sensitive Information
