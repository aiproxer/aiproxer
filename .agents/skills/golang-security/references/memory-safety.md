# Memory Safety Security Rules

Memory safety vulnerabilities can lead to crashes, data corruption, and security compromises.

**Rules:**

1. Integer overflow MUST be checked at boundaries — NEVER trust unchecked arithmetic on external input.
2. Review unsafe operations against documented pointer/lifetime/alignment rules and demonstrated need; usage alone is not a vulnerability.
3. Combine ownership/happens-before review with race tests on supported platforms; race tests cover exercised executions only.

---

## Integer Overflow

Integer overflows can cause unexpected behavior and crashes.

**Bad:**

```go
func allocateBuffer(rows, cols int) []byte {
    size := rows * cols  // DON'T: Can overflow
    return make([]byte, size)
}
```

**Good:**

```go
func safeMultiply(a, b int) (int, error) {
    if a < 0 || b < 0 {
        return 0, errors.New("negative dimensions")
    }
    if a == 0 || b == 0 {
        return 0, nil
    }
    maxInt := int(^uint(0) >> 1)
    if a > maxInt/b {
        return 0, errors.New("integer overflow")
    }
    result := a * b
    if result/b != a {
        return 0, errors.New("overflow detected")
    }
    return result, nil
}

func allocateBuffer(rows, cols int) ([]byte, error) {
    size, err := safeMultiply(rows, cols)
    if err != nil {
        return nil, err
    }
    const maxBufferSize = 100 * 1024 * 1024 // 100MB limit
    if size > maxBufferSize {
        return nil, errors.New("buffer size exceeds limit")
    }
    return make([]byte, size), nil
}
```

---

## math/big.Rat Issues

Rat can consume large amounts of memory if denominators grow without bounds.

**Bad:**

```go
import "math/big"

func unsafeFraction(operations int) *big.Rat {
    r := big.NewRat(1, 1)
    for i := 0; i < operations; i++ {
        r.Mul(r, big.NewRat(int64(i+1), int64(i+2)))  // DON'T
    }
    return r  // Could be memory intensive
}
```

**Good:**

```go
const maxRatNumBits = 1000

func safeFraction(operations int) (*big.Rat, error) {
    r := big.NewRat(1, 1)
    for i := 0; i < operations; i++ {
        r.Mul(r, big.NewRat(int64(i+1), int64(i+2)))
        if r.Num().BitLen() > maxRatNumBits || r.Denom().BitLen() > maxRatNumBits {
            return nil, errors.New("fraction precision too large")
        }
    }
    return r, nil
}
```

---

## Aliasing and ownership

Aliasing is a defect when a caller and callee unexpectedly share mutable state or access it concurrently. In-place reversal is valid when the caller grants mutation ownership. Go's built-in `copy` handles overlapping slices; an unsafe address-overlap detector and temporary buffer are unnecessary.

```go
// Correct even when dest and src overlap.
copy(dest, src)

// Independent storage when the API promises a snapshot.
snapshot := bytes.Clone(src)
```

Check whether a retained slice/map or returned pooled buffer is still owned elsewhere. A full-slice expression prevents append from overwriting extra capacity but does not isolate existing elements; slice/map clones are shallow for pointer-bearing elements. See the [language specification](https://go.dev/ref/spec#Appending_and_copying_slices).

---

## Use of unsafe Package

The unsafe package bypasses Go's type safety and memory safety.

**Bad:**

```go
import "unsafe"

func UnsafeStringToBytes(s string) []byte {
    return (*[0x7fffffff]byte)(unsafe.Pointer(
        (*reflect.StringHeader)(unsafe.Pointer(&s)).Data,
    ))[:len(s):len(s)]  // DON'T: memory corruption risk
}

func TypePun(value uint64) float64 {
    return *(*float64)(unsafe.Pointer(&value))  // DON'T
}
```

**Good:**

```go
// Safe string encoding
func StringToBytes(s string) []byte {
    return []byte(s)
}
func BytesToString(b []byte) string {
    return string(b)
}

// Safe type conversion
import "encoding/binary"
func Uint64ToFloat64(value uint64) float64 {
    buf := make([]byte, 8)
    binary.LittleEndian.PutUint64(buf, value)
    bits := binary.LittleEndian.Uint64(buf)
    return math.Float64frombits(bits)
}
```

---

## Data Races

Explicit ownership and synchronization are the defense; the race detector provides dynamic evidence for exercised paths.

**Bad:**

```go
type Counter struct {
    value int
}

func (c *Counter) Increment() {
    c.value++  // DON'T: Data race without sync
}
```

**Good:**

```go
import "sync"

type Counter struct {
    value int
    mu    sync.Mutex
}

func (c *Counter) Increment() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.value++
}

// Or atomic for simple cases
import "sync/atomic"
type AtomicCounter struct {
    value int64
}
func (c *AtomicCounter) Increment() {
    atomic.AddInt64(&c.value, 1)
}
```

## Always Run Race Detector

```bash
go test -race ./...
go build -race
```

---

## CWE References

- **CWE-190**: Integer Overflow or Wraparound
- **CWE-119**: Improper Restriction of Operations within Bounds
- **CWE-125**: Out-of-bounds Read
- **CWE-787**: Out-of-bounds Write
- **CWE-362**: Race Condition
- **CWE-367**: Time-of-check Time-of-use (TOCTOU)
