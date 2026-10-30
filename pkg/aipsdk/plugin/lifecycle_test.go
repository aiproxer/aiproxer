package plugin_test

import (
	"context"
	"testing"

	aipsdkplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type noopLifecycle struct{}

func (noopLifecycle) Start(context.Context) error { return nil }

func (noopLifecycle) Stop(context.Context) error { return nil }

func TestLifecycleContractIsImplementable(t *testing.T) {
	t.Parallel()

	var _ aipsdkplugin.Lifecycle = noopLifecycle{}
}
