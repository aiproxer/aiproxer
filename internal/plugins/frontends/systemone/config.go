package systemone

import (
	"errors"

	"gopkg.in/yaml.v3"
)

// ID names the opt-in System One frontend contribution.
const ID = "systemone"

// Config bounds the number of questions accepted by this frontend instance.
type Config struct {
	MaxQuestions int `yaml:"max_questions"`
}

// DecodeConfig accepts only the System One frontend's bounded configuration.
func DecodeConfig(node yaml.Node) (Config, error) {
	cfg := Config{MaxQuestions: 256}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = *node.Content[0]
	}
	if node.Kind == 0 || node.Tag == "!!null" {
		return cfg, nil
	}
	if node.Kind != yaml.MappingNode {
		return cfg, errors.New("systemone config must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != "max_questions" {
			return cfg, errors.New("unknown systemone config field")
		}
	}
	if err := node.Decode(&cfg); err != nil || cfg.MaxQuestions <= 0 {
		return cfg, errors.New("systemone max_questions must be a positive integer")
	}
	return cfg, nil
}
