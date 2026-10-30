package runtimehost_test

// Task 1.4 characterization: freeze request-generation binding for the future
// large-payload lane (requirements 1.9, 5.11, 6.7; design sections 2, 7, 8, 16
// and rebaseline findings 4-5).
//
// What this proves with real existing seams only:
//   - GenerationDispatcher.ServeHTTP acquires exactly one Manager lease per HTTP
//     request, derives Handler + RequestBinding from that same lease.gen, and
//     holds the lease for the handler lifetime (generation_dispatcher.go:30-48,
//     lease.go:23-37, request_binding.go:21-27).
//   - The generation-scoped executor is co-located with the handler in the same
//     immutable PublishedRequestPlane object (as GenerationBundle does:
//     Handler() + ExecutorView() from one bundle, never rebound after publish).
//     An in-flight request therefore cannot resolve assess-phase work against
//     generation N and execute/fallback-phase work against generation N+1 when
//     both phases reuse the pinned plane/executor.
//   - A naive re-acquire after reload (Manager.Acquire / GenerationExecutor
//     facade Execute) does observe N+1, which is exactly why future
//     AssessLargeBody/ExecuteLargeBody must reuse the pinned executor instead
//     of re-acquiring per phase.
//
// What this explicitly does NOT claim:
//   - AssessLargeBody / ExecuteLargeBody / OpenWire do not exist yet (1.1
//     baseline grep for those symbols is zero). This fixture does not simulate
//     a nonexistent production assessment with mocked unrelated callbacks.
//     Both phased invocations below go through the SAME pinned plane object
//     selected by the real dispatcher lease, verified by pointer equality
//     before and after a real Publish(N+1).
//
// Public aipsdk.ExecutorView is unchanged; test-only, no production diff.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestGenerationDispatcher_RequestPinnedExecutorAcrossReload(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	oldExec := &recordingExec{id: "old"}
	newExec := &recordingExec{id: "new"}

	entered := make(chan struct{})
	release := make(chan struct{})

	// Old plane models the production co-location: Handler and ExecutorView
	// come from the same immutable plane object bound to generation N.
	oldPlane := &execPlane{exec: oldExec}
	oldPlane.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Errorf("old request: missing binding")
			_, _ = io.WriteString(w, "old-done")
			close(entered)
			return
		}
		if got := b.Meta().ID; got != 1 {
			t.Errorf("old request pre-reload binding ID=%d want 1", got)
		}
		if got := b.Meta().Label; got != "old" {
			t.Errorf("old request pre-reload label=%q want %q", got, "old")
		}
		// Assess-like phase: resolve the executor through the SAME pinned
		// plane object the dispatcher selected, not via a fresh Acquire.
		viewBefore := oldPlane.ExecutorView()
		if viewBefore == nil {
			t.Errorf("old request: nil executor before reload")
			close(entered)
			_, _ = io.WriteString(w, "old-done")
			return
		}
		s1, err := viewBefore.Execute(context.Background(), &aipapi.Call{})
		if err != nil {
			t.Errorf("old request pre-reload execute: %v", err)
		} else if s1 != nil {
			_, _ = s1.Recv(context.Background())
			_ = s1.Close()
		}
		close(entered)
		<-release
		// Execute/fallback-like phase: the same request must still observe
		// the same generation binding and the identical executor instance
		// even though Manager.Active() has advanced to N+1.
		if got := b.Meta().ID; got != 1 {
			t.Errorf("old request post-reload binding ID=%d want 1 (mixed generations)", got)
		}
		if got := b.Meta().Label; got != "old" {
			t.Errorf("old request post-reload label=%q want %q (mixed generations)", got, "old")
		}
		viewAfter := oldPlane.ExecutorView()
		if viewAfter == nil {
			t.Errorf("old request: nil executor after reload")
		} else if viewAfter != viewBefore {
			t.Errorf("old request: executor changed across reload (assess N vs execute N+1 hazard)")
		}
		if viewAfter != nil {
			s2, err := viewAfter.Execute(context.Background(), &aipapi.Call{})
			if err != nil {
				t.Errorf("old request post-reload execute: %v", err)
			} else if s2 != nil {
				_, _ = s2.Recv(context.Background())
				_ = s2.Close()
			}
		}
		if active := m.Active(); active == nil || active.ID() != 2 {
			t.Errorf("old request: expected active generation 2 during post-reload phase")
		}
		_, _ = io.WriteString(w, "old-done")
	})
	oldGen := m.PrepareRequestPlane("old", oldPlane)
	if err := m.Publish(oldGen); err != nil {
		t.Fatalf("Publish old: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		rr := httptest.NewRecorder()
		d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/blocked", nil))
		done <- rr.Body.String()
	}()
	<-entered

	newPlane := &execPlane{exec: newExec}
	newPlane.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := runtimehost.BindingFromContext(r.Context())
		if !ok {
			t.Errorf("new request: missing binding")
			_, _ = io.WriteString(w, "new-done")
			return
		}
		if got := b.Meta().ID; got != 2 {
			t.Errorf("new request binding ID=%d want 2", got)
		}
		view := newPlane.ExecutorView()
		if view == nil {
			t.Errorf("new request: nil executor")
			_, _ = io.WriteString(w, "new-done")
			return
		}
		s, err := view.Execute(context.Background(), &aipapi.Call{})
		if err != nil {
			t.Errorf("new request execute: %v", err)
		} else if s != nil {
			_, _ = s.Recv(context.Background())
			_ = s.Close()
		}
		_, _ = io.WriteString(w, "new-done")
	})
	newGen := m.PrepareRequestPlane("new", newPlane)
	if err := m.Publish(newGen); err != nil {
		t.Fatalf("Publish new: %v", err)
	}
	if got := m.Active().ID(); got != 2 {
		t.Fatalf("active ID=%d want 2", got)
	}

	rrNew := httptest.NewRecorder()
	d.ServeHTTP(rrNew, httptest.NewRequest(http.MethodGet, "/new", nil))
	if rrNew.Body.String() != "new-done" {
		t.Fatalf("new body=%q want %q", rrNew.Body.String(), "new-done")
	}
	if got := newExec.calls.Load(); got != 1 {
		t.Fatalf("new exec calls=%d want 1 (new request pinned to N+1)", got)
	}
	if got := oldExec.calls.Load(); got != 1 {
		t.Fatalf("old exec calls=%d want 1 before unblock (assess-like phase only)", got)
	}
	if oldGen.Refs() < 1 {
		t.Fatalf("old refs=%d want retained lease during blocked request", oldGen.Refs())
	}

	close(release)
	if body := <-done; body != "old-done" {
		t.Fatalf("blocked body=%q want %q", body, "old-done")
	}
	// Both phased invocations of the single in-flight HTTP request hit the
	// same generation-N executor; the N+1 executor served only the new request.
	if got := oldExec.calls.Load(); got != 2 {
		t.Fatalf("old exec calls=%d want 2 (both phases pinned to N)", got)
	}
	if got := newExec.calls.Load(); got != 1 {
		t.Fatalf("new exec calls=%d want 1 (no cross-generation execution)", got)
	}
	if got := oldGen.Refs(); got != 0 {
		t.Fatalf("old refs after completion=%d want 0", got)
	}

	// Contrast that justifies the future two-phase rule: a fresh acquire after
	// reload observes N+1, so future Assess/Execute must reuse the pinned
	// executor captured above instead of re-acquiring per phase.
	facade := runtimehost.NewGenerationExecutor(m)
	stream, err := facade.Execute(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatalf("facade Execute after reload: %v", err)
	}
	if stream != nil {
		_, _ = stream.Recv(context.Background())
		_ = stream.Close()
	}
	if got := newExec.calls.Load(); got != 2 {
		t.Fatalf("facade routed calls: new=%d want 2 (fresh acquire sees N+1)", got)
	}
	if got := oldExec.calls.Load(); got != 2 {
		t.Fatalf("facade must not touch pinned N executor: old=%d want 2", got)
	}
}
