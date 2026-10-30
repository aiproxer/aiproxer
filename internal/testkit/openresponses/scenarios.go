package openresponses

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type ScenarioKind string

const (
	ScenarioKindJSONText     ScenarioKind = "json_text"
	ScenarioKindSSEText      ScenarioKind = "sse_text"
	ScenarioKindTools        ScenarioKind = "tools"
	ScenarioKindMultimodal   ScenarioKind = "multimodal"
	ScenarioKindReasoning    ScenarioKind = "reasoning"
	ScenarioKindContinuation ScenarioKind = "continuation"
	ScenarioKindCompaction   ScenarioKind = "compaction"
	ScenarioKindExtensions   ScenarioKind = "extensions"
	ScenarioKindWebSocket    ScenarioKind = "websocket"
	ScenarioKindNegative     ScenarioKind = "negative_validation"
	ScenarioKindAdversarial  ScenarioKind = "adversarial"
)

var validScenarioIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type ScenarioDescriptor struct {
	ID          string       `json:"id"`
	Kind        ScenarioKind `json:"kind"`
	Description string       `json:"description"`
}

func (s ScenarioDescriptor) Validate() error {
	if !validScenarioIDPattern.MatchString(s.ID) {
		return fmt.Errorf("invalid scenario ID format %q: must be lowercase kebab-case", s.ID)
	}
	switch s.Kind {
	case ScenarioKindJSONText, ScenarioKindSSEText, ScenarioKindTools, ScenarioKindMultimodal,
		ScenarioKindReasoning, ScenarioKindContinuation, ScenarioKindCompaction, ScenarioKindExtensions,
		ScenarioKindWebSocket, ScenarioKindNegative, ScenarioKindAdversarial:
		// Valid
	default:
		return fmt.Errorf("unknown scenario kind %q", s.Kind)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("scenario %q description cannot be empty", s.ID)
	}
	return nil
}

type ScenarioRegistry struct {
	mu        sync.RWMutex
	scenarios map[string]ScenarioDescriptor
}

func NewScenarioRegistry() *ScenarioRegistry {
	return &ScenarioRegistry{
		scenarios: make(map[string]ScenarioDescriptor),
	}
}

func (r *ScenarioRegistry) Register(sd ScenarioDescriptor) error {
	if err := sd.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.scenarios[sd.ID]; exists {
		return fmt.Errorf("duplicate scenario ID registered: %q", sd.ID)
	}
	r.scenarios[sd.ID] = sd
	return nil
}

func (r *ScenarioRegistry) Get(id string) (ScenarioDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sd, ok := r.scenarios[id]
	return sd, ok
}

func (r *ScenarioRegistry) List() []ScenarioDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ScenarioDescriptor, 0, len(r.scenarios))
	for _, sd := range r.scenarios {
		out = append(out, sd)
	}
	return out
}
