package standardplugins

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_propagatesUpstreamHTTP(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	var got *http.Client
	id := "probe-upstream-http-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterBackend(id, func(n yaml.Node, upstreamHTTP *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		got = upstreamHTTP
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	want := &http.Client{}
	if _, err := reg.BuildBackend(id, yaml.Node{}, want, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("upstream HTTP: got %p want %p", got, want)
	}

	if _, err := reg.BuildBackend(id, yaml.Node{}, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("nil upstream: got %p want nil", got)
	}
}
