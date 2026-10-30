package openaichat_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
)

func TestOracleLedger_success(t *testing.T) {
	t.Parallel()
	var saw atomic.Int32
	ledger := refbackend.NewOracleLedger(
		func(body []byte) error {
			saw.Add(1)
			if !strings.Contains(string(body), `"messages"`) {
				return errors.New("structural mismatch: messages_missing")
			}
			return nil
		},
		func(body []byte) error {
			saw.Add(1)
			if strings.Contains(string(body), "leak-secret") {
				return errors.New("structural mismatch: unexpected_field")
			}
			return nil
		},
	)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder: refbackend.ScriptedResponder([]refbackend.ScriptedTurn{
			{VisibleText: "a"},
			{VisibleText: "b"},
		}),
	}))
	t.Cleanup(srv.Close)

	postChat(t, srv.URL, `{"model":"m","messages":[{"role":"user","content":"u1"}]}`)
	postChat(t, srv.URL, `{"model":"m","messages":[{"role":"user","content":"u2"}]}`)
	if err := ledger.Err(); err != nil {
		t.Fatalf("ledger err: %v", err)
	}
	if ledger.Count() != 2 || saw.Load() != 2 {
		t.Fatalf("count=%d saw=%d", ledger.Count(), saw.Load())
	}
}

func TestOracleLedger_mismatchAndFirstError(t *testing.T) {
	t.Parallel()
	ledger := refbackend.NewOracleLedger(
		func([]byte) error { return nil },
		func([]byte) error { return errors.New("structural mismatch: reasoning_count") },
		func([]byte) error { return errors.New("structural mismatch: later") },
	)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder: refbackend.ScriptedResponder([]refbackend.ScriptedTurn{
			{VisibleText: "a"}, {VisibleText: "b"}, {VisibleText: "c"},
		}),
	}))
	t.Cleanup(srv.Close)

	postChat(t, srv.URL, `{"model":"m","messages":[{"role":"user","content":"1"}]}`)
	postChat(t, srv.URL, `{"model":"m","messages":[{"role":"user","content":"2"}]}`)
	postChat(t, srv.URL, `{"model":"m","messages":[{"role":"user","content":"3"}]}`)

	err := ledger.Err()
	if err == nil {
		t.Fatal("expected mismatch")
	}
	if !strings.Contains(err.Error(), "reasoning_count") {
		t.Fatalf("first error must win; got %v", err)
	}
	if strings.Contains(err.Error(), "later") {
		t.Fatal("must keep first error only")
	}
	if ledger.Count() != 3 {
		t.Fatalf("count=%d", ledger.Count())
	}
	if strings.Contains(err.Error(), `"content"`) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("ledger error must not leak payload; got %v", err)
	}
}

func TestOracleLedger_concurrencyRace(t *testing.T) {
	t.Parallel()
	const n = 64
	validators := make([]refbackend.RequestValidator, n)
	for i := range validators {
		validators[i] = func([]byte) error { return nil }
	}
	ledger := refbackend.NewOracleLedger(validators...)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder: func(refbackend.Request) refbackend.Response {
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			postChat(t, srv.URL, fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"%d"}]}`, i))
		}(i)
	}
	wg.Wait()
	if err := ledger.Err(); err != nil {
		t.Fatal(err)
	}
	if ledger.Count() != n {
		t.Fatalf("count=%d want=%d", ledger.Count(), n)
	}
}

func TestOracleLedger_invokedOnClonedBodyBeforeResponder(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var validateBody, responderBody []byte
	var order []string
	ledger := refbackend.NewOracleLedger(func(body []byte) error {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, "validate")
		body[0] = 'X' // mutate oracle clone; must not affect responder
		validateBody = append([]byte(nil), body...)
		return nil
	})
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      ledger.Hook(),
		Responder: func(req refbackend.Request) refbackend.Response {
			mu.Lock()
			defer mu.Unlock()
			responderBody = append([]byte(nil), req.Body...)
			order = append(order, "respond")
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	raw := `{"model":"m","messages":[{"role":"user","content":"z"}]}`
	postChat(t, srv.URL, raw)
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "validate" || order[1] != "respond" {
		t.Fatalf("order=%v", order)
	}
	if len(validateBody) == 0 || validateBody[0] != 'X' {
		t.Fatal("validator must receive a mutable clone")
	}
	if string(responderBody) != raw {
		t.Fatalf("responder must see unmutated clone; got_len=%d", len(responderBody))
	}
	if ledger.Count() != 1 || ledger.Err() != nil {
		t.Fatalf("ledger count=%d err=%v", ledger.Count(), ledger.Err())
	}
}

func postChat(t *testing.T, base, body string) {
	t.Helper()
	resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
