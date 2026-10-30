package aipruntime_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
)

func TestExecutorView_PrePublicationALegCancelSurvivesReload(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	const original = `text: "[dogfood] local stub"`
	if !strings.Contains(string(raw), original) {
		t.Fatalf("fixture missing %q", original)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	rt, err := aipruntime.Build(context.Background(), aipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	view := rt.ExecutorView()
	if view == nil {
		t.Fatal("expected ExecutorView")
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("create pre-publication A-leg")},
		}},
	}
	oldStream, err := view.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("pre-publication Execute: %v", err)
	}
	t.Cleanup(func() { _ = oldStream.Close() })
	oldALeg := call.Session.ALegID
	if oldALeg == "" {
		t.Fatal("public Execute did not assign an A-leg ID")
	}

	changed := strings.Replace(string(raw), original, `text: "generation-two"`, 1)
	tmp := path + ".replacement"
	if err := os.WriteFile(tmp, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	res := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "facade-cancel-test"})
	if res.Category != aipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", res.Category, res.ReasonCategory)
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity changed across publication")
	}

	// Cancel before Collect so stream completion cannot race the cancel path.
	if err := view.CancelALeg(context.Background(), aipapi.ALegCancelRequest{
		ALegID: oldALeg,
		Reason: "cross-generation-public-facade-test",
	}); err != nil {
		t.Fatalf("CancelALeg: %v", err)
	}
	oldCollected, collectErr := aipapi.Collect(context.Background(), oldStream)
	if collectErr == nil {
		if got := oldCollected.Text.String(); got != "" && got != "[dogfood] local stub" {
			t.Fatalf("pre-publication stream mixed generations after cancel: %q", got)
		}
	}

	// Same ExecutorView must still reach process-owned dispatch for the new generation.
	call2 := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("post-reload generation")},
		}},
	}
	newStream, err := view.Execute(context.Background(), call2)
	if err != nil {
		t.Fatalf("post-reload Execute: %v", err)
	}
	t.Cleanup(func() { _ = newStream.Close() })
	newCollected, err := aipapi.Collect(context.Background(), newStream)
	if err != nil {
		t.Fatalf("post-reload Collect: %v", err)
	}
	if got := newCollected.Text.String(); got != "generation-two" {
		t.Fatalf("ExecutorView must reach process-owned work across generations; got %q", got)
	}
}
