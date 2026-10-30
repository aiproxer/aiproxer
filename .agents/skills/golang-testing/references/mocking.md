# Fakes and mocks

Define an interface at the consumer boundary and implement the smallest fake needed by the test. A fake that models state and failure modes is often clearer than a mock that asserts every call. Use interaction assertions only when call count/order is part of the contract.

```go
type Clock interface { Now() time.Time }

type fixedClock struct{ now time.Time }
func (c fixedClock) Now() time.Time { return c.now }
```

Keep fakes deterministic and reset per test. Exercise errors, cancellation, partial results, and retries explicitly. Avoid mocks that return zero values for every unconfigured call; that can hide a missing setup. Prefer a real in-memory implementation when protocol or serialization behavior is under test.
