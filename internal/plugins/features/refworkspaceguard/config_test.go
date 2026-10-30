package refworkspaceguard

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	intPtr := func(i int) *int { return &i }

	tests := []struct {
		name    string
		yaml    string
		want    Config
		wantErr bool
	}{
		{
			name: "empty input",
			yaml: "",
			want: Config{
				ProjectRoot: "/ref/workspace",
				DirtyTree:   true,
				Markers:     []string{".refws"},
				Labels:      map[string]string{LabelDenyHeat: "1"},
			},
			wantErr: false,
		},
		{
			name: "null scalar",
			yaml: "null",
			want: Config{
				ProjectRoot: "/ref/workspace",
				DirtyTree:   true,
				Markers:     []string{".refws"},
				Labels:      map[string]string{LabelDenyHeat: "1"},
			},
			wantErr: false,
		},
		{
			name:    "invalid scalar",
			yaml:    "foo",
			want:    Config{},
			wantErr: true,
		},
		{
			name:    "sequence node",
			yaml:    "- foo\n- bar",
			want:    Config{},
			wantErr: true,
		},
		{
			name: "valid mapping with project root",
			yaml: `
project_root: "/custom/root"
dirty_tree: false
markers:
  - ".custom"
labels:
  foo: "bar"
`,
			want: Config{
				ProjectRoot: "/custom/root",
				DirtyTree:   false,
				Markers:     []string{".custom"},
				Labels: map[string]string{
					"foo":         "bar",
					LabelDenyHeat: "1",
				},
			},
			wantErr: false,
		},
		{
			name: "valid mapping missing project root",
			yaml: `
order: 5
dirty_tree: false
`,
			want: Config{
				Order:       intPtr(5),
				ProjectRoot: "/ref/workspace", // defaults
				DirtyTree:   true,             // default
				Markers:     []string{".refws"},
				Labels:      map[string]string{LabelDenyHeat: "1"},
			},
			wantErr: false,
		},
		{
			name: "invalid order",
			yaml: `
order: -1
`,
			want:    Config{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yaml), &n); err != nil {
				t.Fatalf("failed to unmarshal yaml: %v", err)
			}

			got, err := DecodeConfig(n)
			if (err != nil) != tt.wantErr {
				t.Errorf("DecodeConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DecodeConfig() got = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeConfig_NodeTests(t *testing.T) {
	t.Parallel()

	t.Run("kind 0 node", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeConfig(yaml.Node{Kind: 0})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		want := defaultConfig()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("empty document node", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeConfig(yaml.Node{Kind: yaml.DocumentNode})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		want := defaultConfig()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("invalid mapping decode", func(t *testing.T) {
		t.Parallel()
		n := yaml.Node{
			Kind: yaml.MappingNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "order"},
				{Kind: yaml.ScalarNode, Value: "not-an-int"},
			},
		}
		_, err := DecodeConfig(n)
		if err == nil {
			t.Error("expected error for invalid mapping decode, got nil")
		}
	})
}
