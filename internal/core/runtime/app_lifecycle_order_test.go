package runtime_test

import (
	"context"
	"errors"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type seqLifecycle struct {
	id     string
	starts *[]string
	stops  *[]string
}

func (s seqLifecycle) Start(context.Context) error {
	*s.starts = append(*s.starts, s.id)
	return nil
}

func (s seqLifecycle) Stop(context.Context) error {
	*s.stops = append(*s.stops, s.id)
	return nil
}

func TestApp_lifecycleStartOrderAndReverseStop(t *testing.T) {
	t.Parallel()
	var starts, stops []string
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{
			seqLifecycle{id: "a", starts: &starts, stops: &stops},
			seqLifecycle{id: "b", starts: &starts, stops: &stops},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 2 || starts[0] != "a" || starts[1] != "b" {
		t.Fatalf("start order: %v", starts)
	}
	app.Shutdown(ctx)
	if len(stops) != 2 || stops[0] != "b" || stops[1] != "a" {
		t.Fatalf("reverse stop order: %v", stops)
	}
}

type failStartLifecycle struct{}

func (failStartLifecycle) Start(context.Context) error { return errors.New("start failed") }

func (failStartLifecycle) Stop(context.Context) error { return nil }

func TestApp_startPropagatesLifecycleErrorBeforeTraffic(t *testing.T) {
	t.Parallel()
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger:     testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{failStartLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(context.Background())
	if err == nil {
		t.Fatal("expected start error")
	}
}

type snapLifecycle struct {
	id     string
	fail   bool
	starts *[]string
	stops  *[]string
}

func (s snapLifecycle) Start(context.Context) error {
	*s.starts = append(*s.starts, s.id)
	if s.fail {
		return errors.New("start failed")
	}
	return nil
}

func (s snapLifecycle) Stop(context.Context) error {
	*s.stops = append(*s.stops, s.id)
	return nil
}

func TestApp_Start_stopsEarlierLifecyclesWhenLaterStartFails(t *testing.T) {
	t.Parallel()
	var starts, stops []string
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{
			snapLifecycle{id: "a", starts: &starts, stops: &stops},
			snapLifecycle{id: "b", fail: true, starts: &starts, stops: &stops},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(context.Background())
	if err == nil {
		t.Fatal("expected start error")
	}
	if len(starts) != 2 || starts[0] != "a" || starts[1] != "b" {
		t.Fatalf("starts: %v", starts)
	}
	if len(stops) != 1 || stops[0] != "a" {
		t.Fatalf("expected rollback stop of first lifecycle only, stops=%v", stops)
	}
}
