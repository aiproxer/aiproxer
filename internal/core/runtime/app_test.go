package runtime_test

import (
	"context"
	"errors"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type stopOnlyLifecycle struct {
	id    string
	stops *[]string
}

func (s stopOnlyLifecycle) Start(context.Context) error { return nil }

func (s stopOnlyLifecycle) Stop(context.Context) error {
	*s.stops = append(*s.stops, s.id)
	return nil
}

func TestNewRequiresConfig(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !errors.Is(err, runtime.ErrNilConfig) {
		t.Fatalf("expected errors.Is(err, ErrNilConfig), got %v", err)
	}
}

func TestNewRejectsNilLogger(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
	if !errors.Is(err, runtime.ErrNilLogger) {
		t.Fatalf("expected ErrNilLogger, got %v", err)
	}
}

func TestNewAcceptsMinimalConfig(t *testing.T) {
	t.Parallel()

	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app == nil {
		t.Fatal("expected app instance")
	}
}

func TestNewRejectsDuplicatePluginRegistrations(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Registrations: []aipsdk.Registration{
			{ID: "dup", Kind: aipsdk.PluginKindFrontend},
			{ID: "dup", Kind: aipsdk.PluginKindFrontend},
		},
		Mandatory: nil,
	})
	if err == nil {
		t.Fatal("expected duplicate registration error")
	}
	if !errors.Is(err, aipsdk.ErrDuplicateRegistration) {
		t.Fatalf("expected ErrDuplicateRegistration, got %v", err)
	}
}

func TestNewRejectsMissingMandatoryPlugin(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Registrations: []aipsdk.Registration{
			{ID: "only-one", Kind: aipsdk.PluginKindFrontend},
		},
		Mandatory: []aipsdk.Requirement{
			{Kind: aipsdk.PluginKindBackend, ID: "missing-backend"},
		},
	})
	if err == nil {
		t.Fatal("expected missing mandatory plugin error")
	}

	if _, ok := errors.AsType[*aipsdk.MissingRequirementError](err); !ok {
		t.Fatalf("expected MissingRequirementError, got %v", err)
	}
}

func TestShutdownHandlesNilAppAndNilLifecycles(t *testing.T) {
	t.Parallel()

	var nilApp *runtime.App
	nilApp.Shutdown(context.Background())

	var stops []string
	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: testkit.DiscardLogger(),
		Lifecycles: []aipplugin.Lifecycle{
			nil,
			stopOnlyLifecycle{id: "a", stops: &stops},
			nil,
			stopOnlyLifecycle{id: "b", stops: &stops},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	app.Shutdown(context.Background())

	if len(stops) != 2 || stops[0] != "b" || stops[1] != "a" {
		t.Fatalf("reverse stop order with nil lifecycles: %v", stops)
	}
}
