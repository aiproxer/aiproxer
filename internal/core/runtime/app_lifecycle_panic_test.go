package runtime_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type panicStartLifecycle struct{}

func (panicStartLifecycle) Start(context.Context) error {
	panic("start-panic-secret-NEVER-IN-ERROR")
}

func (panicStartLifecycle) Stop(context.Context) error { return nil }

func TestApp_Start_lifecycleStartPanic_returnsPanicErrorWithoutLeak(t *testing.T) {
	t.Parallel()
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger:     testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{panicStartLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) || pe == nil {
		t.Fatalf("want errors.As PanicError, got %T: %v", err, err)
	}
	if pe.Boundary() != safety.BoundaryWorker {
		t.Fatalf("boundary: %q", pe.Boundary())
	}
	if pe.Operation() != "lifecycle_start" {
		t.Fatalf("operation: %q", pe.Operation())
	}
	if !strings.HasPrefix(err.Error(), "runtime: lifecycle start:") {
		t.Fatalf("wrap prefix: %q", err.Error())
	}
	if strings.Contains(err.Error(), "start-panic-secret") {
		t.Fatal("Error() must not include raw panic text")
	}
}

type rollbackPanicStopLifecycle struct {
	id            string
	starts, stops *[]string
	panicOnStop   bool
}

func (r rollbackPanicStopLifecycle) Start(context.Context) error {
	*r.starts = append(*r.starts, r.id)
	return nil
}

func (r rollbackPanicStopLifecycle) Stop(context.Context) error {
	*r.stops = append(*r.stops, r.id)
	if r.panicOnStop {
		panic("rollback-stop-panic-secret")
	}
	return nil
}

type failAfterStartLifecycle struct{}

func (failAfterStartLifecycle) Start(context.Context) error {
	return errors.New("primary-start-failure")
}

func (failAfterStartLifecycle) Stop(context.Context) error { return nil }

type rollbackPanicLogProbe struct {
	saw bool
}

func (p *rollbackPanicLogProbe) Enabled(context.Context, slog.Level) bool { return true }

func (p *rollbackPanicLogProbe) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelError && strings.Contains(r.Message, "start failure") && strings.Contains(r.Message, "panic") {
		p.saw = true
	}
	return nil
}

func (p *rollbackPanicLogProbe) WithAttrs([]slog.Attr) slog.Handler { return p }

func (p *rollbackPanicLogProbe) WithGroup(string) slog.Handler { return p }

func TestApp_Start_rollbackStopPanic_logsAndPreservesPrimaryStartFailure(t *testing.T) {
	t.Parallel()
	var starts, stops []string
	probe := &rollbackPanicLogProbe{}
	h := slog.New(probe)
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: h,
		Lifecycles: []aipplugin.Lifecycle{
			rollbackPanicStopLifecycle{id: "a", starts: &starts, stops: &stops, panicOnStop: true},
			failAfterStartLifecycle{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); ok {
		t.Fatal("primary error must not be the rollback panic")
	}
	if !strings.Contains(err.Error(), "primary-start-failure") {
		t.Fatalf("wrapped message should include primary: %q", err.Error())
	}
	if strings.Contains(err.Error(), "rollback-stop-panic-secret") {
		t.Fatal("must not leak rollback panic into returned error")
	}
	if len(starts) != 1 || starts[0] != "a" {
		t.Fatalf("starts: %v", starts)
	}
	if len(stops) != 1 || stops[0] != "a" {
		t.Fatalf("rollback attempted stop of a: stops=%v", stops)
	}
	if !probe.saw {
		t.Fatal("expected error log for rollback stop panic")
	}
}

type seqPanicStopLifecycle struct {
	id    string
	stops *[]string
	panic bool
}

func (s seqPanicStopLifecycle) Start(context.Context) error { return nil }

func (s seqPanicStopLifecycle) Stop(context.Context) error {
	*s.stops = append(*s.stops, s.id)
	if s.panic {
		panic("shutdown-panic-secret")
	}
	return nil
}

func TestApp_Shutdown_stopPanicContinuesReverseOrder(t *testing.T) {
	t.Parallel()
	var stops []string
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{
			seqPanicStopLifecycle{id: "a", stops: &stops},
			seqPanicStopLifecycle{id: "b", stops: &stops, panic: true},
			seqPanicStopLifecycle{id: "c", stops: &stops},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Shutdown(context.Background())
	if len(stops) != 3 {
		t.Fatalf("want all three Stop calls, got %v", stops)
	}
	if stops[0] != "c" || stops[1] != "b" || stops[2] != "a" {
		t.Fatalf("reverse order with continuation: %v", stops)
	}
}
