# HTTP tests

Use `httptest.NewRequest`, `httptest.NewRecorder`, and `httptest.NewServer` for in-process handlers and client behavior. Assert status, headers, body, context cancellation, and error mapping; avoid asserting incidental header order or private helper calls.

```go
func TestHealth(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/health", nil)
    rec := httptest.NewRecorder()

    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK { t.Fatalf("status = %d", rec.Code) }
}
```

Close response bodies from test servers. For timeout tests, use a controllable handler/channel or `testing/synctest` rather than sleeping for a guessed interval. Ensure server shutdown is registered with `t.Cleanup`.
