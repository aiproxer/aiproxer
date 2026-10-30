package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegisterStartedCatalogClosersRollbackQuiescesBeforeClose(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	var mu sync.Mutex
	var order []string
	track := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	started := &startedModelCatalog{
		closers:        []func() error{track("catalog-close")},
		quiesceClosers: []func() error{track("refresh-cancel-wait")},
	}
	registerStartedCatalogClosers(ledger, started)
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := fmt.Sprint(order)
	mu.Unlock()
	if got != "[refresh-cancel-wait catalog-close]" {
		t.Fatalf("rollback order=%s want refresh cancel/wait before catalog close", got)
	}
}

type nonCloneableIdleTransport struct {
	idleCalls atomic.Int32
}

func (t *nonCloneableIdleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected round trip")
}

func (t *nonCloneableIdleTransport) CloseIdleConnections() {
	t.idleCalls.Add(1)
}

func TestCloneCatalogHTTPClientDoesNotClaimNonCloneableInjectedTransport(t *testing.T) {
	t.Parallel()
	transport := &nonCloneableIdleTransport{}
	injected := &http.Client{Transport: transport}
	clone, cleanup := cloneCatalogHTTPClient(injected)
	if clone == injected {
		t.Fatal("catalog client must be a client clone")
	}
	if clone.Transport != transport {
		t.Fatal("non-cloneable transport identity should be shared but non-owned")
	}
	cleanup()
	if got := transport.idleCalls.Load(); got != 0 {
		t.Fatalf("candidate claimed injected transport cleanup: calls=%d", got)
	}
	if !strings.Contains(fmt.Sprintf("%T", clone.Transport), "nonCloneableIdleTransport") {
		t.Fatalf("unexpected transport type %T", clone.Transport)
	}
}
