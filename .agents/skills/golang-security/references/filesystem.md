# Filesystem Security Rules

Filesystem vulnerabilities can lead to unauthorized file access, data leakage, and denial-of-service attacks.

**Rules:**

1. File paths MUST be sanitized against traversal (`../`).
2. `os.Root` SHOULD be used for scoped file access (Go 1.24+).
3. Zip extraction MUST check for ZipSlip path traversal.
4. Temporary files MUST use `os.CreateTemp` — NEVER predictable names.
5. File permissions MUST be restrictive (0600 for secrets, 0750 for directories).

---

## Directory Traversal

Paths like `../../etc/passwd` access files outside intended directory.

**Bad:**

```go
filepath := filepath.Join("/var/www", filename) // DON'T
http.ServeFile(w, r, filepath)
```

**Good (Go 1.24+) — use `os.Root` for safe, scoped directory access:**

```go
root, err := os.OpenRoot("/var/www")
if err != nil { return err }
defer root.Close()
f, err := root.Open(filename) // cannot escape root directory
```

`os.Root` provides directory-scoped operations and rejects paths that escape the root, including relevant symlink escapes. Still bound file sizes/counts, validate the requested operation, and check every error.

**When `os.Root` is unavailable:**

Use a reviewed directory-confinement implementation that resolves symlinks and checks `filepath.Rel` with platform-aware separators, then open the file without a check/use race where possible. A lexical `Clean` plus string-prefix check is not a security boundary.

---

## Zip Archive Path Traversal

Malicious zip files can escape extraction directory.

**Bad:**

```go
for _, file := range reader.File {
    path := filepath.Join(dest, file.Name) // DON'T: No validation
    file.Create(path)
}
```

**Good (Go 1.24+) — use `os.Root` to scope extraction:**

```go
root, err := os.OpenRoot(dest)
if err != nil { return err }
defer root.Close()
for _, file := range reader.File {
    f, err := root.OpenFile(file.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
    if err != nil { return err } // rejects paths escaping root
    // ... copy contents ...
    f.Close()
}
```

If `os.Root` is unavailable, use a reviewed archive-extraction library or a symlink-aware confinement implementation. Reject absolute and traversal names, bound entry count and decompressed bytes, and avoid a lexical prefix check as the only defense.

---

## Decompression Bomb

Tiny compressed files can expand to GBs.

**Bad:**

```go
gr, _ := gzip.NewReader(f)
out, _ := os.Create(dst)
io.Copy(out, gr) // DON'T: No size limits
```

**Good:**

```go
const maxDecompressedSize = 100 * 1024 * 1024 // 100MB limit

// Read at most one byte beyond the permitted size so overflow is an error,
// rather than silently accepting a truncated payload.
lr := &io.LimitedReader{R: gr, N: maxDecompressedSize + 1}
n, err := io.Copy(out, lr)
if err != nil { return err }
if n > maxDecompressedSize { return errors.New("decompressed data exceeds limit") }
// The caller owns closing gr/out, reporting close errors, and removing partial output.


```

---

## Insecure Temporary File Creation

Creating temp files without proper permissions.

**Bad:**

```go
f, _ := os.Create("/tmp/myapp.temp") // DON'T: Predictable name
f.WriteString(data)
```

**Good:**

```go
f, err := os.CreateTemp("", "myapp.*")
if err != nil { return err }
defer os.Remove(f.Name())
defer f.Close() // choose an explicit close-error policy for writes
// CreateTemp already creates the file with mode 0600 (before umask).
```

---

## Insecure File Permissions

Opening files with excessive permissions.

**Bad:**

```go
f, _ := os.OpenFile("config.json", os.O_CREATE, 0644) // DON'T: World-readable
```

**Good:**

```go
f, _ := os.OpenFile("config.json", os.O_CREATE, 0600) // OK: Owner only
```

---

## Insecure mkdir

Creating directories with overly permissive permissions.

**Bad:**

```go
os.MkdirAll("/var/myapp/cache", 0777) // DON'T: World-writable
```

**Good:**

```go
os.MkdirAll("/var/myapp/cache", 0750) // Owner writable; group can read/traverse
```

---

## Insecure File Write Permissions

Opening files for writing with inappropriate permissions.

**Bad:**

```go
os.OpenFile("app.log", os.O_CREATE, 0666) // DON'T: World-writable
```

**Good:**

```go
os.OpenFile("app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640) // OK
```

---

## Tainted File Read

Reading files based on unvalidated input.

**Bad:**

```go
func readFile(filename string) ([]byte, error) {
    return os.ReadFile(filename) // DON'T: No validation
}
```

**Good (Go 1.24+):**

```go
const allowedDir = "/var/www/public/"

func readFile(filename string) ([]byte, error) {
    root, err := os.OpenRoot(allowedDir)
    if err != nil { return nil, err }
    defer root.Close()
    f, err := root.Open(filename) // cannot escape root directory
    if err != nil { return nil, err }
    defer f.Close()
    return io.ReadAll(f)
}
```

If `os.Root` is unavailable, use the same reviewed, symlink-aware confinement approach described above; rejecting a substring or checking a string prefix alone is insufficient.

---

## CWE References

- **CWE-22**: Path Traversal (Directory Traversal)
- **CWE-409**: Zip Bomb Decompression
- **CWE-379**: Insecure Temp File Creation
- **CWE-732**: Incorrect File Permissions
