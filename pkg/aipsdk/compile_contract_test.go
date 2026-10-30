package aipsdk_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestRegistrationAndConfigTypesAreConstructible(t *testing.T) {
	t.Parallel()

	var (
		reg aipsdk.Registration
		_   aipsdk.ConfigPayload
		_   aipsdk.PluginKind
		_   aipsdk.Requirement
	)
	reg = aipsdk.Registration{
		ID:     "x",
		Kind:   aipsdk.PluginKindFrontend,
		Config: aipsdk.ConfigPayload{Node: yaml.Node{Kind: yaml.MappingNode}},
	}
	_ = reg
}
