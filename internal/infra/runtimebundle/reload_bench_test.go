package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

const (
	dogfoodLocalStubRelPath = "../../../config/examples/dogfood-local-stub.yaml"
	localStubTextOriginal   = `text: "[dogfood] local stub"`
	// generation-one / generation-two are equal length so alternate bodies stay
	// byte-length-stable for reload source integrity comparisons.
	localStubTextGenOne = `text: "generation-one"`
	localStubTextGenTwo = `text: "generation-two"`
)

// BenchmarkCandidateCompilation measures the real production generation
// compiler, including standard plugin/frontend/feature construction. Candidate
// resources are rolled back after every iteration, as check-config would do.
func BenchmarkCandidateCompilation(b *testing.B) {
	host, err := runtimebundle.BuildHost(b.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      bpkit.WriteDogfoodLocalStubConfig(b),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		b.Fatalf("BuildHost: %v", err)
	}
	b.Cleanup(func() { _ = host.Close(context.Background()) })
	compiler := runtimebundle.GenerationCompiler{
		Process: runtimebundle.HostProcess(host),
		Compose: stdhttp.ComposeStandardHTTP,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		plane, err := compiler.Compile(b.Context(), host.Config(), nil)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		if err := plane.Close(); err != nil {
			b.Fatalf("rollback: %v", err)
		}
	}
}

// BenchmarkBuildHost times the complete production BuildHost transaction for
// the standard distribution + static dogfood/local-stub config.
//
// Baseline equivalence note: BuildHost did not exist at
// efe4624909cea318c7211d5cb3734059d3210802. The equivalent baseline
// transaction is BuildBootstrap(Mode: BootstrapServe,
// HandlerComposer: stdhttp.ComposeRequestPlane) plus AttachReloadHost(...).
// Timing only BuildBootstrap is invalid because it omits coordinator/source
// binding. Hermes applies a reviewed baseline-only overlay for A/B capture.
func BenchmarkBuildHost(b *testing.B) {
	in := runtimebundle.BuildHostInput{
		ConfigPath:      bpkit.WriteDogfoodLocalStubConfig(b),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		host, err := runtimebundle.BuildHost(b.Context(), in)
		if err != nil {
			b.Fatalf("BuildHost: %v", err)
		}
		b.StopTimer()
		if err := host.Close(context.Background()); err != nil {
			b.Fatalf("Close: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkSuccessfulReload times a complete real Host.Reload(TriggerAPI)
// publish transaction after an atomic equal-length config rename.
//
// Baseline equivalence note: baseline reload calls the then-canonical
// coordinator with TriggerAPI. Baseline cleanup outside timing must perform
// its complete approved shutdown path. Hermes applies a reviewed baseline-only
// overlay for isolated baseline-vs-final capture.
func BenchmarkSuccessfulReload(b *testing.B) {
	base, err := os.ReadFile(dogfoodLocalStubRelPath)
	if err != nil {
		b.Fatalf("read dogfood fixture: %v", err)
	}
	bodyOne := localStubVersionBody(b, string(base), localStubTextGenOne)
	bodyTwo := localStubVersionBody(b, string(base), localStubTextGenTwo)
	if len(bodyOne) != len(bodyTwo) {
		b.Fatalf("alternate bodies must be equal length: %d vs %d", len(bodyOne), len(bodyTwo))
	}

	dir := b.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	atomicReplaceFile(b, cfgPath, bodyOne)

	host, err := runtimebundle.BuildHost(b.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		b.Fatalf("BuildHost: %v", err)
	}
	b.Cleanup(func() { _ = host.Close(context.Background()) })
	mgr := runtimebundle.HostManager(host)
	if mgr == nil {
		b.Fatal("nil HostManager")
	}

	next := bodyTwo
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		atomicReplaceFile(b, cfgPath, next)
		if next == bodyTwo {
			next = bodyOne
		} else {
			next = bodyTwo
		}
		waitRetainedBelow(b, mgr, runtimebundle.DefaultMaxRetainedGenerations-2, 5*time.Second)
		b.StartTimer()

		res := host.Reload(b.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "bench-success"})
		if res.Category != sdkreload.ResultPublished {
			b.Fatalf("reload category=%q want %q reason=%q", res.Category, sdkreload.ResultPublished, res.ReasonCategory)
		}

		b.StopTimer()
		waitRetainedBelow(b, mgr, runtimebundle.DefaultMaxRetainedGenerations-2, 5*time.Second)
		b.StartTimer()
	}
}

// BenchmarkNoopReload times a complete real Host.Reload(TriggerAPI) when the
// effective config is unchanged and must resolve to ResultNoop.
//
// Baseline equivalence note: baseline reload calls the then-canonical
// coordinator with TriggerAPI against an unchanged source. Hermes applies a
// reviewed baseline-only overlay for isolated baseline-vs-final capture.
func BenchmarkNoopReload(b *testing.B) {
	dir := b.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	raw, err := os.ReadFile(dogfoodLocalStubRelPath)
	if err != nil {
		b.Fatalf("read dogfood fixture: %v", err)
	}
	atomicReplaceFile(b, cfgPath, string(raw))

	host, err := runtimebundle.BuildHost(b.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		b.Fatalf("BuildHost: %v", err)
	}
	b.Cleanup(func() { _ = host.Close(context.Background()) })

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		res := host.Reload(b.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "bench-noop"})
		if res.Category != sdkreload.ResultNoop {
			b.Fatalf("reload category=%q want %q reason=%q", res.Category, sdkreload.ResultNoop, res.ReasonCategory)
		}
	}
}

func localStubVersionBody(b *testing.B, base, replacement string) string {
	b.Helper()
	if !strings.Contains(base, localStubTextOriginal) {
		b.Fatalf("fixture missing %q", localStubTextOriginal)
	}
	return strings.Replace(base, localStubTextOriginal, replacement, 1)
}

func atomicReplaceFile(b *testing.B, path, body string) {
	b.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		b.Fatalf("write temp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		b.Fatalf("rename: %v", err)
	}
}

// waitRetainedBelow sweeps closed generations and waits until retained count is
// at or below limit so successful-reload iterations never hit the hard retention
// budget. Timing must stay stopped around this helper.
func waitRetainedBelow(b *testing.B, mgr *runtimehost.Manager, limit int, timeout time.Duration) {
	b.Helper()
	deadline := time.Now().Add(timeout)
	for {
		mgr.SweepClosed()
		if mgr.RetainedCount() <= limit {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("retained=%d still above %d after %s", mgr.RetainedCount(), limit, timeout)
		}
		time.Sleep(time.Millisecond)
	}
}
