# Injection Security Rules

Injection vulnerabilities allow attackers to execute arbitrary code, queries, or commands.

**Rules:**

1. SQL queries MUST use parameterized placeholders — NEVER concatenate user input.
2. Command execution MUST use `exec.Command` with separate args — NEVER shell interpolation.
3. HTML output MUST use `html/template` for automatic escaping.
4. SSRF: outbound URLs MUST be validated against an allowlist.

---

## SQL Injection

Building SQL queries by concatenating user input. Always use prepared statements with placeholders.

**Bad:**

```go
query := fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", input)
query := "SELECT * FROM users WHERE id = " + id
query := "DELETE FROM orders WHERE id = " + strconv.Itoa(orderID) // safe but inconsistent — use placeholders everywhere
```

**Good:**

```go
// Placeholder syntax varies by driver: $1 (pgx/lib/pq), ? (MySQL/SQLite)
db.QueryRow("SELECT * FROM users WHERE name = $1", input)
db.Exec("DELETE FROM orders WHERE id = $1", orderID)
```

### Dynamic IN clauses

Never build `IN (...)` by joining user strings. Generate numbered placeholders.

**Bad:**

```go
query := fmt.Sprintf("SELECT * FROM users WHERE id IN (%s)", strings.Join(ids, ","))
```

**Good:**

```go
// Build placeholders: $1, $2, $3, ...
placeholders := make([]string, len(ids))
args := make([]any, len(ids))
for i, id := range ids {
    placeholders[i] = fmt.Sprintf("$%d", i+1)
    args[i] = id
}
query := fmt.Sprintf("SELECT * FROM users WHERE id IN (%s)", strings.Join(placeholders, ","))
rows, err := db.Query(query, args...)
```

With `sqlx`:

```go
query, args, err := sqlx.In("SELECT * FROM users WHERE id IN (?)", ids)
query = db.Rebind(query) // converts ? to $1,$2,... for postgres
rows, err := db.Query(query, args...)
```

### Dynamic column names and ORDER BY

Placeholders only work for **values**, not identifiers (table/column names) or SQL keywords. Allowlist identifiers explicitly.

**Bad:**

```go
query := fmt.Sprintf("SELECT * FROM users ORDER BY %s", sortCol) // SQL injection
```

**Good:**

```go
allowed := map[string]string{
    "name": "name", "created": "created_at", "email": "email",
}
col, ok := allowed[sortCol]
if !ok {
    col = "created_at"
}
query := fmt.Sprintf("SELECT * FROM users ORDER BY %s", col) // safe: col is from allowlist
```

### Dynamic WHERE filters

Build queries incrementally; parameterize every user-supplied value.

```go
var conditions []string
var args []any
idx := 1

if name != "" {
    conditions = append(conditions, fmt.Sprintf("name = $%d", idx))
    args = append(args, name)
    idx++
}
if minAge > 0 {
    conditions = append(conditions, fmt.Sprintf("age >= $%d", idx))
    args = append(args, minAge)
    idx++
}

query := "SELECT * FROM users"
if len(conditions) > 0 {
    query += " WHERE " + strings.Join(conditions, " AND ")
}
rows, err := db.Query(query, args...)
```

### Query-library choice

`database/sql`, sqlx, and pgx can all parameterize values safely; none makes concatenated SQL safe automatically. Preserve the selected library unless specific ergonomics or PostgreSQL features justify a change. A parameterized call does not necessarily prepare a persistent server-side statement.

---

## XPath Injection

XPath injection allows manipulation of XML data queries.

**Bad:**

```go
xpathQuery := "//user[@username='" + username + "']" // Vulnerable
```

**Good:**

```go
// Use numeric ID
xpathQuery := fmt.Sprintf("//user[@id='%d']", userID)

// Or parse XML without XPath
```

---

## Code Injection

Generating code from unvalidated user input.

**Bad:**

```go
template := "func handle" + resourceName + "() {...}" // DON'T
```

**Good:**

```go
// Validate resource name matches whitelist
if !allowedResources[resourceName] {
    return errors.New("invalid resource")
}
// Use predefined templates
```

---

## Command Injection

Passing unvalidated input to shell commands.

**Bad:**

```go
cmd := exec.Command("sh", "-c", "rm -f /tmp/"+filename) // DON'T
```

**Good:**

```go
// Validate before constructing or executing the operation. Separate arguments
// avoid shell parsing, but path traversal and executable option parsing remain.
if filename == "." || filename == ".." || filepath.Base(filename) != filename {
    return errors.New("invalid filename")
}
cmd := exec.CommandContext(ctx, "rm", "-f", "--", filepath.Join("/tmp", filename))
return cmd.Run()
```

---

## Template source versus template data

Passing untrusted data to a fixed `html/template` is supported by its security model. The dangerous boundary is parsing attacker-controlled template source, exposing powerful template functions, or marking untrusted data as `template.HTML`, `template.JS`, or `template.URL`.

```go
// Unsafe when templateSource is attacker-controlled.
t, err := template.New("page").Funcs(privilegedFuncs).Parse(templateSource)

// Fixed trusted source; Execute treats data as untrusted and escapes by context.
t, err := template.New("page").Parse("<div>{{.}}</div>")
if err != nil { return err }
return t.Execute(w, input)
```

Check which template package is imported and which output context is used. `text/template` does not perform HTML escaping. See the [html/template security model](https://pkg.go.dev/html/template#hdr-Security_Model).

---

## Cross-Site Scripting (XSS)

XSS allows attackers to execute malicious scripts.

**Bad:**

```go
w.Write([]byte(fmt.Sprintf("<div>%s</div>", data))) // DON'T
```

**Good:**

```go
import "html/template"
t := template.Must(template.New("safe").Parse("<div>{{.}}</div>"))
t.Execute(w, data) // Auto-escapes
```

---

## HTML Tag Injection

Injecting HTML tags through unvalidated input.

**Bad:**

```go
fmt.Fprintf(w, "<div>Welcome, %s!</div>", input) // DON'T
```

**Good:**

```go
import "html"
escaped := html.EscapeString(input)
fmt.Fprintf(w, "<div>Welcome, %s!</div>", escaped)
```

---

## Server-Side Request Forgery (SSRF)

Forcing the server to make requests to unintended endpoints.

**Bad:**

```go
url := r.URL.Query().Get("url")
resp, _ := http.Get(url) // DON'T: No validation
```

**Review the complete destination policy:**

Parse with checked errors; constrain scheme, credentials, exact allowed host and port, and redirect targets. Hostname-only checks do not defend against DNS answers pointing to private addresses or rebinding between validation and connection. Enforce the policy on resolved addresses at dial time, including IPv4/IPv6 and the deployment's proxy behavior, or use controlled egress. Revalidate every redirect; otherwise reject redirects. Use bounded contexts and response sizes.

A validator that checks only `isInternalIP(u.Hostname())` or a `metadata.` substring is incomplete. Test DNS-to-private-address, redirect-to-private-address, alternate address forms, credentials, and malformed URL cases.


---

## Unsafe Deserialization and Resource Exhaustion

Deserializing untrusted input can consume excessive CPU or memory and can expose implementation details. Go's `encoding/gob` is not a Java-style object-execution mechanism, but an external boundary should still use a deliberately specified format and validate size, shape, and permitted types.

**Bad:**

```go
dec := gob.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)) // Prefer JSON/protobuf at external boundaries
var user interface{}
if err := dec.Decode(&user); err != nil { return err }
```

**Good:**

```go
import "encoding/json"
r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
dec := json.NewDecoder(r.Body)
var user User
if err := dec.Decode(&user); err != nil { return err }
// Validate fields and reject trailing values when the contract requires one object.
// JSON still needs resource limits; changing codecs alone is not a defense.
```

---

## CWE References

- **CWE-78**: OS Command Injection
- **CWE-89**: SQL Injection
- **CWE-94**: Code Injection
- **CWE-79**: Cross-site Scripting (XSS)
- **CWE-918**: Server-Side Request Forgery (SSRF)
- **CWE-502**: Deserialization of Untrusted Data
- **CWE-20**: Improper Input Validation
