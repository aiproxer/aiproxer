package prerequestpolicy_test

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func yamlNode(t *testing.T, s string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
